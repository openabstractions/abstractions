//go:build windows

package main

import (
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	identity "github.com/openabstractions/abstraction-identity"
)

// Standard Win32 controls, system colors and a creator-thread message loop.
// No Monitor code or permission presenter runs inside this application.
// https://learn.microsoft.com/windows/win32/learnwin32/creating-a-window
// https://learn.microsoft.com/windows/win32/procthread/creating-windows-in-threads
// https://learn.microsoft.com/windows/win32/api/winuser/nf-winuser-isdialogmessagew
var (
	appUser32           = syscall.NewLazyDLL("user32.dll")
	appGDI32            = syscall.NewLazyDLL("gdi32.dll")
	appKernel32         = syscall.NewLazyDLL("kernel32.dll")
	appRegisterClass    = appUser32.NewProc("RegisterClassExW")
	appCreateWindow     = appUser32.NewProc("CreateWindowExW")
	appDestroyWindow    = appUser32.NewProc("DestroyWindow")
	appDefWindowProc    = appUser32.NewProc("DefWindowProcW")
	appGetMessage       = appUser32.NewProc("GetMessageW")
	appTranslateMessage = appUser32.NewProc("TranslateMessage")
	appDispatchMessage  = appUser32.NewProc("DispatchMessageW")
	appIsDialogMessage  = appUser32.NewProc("IsDialogMessageW")
	appPostQuit         = appUser32.NewProc("PostQuitMessage")
	appPostMessage      = appUser32.NewProc("PostMessageW")
	appSendMessage      = appUser32.NewProc("SendMessageW")
	appShowWindow       = appUser32.NewProc("ShowWindow")
	appMoveWindow       = appUser32.NewProc("MoveWindow")
	appSetWindowPos     = appUser32.NewProc("SetWindowPos")
	appSetWindowText    = appUser32.NewProc("SetWindowTextW")
	appGetClientRect    = appUser32.NewProc("GetClientRect")
	appAdjustWindowRect = appUser32.NewProc("AdjustWindowRectEx")
	appEnableWindow     = appUser32.NewProc("EnableWindow")
	appLoadCursor       = appUser32.NewProc("LoadCursorW")
	appLoadIcon         = appUser32.NewProc("LoadIconW")
	appSetFocus         = appUser32.NewProc("SetFocus")
	appGetDPI           = appUser32.NewProc("GetDpiForWindow")
	appDPIAware         = appUser32.NewProc("SetProcessDPIAware")
	appDPIContext       = appUser32.NewProc("SetProcessDpiAwarenessContext")
	appGetSysColor      = appUser32.NewProc("GetSysColor")
	appGetSysColorBrush = appUser32.NewProc("GetSysColorBrush")
	appInvalidateRect   = appUser32.NewProc("InvalidateRect")
	appMessageBox       = appUser32.NewProc("MessageBoxW")
	appCreateFont       = appGDI32.NewProc("CreateFontW")
	appDeleteObject     = appGDI32.NewProc("DeleteObject")
	appSetTextColor     = appGDI32.NewProc("SetTextColor")
	appSetBkColor       = appGDI32.NewProc("SetBkColor")
	appGetModuleHandle  = appKernel32.NewProc("GetModuleHandleW")
)

const (
	appWSOverlappedWindow = 0x00CF0000
	appWSChild            = 0x40000000
	appWSVisible          = 0x10000000
	appWSTabStop          = 0x00010000
	appWSVScroll          = 0x00200000
	appWSExControlParent  = 0x00010000
	appWSExClientEdge     = 0x00000200
	appCWUseDefault       = 0x80000000
	appWMDestroy          = 0x0002
	appWMSize             = 0x0005
	appWMClose            = 0x0010
	appWMSettingChange    = 0x001A
	appWMGetMinMaxInfo    = 0x0024
	appWMSetFont          = 0x0030
	appWMCommand          = 0x0111
	appWMCtlColorEdit     = 0x0133
	appWMCtlColorStatic   = 0x0138
	appWMThemeChanged     = 0x031A
	appWMCompleted        = 0x8001
	appCBSAddString       = 0x0143
	appCBSGetCurSel       = 0x0147
	appCBSSetCurSel       = 0x014E
	appReadID             = 101
	appUseID              = 102
	appSecretID           = 103
	appColorWindow        = 5
	appColorWindowText    = 8
	appColorBtnFace       = 15
	appColorBtnText       = 18
)

