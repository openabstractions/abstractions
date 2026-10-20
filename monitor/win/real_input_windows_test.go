//go:build windows && !panel

package win

import (
	"bytes"
	"log"
	"os"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The decision on noted input: the newest press that could have clicked the
// button decides; real counts once, injected never, a press before the
// answers armed never, and no press means a message or UI Automation.
func TestRealInputDecision(t *testing.T) {
	dialog, button := uintptr(0x1000), uintptr(0x2000)
	target := realInputTarget{dialog: dialog, button: button, focused: true, label: "Allow"}
	click := func(injected, lowerIL bool) realInputEvent {
		return realInputEvent{at: time.Now(), mouse: true, target: button, root: dialog, injected: injected, lowerIL: lowerIL}
	}

	g := &realInputGuard{kind: "rights.first_use"}
	g.record(click(false, false))
	g.arm()
	if ok, _ := g.decide(target); ok {
		t.Fatal("a click made before the answers armed answered")
	}

	g = &realInputGuard{kind: "rights.first_use"}
	g.arm()
	if ok, source := g.decide(target); ok || source != noRealInputSource {
		t.Fatalf("a click with no input answered: %t %q", ok, source)
	}
	g.record(click(true, false))
	if ok, source := g.decide(target); ok || source != "injected mouse input" {
		t.Fatalf("an injected click answered: %t %q", ok, source)
	}
	g.record(click(true, true))
	if ok, source := g.decide(target); ok || source != "injected mouse input from a lower-integrity program" {
		t.Fatalf("a lower-integrity click answered: %t %q", ok, source)
	}
	g.record(click(false, false))
	if ok, _ := g.decide(target); !ok {
		t.Fatal("a person's click did not answer")
	}
	if ok, _ := g.decide(target); ok {
		t.Fatal("one click answered twice")
	}
	elsewhere := click(false, false)
	elsewhere.target = 0x3000
	g.record(elsewhere)
	if ok, _ := g.decide(target); ok {
		t.Fatal("a click on another control answered this button")
	}
	other := click(false, false)
	other.root = 0x4000
	g.record(other)
	if ok, _ := g.decide(target); ok {
		t.Fatal("a click in another window answered")
	}

	key := func(vk uint32, injected bool) realInputEvent {
		return realInputEvent{at: time.Now(), vk: vk, root: dialog, injected: injected}
	}
	g.record(key(vkReturn, true))
	if ok, source := g.decide(target); ok || source != "injected keyboard input" {
		t.Fatalf("an injected Enter answered: %t %q", ok, source)
	}
	g.record(key(vkSpace, false))
	if ok, _ := g.decide(target); !ok {
		t.Fatal("Space on the focused button did not answer")
	}
	unfocused := target
	unfocused.focused = false
	g.record(key(vkSpace, false))
	if ok, _ := g.decide(unfocused); ok {
		t.Fatal("Space answered a button without the focus")
	}
	g.record(key(vkReturn, false))
	if ok, _ := g.decide(unfocused); ok {
		t.Fatal("Enter answered a button that is neither default nor focused")
	}
	unfocused.isDefault = true
	g.record(key(vkReturn, false))
	if ok, _ := g.decide(unfocused); !ok {
		t.Fatal("Enter did not answer the default button")
	}
	labelled := unfocused
	labelled.isDefault, labelled.label = false, "&Allow"
	g.record(key('A', false))
	if ok, _ := g.decide(labelled); !ok {
		t.Fatal("the access key did not answer its button")
	}
	g.record(key('B', false))
	if ok, _ := g.decide(labelled); ok {
		t.Fatal("another letter answered")
	}

	stale := click(false, false)
	stale.at = time.Now().Add(-realInputWindow - time.Second)
	g = &realInputGuard{}
	g.record(stale)
	if ok, _ := g.decide(target); ok {
		t.Fatal("a click older than the window answered")
	}
}

func TestRealInputAccessKey(t *testing.T) {
	for label, want := range map[string]rune{"&Allow": 'A', "Not &now": 'N', "Tom && Jerry": 0, "Allow": 0, "A&&&b": 'B', "&1 hour": '1', "x&": 0} {
		if got := accessKey(label); got != want {
			t.Errorf("accessKey(%q) = %q, want %q", label, got, want)
		}
	}
}

// Windows supplies lParam as an address in native memory.
func dispatchNativeHook[T msllHookStruct | kbdllHookStruct](t *testing.T, hook func(int, uintptr, uintptr) uintptr, msg uintptr, info T) {
	t.Helper()
	data, err := windows.VirtualAlloc(0, unsafe.Sizeof(info), windows.MEM_RESERVE|windows.MEM_COMMIT, windows.PAGE_READWRITE)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := windows.VirtualFree(data, 0, windows.MEM_RELEASE); err != nil {
			t.Error(err)
		}
	}()
	*(*T)(unsafe.Pointer(data)) = info
	hook(0, msg, data)
}

