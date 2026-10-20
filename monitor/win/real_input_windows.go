//go:build windows && !panel

package win

import (
	"fmt"
	"log"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
	"unicode"
	"unsafe"

	"golang.org/x/sys/windows"
)

// An authority-granting prompt counts an answer only when a person's own
// mouse or keyboard pressed the answer button (POPUP-STYLE Q14 part D, owner
// decision 2026-11-12). While such a prompt shows, low-level mouse and
// keyboard hooks on a dedicated thread note each button release and each
// Enter, Space and Alt+access-key press, with Windows' injected flags. An
// answer button's click counts only when the newest matching input was real
// and arrived after the answers armed. A click with no matching input
// (BM_CLICK, a WM_COMMAND or TDM_CLICK_BUTTON from another program, UI
// Automation Invoke) or with injected input (SendInput, keybd_event) is
// ignored, logged once, and the prompt stays open.
//
// This interim leaves two gaps for 0.9.0's privileged presenter: code
// injected into the presenter itself, and the On-Screen Keyboard, whose
// keys Windows flags as injected.

var (
	inputSetHook       = user32.NewProc("SetWindowsHookExW")
	inputUnhook        = user32.NewProc("UnhookWindowsHookEx")
	inputNextHook      = user32.NewProc("CallNextHookEx")
	inputPeekMessage   = user32.NewProc("PeekMessageW")
	inputPostThread    = user32.NewProc("PostThreadMessageW")
	inputGetForeground = user32.NewProc("GetForegroundWindow")
	inputGetAncestor   = user32.NewProc("GetAncestor")
	inputFromPhysical  = user32.NewProc("WindowFromPhysicalPoint")
	inputMouseCallback = syscall.NewCallback(inputMouseHook)
	inputKeyCallback   = syscall.NewCallback(inputKeyHook)
	activeRealInput    atomic.Pointer[realInputGuard]
)

const (
	whKeyboardLL       = 13
	whMouseLL          = 14
	wmLButtonUp        = 0x0202
	wmRButtonUp        = 0x0205
	wmKeyDown          = 0x0100
	wmKeyUp            = 0x0101
	wmSysKeyDown       = 0x0104
	wmSysKeyUp         = 0x0105
	wmQuit             = 0x0012
	llmhfInjected      = 0x00000001
	llmhfLowerILInject = 0x00000002
	llkhfInjected      = 0x00000010
	llkhfLowerILInject = 0x00000002
	llkhfAltDown       = 0x00000020
	vkReturn           = 0x0D
	vkSpace            = 0x20
	gaRoot             = 2
	realInputWindow    = 2 * time.Second
	realInputEvents    = 16
	realInputLogLimit  = 32
	noRealInputSource  = "a window message or UI Automation, with no mouse or key press"
)

