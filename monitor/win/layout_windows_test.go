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
}

const (
	cPtr   = unsafe.Sizeof(uintptr(0)) // HWND, LPWSTR, WPARAM, LPARAM, HANDLE, WNDPROC
	cInt   = 4                         // int, UINT, DWORD, LONG, BOOL
	cShort = 2
)

// wndClassExW is WNDCLASSEXW, winuser.h.
var wndClassExW = []cField{
	{"cbSize", cInt}, {"style", cInt}, {"lpfnWndProc", cPtr},
	{"cbClsExtra", cInt}, {"cbWndExtra", cInt},
	{"hInstance", cPtr}, {"hIcon", cPtr}, {"hCursor", cPtr}, {"hbrBackground", cPtr},
	{"lpszMenuName", cPtr}, {"lpszClassName", cPtr}, {"hIconSm", cPtr},
}

// msgW is MSG, winuser.h. POINT pt is flattened to its two LONGs.
var msgW = []cField{
	{"hwnd", cPtr}, {"message", cInt}, {"wParam", cPtr}, {"lParam", cPtr},
	{"time", cInt}, {"pt.x", cInt}, {"pt.y", cInt},
}

// rectW is RECT, windef.h.
var rectW = []cField{{"left", cInt}, {"top", cInt}, {"right", cInt}, {"bottom", cInt}}

// lvColumnW is LVCOLUMNW, commctrl.h.
var lvColumnW = []cField{
	{"mask", cInt}, {"fmt", cInt}, {"cx", cInt}, {"pszText", cPtr},
	{"cchTextMax", cInt}, {"iSubItem", cInt}, {"iImage", cInt}, {"iOrder", cInt},
	{"cxMin", cInt}, {"cxDefault", cInt}, {"cxIdeal", cInt},
}

// lvItemW is LVITEMW, commctrl.h.
var lvItemW = []cField{
	{"mask", cInt}, {"iItem", cInt}, {"iSubItem", cInt},
	{"state", cInt}, {"stateMask", cInt},
	{"pszText", cPtr}, {"cchTextMax", cInt}, {"iImage", cInt}, {"lParam", cPtr},
	{"iIndent", cInt}, {"iGroupId", cInt}, {"cColumns", cInt},
	{"puColumns", cPtr}, {"piColFmt", cPtr}, {"iGroup", cInt},
}

// initCommonControlsExW is INITCOMMONCONTROLSEX, commctrl.h.
var initCommonControlsExW = []cField{{"dwSize", cInt}, {"dwICC", cInt}}

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

func TestLVColumnLayout(t *testing.T) {
	var v lvColumn
	match(t, "LVCOLUMNW", lvColumnW, unsafe.Sizeof(v),
		unsafe.Offsetof(v.Mask), unsafe.Offsetof(v.Fmt), unsafe.Offsetof(v.CX),
		unsafe.Offsetof(v.Text), unsafe.Offsetof(v.TextMax), unsafe.Offsetof(v.SubItem),
		unsafe.Offsetof(v.Image), unsafe.Offsetof(v.Order), unsafe.Offsetof(v.CXMin),
		unsafe.Offsetof(v.CXDefault), unsafe.Offsetof(v.CXIdeal))
}

func TestLVItemLayout(t *testing.T) {
	var v lvItem
	match(t, "LVITEMW", lvItemW, unsafe.Sizeof(v),
		unsafe.Offsetof(v.Mask), unsafe.Offsetof(v.Item), unsafe.Offsetof(v.SubItem),
		unsafe.Offsetof(v.State), unsafe.Offsetof(v.StateMask),
		unsafe.Offsetof(v.Text), unsafe.Offsetof(v.TextMax), unsafe.Offsetof(v.Image),
		unsafe.Offsetof(v.Param), unsafe.Offsetof(v.Indent), unsafe.Offsetof(v.GroupID),
		unsafe.Offsetof(v.Columns), unsafe.Offsetof(v.PColumns), unsafe.Offsetof(v.PColFmt),
		unsafe.Offsetof(v.Group))
}

func TestInitCommonControlsExLayout(t *testing.T) {
	var v initCommonControlsEx
	match(t, "INITCOMMONCONTROLSEX", initCommonControlsExW, unsafe.Sizeof(v),
		unsafe.Offsetof(v.Size), unsafe.Offsetof(v.Classes))
}

// match reports every place the Go struct disagrees with the transcribed header.
func match(t *testing.T, name string, fields []cField, size uintptr, at ...uintptr) {
	t.Helper()
	if len(at) != len(fields) {
		t.Fatalf("%s: %d offsets given for %d fields", name, len(at), len(fields))
	}
	var next, widest uintptr
	for i, f := range fields {
		next = roundUp(next, f.size)
		if at[i] != next {
			t.Errorf("%s.%s is at %d, Go puts it at %d", name, f.name, next, at[i])
		}
		next += f.size
		widest = max(widest, f.size)
	}
	if want := roundUp(next, widest); size != want {
		t.Errorf("%s is %d bytes, Go makes it %d", name, want, size)
	}
}

func roundUp(n, to uintptr) uintptr { return (n + to - 1) / to * to }