// The hook procedures note the injected flags Windows sets and keep no key
// a person types into a field.
func TestRealInputHooksNoteFlags(t *testing.T) {
	g := &realInputGuard{kind: "rights.first_use"}
	activeRealInput.Store(g)
	t.Cleanup(func() { activeRealInput.CompareAndSwap(g, nil) })
	mouse := func(msg uintptr, flags uint32) {
		dispatchNativeHook(t, inputMouseHook, msg, msllHookStruct{X: 1, Y: 1, Flags: flags})
	}
	key := func(msg uintptr, vk, flags uint32) {
		dispatchNativeHook(t, inputKeyHook, msg, kbdllHookStruct{VK: vk, Flags: flags})
	}
	mouse(0x0201, 0)                  // WM_LBUTTONDOWN: not noted
	mouse(0x0200, 0)                  // WM_MOUSEMOVE: not noted
	key(wmKeyDown, 'S', 0)            // a letter without Alt: never kept
	key(wmKeyDown, 0x09, 0)           // Tab: not noted
	mouse(wmLButtonUp, llmhfInjected) // SendInput click
	mouse(wmLButtonUp, llmhfInjected|llmhfLowerILInject)
	key(wmKeyDown, vkReturn, llkhfInjected) // SendInput Enter
	key(wmKeyUp, vkSpace, 0)                // a person's Space
	key(wmSysKeyDown, 'A', llkhfAltDown)    // a person's Alt+A
	mouse(wmLButtonUp, 0)                   // a person's click
	mouse(wmRButtonUp, 0)                   // a person's click with swapped buttons
	if g.next != 7 {
		t.Fatalf("noted %d events, want 7", g.next)
	}
	want := []struct {
		mouse             bool
		vk                uint32
		injected, lowerIL bool
	}{{true, 0, true, false}, {true, 0, true, true}, {false, vkReturn, true, false}, {false, vkSpace, false, false}, {false, 'A', false, false}, {true, 0, false, false}, {true, 0, false, false}}
	for i, w := range want {
		e := g.events[i]
		if e.mouse != w.mouse || e.vk != w.vk || e.injected != w.injected || e.lowerIL != w.lowerIL {
			t.Errorf("event %d = %+v, want %+v", i, e, w)
		}
	}
}

// The hooks install on their own thread and come off again.
func TestRealInputHooksInstall(t *testing.T) {
	stop, err := guardRealInput("rights.first_use")
	if err != nil {
		t.Fatal(err)
	}
	if activeRealInput.Load() == nil {
		t.Fatal("no guard while the hooks are installed")
	}
	stop()
	if activeRealInput.Load() != nil {
		t.Fatal("the guard outlived its hooks")
	}
}

