package win

import (
	"fmt"
	"syscall"
	"unsafe"
)

var (
	shell32 = syscall.NewLazyDLL("shell32.dll")

	shellNotifyIcon = shell32.NewProc("Shell_NotifyIconW")

	createPopupMenu  = user32.NewProc("CreatePopupMenu")
	destroyMenu      = user32.NewProc("DestroyMenu")
	appendMenu       = user32.NewProc("AppendMenuW")
	trackPopupMenuEx = user32.NewProc("TrackPopupMenuEx")
	setForegroundWin = user32.NewProc("SetForegroundWindow")
	getCursorPos     = user32.NewProc("GetCursorPos")
)

const (
	// wmTrayCallback is the message Shell_NotifyIcon delivers icon and
	// balloon events on, dispatched in win_windows.go beside the window's
	// other messages. wmApp is that file's own reserved-range base.
	wmTrayCallback = wmApp + 2
	wmNull         = 0x0000

	nimAdd        = 0
	nimModify     = 1
	nimDelete     = 2
	nimSetVersion = 4

	nifMessage = 0x00000001
	nifIcon    = 0x00000002
	nifTip     = 0x00000004
	nifInfo    = 0x00000010

	niifInfo = 0x00000001

	// notifyIconVersion (NOTIFYICON_VERSION) keeps the original wParam/lParam
	// click semantics below while also enabling the NIN_BALLOONUSERCLICK
	// message a shown balloon needs to be clickable.
	notifyIconVersion = 3

	wmLButtonUp = 0x0202
	wmRButtonUp = 0x0205
	wmUser      = 0x0400
	// ninBalloonUserClick (NIN_BALLOONUSERCLICK) is sent when a person clicks
	// the balloon itself, distinct from clicking the tray icon.
	ninBalloonUserClick = wmUser + 5

	mfString = 0x00000000

	tpmLeftAlign   = 0x0000
	tpmRightButton = 0x0002
)

type point struct{ X, Y int32 }

// notifyIconData is Shell_NotifyIconW's NOTIFYICONDATAW, transcribed from the
// Windows shell header. Every field after Size is a fixed-width integer, a
// fixed-size array or a pointer-sized handle, so Go's own alignment
// reproduces the Microsoft layout on both the 32- and 64-bit builds without
// hand-written padding, the same discipline the structs in win_windows.go
// follow. Version stands in for the header's union of uTimeout and uVersion:
// this file only ever sets it through NIM_SETVERSION, so one field serves.
type notifyIconData struct {
	Size            uint32
	Wnd             syscall.Handle
	ID              uint32
	Flags           uint32
	CallbackMessage uint32
	Icon            syscall.Handle
	Tip             [128]uint16
	State           uint32
	StateMask       uint32
	Info            [256]uint16
	Version         uint32
	InfoTitle       [64]uint16
	InfoFlags       uint32
	GUIDItem        [16]byte
	BalloonIcon     syscall.Handle
}

// MenuItem is one line of a Tray's right-click menu.
type MenuItem struct {
	Text string
	Do   func()
}

// Tray is a Shell_NotifyIcon notification-area icon riding a Window's
// message queue. Build it inside win.RunHidden (or win.Run): the icon's
// callback message and its menu commands both arrive as messages to that
// window, dispatched the same way a button's click does.
type Tray struct {
	w  *Window
	id uint32
}

