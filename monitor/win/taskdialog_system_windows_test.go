//go:build windows

package win

import (
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/windows"
)

// TestTaskDialogResolvesFromSystemComctl32UnderManifest shows that a
// system-directory load of comctl32.dll still follows the Panel manifest's
// Common Controls 6 dependency: under the manifest's activation context the
// load resolves to the WinSxS version 6 copy and TaskDialogIndirect, which
// only version 6 exports, resolves. No dialog is shown.
func TestTaskDialogResolvesFromSystemComctl32UnderManifest(t *testing.T) {
	if !comctl32.System {
		t.Fatal("comctl32 is bound with the default DLL search; want windows.NewLazySystemDLL")
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	deactivate := withPanelManifest(t)
	defer deactivate()
	if t.Failed() {
		return
	}
	// A fresh binding loads under the active context, independent of any
	// comctl32 instance an earlier test in this process loaded.
	dll := windows.NewLazySystemDLL(comctl32.Name)
	if err := dll.NewProc("TaskDialogIndirect").Find(); err != nil {
		t.Fatalf("TaskDialogIndirect under the Panel manifest: %v", err)
	}
	buf := make([]uint16, windows.MAX_LONG_PATH)
	n, err := windows.GetModuleFileName(windows.Handle(dll.Handle()), &buf[0], uint32(len(buf)))
	if err != nil {
		t.Fatal(err)
	}
	path := strings.ToLower(syscall.UTF16ToString(buf[:n]))
	if !strings.Contains(path, filepath.FromSlash("/winsxs/")) || !strings.Contains(path, "common-controls_6595b64144ccf1df_6.") {
		t.Fatalf("comctl32 loaded from %s; want the WinSxS Common Controls 6 assembly", path)
	}
}