// Opt-in with OA_REAL_INPUT_FLAGS=1: the flags this Windows build sets on
// SendInput, read by low-level hooks like the presenter's. It moves the
// mouse one pixel right and back, and releases F24, which no key press
// preceded. Windows drops a zero-distance move without passing it to hooks.
func TestRealInputFlagsOnThisBuild(t *testing.T) {
	if os.Getenv("OA_REAL_INPUT_FLAGS") == "" {
		t.Skip("OA_REAL_INPUT_FLAGS=1 reads the injected flags of one harmless SendInput")
	}
	const mark = 0x0a5e1
	var mouseFlags, keyFlags atomic.Int64
	mouseFlags.Store(-1)
	keyFlags.Store(-1)
	mouseHook := syscall.NewCallback(func(code int, wparam, lparam uintptr) uintptr {
		if info := (*msllHookStruct)(unsafe.Pointer(lparam)); code == 0 && info.ExtraInfo == mark {
			mouseFlags.Store(int64(info.Flags))
		}
		r, _, _ := inputNextHook.Call(0, uintptr(code), wparam, lparam)
		return r
	})
	keyHook := syscall.NewCallback(func(code int, wparam, lparam uintptr) uintptr {
		if info := (*kbdllHookStruct)(unsafe.Pointer(lparam)); code == 0 && info.ExtraInfo == mark {
			keyFlags.Store(int64(info.Flags))
		}
		r, _, _ := inputNextHook.Call(0, uintptr(code), wparam, lparam)
		return r
	})
	thread := make(chan uint32, 1)
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer close(done)
		var msg [8]uint64
		inputPeekMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0, 0)
		instance, _, _ := getModuleHandle.Call(0)
		m, _, _ := inputSetHook.Call(whMouseLL, mouseHook, instance, 0)
		k, _, _ := inputSetHook.Call(whKeyboardLL, keyHook, instance, 0)
		defer inputUnhook.Call(m)
		defer inputUnhook.Call(k)
		thread <- windows.GetCurrentThreadId()
		for {
			if r, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&msg[0])), 0, 0, 0); int32(r) <= 0 {
				return
			}
		}
	}()
	tid := <-thread
	defer func() { inputPostThread.Call(uintptr(tid), wmQuit, 0, 0); <-done }()
	type mouseInput struct {
		Type      uint32
		_         [unsafe.Sizeof(uintptr(0)) - 4]byte
		DX, DY    int32
		MouseData uint32
		Flags     uint32
		Time      uint32
		Extra     uintptr
	}
	type keyInput struct {
		Type     uint32
		_        [unsafe.Sizeof(uintptr(0)) - 4]byte
		VK, Scan uint16
		Flags    uint32
		Time     uint32
		Extra    uintptr
		_        [8]byte
	}
	// INPUT is a type and a pointer-aligned union the size of MOUSEINPUT;
	// both records are laid out and padded to that size.
	send := user32.NewProc("SendInput")
	moves := [2]mouseInput{{Type: 0, DX: 1, Flags: 0x0001, Extra: mark}, {Type: 0, DX: -1, Flags: 0x0001, Extra: mark}} // MOUSEEVENTF_MOVE
	if unsafe.Sizeof(moves[0]) != unsafe.Sizeof(keyInput{}) {
		t.Fatalf("INPUT records differ: mouse %d, key %d", unsafe.Sizeof(moves[0]), unsafe.Sizeof(keyInput{}))
	}
	if n, _, err := send.Call(2, uintptr(unsafe.Pointer(&moves[0])), unsafe.Sizeof(moves[0])); n != 2 {
		t.Fatalf("SendInput mouse: %v", err)
	}
	k := keyInput{Type: 1, VK: 0x87, Flags: 0x0002, Extra: mark} // VK_F24, KEYEVENTF_KEYUP
	if n, _, err := send.Call(1, uintptr(unsafe.Pointer(&k)), unsafe.Sizeof(k)); n != 1 {
		t.Fatalf("SendInput key: %v", err)
	}
	for deadline := time.Now().Add(2 * time.Second); (mouseFlags.Load() < 0 || keyFlags.Load() < 0) && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
	}
	v := windowsVersion()
	t.Logf("Windows %d.%d build %d: SendInput mouse flags %#x, key flags %#x", v.MajorVersion, v.MinorVersion, v.BuildNumber, mouseFlags.Load(), keyFlags.Load())
	if mouseFlags.Load() < 0 || mouseFlags.Load()&llmhfInjected == 0 {
		t.Errorf("SendInput mouse move not flagged injected: %#x", mouseFlags.Load())
	}
	if keyFlags.Load() < 0 || keyFlags.Load()&llkhfInjected == 0 {
		t.Errorf("SendInput key not flagged injected: %#x", keyFlags.Load())
	}
}

