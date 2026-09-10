package win

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	gdi32    = syscall.NewLazyDLL("gdi32.dll")
	comctl32 = syscall.NewLazyDLL("comctl32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	registerClass       = user32.NewProc("RegisterClassExW")
	createWindow        = user32.NewProc("CreateWindowExW")
	defWindowProc       = user32.NewProc("DefWindowProcW")
	getMessage          = user32.NewProc("GetMessageW")
	translateMessage    = user32.NewProc("TranslateMessage")
	dispatchMessage     = user32.NewProc("DispatchMessageW")
	isDialogMessage     = user32.NewProc("IsDialogMessageW")
	postQuit            = user32.NewProc("PostQuitMessage")
	postMessage         = user32.NewProc("PostMessageW")
	sendMessage         = user32.NewProc("SendMessageW")
	showWindow          = user32.NewProc("ShowWindow")
	moveWindow          = user32.NewProc("MoveWindow")
	getClientRect       = user32.NewProc("GetClientRect")
	getWindowRect       = user32.NewProc("GetWindowRect")
	loadCursor          = user32.NewProc("LoadCursorW")
	loadIcon            = user32.NewProc("LoadIconW")
	setWindowText       = user32.NewProc("SetWindowTextW")
	getWindowText       = user32.NewProc("GetWindowTextW")
	getWindowTextLength = user32.NewProc("GetWindowTextLengthW")
	setFocus            = user32.NewProc("SetFocus")
	messageBox          = user32.NewProc("MessageBoxW")
	dpiAware            = user32.NewProc("SetProcessDPIAware")
	dpiAwareness        = user32.NewProc("SetProcessDpiAwarenessContext")
	dpiForWindow        = user32.NewProc("GetDpiForWindow")

	createSolidBrush = gdi32.NewProc("CreateSolidBrush")
	createFont       = gdi32.NewProc("CreateFontW")
	setBkColor       = gdi32.NewProc("SetBkColor")
	setTextColor     = gdi32.NewProc("SetTextColor")

	initCommonControls = comctl32.NewProc("InitCommonControlsEx")
	getModuleHandle    = kernel32.NewProc("GetModuleHandleW")
	getConsoleWindow   = kernel32.NewProc("GetConsoleWindow")
)

const (
	wsOverlappedWindow = 0x00CF0000
	wsChild            = 0x40000000
	wsVisible          = 0x10000000
	wsTabStop          = 0x00010000
	wsBorder           = 0x00800000
	wsExControlParent  = 0x00010000
	cwUseDefault       = 0x80000000

	wmDestroy        = 0x0002
	wmSize           = 0x0005
	wmSetFont        = 0x0030
	wmSetText        = 0x000C
	wmCommand        = 0x0111
	wmCtlColorEdit   = 0x0133
	wmCtlColorStatic = 0x0138
	wmApp            = 0x8000
	wmQueued         = wmApp + 1

	swShowNormal = 1
	redraw       = 1

	idcArrow           = 32512
	idiApplication     = 32512
	mbIconInformation  = 0x40
	iccListViewClasses = 0x0001

	defaultCharSet   = 1
	clearTypeQuality = 5

	ssLeft        = 0x0000
	ssNoPrefix    = 0x0080
	esAutoHScroll = 0x0080

	lvmFirst          = 0x1000
	lvmDeleteAllItems = lvmFirst + 9
	lvmInsertColumn   = lvmFirst + 97
	lvmInsertItem     = lvmFirst + 77
	lvmSetItemText    = lvmFirst + 116
	lvmSetItemState   = lvmFirst + 43
	lvmGetNextItem    = lvmFirst + 12
	lvmSetExStyle     = lvmFirst + 54

	lvsReport        = 0x0001
	lvsSingleSel     = 0x0004
	lvsShowSelAlways = 0x0008
	lvsNoSortHeader  = 0x8000

	lvsExFullRowSelect = 0x0020
	lvsExDoubleBuffer  = 0x00010000

	lvifText     = 0x0001
	lvifState    = 0x0008
	lvisSelected = 0x0002
	lvisFocused  = 0x0001
	lvniSelected = 0x0002

	lvcfWidth   = 0x0002
	lvcfText    = 0x0004
	lvcfSubItem = 0x0008

	inkText = 0x00202020
	inkDim  = 0x00606060
	paper   = 0x00FFFFFF
)