// msllHookStruct is MSLLHOOKSTRUCT.
type msllHookStruct struct {
	X, Y      int32
	MouseData uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// kbdllHookStruct is KBDLLHOOKSTRUCT.
type kbdllHookStruct struct {
	VK, Scan  uint32
	Flags     uint32
	Time      uint32
	ExtraInfo uintptr
}

// realInputEvent is one noted press: a mouse button release over a window, or
// an Enter, Space or Alt+letter/digit key transition. Other keys are never
// noted, so text typed into a prompt's field (a provider key) is not kept.
type realInputEvent struct {
	at       time.Time
	mouse    bool
	vk       uint32
	target   uintptr // mouse: the window under the release, hit-tested when it happened
	root     uintptr // mouse: target's top-level window; key: the foreground window
	injected bool
	lowerIL  bool
}

// source names where an ignored answer came from, for the log. It holds no
// key, position or text.
func (e realInputEvent) source() string {
	device := "keyboard"
	if e.mouse {
		device = "mouse"
	}
	if e.lowerIL {
		return "injected " + device + " input from a lower-integrity program"
	}
	return "injected " + device + " input"
}

// realInputGuard holds the noted input for the one authority-granting prompt
// the presenter's UI thread shows.
type realInputGuard struct {
	kind   string
	mu     sync.Mutex
	events [realInputEvents]realInputEvent
	next   int
	// armed and used are event numbers: an event numbered below either
	// answers nothing. Event times only bound an event's age; Windows' clock
	// ticks too coarsely to order events.
	armed  int
	used   int
	logged int
}

func (g *realInputGuard) record(e realInputEvent) {
	g.mu.Lock()
	g.events[g.next%realInputEvents] = e
	g.next++
	g.mu.Unlock()
}

// arm starts counting input: a press made before the answers were armed
// (while the buttons were still disabled) never answers.
func (g *realInputGuard) arm() {
	g.mu.Lock()
	g.armed = g.next
	g.mu.Unlock()
}

// realInputTarget describes the answer button whose click is being decided.
type realInputTarget struct {
	dialog    uintptr // the prompt's top-level window
	button    uintptr // the answer button's window
	isDefault bool    // the button is the prompt's own default (Enter)
	focused   bool    // the button holds the focus now
	label     string  // the button's label, for its access key
}

// realInputTargetOn describes button of dialog as it stands on the dialog's
// own thread.
func realInputTargetOn(dialog, button uintptr, isDefault bool, label string) realInputTarget {
	focus, _, _ := permissionGetFocus.Call()
	return realInputTarget{dialog: dialog, button: button, isDefault: isDefault, focused: button != 0 && focus == button, label: label}
}

func (t realInputTarget) matches(e realInputEvent) bool {
	if e.root != t.dialog {
		return false
	}
	if e.mouse {
		return t.button != 0 && e.target == t.button
	}
	switch e.vk {
	case vkReturn:
		return t.isDefault || t.focused
	case vkSpace:
		return t.focused
	}
	key := accessKey(t.label)
	return key != 0 && uint32(key) == e.vk
}

// accessKey is the letter or digit after a single & in a button label, upper
// case, or 0.
func accessKey(label string) rune {
	runes := []rune(label)
	for i := 0; i+1 < len(runes); i++ {
		if runes[i] != '&' {
			continue
		}
		if runes[i+1] == '&' {
			i++
			continue
		}
		r := unicode.ToUpper(runes[i+1])
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return r
		}
		return 0
	}
	return 0
}

// decide reports whether the click on t counts. The newest noted press that
// could have clicked t decides: real and unused counts once; injected never
// does. With none, the click came from a message or UI Automation.
func (g *realInputGuard) decide(t realInputTarget) (bool, string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	for seq := g.next - 1; seq >= 0 && seq >= g.next-realInputEvents; seq-- {
		e := g.events[seq%realInputEvents]
		if now.Sub(e.at) > realInputWindow || seq < g.armed {
			break
		}
		if !t.matches(e) {
			continue
		}
		if e.injected {
			return false, e.source()
		}
		if seq < g.used {
			break
		}
		g.used = seq + 1
		return true, ""
	}
	return false, noRealInputSource
}

// ignored writes one log line per ignored answer, up to a limit per prompt.
// The person is not told: nothing they did was refused.
func (g *realInputGuard) ignored(source string) {
	g.mu.Lock()
	g.logged++
	n := g.logged
	g.mu.Unlock()
	switch {
	case n < realInputLogLimit:
		log.Printf("native question: ignored an answer to a %s prompt; it came from %s", g.kind, source)
	case n == realInputLogLimit:
		log.Printf("native question: ignored an answer to a %s prompt; it came from %s; further ignored answers to this prompt are not logged", g.kind, source)
	}
}

// realInputAnswer decides an answer click on a prompt. A prompt without
// RealInputOnly answers as before. A RealInputOnly prompt whose guard is not
// running answers nothing.
func realInputAnswer(prompt QuestionPrompt, t realInputTarget) bool {
	if !prompt.RealInputOnly {
		return true
	}
	g := activeRealInput.Load()
	if g == nil {
		log.Printf("native question: ignored an answer to a %s prompt; the real-input check is not running", prompt.Kind)
		return false
	}
	ok, source := g.decide(t)
	if !ok {
		g.ignored(source)
	}
	return ok
}

// armRealInput starts counting real input for the prompt on screen.
func armRealInput() {
	if g := activeRealInput.Load(); g != nil {
		g.arm()
	}
}

// guardRealInput installs the hooks for one authority-granting prompt. stop
// removes them; it is called on the same UI thread after the prompt closes.
func guardRealInput(kind string) (stop func(), err error) {
	if kind == "" {
		kind = "permission"
	}
	g := &realInputGuard{kind: kind}
	unhook, err := startRealInputHooks()
	if err != nil {
		return nil, err
	}
	activeRealInput.Store(g)
	return func() {
		activeRealInput.CompareAndSwap(g, nil)
		unhook()
	}, nil
}