// onPromptDesktop runs f on an OS thread bound to the private desktop. A
// thread the runtime reuses may already own windows or hooks on the
// person's desktop, which SetThreadDesktop refuses; such a thread is
// retired (its goroutine exits locked) and a fresh one tried.
func onPromptDesktop(t *testing.T, desktop uintptr, f func()) chan struct{} {
	done := make(chan struct{})
	go func() {
		defer close(done)
		for attempt := 0; attempt < 16; attempt++ {
			bound := make(chan bool)
			go func() {
				runtime.LockOSThread()
				if ok, _, _ := renderSetThreadDesktop.Call(desktop); ok == 0 {
					bound <- false
					return
				}
				bound <- true
				f()
				bound <- true
			}()
			if <-bound {
				<-bound
				return
			}
		}
		t.Errorf("no thread could join the private desktop")
	}()
	return done
}

// guardedFixture is a first-use permission prompt as the presenter shows it:
// Allow and Not now, Not now the default.
func guardedFixture(realInputOnly bool) QuestionPrompt {
	empty := ""
	return QuestionPrompt{
		Title:       "OpenAbstractions",
		Instruction: "Allow Example to read your saved pictures?",
		Content:     "Example wants to read your saved pictures.",
		Choices:     []QuestionChoice{{Label: "Allow", Value: "allow"}, {Label: "Not now", Value: "refuse"}},
		Default:     "refuse",
		Dismiss:     "refuse",
		Permission:  true,
		PermissionView: &PermissionView{Question: "Allow Example to read your saved pictures?", Requester: "Example",
			Publisher: "Publisher not verified", Scope: "Read your saved pictures.", Warning: &empty},
		Level:         PromptLevelGray,
		RealInputOnly: realInputOnly,
		Kind:          "rights.first_use",
	}
}

type openPrompt struct {
	hwnd   uintptr
	answer chan string
}

// openGuarded shows prompt through ShowQuestion on the private desktop and
// waits until its answers are armed.
func openGuarded(t *testing.T, desktop uintptr, prompt QuestionPrompt) openPrompt {
	t.Helper()
	activePermissionDialog.Store(0)
	p := openPrompt{answer: make(chan string, 1)}
	onPromptDesktop(t, desktop, func() {
		defer withPanelManifest(t)()
		if permissionThreadDPI.Find() == nil {
			permissionThreadDPI.Call(^uintptr(3))
		}
		option, err := ShowQuestion(Occurrence{ID: "fixture-real-input", Prompt: prompt})
		if err != nil {
			option = "error: " + err.Error()
		}
		p.answer <- option
	})
	for deadline := time.Now().Add(5 * time.Second); p.hwnd == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		p.hwnd = activePermissionDialog.Load()
	}
	if p.hwnd == 0 {
		select {
		case option := <-p.answer:
			t.Fatalf("prompt did not appear: %s", option)
		default:
		}
		t.Fatal("prompt did not appear")
	}
	time.Sleep(600 * time.Millisecond) // past the 500 ms arming
	armed(t, p.hwnd, p.button(firstQuestionButton))
	return p
}

// armed waits for the prompt's answers to arm. Windows 11 throttles the
// timers of windows nobody can see, which every window on a private desktop
// is; past the arming delay the test delivers the arming timer tick itself.
func armed(t *testing.T, dialog, button uintptr) {
	t.Helper()
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if enabled, _, _ := permissionIsEnabled.Call(button); enabled != 0 {
			return
		}
	}
	sendMessage.Call(dialog, permissionWMTimer, permissionTimerID, 0)
	if enabled, _, _ := permissionIsEnabled.Call(button); enabled == 0 {
		t.Fatal("the answers did not arm")
	}
}