type appWndClass struct {
	Size, Style                   uint32
	WndProc                       uintptr
	ClsExtra, WndExtra            int32
	Instance, Icon, Cursor, Brush syscall.Handle
	MenuName, ClassName           *uint16
	IconSm                        syscall.Handle
}

type appMessage struct {
	Window         syscall.Handle
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	X, Y           int32
}

type appRect struct{ Left, Top, Right, Bottom int32 }
type appPoint struct{ X, Y int32 }
type appMinMaxInfo struct{ Reserved, MaxSize, MaxPosition, MinTrack, MaxTrack appPoint }
type appControl struct {
	Handle              syscall.Handle
	X, Y, Width, Height int
}

type playgroundWindow struct {
	h                                 syscall.Handle
	dpi                               int
	font, headingFont                 syscall.Handle
	controls                          []appControl
	secret, read, use, result, footer syscall.Handle
	actions                           *windowActions
	// Protect posting against destruction and HWND reuse. Controls are accessed
	// only by the locked OS thread; the worker posts a pointer-free notification.
	posting sync.Mutex
	closed  bool
}

var appWindows = struct {
	sync.Mutex
	items map[syscall.Handle]*playgroundWindow
}{items: make(map[syscall.Handle]*playgroundWindow)}

var appWindowProc = syscall.NewCallback(dispatchAppWindow)
var appWindowClass = sync.OnceValues(registerAppWindow)

func appUTF16(s string) *uint16 {
	p, _ := syscall.UTF16PtrFromString(s) // all callers supply fixed UI text or validated local paths
	return p
}

func registerAppWindow() (*uint16, error) {
	if appDPIContext.Find() != nil {
		appDPIAware.Call()
	} else if ok, _, _ := appDPIContext.Call(^uintptr(1)); ok == 0 {
		appDPIAware.Call()
	}
	class := appUTF16("openabstractions.rights-playground.app")
	instance, _, _ := appGetModuleHandle.Call(0)
	cursor, _, _ := appLoadCursor.Call(0, 32512)
	icon, _, _ := appLoadIcon.Call(0, 32512)
	wc := appWndClass{Size: uint32(unsafe.Sizeof(appWndClass{})), WndProc: appWindowProc,
		Instance: syscall.Handle(instance), Cursor: syscall.Handle(cursor), Icon: syscall.Handle(icon),
		IconSm: syscall.Handle(icon), Brush: appColorBtnFace + 1, ClassName: class}
	if ok, _, err := appRegisterClass.Call(uintptr(unsafe.Pointer(&wc))); ok == 0 {
		return nil, fmt.Errorf("register app window: %w", err)
	}
	return class, nil
}

func appWindow(c connection) error {
	program, err := actualAppProgram(c)
	if err != nil {
		return err
	}
	label := "App B"
	if identity.SamePathName(program, identity.CanonicalProgramPath(c.AppA)) {
		label = "App A"
	}
	return runAppWindow(c, label, filepath.Base(program), true, nil)
}

func reportWindowError() {
	appMessageBox.Call(0, uintptr(unsafe.Pointer(appUTF16("The app could not open this isolated playground. Start the prepared pack first."))),
		uintptr(unsafe.Pointer(appUTF16("Secret playground"))), 0x10)
}

