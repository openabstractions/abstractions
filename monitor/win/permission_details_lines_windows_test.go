package win

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// The details box and the overflow box are multiline EDIT controls, which
// break lines only at CR LF. Popup details are joined with bare line feeds;
// each must still show one line per entry.
func TestPermissionDetailsBoxesBreakAtBareLineFeeds(t *testing.T) {
	if got := editText("Program: a\nQuestion: q\r\nAsked: now"); got != "Program: a\r\nQuestion: q\r\nAsked: now" {
		t.Fatalf("editText %q", got)
	}
	// Keep synchronous EDIT messages and cleanup on the window's creating thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	instance, _, _ := getModuleHandle.Call(0)
	hwnd, _, err := createWindow.Call(0, uintptr(unsafe.Pointer(utf16("#32770"))), uintptr(unsafe.Pointer(utf16("Hidden details lines test"))),
		0x80000000|0x00c00000, 0, 0, 560, 380, 0, 0, instance, 0)
	if hwnd == 0 {
		t.Fatalf("hidden dialog: %v", err)
	}
	defer destroyWindowProc.Call(hwnd)
	d := &permissionDialog{hwnd: hwnd, dpi: 96}
	d.replaceFonts()
	defer d.freeResources()
	lines := func(h uintptr) (string, uintptr) {
		moveWindow.Call(h, 0, 0, 520, 240, 1)
		length, _, _ := getWindowTextLength.Call(h)
		buf := make([]uint16, length+1)
		getWindowText.Call(h, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
		count, _, _ := sendMessage.Call(h, 0x00BA, 0, 0) // EM_GETLINECOUNT
		return syscall.UTF16ToString(buf), count
	}
	details, err := d.control("EDIT", "Request: give Codex tasks.\n\nProgram: C:\\codex.exe\nQuestion: q1", wsTabStop|0x0004|0x0040|0x0800|0x00200000, permissionTextID)
	if err != nil {
		t.Fatal(err)
	}
	if text, count := lines(details); text != "Request: give Codex tasks.\r\n\r\nProgram: C:\\codex.exe\r\nQuestion: q1" || count != 4 {
		t.Fatalf("details box %q shows %d lines", text, count)
	}
	overflow := d.ensureOverflow("Check that both computers show 123456.\n\nThis computer allows: models.")
	if overflow == 0 {
		t.Fatalf("overflow box: %v", d.err)
	}
	if text, count := lines(overflow); text != "Check that both computers show 123456.\r\n\r\nThis computer allows: models." || count != 3 {
		t.Fatalf("overflow box %q shows %d lines", text, count)
	}
}
