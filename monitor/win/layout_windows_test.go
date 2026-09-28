package win

import (
	"testing"
	"unsafe"
)

// A struct passed to a Windows call has to match the one the header declares
// byte for byte, and a mismatch corrupts memory rather than returning a wrong
// answer. What follows transcribes each header's field list — names in order,
// and the size of each field's C type — and lays it out by the rule the
// Microsoft compilers use: every field aligned to its own size, the whole
// rounded up to the widest field.
//
// The offsets are therefore recomputed for whichever architecture this test is
// built for, so `GOARCH=386 go test` checks the 32-bit layout on a 64-bit
// machine. A reviewer checks the transcription against the header's field list
// and nothing else; the arithmetic checks itself.

type cField struct {
	name string
	size uintptr
	// align is the field's alignment requirement when it differs from its
	// size — an array's total size is not itself an alignment, unlike every
	// scalar field below it. Zero means align equals size, true of a scalar.
	align uintptr
}

const (
	cPtr   = unsafe.Sizeof(uintptr(0)) // HWND, LPWSTR, WPARAM, LPARAM, HANDLE, WNDPROC
	cInt   = 4                         // int, UINT, DWORD, LONG, BOOL
	cShort = 2
)

// wndClassExW is WNDCLASSEXW, winuser.h.
var wndClassExW = []cField{
	{"cbSize", cInt, 0}, {"style", cInt, 0}, {"lpfnWndProc", cPtr, 0},
	{"cbClsExtra", cInt, 0}, {"cbWndExtra", cInt, 0},
	{"hInstance", cPtr, 0}, {"hIcon", cPtr, 0}, {"hCursor", cPtr, 0}, {"hbrBackground", cPtr, 0},
	{"lpszMenuName", cPtr, 0}, {"lpszClassName", cPtr, 0}, {"hIconSm", cPtr, 0},
}

// msgW is MSG, winuser.h. POINT pt is flattened to its two LONGs.
var msgW = []cField{
	{"hwnd", cPtr, 0}, {"message", cInt, 0}, {"wParam", cPtr, 0}, {"lParam", cPtr, 0},
	{"time", cInt, 0}, {"pt.x", cInt, 0}, {"pt.y", cInt, 0},
}

// rectW is RECT, windef.h.
var rectW = []cField{{"left", cInt, 0}, {"top", cInt, 0}, {"right", cInt, 0}, {"bottom", cInt, 0}}

// MINMAXINFO is five POINTs. POINT is two LONGs and has four-byte alignment.
var minMaxInfoW = []cField{
	{"ptReserved", 2 * cInt, cInt}, {"ptMaxSize", 2 * cInt, cInt},
	{"ptMaxPosition", 2 * cInt, cInt}, {"ptMinTrackSize", 2 * cInt, cInt},
	{"ptMaxTrackSize", 2 * cInt, cInt},
}

// notifyIconDataW is NOTIFYICONDATAW, shellapi.h. Each WCHAR array's own size
// is its element count times cShort, but a WCHAR array aligns like one WCHAR,
// not like its whole span, so it carries an explicit align. guidItem is
// GUID's 16 bytes (DWORD, two WORDs, eight BYTEs), which align like its
// widest member, a DWORD. DUMMYUNIONNAME (uTimeout/uVersion) is one UINT.
var notifyIconDataW = []cField{
	{"cbSize", cInt, 0}, {"hWnd", cPtr, 0}, {"uID", cInt, 0}, {"uFlags", cInt, 0},
	{"uCallbackMessage", cInt, 0}, {"hIcon", cPtr, 0},
	{"szTip", 128 * cShort, cShort},
	{"dwState", cInt, 0}, {"dwStateMask", cInt, 0},
	{"szInfo", 256 * cShort, cShort},
	{"DUMMYUNIONNAME", cInt, 0},
	{"szInfoTitle", 64 * cShort, cShort},
	{"dwInfoFlags", cInt, 0},
	{"guidItem", 16, cInt},
	{"hBalloonIcon", cPtr, 0},
}