func runAppWindow(c connection, label, programName string, visible bool, ready func(*playgroundWindow)) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	w, err := openAppWindow(c, label, programName)
	if err != nil {
		return err
	}
	defer func() {
		w.actions.close()
		appWindows.Lock()
		delete(appWindows.items, w.h)
		appWindows.Unlock()
		appDeleteObject.Call(uintptr(w.font))
		appDeleteObject.Call(uintptr(w.headingFont))
	}()
	if ready != nil {
		ready(w)
	}
	if visible {
		appShowWindow.Call(uintptr(w.h), 1)
		appSetFocus.Call(uintptr(w.secret))
	}
	var message appMessage
	for {
		value, _, err := appGetMessage.Call(uintptr(unsafe.Pointer(&message)), 0, 0, 0)
		if int32(value) == -1 {
			w.close()
			return fmt.Errorf("app message loop: %w", err)
		}
		if value == 0 {
			return nil
		}
		if handled, _, _ := appIsDialogMessage.Call(uintptr(w.h), uintptr(unsafe.Pointer(&message))); handled != 0 {
			continue
		}
		appTranslateMessage.Call(uintptr(unsafe.Pointer(&message)))
		appDispatchMessage.Call(uintptr(unsafe.Pointer(&message)))
	}
}

func openAppWindow(c connection, label, programName string) (*playgroundWindow, error) {
	class, err := appWindowClass()
	if err != nil {
		return nil, err
	}
	instance, _, _ := appGetModuleHandle.Call(0)
	h, _, err := appCreateWindow.Call(appWSExControlParent, uintptr(unsafe.Pointer(class)),
		uintptr(unsafe.Pointer(appUTF16(label+" — Secret playground"))), appWSOverlappedWindow,
		appCWUseDefault, appCWUseDefault, 620, 470, 0, 0, instance, 0)
	if h == 0 {
		return nil, fmt.Errorf("create app window: %w", err)
	}
	err = nil // Win32's success return, rather than its unspecified last-error value, decides success.
	w := &playgroundWindow{h: syscall.Handle(h), dpi: 96}
	w.actions = newWindowActions(c, w.notify)
	appWindows.Lock()
	appWindows.items[w.h] = w
	appWindows.Unlock()
	if appGetDPI.Find() == nil {
		if dpi, _, _ := appGetDPI.Call(h); dpi > 0 {
			w.dpi = int(dpi)
		}
	}
	w.font = w.makeFont(14, 400)
	w.headingFont = w.makeFont(22, 600)
	add := func(class, text string, id, style, x, y, width, height int) syscall.Handle {
		if err != nil {
			return 0
		}
		var handle syscall.Handle
		handle, err = w.control(instance, class, text, id, style, x, y, width, height)
		return handle
	}
	heading := add("STATIC", label+" · "+programName, 0, 0, 24, 22, 570, 30)
	appSendMessage.Call(uintptr(heading), appWMSetFont, uintptr(w.headingFont), 1)
	add("STATIC", label+" makes its own requests. The other app's access is independent.", 0, 0, 24, 60, 570, 32)
	add("STATIC", "Dummy secret", 0, 0, 24, 104, 100, 24)
	w.secret = add("COMBOBOX", "", appSecretID, appWSTabStop|appWSVScroll|3, 136, 100, 190, 160)
	for _, name := range []string{"alpha", "beta"} {
		appSendMessage.Call(uintptr(w.secret), appCBSAddString, 0, uintptr(unsafe.Pointer(appUTF16(name))))
	}
	appSendMessage.Call(uintptr(w.secret), appCBSSetCurSel, 0, 0)
	add("STATIC", "When OA asks for permission, decide there. Then click the same action again.", 0, 0, 24, 142, 570, 36)
	w.read = add("BUTTON", "Read secret", appReadID, appWSTabStop, 24, 194, 272, 38)
	w.use = add("BUTTON", "Use secret", appUseID, appWSTabStop, 320, 194, 272, 38)
	add("STATIC", "Returns the raw dummy value to this app, verifies it, then clears the copy.", 0, 0, 24, 244, 272, 52)
	add("STATIC", "Uses the saved secret for a dummy download. The secret stays in the runtime.", 0, 0, 320, 244, 272, 52)
	add("STATIC", "Result", 0, 0, 24, 308, 570, 22)
	w.result = add("EDIT", "Choose alpha or beta, then Read secret or Use secret.", 0, appWSTabStop|appWSVScroll|0x0004|0x0040|0x0800, 24, 334, 568, 82)
	w.footer = add("STATIC", "Closing this app cancels its current wait. The runtime keeps running.", 0, 0, 24, 432, 570, 24)
	if err != nil || w.font == 0 || w.headingFont == 0 {
		w.close()
		appWindows.Lock()
		delete(appWindows.items, w.h)
		appWindows.Unlock()
		appDeleteObject.Call(uintptr(w.font))
		appDeleteObject.Call(uintptr(w.headingFont))
		if err == nil {
			err = fmt.Errorf("create app fonts")
		}
		return nil, err
	}
	r := appRect{Right: int32(w.px(620)), Bottom: int32(w.px(470))}
	appAdjustWindowRect.Call(uintptr(unsafe.Pointer(&r)), appWSOverlappedWindow, 0, appWSExControlParent)
	appSetWindowPos.Call(h, 0, 0, 0, uintptr(r.Right-r.Left), uintptr(r.Bottom-r.Top), 0x0002|0x0004|0x0010)
	w.layout()
	return w, nil
}