// outcome waits briefly for an answer; an open prompt is closed with Cancel
// afterwards, which answers its Dismiss choice.
func (p openPrompt) outcome(t *testing.T) (string, bool) {
	t.Helper()
	select {
	case option := <-p.answer:
		return option, true
	case <-time.After(1200 * time.Millisecond):
		sendMessage.Call(p.hwnd, permissionWMClose, 0, 0)
		select {
		case <-p.answer:
		case <-time.After(5 * time.Second):
			t.Fatal("the prompt did not close")
		}
		return "", false
	}
}

func (p openPrompt) button(id int) uintptr {
	h, _, _ := permissionGetItem.Call(p.hwnd, uintptr(id))
	return h
}

// feedMouse hands the production mouse hook a left-button release at the
// centre of button, as Windows would for a click there, with flags.
func feedMouse(t *testing.T, button uintptr, flags uint32) {
	if permissionThreadDPI.Find() == nil { // physical coordinates, as the hook receives them
		previous, _, _ := permissionThreadDPI.Call(^uintptr(3))
		defer permissionThreadDPI.Call(previous)
	}
	var r rect
	getWindowRect.Call(button, uintptr(unsafe.Pointer(&r)))
	info := msllHookStruct{X: (r.Left + r.Right) / 2, Y: (r.Top + r.Bottom) / 2, Flags: flags}
	dispatchNativeHook(t, inputMouseHook, wmLButtonUp, info)
}

// feedKey hands the production keyboard hook one key press with flags while
// dialog has the foreground. A private desktop never has the foreground, so
// the test supplies it for this one call.
func feedKey(t *testing.T, dialog uintptr, vk uint32, flags uint32) {
	previous := inputForeground
	inputForeground = func() uintptr { return dialog }
	defer func() { inputForeground = previous }()
	info := kbdllHookStruct{VK: vk, Flags: flags}
	dispatchNativeHook(t, inputKeyHook, wmKeyDown, info)
}

// focusButton moves the keyboard focus to button, as Tab does.
func focusButton(dialog, button uintptr) {
	sendMessage.Call(dialog, 0x0028, button, 1) // WM_NEXTDLGCTL to that control
}

// pressKey delivers a key press to button, which holds the focus, as the
// input queue does.
func pressKey(button uintptr, vk uintptr) {
	postMessage.Call(button, wmKeyDown, vk, 0)
	postMessage.Call(button, wmKeyUp, vk, 0xC0000000)
}

const bmClick = 0x00F5