const (
	// LVM_GETNEXTITEM searches after the item it is given, so -1 starts at the top.
	beforeFirstItem = ^uintptr(0)

	// DPI_AWARENESS_CONTEXT_SYSTEM_AWARE, which the header spells as the
	// pointer-shaped constant -2.
	dpiContextSystemAware = ^uintptr(1)
)

// The five structs below are the C structs the calls above take. Their field
// order and types are transcribed from the Windows headers and checked against
// them, on both the 32- and 64-bit layouts, by layout_windows_test.go. Go's own
// alignment reproduces the Microsoft layout exactly, so none of them carries
// hand-written padding — adding some breaks the 32-bit build silently, which is
// what the test exists to catch.

type wndClassEx struct {
	Size, Style                   uint32
	WndProc                       uintptr
	ClsExtra, WndExtra            int32
	Instance, Icon, Cursor, Brush syscall.Handle
	MenuName, ClassName           *uint16
	IconSm                        syscall.Handle
}

type message struct {
	Hwnd   syscall.Handle
	Msg    uint32
	WParam uintptr
	LParam uintptr
	Time   uint32
	X, Y   int32
}

type rect struct{ Left, Top, Right, Bottom int32 }

type lvColumn struct {
	Mask      uint32
	Fmt, CX   int32
	Text      *uint16
	TextMax   int32
	SubItem   int32
	Image     int32
	Order     int32
	CXMin     int32
	CXDefault int32
	CXIdeal   int32
}

type lvItem struct {
	Mask             uint32
	Item, SubItem    int32
	State, StateMask uint32
	Text             *uint16
	TextMax          int32
	Image            int32
	Param            uintptr
	Indent           int32
	GroupID          int32
	Columns          uint32
	PColumns         uintptr
	PColFmt          uintptr
	Group            int32
}

type initCommonControlsEx struct{ Size, Classes uint32 }

var (
	live   sync.Mutex
	byHwnd = map[syscall.Handle]*Window{}
	proc   = syscall.NewCallback(dispatch)

	sharedPanel = sync.OnceValues(register)
)

// panel is what every window of ours is made from, and all of it is
// process-wide: a window class may be registered only once, the dpi mode may be
// chosen only before the first window exists, and one background brush serves
// every window that asks.
type panel struct {
	class    *uint16
	brush    syscall.Handle
	instance uintptr
}

func register() (panel, error) {
	makeDPIAware()

	icc := initCommonControlsEx{Size: uint32(unsafe.Sizeof(initCommonControlsEx{})), Classes: iccListViewClasses}
	initCommonControls.Call(uintptr(unsafe.Pointer(&icc)))

	inst, _, _ := getModuleHandle.Call(0)
	cursor, _, _ := loadCursor.Call(0, idcArrow)
	icon, _, _ := loadIcon.Call(0, idiApplication)
	brush, _, _ := createSolidBrush.Call(paper)
	p := panel{class: utf16("abstraction.panel"), brush: syscall.Handle(brush), instance: inst}

	wc := wndClassEx{
		Size: uint32(unsafe.Sizeof(wndClassEx{})), WndProc: proc,
		Instance: syscall.Handle(inst), Icon: syscall.Handle(icon),
		Cursor: syscall.Handle(cursor), Brush: p.brush, ClassName: p.class,
		IconSm: syscall.Handle(icon),
	}
	if r, _, err := registerClass.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		return panel{}, fmt.Errorf("register window class: %w", err)
	}
	return p, nil
}

func makeDPIAware() {
	if dpiAwareness.Find() == nil {
		if ok, _, _ := dpiAwareness.Call(dpiContextSystemAware); ok != 0 {
			return
		}
	}
	dpiAware.Call()
}

// Window is one top-level window and everything drawn in it.
type Window struct {
	h        syscall.Handle
	instance uintptr
	dpi      int
	body     syscall.Handle
	head     syscall.Handle
	brush    syscall.Handle

	next  int32
	click map[int32]func()
	tint  map[syscall.Handle]uint32
	size  func(w, h int)

	mu     sync.Mutex
	queued []func()
}