func (w *playgroundWindow) px(n int) int { return (n*w.dpi + 48) / 96 }

func (w *playgroundWindow) makeFont(height, weight int) syscall.Handle {
	h, _, _ := appCreateFont.Call(uintptr(-int32(w.px(height))), 0, 0, 0, uintptr(weight), 0, 0, 0,
		1, 0, 0, 5, 0, uintptr(unsafe.Pointer(appUTF16("Segoe UI"))))
	return syscall.Handle(h)
}

func (w *playgroundWindow) control(instance uintptr, class, text string, id, style, x, y, width, height int) (syscall.Handle, error) {
	exStyle := uintptr(0)
	if class == "EDIT" {
		exStyle = appWSExClientEdge
	}
	h, _, err := appCreateWindow.Call(exStyle, uintptr(unsafe.Pointer(appUTF16(class))), uintptr(unsafe.Pointer(appUTF16(text))),
		uintptr(appWSChild|appWSVisible|style), uintptr(w.px(x)), uintptr(w.px(y)), uintptr(w.px(width)), uintptr(w.px(height)),
		uintptr(w.h), uintptr(id), instance, 0)
	if h == 0 {
		return 0, fmt.Errorf("create app control: %w", err)
	}
	appSendMessage.Call(h, appWMSetFont, uintptr(w.font), 1)
	w.controls = append(w.controls, appControl{syscall.Handle(h), x, y, width, height})
	return syscall.Handle(h), nil
}

func (w *playgroundWindow) layout() {
	var client appRect
	appGetClientRect.Call(uintptr(w.h), uintptr(unsafe.Pointer(&client)))
	width := int(client.Right) * 96 / w.dpi
	height := int(client.Bottom) * 96 / w.dpi
	for _, c := range w.controls {
		x, y, cw, ch := c.X, c.Y, c.Width, c.Height
		if c.Width == 570 {
			cw = width - 48
		}
		if c.Width == 272 {
			cw = (width - 72) / 2
			if c.X == 320 {
				x = cw + 48
			}
		}
		if c.Handle == w.result {
			cw, ch = width-48, height-388
		} else if c.Handle == w.footer {
			y, cw = height-38, width-48
		}
		appMoveWindow.Call(uintptr(c.Handle), uintptr(w.px(x)), uintptr(w.px(y)), uintptr(w.px(cw)), uintptr(w.px(ch)), 1)
	}
}