// Every way a program on the person's desktop could answer an
// authority-granting prompt without the person: SendInput clicks and keys
// (their hook events carry the injected flag), BM_CLICK, a WM_COMMAND from
// another program, a posted Enter, and UI Automation Invoke. The prompt
// stays open after each, and each is logged once. The same prompt without
// RealInputOnly answers to every one of them, so each attack is live.
func TestRealInputIgnoresProgrammaticAnswers(t *testing.T) {
	if !modernPermissionFor(QuestionPrompt{Permission: true}, windowsVersion()) {
		t.Skip("needs the Windows 11 renderer")
	}
	countPermissionChimes(t)
	desktop := newRenderDesktop(t)
	var logged bytes.Buffer
	log.SetOutput(&logged)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })
	allow := firstQuestionButton
	attacks := []struct {
		name   string
		source string
		answer string // the unguarded prompt's answer
		run    func(p openPrompt)
	}{
		{"SendInput click", "injected mouse input", "allow", func(p openPrompt) {
			feedMouse(t, p.button(allow), llmhfInjected)
			sendMessage.Call(p.button(allow), bmClick, 0, 0)
		}},
		{"SendInput click from a lower-integrity program", "injected mouse input from a lower-integrity program", "allow", func(p openPrompt) {
			feedMouse(t, p.button(allow), llmhfInjected|llmhfLowerILInject)
			sendMessage.Call(p.button(allow), bmClick, 0, 0)
		}},
		{"SendInput Enter", "injected keyboard input", "allow", func(p openPrompt) {
			focusButton(p.hwnd, p.button(allow))
			feedKey(t, p.hwnd, vkReturn, llkhfInjected)
			pressKey(p.button(allow), vkReturn)
		}},
		{"SendInput Space", "injected keyboard input", "allow", func(p openPrompt) {
			focusButton(p.hwnd, p.button(allow))
			feedKey(t, p.hwnd, vkSpace, llkhfInjected)
			pressKey(p.button(allow), vkSpace)
		}},
		{"BM_CLICK", noRealInputSource, "allow", func(p openPrompt) {
			sendMessage.Call(p.button(allow), bmClick, 0, 0)
		}},
		{"WM_COMMAND", noRealInputSource, "allow", func(p openPrompt) {
			postMessage.Call(p.hwnd, wmCommand, uintptr(allow), p.button(allow))
		}},
		{"posted Enter", noRealInputSource, "allow", func(p openPrompt) {
			focusButton(p.hwnd, p.button(allow))
			pressKey(p.button(allow), vkReturn)
		}},
		{"UI Automation Invoke", noRealInputSource, "allow", func(p openPrompt) {
			if err := uiaInvoke(p.button(allow)); err != nil {
				t.Errorf("UI Automation Invoke: %v", err)
			}
		}},
	}
	for _, attack := range attacks {
		t.Run(attack.name, func(t *testing.T) {
			open := openGuarded(t, desktop, guardedFixture(false))
			<-onPromptDesktop(t, desktop, func() { attack.run(open) })
			if option, answered := open.outcome(t); !answered || option != attack.answer {
				t.Fatalf("without the guard the attack answered %q (answered=%t), want %q: the attack is not live", option, answered, attack.answer)
			}
			logged.Reset()
			open = openGuarded(t, desktop, guardedFixture(true))
			<-onPromptDesktop(t, desktop, func() { attack.run(open) })
			if option, answered := open.outcome(t); answered {
				t.Fatalf("the guarded prompt answered %q", option)
			}
			want := "native question: ignored an answer to a rights.first_use prompt; it came from " + attack.source
			if got := strings.Count(logged.String(), want); got != 1 {
				t.Fatalf("log %q: want one %q", logged.String(), want)
			}
		})
	}
}

// A person's own click, Enter and Space, noted by the hooks without the
// injected flag, answer the guarded prompt.
func TestRealInputAnswersFromThePerson(t *testing.T) {
	if !modernPermissionFor(QuestionPrompt{Permission: true}, windowsVersion()) {
		t.Skip("needs the Windows 11 renderer")
	}
	countPermissionChimes(t)
	desktop := newRenderDesktop(t)
	allow := firstQuestionButton
	for _, person := range []struct {
		name, answer string
		run          func(p openPrompt)
	}{
		{"click Allow", "allow", func(p openPrompt) {
			feedMouse(t, p.button(allow), 0)
			sendMessage.Call(p.button(allow), bmClick, 0, 0)
		}},
		{"Tab to Allow, Enter", "allow", func(p openPrompt) {
			focusButton(p.hwnd, p.button(allow))
			feedKey(t, p.hwnd, vkReturn, 0)
			pressKey(p.button(allow), vkReturn)
		}},
		{"Tab to Allow, Space", "allow", func(p openPrompt) {
			focusButton(p.hwnd, p.button(allow))
			feedKey(t, p.hwnd, vkSpace, 0)
			pressKey(p.button(allow), vkSpace)
		}},
		{"Tab to Not now, Enter", "refuse", func(p openPrompt) {
			focusButton(p.hwnd, p.button(allow+1))
			feedKey(t, p.hwnd, vkReturn, 0)
			pressKey(p.button(allow+1), vkReturn)
		}},
	} {
		t.Run(person.name, func(t *testing.T) {
			open := openGuarded(t, desktop, guardedFixture(true))
			<-onPromptDesktop(t, desktop, func() { person.run(open) })
			if option, answered := open.outcome(t); !answered || option != person.answer {
				t.Fatalf("the person's %s answered %q (answered=%t), want %q", person.name, option, answered, person.answer)
			}
		})
	}
}