// NewTray adds the icon to w, with a right click opening items as a popup
// menu and a left click on the icon, or on a shown balloon, calling onClick.
// w must come from RunHidden or Run and must not already carry a tray icon.
func NewTray(w *Window, tooltip string, onClick func(), items []MenuItem) (*Tray, error) {
	inst, _, _ := getModuleHandle.Call(0) //unchecked: GetModuleHandle(NULL) is the running image and cannot fail
	icon := loadAppIcon(inst, 16)         // the notification area's own icon size
	t := &Tray{w: w, id: 1}

	menu, _, err := createPopupMenu.Call()
	if menu == 0 {
		return nil, fmt.Errorf("create tray menu: %w", err)
	}
	for _, item := range items {
		w.next++
		if item.Do != nil {
			w.click[w.next] = item.Do
		}
		//unchecked: Call's error is the raw GetLastError, not meaningful without a failed primary return, which nothing here inspects; a missing menu item has no recovery
		appendMenu.Call(menu, mfString, uintptr(w.next), uintptr(unsafe.Pointer(utf16(item.Text))))
	}

	w.trayMsg = func(_, lparam uintptr) {
		switch lparam {
		case wmLButtonUp, ninBalloonUserClick:
			if onClick != nil {
				onClick()
			}
		case wmRButtonUp:
			var p point
			//unchecked: Call's error is the raw GetLastError, not meaningful without a failed primary return; a stale cursor position at worst mispositions the menu
			getCursorPos.Call(uintptr(unsafe.Pointer(&p)))
			// TrackPopupMenu's own documented requirement: without bringing
			// the (invisible) window to the foreground first, the menu does
			// not reliably dismiss on an outside click.
			//unchecked: Call's error is the raw GetLastError, not meaningful without a failed primary return; nothing here can retry bringing the window forward
			setForegroundWin.Call(uintptr(w.h))
			//unchecked: Call's error is the raw GetLastError, not meaningful without a failed primary return; the popup menu simply never opens on failure, with nothing further to do
			trackPopupMenuEx.Call(menu, tpmLeftAlign|tpmRightButton, uintptr(p.X), uintptr(p.Y), uintptr(w.h), 0)
			//unchecked: Call's error is the raw GetLastError, not meaningful without a failed primary return; the null message it posts is a best-effort menu dismissal nudge
			postMessage.Call(uintptr(w.h), wmNull, 0, 0)
		}
	}

	data := notifyIconData{
		Size: uint32(unsafe.Sizeof(notifyIconData{})), Wnd: w.h, ID: t.id,
		Flags: nifMessage | nifIcon | nifTip, CallbackMessage: wmTrayCallback, Icon: syscall.Handle(icon),
	}
	copy(data.Tip[:], clipUTF16(tooltip, len(data.Tip)))
	if ok, _, err := shellNotifyIcon.Call(nimAdd, uintptr(unsafe.Pointer(&data))); ok == 0 {
		//unchecked: Call's error is the raw GetLastError, not meaningful without a failed primary return; the function is already returning the add failure above
		destroyMenu.Call(menu)
		return nil, fmt.Errorf("add tray icon: %w", err)
	}
	version := notifyIconData{Size: data.Size, Wnd: w.h, ID: t.id, Version: notifyIconVersion}
	//unchecked: Call's error is the raw GetLastError, not meaningful without a failed primary return; the icon already exists at this point, so this only refines its message version
	shellNotifyIcon.Call(nimSetVersion, uintptr(unsafe.Pointer(&version)))
	return t, nil
}

// SetTooltip replaces the icon's hover text, such as one status change while
// the runtime cannot be reached, and again once it answers again.
func (t *Tray) SetTooltip(text string) {
	data := notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), Wnd: t.w.h, ID: t.id, Flags: nifTip}
	copy(data.Tip[:], clipUTF16(text, len(data.Tip)))
	//unchecked: Call's error is the raw GetLastError, not meaningful without a failed primary return; a stale tooltip at worst shows the previous status text
	shellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

// Notify shows one Windows balloon notification.
func (t *Tray) Notify(title, text string) {
	data := notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), Wnd: t.w.h, ID: t.id,
		Flags: nifInfo, InfoFlags: niifInfo}
	copy(data.Info[:], clipUTF16(text, len(data.Info)))
	copy(data.InfoTitle[:], clipUTF16(title, len(data.InfoTitle)))
	//unchecked: Call's error is the raw GetLastError, not meaningful without a failed primary return; a dropped balloon notification has no recovery
	shellNotifyIcon.Call(nimModify, uintptr(unsafe.Pointer(&data)))
}

// Close removes the icon. Windows also removes a still-registered icon when
// the process exits, so this is for a clean disappearance on Quit rather
// than a leak this would otherwise cause.
func (t *Tray) Close() {
	data := notifyIconData{Size: uint32(unsafe.Sizeof(notifyIconData{})), Wnd: t.w.h, ID: t.id}
	//unchecked: Call's error is the raw GetLastError, not meaningful without a failed primary return; Windows removes a still-registered icon on process exit anyway, per the comment above
	shellNotifyIcon.Call(nimDelete, uintptr(unsafe.Pointer(&data)))
}

// clipUTF16 encodes s as null-terminated UTF-16 no longer than max elements,
// which a fixed NOTIFYICONDATAW buffer requires: Shell_NotifyIconW reads
// exactly this field's width, and a missing terminator reads past it.
func clipUTF16(s string, max int) []uint16 {
	u, err := syscall.UTF16FromString(s)
	if err != nil || max <= 0 {
		return []uint16{0}
	}
	if len(u) > max {
		u = append(append([]uint16{}, u[:max-1]...), 0)
	}
	return u
}
