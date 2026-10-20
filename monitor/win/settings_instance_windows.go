//go:build windows

package win

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	settingsEnumWindows  = user32.NewProc("EnumWindows")
	settingsSetProp      = user32.NewProc("SetPropW")
	settingsGetProp      = user32.NewProc("GetPropW")
	settingsWindowPID    = user32.NewProc("GetWindowThreadProcessId")
	settingsShowWindow   = user32.NewProc("ShowWindowAsync")
	settingsForeground   = user32.NewProc("SetForegroundWindow")
	settingsEnumCallback = syscall.NewCallback(enumerateSettingsWindow)
)

type settingsInstance struct {
	handle   windows.Handle
	property *uint16
}

// The kernel object covers startup as well as the window lifetime. Local scopes
// it to this logon session; the SID separates accounts in the same session.
// Last-handle close (including a crash) lets the next launch become the owner.
func claimSettingsInstance(key string) (*settingsInstance, bool, error) {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return nil, false, err
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		return nil, false, err
	}
	name := settingsInstanceName(key, user.User.Sid)
	property := utf16(fmt.Sprintf("OpenAbstractions.Settings.%x", sha256.Sum256([]byte(key))))
	deadline := time.Now().Add(15 * time.Second)
	for {
		handle, err := windows.CreateMutex(nil, false, name)
		if err == nil {
			return &settingsInstance{handle: handle, property: property}, false, nil
		}
		if !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
			return nil, false, err
		}
		if err := windows.CloseHandle(handle); err != nil {
			return nil, false, err
		}
		if focusSettingsWindow(property, user.User.Sid, session) {
			return nil, true, nil
		}
		if time.Now().After(deadline) {
			return nil, false, errors.New("the existing Panel window is still starting")
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func settingsInstanceName(key string, sid *windows.SID) *uint16 {
	return utf16(fmt.Sprintf(`Local\OpenAbstractions.Settings.%s.%x`, sid.String(), sha256.Sum256([]byte(key))))
}

func (s *settingsInstance) close() { windows.CloseHandle(s.handle) }

func (s *settingsInstance) mark(hwnd uintptr) bool {
	ok, _, _ := settingsSetProp.Call(hwnd, uintptr(unsafe.Pointer(s.property)), uintptr(os.Getpid()))
	return ok != 0
}

// Enumerate only explicitly marked settings windows. Native Ask owners and
// permission dialogs share the Panel window class and title.
func focusSettingsWindow(property *uint16, sid *windows.SID, session uint32) bool {
	search := settingsWindowSearch{property: property, sid: sid, session: session}
	settingsEnumWindows.Call(settingsEnumCallback, uintptr(unsafe.Pointer(&search)))
	return search.found
}

type settingsWindowSearch struct {
	property *uint16
	sid      *windows.SID
	session  uint32
	found    bool
}

// EnumWindows calls synchronously. The typed callback argument keeps the Go
// search pointer intact across the native callback trampoline.
func enumerateSettingsWindow(hwnd uintptr, search *settingsWindowSearch) uintptr {
	property, sid, session := search.property, search.sid, search.session
	marked, _, _ := settingsGetProp.Call(hwnd, uintptr(unsafe.Pointer(property)))
	if marked == 0 {
		return 1
	}
	var pid uint32
	settingsWindowPID.Call(hwnd, uintptr(unsafe.Pointer(&pid)))
	if uintptr(pid) != marked || !settingsSameUserSession(pid, sid, session) {
		return 1
	}
	minimized, _, _ := isIconic.Call(hwnd)
	if minimized != 0 {
		settingsShowWindow.Call(hwnd, 9) // SW_RESTORE
	} else {
		settingsShowWindow.Call(hwnd, 5) // SW_SHOW preserves a maximized window.
	}
	// This call belongs to the newly launched foreground process. Passing a
	// message to the old process would lose the launcher's foreground rights.
	settingsForeground.Call(hwnd)
	search.found = true
	return 0
}

func settingsSameUserSession(pid uint32, sid *windows.SID, session uint32) bool {
	var otherSession uint32
	if windows.ProcessIdToSessionId(pid, &otherSession) != nil || otherSession != session {
		return false
	}
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, pid)
	if err != nil {
		return false
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token) != nil {
		return false
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	return err == nil && windows.EqualSid(sid, user.User.Sid)
}