// The TaskDialog the Windows 11 layout falls back to keeps the guard:
// TDM_CLICK_BUTTON and BM_CLICK from a program are ignored, a person's
// click answers.
func TestRealInputTaskDialog(t *testing.T) {
	countPermissionChimes(t)
	desktop := newRenderDesktop(t)
	// comctl32 binds once per process: version 6, which has TaskDialog, only
	// when its first load happens under the Panel's manifest. Another test
	// of this package may load version 5 first.
	var missing error
	<-onPromptDesktop(t, desktop, func() { defer withPanelManifest(t)(); missing = taskDialogIndirect.Find() })
	if missing != nil {
		t.Skipf("comctl32 version 6 is not loaded in this test process (run -run TestRealInputTaskDialog alone): %v", missing)
	}
	show := func(prompt QuestionPrompt) (uintptr, chan string) {
		activeTaskDialogHwnd.Store(0)
		answer := make(chan string, 1)
		onPromptDesktop(t, desktop, func() {
			defer withPanelManifest(t)()
			if prompt.RealInputOnly {
				stop, err := guardRealInput(prompt.Kind)
				if err != nil {
					answer <- "error: " + err.Error()
					return
				}
				defer stop()
			}
			prompt.chime = newQuestionChime(prompt)
			option, err := showTaskDialogQuestion(prompt)
			if err != nil {
				option = "error: " + err.Error()
			}
			answer <- option
		})
		var hwnd uintptr
		for deadline := time.Now().Add(5 * time.Second); hwnd == 0 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			hwnd = activeTaskDialogHwnd.Load()
		}
		if hwnd == 0 {
			select {
			case option := <-answer:
				t.Fatalf("TaskDialog did not appear: %s", option)
			default:
			}
			t.Fatal("TaskDialog did not appear")
		}
		// The TaskDialog arms on its own timer, which Windows throttles for
		// a window nobody can see.
		for deadline := time.Now().Add(10 * time.Second); !questionArmed.Load() && time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		}
		if !questionArmed.Load() {
			t.Fatal("the TaskDialog's answers did not arm")
		}
		return hwnd, answer
	}
	wait := func(hwnd uintptr, answer chan string) (string, bool) {
		select {
		case option := <-answer:
			return option, true
		case <-time.After(1200 * time.Millisecond):
			sendMessage.Call(hwnd, tdmClickButton, idcCancel, 0)
			<-answer
			return "", false
		}
	}
	allowButton := func(hwnd uintptr) (h uintptr) {
		<-onPromptDesktop(t, desktop, func() { h = findTaskDialogButton(hwnd, "Allow") })
		if h == 0 {
			t.Fatal("no Allow button in the TaskDialog")
		}
		return h
	}

	hwnd, answer := show(guardedFixture(false))
	<-onPromptDesktop(t, desktop, func() { sendMessage.Call(hwnd, tdmClickButton, uintptr(firstQuestionButton), 0) })
	if option, answered := wait(hwnd, answer); !answered || option != "allow" {
		t.Fatalf("unguarded TDM_CLICK_BUTTON answered %q (%t): the attack is not live", option, answered)
	}
	for _, attack := range []struct {
		name string
		run  func(hwnd uintptr)
	}{
		{"TDM_CLICK_BUTTON", func(hwnd uintptr) { sendMessage.Call(hwnd, tdmClickButton, uintptr(firstQuestionButton), 0) }},
		{"BM_CLICK", func(hwnd uintptr) { sendMessage.Call(findTaskDialogButton(hwnd, "Allow"), bmClick, 0, 0) }},
		{"SendInput click", func(hwnd uintptr) {
			b := findTaskDialogButton(hwnd, "Allow")
			feedMouse(t, b, llmhfInjected)
			sendMessage.Call(b, bmClick, 0, 0)
		}},
	} {
		hwnd, answer := show(guardedFixture(true))
		<-onPromptDesktop(t, desktop, func() { attack.run(hwnd) })
		if option, answered := wait(hwnd, answer); answered {
			t.Fatalf("%s answered the guarded TaskDialog with %q", attack.name, option)
		}
	}
	hwnd, answer = show(guardedFixture(true))
	button := allowButton(hwnd)
	<-onPromptDesktop(t, desktop, func() {
		feedMouse(t, button, 0)
		sendMessage.Call(button, bmClick, 0, 0)
	})
	if option, answered := wait(hwnd, answer); !answered || option != "allow" {
		t.Fatalf("a person's click on the TaskDialog answered %q (%t)", option, answered)
	}
}