// Run opens the window, hands it to build, and pumps its messages until it
// closes. Size is given at 96 dpi and scaled to this machine's.
//
// The window, its controls and its message loop all live on one OS thread —
// Windows gives a handle to the thread that created it and GetMessage only ever
// sees that thread's queue. Holding the whole life of the window inside a single
// locked call is what makes that true without anyone having to remember it, so
// there is no way to open a window here and pump it somewhere else.
//
// Work that must not block the window goes to Do.
func Run(title string, width, height int, build func(*Window)) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	w, err := open(title, width, height)
	if err != nil {
		return err
	}
	defer forget(w.h)

	build(w)
	w.pump()
	return nil
}

func open(title string, width, height int) (*Window, error) {
	p, err := sharedPanel()
	if err != nil {
		return nil, err
	}

	hwnd, _, err := createWindow.Call(wsExControlParent,
		uintptr(unsafe.Pointer(p.class)), uintptr(unsafe.Pointer(utf16(title))),
		wsOverlappedWindow, cwUseDefault, cwUseDefault, uintptr(width), uintptr(height),
		0, 0, p.instance, 0)
	if hwnd == 0 {
		return nil, fmt.Errorf("create window: %w", err)
	}

	w := &Window{h: syscall.Handle(hwnd), instance: p.instance, dpi: 96, brush: p.brush,
		click: map[int32]func(){}, tint: map[syscall.Handle]uint32{}, next: 100}
	live.Lock()
	byHwnd[w.h] = w
	live.Unlock()

	// The window's own dpi, not the system's. A process Windows refused to make
	// dpi-aware is handed a 96-dpi coordinate space and the picture is stretched
	// for it afterwards; asking the system instead would scale the layout a
	// second time, inside a space that was never enlarged. This machine draws
	// that exactly: a 150% display, a layout 1.5x too big, and a window still
	// 800 units wide.
	if dpiForWindow.Find() == nil {
		if d, _, _ := dpiForWindow.Call(hwnd); d >= 96 {
			w.dpi = int(d)
		}
	}
	w.body = w.font(9, 400)
	w.head = w.font(12, 600)
	w.client(width, height)
	return w, nil
}

func forget(h syscall.Handle) {
	live.Lock()
	delete(byHwnd, h)
	live.Unlock()
}

func (w *Window) pump() {
	showWindow.Call(uintptr(w.h), swShowNormal)
	if w.size != nil {
		w.resized()
	}
	var m message
	for {
		r, _, _ := getMessage.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(r) <= 0 {
			return
		}
		if ok, _, _ := isDialogMessage.Call(uintptr(w.h), uintptr(unsafe.Pointer(&m))); ok != 0 {
			continue
		}
		translateMessage.Call(uintptr(unsafe.Pointer(&m)))
		dispatchMessage.Call(uintptr(unsafe.Pointer(&m)))
	}
}

// client sizes the window so its client area is w by h at this machine's dpi.
func (w *Window) client(cw, ch int) {
	var outer, inner rect
	getWindowRect.Call(uintptr(w.h), uintptr(unsafe.Pointer(&outer)))
	getClientRect.Call(uintptr(w.h), uintptr(unsafe.Pointer(&inner)))
	frame := int(outer.Right-outer.Left) - int(inner.Right)
	title := int(outer.Bottom-outer.Top) - int(inner.Bottom)
	moveWindow.Call(uintptr(w.h), uintptr(outer.Left), uintptr(outer.Top),
		uintptr(w.px(cw)+frame), uintptr(w.px(ch)+title), redraw)
}

func (w *Window) font(pt, weight int) syscall.Handle {
	const (
		autoWidth, noEscapement, noOrientation      = 0, 0, 0
		upright, notUnderlined, notStruckOut        = 0, 0, 0
		defaultPrecision, defaultClipping, anyPitch = 0, 0, 0
	)
	// A negative height asks for that character height; the same number
	// positive is the cell height, which draws visibly larger.
	height := -(pt * w.dpi) / 72
	h, _, _ := createFont.Call(
		uintptr(height), autoWidth, noEscapement, noOrientation, uintptr(weight),
		upright, notUnderlined, notStruckOut,
		defaultCharSet, defaultPrecision, defaultClipping, clearTypeQuality, anyPitch,
		uintptr(unsafe.Pointer(utf16("Segoe UI"))))
	return syscall.Handle(h)
}

func (w *Window) px(n int) int { return n * w.dpi / 96 }