func (w *playgroundWindow) setBusy(busy bool) {
	enabled := uintptr(1)
	if busy {
		enabled = 0
	}
	for _, h := range []syscall.Handle{w.secret, w.read, w.use} {
		appEnableWindow.Call(uintptr(h), enabled)
	}
}

func (w *playgroundWindow) start(verb string) {
	selection, _, _ := appSendMessage.Call(uintptr(w.secret), appCBSGetCurSel, 0, 0)
	if selection > 1 {
		return
	}
	label := []string{"alpha", "beta"}[selection]
	if w.actions.start(windowAction{verb, label}) {
		w.setBusy(true)
		appSetWindowText.Call(uintptr(w.result), uintptr(unsafe.Pointer(appUTF16("Running "+verb+" "+label+"… Permission decisions are handled in OA."))))
	}
}

func (w *playgroundWindow) notify() {
	w.posting.Lock()
	defer w.posting.Unlock()
	if !w.closed {
		// The completion stays in Go-owned state; no pointer crosses PostMessage.
		// https://learn.microsoft.com/windows/win32/api/winuser/nf-winuser-postmessagew
		appPostMessage.Call(uintptr(w.h), appWMCompleted, 0, 0)
	}
}

func (w *playgroundWindow) close() {
	w.actions.close()
	w.posting.Lock()
	w.closed = true
	w.posting.Unlock()
	appDestroyWindow.Call(uintptr(w.h))
}

func dispatchAppWindow(h uintptr, message uint32, wparam, lparam uintptr) uintptr {
	appWindows.Lock()
	w := appWindows.items[syscall.Handle(h)]
	appWindows.Unlock()
	if w != nil {
		switch message {
		case appWMCommand:
			if wparam>>16 == 0 {
				switch int(wparam & 0xffff) {
				case appReadID:
					if lparam == uintptr(w.read) {
						w.start("read")
					}
				case appUseID:
					if lparam == uintptr(w.use) {
						w.start("use")
					}
				}
			}
			return 0
		case appWMCompleted:
			if result, ok := w.actions.take(); ok {
				appSetWindowText.Call(uintptr(w.result), uintptr(unsafe.Pointer(appUTF16(windowResult(result)))))
				w.setBusy(false)
			}
			return 0
		case appWMClose:
			w.close()
			return 0
		case appWMDestroy:
			w.actions.close()
			w.posting.Lock()
			w.closed = true
			w.posting.Unlock()
			appPostQuit.Call(0)
			return 0
		case appWMSize:
			if w.result != 0 {
				w.layout()
			}
			return 0
		case appWMGetMinMaxInfo:
			r := appRect{Right: int32(w.px(560)), Bottom: int32(w.px(440))}
			appAdjustWindowRect.Call(uintptr(unsafe.Pointer(&r)), appWSOverlappedWindow, 0, appWSExControlParent)
			limits := (*appMinMaxInfo)(unsafe.Pointer(lparam))
			limits.MinTrack = appPoint{r.Right - r.Left, r.Bottom - r.Top}
			return 0
		case appWMCtlColorStatic, appWMCtlColorEdit:
			background, foreground := uintptr(appColorBtnFace), uintptr(appColorBtnText)
			if lparam == uintptr(w.result) {
				background, foreground = appColorWindow, appColorWindowText
			}
			bg, _, _ := appGetSysColor.Call(background)
			fg, _, _ := appGetSysColor.Call(foreground)
			appSetBkColor.Call(wparam, bg)
			appSetTextColor.Call(wparam, fg)
			brush, _, _ := appGetSysColorBrush.Call(background)
			return brush
		case appWMSettingChange, appWMThemeChanged:
			appInvalidateRect.Call(h, 0, 1)
		}
	}
	result, _, _ := appDefWindowProc.Call(h, uintptr(message), wparam, lparam)
	return result
}