var (
	uiaOle32            = windows.NewLazySystemDLL("ole32.dll")
	uiaCoInitialize     = uiaOle32.NewProc("CoInitializeEx")
	uiaCoUninit         = uiaOle32.NewProc("CoUninitialize")
	uiaCoCreate         = uiaOle32.NewProc("CoCreateInstance")
	clsidCUIAutomation  = windows.GUID{Data1: 0xff48dba4, Data2: 0x60ef, Data3: 0x4201, Data4: [8]byte{0xaa, 0x87, 0x54, 0x10, 0x3e, 0xef, 0x59, 0x4e}}
	iidIUIAutomation    = windows.GUID{Data1: 0x30cbe57d, Data2: 0xd9d0, Data3: 0x452a, Data4: [8]byte{0xab, 0x13, 0x7a, 0xc5, 0xac, 0x48, 0x25, 0xee}}
	iidIUIAutomationInv = windows.GUID{Data1: 0xfb377fbe, Data2: 0x8ea6, Data3: 0x46d5, Data4: [8]byte{0x9c, 0x73, 0x64, 0x99, 0x64, 0x2d, 0x30, 0x59}}
)

// comMethod is slot n of the COM object's vtable.
func comMethod(object uintptr, n int) comProc {
	vtable := *(*uintptr)(unsafe.Pointer(object))
	return *(*comProc)(unsafe.Pointer(vtable + uintptr(n)*unsafe.Sizeof(uintptr(0))))
}

// uiaInvoke does what an automation client does to press a button:
// IUIAutomation.ElementFromHandle, then the element's Invoke pattern. It
// runs on the calling thread, which is on the prompt's desktop.
func uiaInvoke(button uintptr) error {
	if hr, _, _ := uiaCoInitialize.Call(0, 0); int32(hr) < 0 { // COINIT_MULTITHREADED: no window on this desktop thread
		return syscall.Errno(hr)
	}
	defer uiaCoUninit.Call()
	var automation uintptr
	if hr, _, _ := uiaCoCreate.Call(uintptr(unsafe.Pointer(&clsidCUIAutomation)), 0, 1 /* CLSCTX_INPROC_SERVER */, uintptr(unsafe.Pointer(&iidIUIAutomation)), uintptr(unsafe.Pointer(&automation))); int32(hr) < 0 {
		return syscall.Errno(hr)
	}
	defer comMethod(automation, 2).call(automation)
	var element uintptr
	if hr := comMethod(automation, 6).call(automation, button, uintptr(unsafe.Pointer(&element))); int32(hr) < 0 || element == 0 { // ElementFromHandle
		return syscall.Errno(hr)
	}
	defer comMethod(element, 2).call(element)
	var invoke uintptr
	if hr := comMethod(element, 14).call(element, 10000, uintptr(unsafe.Pointer(&iidIUIAutomationInv)), uintptr(unsafe.Pointer(&invoke))); int32(hr) < 0 || invoke == 0 { // GetCurrentPatternAs(UIA_InvokePatternId)
		return syscall.Errno(hr)
	}
	defer comMethod(invoke, 2).call(invoke)
	if hr := comMethod(invoke, 3).call(invoke); int32(hr) < 0 { // Invoke
		return syscall.Errno(hr)
	}
	return nil
}