// Do runs f on the thread that owns the window. Every control here is touched
// from that thread only: a handle is owned by the thread that created it, and
// the work behind these buttons reaches a NAS and takes seconds.
func (w *Window) Do(f func()) {
	w.mu.Lock()
	w.queued = append(w.queued, f)
	w.mu.Unlock()
	postMessage.Call(uintptr(w.h), wmQueued, 0, 0)
}

// OnSize lays the controls out. Called once at startup and on every resize,
// with the client area in 96-dpi units.
func (w *Window) OnSize(f func(width, height int)) { w.size = f }

func (w *Window) resized() {
	var r rect
	getClientRect.Call(uintptr(w.h), uintptr(unsafe.Pointer(&r)))
	w.size(int(r.Right)*96/w.dpi, int(r.Bottom)*96/w.dpi)
}

func (w *Window) child(class string, style, exStyle uintptr, text string) *Control {
	w.next++
	h, _, _ := createWindow.Call(exStyle,
		uintptr(unsafe.Pointer(utf16(class))), uintptr(unsafe.Pointer(utf16(text))),
		wsChild|wsVisible|style, 0, 0, 0, 0, uintptr(w.h), uintptr(w.next), w.instance, 0)
	c := &Control{h: syscall.Handle(h), w: w, id: w.next, shown: text}
	sendMessage.Call(h, wmSetFont, uintptr(w.body), redraw)
	return c
}

// Control is anything drawn in the window.
type Control struct {
	h     syscall.Handle
	w     *Window
	id    int32
	shown string
}

// Place positions the control in 96-dpi units.
func (c *Control) Place(x, y, w, h int) {
	moveWindow.Call(uintptr(c.h), uintptr(c.w.px(x)), uintptr(c.w.px(y)),
		uintptr(c.w.px(w)), uintptr(c.w.px(h)), redraw)
}

// SetText writes the control's text, and skips the write when it has not
// changed: this window redraws every second and a control rewritten with what it
// already says still flickers and still loses a selection.
func (c *Control) SetText(s string) {
	if c.shown == s {
		return
	}
	c.shown = s
	sendMessage.Call(uintptr(c.h), wmSetText, 0, uintptr(unsafe.Pointer(utf16(s))))
}

func (c *Control) Text() string {
	n, _, _ := getWindowTextLength.Call(uintptr(c.h))
	if n == 0 {
		return ""
	}
	buf := make([]uint16, n+1)
	getWindowText.Call(uintptr(c.h), uintptr(unsafe.Pointer(&buf[0])), n+1)
	return syscall.UTF16ToString(buf)
}

func (c *Control) Focus() { setFocus.Call(uintptr(c.h)) }

// Head is a section title.
func (w *Window) Head(text string) *Control {
	c := w.child("STATIC", ssLeft|ssNoPrefix, 0, text)
	sendMessage.Call(uintptr(c.h), wmSetFont, uintptr(w.head), redraw)
	w.tint[c.h] = inkText
	return c
}

// Dim is secondary text — a reason, a path, a result. STATIC wraps at the width
// it is given, so it is a line or a paragraph.
func (w *Window) Dim(text string) *Control {
	c := w.child("STATIC", ssLeft|ssNoPrefix, 0, text)
	w.tint[c.h] = inkDim
	return c
}

func (w *Window) Button(text string, do func()) *Control {
	c := w.child("BUTTON", wsTabStop, 0, text)
	w.click[c.id] = do
	return c
}

func (w *Window) Field(text string) *Control {
	return w.child("EDIT", wsTabStop|wsBorder|esAutoHScroll, 0, text)
}

// Column is one column of a List, with its width in 96-dpi units.
type Column struct {
	Title string
	Width int
}

// List is a report-mode list view: the control Windows already has for rows a
// person picks one of.
type List struct {
	*Control
	cols []Column
	rows [][]string
}

func (w *Window) List(cols ...Column) *List {
	c := w.child("SysListView32", wsTabStop|wsBorder|lvsReport|lvsSingleSel|lvsShowSelAlways|lvsNoSortHeader, 0, "")
	sendMessage.Call(uintptr(c.h), lvmSetExStyle, 0, lvsExFullRowSelect|lvsExDoubleBuffer)
	for i, col := range cols {
		lc := lvColumn{Mask: lvcfWidth | lvcfText | lvcfSubItem, CX: int32(w.px(col.Width)),
			Text: utf16(col.Title), SubItem: int32(i)}
		sendMessage.Call(uintptr(c.h), lvmInsertColumn, uintptr(i), uintptr(unsafe.Pointer(&lc)))
	}
	return &List{Control: c, cols: cols}
}