func TestWndClassExLayout(t *testing.T) {
	var v wndClassEx
	match(t, "WNDCLASSEXW", wndClassExW, unsafe.Sizeof(v),
		unsafe.Offsetof(v.Size), unsafe.Offsetof(v.Style), unsafe.Offsetof(v.WndProc),
		unsafe.Offsetof(v.ClsExtra), unsafe.Offsetof(v.WndExtra),
		unsafe.Offsetof(v.Instance), unsafe.Offsetof(v.Icon), unsafe.Offsetof(v.Cursor),
		unsafe.Offsetof(v.Brush), unsafe.Offsetof(v.MenuName), unsafe.Offsetof(v.ClassName),
		unsafe.Offsetof(v.IconSm))
}

func TestMessageLayout(t *testing.T) {
	var v message
	match(t, "MSG", msgW, unsafe.Sizeof(v),
		unsafe.Offsetof(v.Hwnd), unsafe.Offsetof(v.Msg), unsafe.Offsetof(v.WParam),
		unsafe.Offsetof(v.LParam), unsafe.Offsetof(v.Time),
		unsafe.Offsetof(v.X), unsafe.Offsetof(v.Y))
}

func TestRectLayout(t *testing.T) {
	var v rect
	match(t, "RECT", rectW, unsafe.Sizeof(v),
		unsafe.Offsetof(v.Left), unsafe.Offsetof(v.Top),
		unsafe.Offsetof(v.Right), unsafe.Offsetof(v.Bottom))
}

func TestMinMaxInfoLayout(t *testing.T) {
	var v minMaxInfo
	match(t, "MINMAXINFO", minMaxInfoW, unsafe.Sizeof(v),
		unsafe.Offsetof(v.Reserved), unsafe.Offsetof(v.MaxSize),
		unsafe.Offsetof(v.MaxPosition), unsafe.Offsetof(v.MinTrack),
		unsafe.Offsetof(v.MaxTrack))
	if unsafe.Sizeof(point{}) != 2*cInt || unsafe.Offsetof(point{}.Y) != cInt {
		t.Fatal("POINT does not contain two contiguous LONG fields")
	}
}

func TestNotifyIconDataLayout(t *testing.T) {
	var v notifyIconData
	match(t, "NOTIFYICONDATAW", notifyIconDataW, unsafe.Sizeof(v),
		unsafe.Offsetof(v.Size), unsafe.Offsetof(v.Wnd), unsafe.Offsetof(v.ID), unsafe.Offsetof(v.Flags),
		unsafe.Offsetof(v.CallbackMessage), unsafe.Offsetof(v.Icon),
		unsafe.Offsetof(v.Tip),
		unsafe.Offsetof(v.State), unsafe.Offsetof(v.StateMask),
		unsafe.Offsetof(v.Info),
		unsafe.Offsetof(v.Version),
		unsafe.Offsetof(v.InfoTitle),
		unsafe.Offsetof(v.InfoFlags),
		unsafe.Offsetof(v.GUIDItem),
		unsafe.Offsetof(v.BalloonIcon))
}

// match reports every place the Go struct disagrees with the transcribed header.
func match(t *testing.T, name string, fields []cField, size uintptr, at ...uintptr) {
	t.Helper()
	if len(at) != len(fields) {
		t.Fatalf("%s: %d offsets given for %d fields", name, len(at), len(fields))
	}
	var next, widest uintptr
	for i, f := range fields {
		align := f.align
		if align == 0 {
			align = f.size
		}
		next = roundUp(next, align)
		if at[i] != next {
			t.Errorf("%s.%s is at %d, Go puts it at %d", name, f.name, next, at[i])
		}
		next += f.size
		widest = max(widest, align)
	}
	if want := roundUp(next, widest); size != want {
		t.Errorf("%s is %d bytes, Go makes it %d", name, want, size)
	}
}

func roundUp(n, to uintptr) uintptr { return (n + to - 1) / to * to }
