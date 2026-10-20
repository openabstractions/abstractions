//go:build windows

package win

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func TestSettingsEnumerationCallbackRetainsSearch(t *testing.T) {
	search := settingsWindowSearch{property: utf16("OpenAbstractions.Settings.test.callback")}
	runtime.GC()
	// HWND zero has no property. Enter through the real native trampoline so
	// race/checkptr exercises the native-to-Go callback argument conversion.
	result, _, _ := syscall.SyscallN(settingsEnumCallback, 0, uintptr(unsafe.Pointer(&search)))
	if result != 1 || search.found {
		t.Fatalf("unmarked window: result=%d, found=%v", result, search.found)
	}
}

func TestSettingsInstanceOwnsStartupAndReleases(t *testing.T) {
	key := fmt.Sprintf("test-%d-%s", os.Getpid(), t.Name())
	owner, forwarded, err := claimSettingsInstance(key)
	if err != nil || forwarded || owner == nil {
		t.Fatalf("claim: %v, %v", forwarded, err)
	}
	// The exclusion exists before a window has been marked, covering concurrent
	// launches during WebView initialization without ever creating a test UI.
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		owner.close()
		t.Fatal(err)
	}
	name := settingsInstanceName(key, user.User.Sid)
	other, err := windows.CreateMutex(nil, false, name)
	if other != 0 {
		windows.CloseHandle(other)
	}
	owner.close()
	if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		t.Fatalf("startup exclusion: %v", err)
	}
	restarted, forwarded, err := claimSettingsInstance(key)
	if err != nil || forwarded || restarted == nil {
		t.Fatalf("restart: %v, %v", forwarded, err)
	}
	restarted.close()
}

func TestSettingsInstanceRecoversOwnerDuringStartup(t *testing.T) {
	key := fmt.Sprintf("test-%d-%s", os.Getpid(), t.Name())
	owner, _, err := claimSettingsInstance(key)
	if err != nil {
		t.Fatal(err)
	}
	closed := make(chan struct{})
	go func() {
		time.Sleep(75 * time.Millisecond)
		owner.close() // Kernel cleanup has the same effect when an owner crashes.
		close(closed)
	}()
	replacement, forwarded, err := claimSettingsInstance(key)
	<-closed
	if err != nil || forwarded || replacement == nil {
		t.Fatalf("recover startup: %v, %v", forwarded, err)
	}
	replacement.close()
}

func TestSettingsInstanceValidatesUserAndSession(t *testing.T) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	var session uint32
	pid := uint32(os.Getpid())
	if err := windows.ProcessIdToSessionId(pid, &session); err != nil {
		t.Fatal(err)
	}
	if !settingsSameUserSession(pid, user.User.Sid, session) {
		t.Fatal("current process rejected")
	}
	if settingsSameUserSession(pid, user.User.Sid, session+1) {
		t.Fatal("different session accepted")
	}
	other, err := windows.CreateWellKnownSid(windows.WinLocalSystemSid)
	if err != nil {
		t.Fatal(err)
	}
	if !windows.EqualSid(other, user.User.Sid) && settingsSameUserSession(pid, other, session) {
		t.Fatal("different user accepted")
	}
}