// Set replaces the contents. Rows already there are written cell by cell rather
// than cleared and rebuilt, so a person's selection survives a redraw.
func (l *List) Set(rows [][]string) {
	if same(l.rows, rows) {
		return
	}
	if len(rows) != len(l.rows) {
		sendMessage.Call(uintptr(l.h), lvmDeleteAllItems, 0, 0)
		for i := range rows {
			it := lvItem{Mask: lvifText, Item: int32(i), Text: utf16("")}
			sendMessage.Call(uintptr(l.h), lvmInsertItem, 0, uintptr(unsafe.Pointer(&it)))
		}
		l.rows = make([][]string, len(rows))
	}
	for i, row := range rows {
		for j := range l.cols {
			cell := ""
			if j < len(row) {
				cell = row[j]
			}
			if i < len(l.rows) && j < len(l.rows[i]) && l.rows[i][j] == cell {
				continue
			}
			it := lvItem{Mask: lvifText, Item: int32(i), SubItem: int32(j), Text: utf16(cell)}
			sendMessage.Call(uintptr(l.h), lvmSetItemText, uintptr(i), uintptr(unsafe.Pointer(&it)))
		}
		l.rows[i] = append([]string(nil), row...)
	}
}

// Chosen is the index of the selected row, or -1.
func (l *List) Chosen() int {
	r, _, _ := sendMessage.Call(uintptr(l.h), lvmGetNextItem, beforeFirstItem, lvniSelected)
	return int(int32(r))
}

func (l *List) Choose(i int) {
	it := lvItem{Mask: lvifState, State: lvisSelected | lvisFocused, StateMask: lvisSelected | lvisFocused}
	sendMessage.Call(uintptr(l.h), lvmSetItemState, uintptr(i), uintptr(unsafe.Pointer(&it)))
}

func same(a, b [][]string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

// Say shows a message box. Used for what a window cannot recover from.
func Say(title, body string) {
	messageBox.Call(0, uintptr(unsafe.Pointer(utf16(body))),
		uintptr(unsafe.Pointer(utf16(title))), mbIconInformation)
}

// Windowed reports that this process has no console, which is what a build for
// the desktop looks like: a GUI-subsystem binary is double-clicked and cannot be
// handed a flag, so the front end it wants has to be read off the build itself.
func Windowed() bool {
	h, _, _ := getConsoleWindow.Call()
	return h == 0
}

func dispatch(hwnd, msg, wparam, lparam uintptr) uintptr {
	live.Lock()
	w := byHwnd[syscall.Handle(hwnd)]
	live.Unlock()
	if w == nil {
		r, _, _ := defWindowProc.Call(hwnd, msg, wparam, lparam)
		return r
	}
	switch msg {
	case wmDestroy:
		postQuit.Call(0)
		return 0
	case wmSize:
		if w.size != nil {
			w.resized()
		}
		return 0
	case wmCommand:
		if do := w.click[controlID(wparam)]; do != nil {
			do()
		}
		return 0
	case wmQueued:
		w.mu.Lock()
		todo := w.queued
		w.queued = nil
		w.mu.Unlock()
		for _, f := range todo {
			f()
		}
		return 0
	case wmCtlColorStatic, wmCtlColorEdit:
		colour, ok := w.tint[syscall.Handle(lparam)]
		if !ok {
			colour = inkText
		}
		setTextColor.Call(wparam, uintptr(colour))
		setBkColor.Call(wparam, paper)
		return uintptr(w.brush)
	}
	r, _, _ := defWindowProc.Call(hwnd, msg, wparam, lparam)
	return r
}

// controlID is LOWORD(wParam) of a WM_COMMAND, which is the id child windows
// are created with. The high word says what the control did and we do not ask.
func controlID(wparam uintptr) int32 { return int32(wparam & 0xFFFF) }

func utf16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		p, _ = syscall.UTF16PtrFromString("")
	}
	return p
}