// startRealInputHooks runs both low-level hooks on a thread of their own
// whose loop only waits, so a busy prompt thread never delays the person's
// input. Windows calls a low-level hook before it queues the input to the
// target window, so a click is noted before its button sees it.
func startRealInputHooks() (func(), error) {
	type started struct {
		thread uint32
		err    error
	}
	ready := make(chan started, 1)
	done := make(chan struct{})
	go func() {
		// The goroutine exits locked: its thread ends with it and never
		// returns to the pool with a message queue.
		runtime.LockOSThread()
		defer close(done)
		var msg [8]uint64                                                   // MSG
		inputPeekMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0, 0) // creates this thread's message queue
		instance, _, _ := getModuleHandle.Call(0)
		mouse, _, mouseErr := inputSetHook.Call(whMouseLL, inputMouseCallback, instance, 0)
		if mouse == 0 {
			ready <- started{err: fmt.Errorf("install the mouse hook: %w", mouseErr)}
			return
		}
		defer inputUnhook.Call(mouse)
		key, _, keyErr := inputSetHook.Call(whKeyboardLL, inputKeyCallback, instance, 0)
		if key == 0 {
			ready <- started{err: fmt.Errorf("install the keyboard hook: %w", keyErr)}
			return
		}
		defer inputUnhook.Call(key)
		ready <- started{thread: windows.GetCurrentThreadId()}
		for {
			if r, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0); int32(r) <= 0 {
				return
			}
		}
	}()
	s := <-ready
	if s.err != nil {
		<-done
		return nil, s.err
	}
	return func() {
		inputPostThread.Call(uintptr(s.thread), wmQuit, 0, 0)
		<-done
	}, nil
}

// inputWindowAt is the window under a physical screen point. A POINT is
// passed by value: one register on 64-bit Windows, two arguments on x86.
var inputWindowAt = func(x, y int32) uintptr {
	var h uintptr
	if unsafe.Sizeof(uintptr(0)) == 8 {
		h, _, _ = inputFromPhysical.Call(uintptr(uint32(x)) | uintptr(uint64(uint32(y))<<32))
	} else {
		h, _, _ = inputFromPhysical.Call(uintptr(uint32(x)), uintptr(uint32(y)))
	}
	return h
}

// inputForeground is the foreground window when a key was pressed. A test
// replaces it: a private desktop never has the foreground.
var inputForeground = func() uintptr {
	h, _, _ := inputGetForeground.Call()
	return h
}

func inputRoot(h uintptr) uintptr {
	if h == 0 {
		return 0
	}
	root, _, _ := inputGetAncestor.Call(h, gaRoot)
	return root
}

func inputMouseHook(code int, wparam, lparam uintptr) uintptr {
	// Both releases are noted: with swapped buttons a left-handed person's
	// primary click may arrive as the right one.
	if code == 0 && (wparam == wmLButtonUp || wparam == wmRButtonUp) && lparam != 0 {
		if g := activeRealInput.Load(); g != nil {
			info := (*msllHookStruct)(unsafe.Pointer(lparam))
			target := inputWindowAt(info.X, info.Y)
			g.record(realInputEvent{at: time.Now(), mouse: true, target: target, root: inputRoot(target),
				injected: info.Flags&(llmhfInjected|llmhfLowerILInject) != 0, lowerIL: info.Flags&llmhfLowerILInject != 0})
		}
	}
	return inputCallNext(code, wparam, lparam)
}

func inputKeyHook(code int, wparam, lparam uintptr) uintptr {
	if code == 0 && lparam != 0 && (wparam == wmKeyDown || wparam == wmKeyUp || wparam == wmSysKeyDown || wparam == wmSysKeyUp) {
		if g := activeRealInput.Load(); g != nil {
			info := (*kbdllHookStruct)(unsafe.Pointer(lparam))
			if noted := info.VK == vkReturn || info.VK == vkSpace ||
				info.Flags&llkhfAltDown != 0 && (info.VK >= '0' && info.VK <= '9' || info.VK >= 'A' && info.VK <= 'Z'); noted {
				g.record(realInputEvent{at: time.Now(), vk: info.VK, root: inputRoot(inputForeground()),
					injected: info.Flags&(llkhfInjected|llkhfLowerILInject) != 0, lowerIL: info.Flags&llkhfLowerILInject != 0})
			}
		}
	}
	return inputCallNext(code, wparam, lparam)
}

// inputCallNext passes the event on unchanged: the hooks only observe.
func inputCallNext(code int, wparam, lparam uintptr) uintptr {
	r, _, _ := inputNextHook.Call(0, uintptr(code), wparam, lparam)
	return r
}
