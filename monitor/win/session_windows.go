//go:build windows

package win

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	wtsapi32                         = syscall.NewLazyDLL("wtsapi32.dll")
	wtsRegisterSessionNotification   = wtsapi32.NewProc("WTSRegisterSessionNotification")
	wtsUnregisterSessionNotification = wtsapi32.NewProc("WTSUnRegisterSessionNotification")
	openInputDesktop                 = user32.NewProc("OpenInputDesktop")
	getUserObjectInformation         = user32.NewProc("GetUserObjectInformationW")
	closeDesktop                     = user32.NewProc("CloseDesktop")
)

const (
	wmWtsSessionChange   = 0x02B1
	notifyForThisSession = 0
	wtsSessionLock       = 0x7
	wtsSessionUnlock     = 0x8
	desktopReadObjects   = 0x0001
	uoiName              = 2
)

// WatchSessionChanges registers this window for this session's lock and
// unlock broadcasts. It fails closed: callers must not present a permission
// prompt if Windows cannot deliver lock changes.
func (w *Window) WatchSessionChanges(onChange func(locked bool)) error {
	if onChange == nil {
		return fmt.Errorf("session change callback is required")
	}
	if ok, _, err := wtsRegisterSessionNotification.Call(uintptr(w.h), notifyForThisSession); ok == 0 {
		return fmt.Errorf("register session notifications: %w", err)
	}
	w.sessionChange = onChange
	return nil
}

// InteractiveDesktop reports whether the input desktop is the user's normal
// unlocked desktop. Access errors and unfamiliar desktops return false.
func InteractiveDesktop() bool {
	desktop, _, _ := openInputDesktop.Call(0, 0, desktopReadObjects)
	if desktop == 0 {
		return false
	}
	defer closeDesktop.Call(desktop)

	var needed uint32
	getUserObjectInformation.Call(desktop, uoiName, 0, 0, uintptr(unsafe.Pointer(&needed)))
	if needed == 0 || needed > 4096 {
		return false
	}
	name := make([]uint16, (needed+1)/2)
	if ok, _, _ := getUserObjectInformation.Call(desktop, uoiName, uintptr(unsafe.Pointer(&name[0])), uintptr(needed), uintptr(unsafe.Pointer(&needed))); ok == 0 {
		return false
	}
	return syscall.UTF16ToString(name) == "Default"
}
