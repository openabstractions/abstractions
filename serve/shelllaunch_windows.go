package main

import (
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// errNoShell is a shell launch with no desktop shell of this account in the
// session: none runs, it runs as another account, or it refuses the request.
var errNoShell = errors.New("no desktop shell of this account")

var (
	shellOle32               = windows.NewLazySystemDLL("ole32.dll")
	shellOleaut32            = windows.NewLazySystemDLL("oleaut32.dll")
	shellUser32              = windows.NewLazySystemDLL("user32.dll")
	coCreateInstance         = shellOle32.NewProc("CoCreateInstance")
	coUninitialize           = shellOle32.NewProc("CoUninitialize")
	sysAllocString           = shellOleaut32.NewProc("SysAllocString")
	sysFreeString            = shellOleaut32.NewProc("SysFreeString")
	getShellWindow           = shellUser32.NewProc("GetShellWindow")
	getWindowThreadProcessID = shellUser32.NewProc("GetWindowThreadProcessId")

	clsidShellWindows       = windows.GUID{Data1: 0x9BA05972, Data2: 0xF6A8, Data3: 0x11CF, Data4: [8]byte{0xA4, 0x42, 0x00, 0xA0, 0xC9, 0x0A, 0x8F, 0x39}}
	iidIShellWindows        = windows.GUID{Data1: 0x85CB6900, Data2: 0x4D95, Data3: 0x11CF, Data4: [8]byte{0x96, 0x0C, 0x00, 0x80, 0xC7, 0xF4, 0xEE, 0x85}}
	iidIServiceProvider     = windows.GUID{Data1: 0x6D5140C1, Data2: 0x7436, Data3: 0x11CE, Data4: [8]byte{0x80, 0x34, 0x00, 0xAA, 0x00, 0x60, 0x09, 0xFA}}
	sidSTopLevelBrowser     = windows.GUID{Data1: 0x4C96BE40, Data2: 0x915C, Data3: 0x11CF, Data4: [8]byte{0x99, 0xD3, 0x00, 0xAA, 0x00, 0x4A, 0xE8, 0x37}}
	iidIShellBrowser        = windows.GUID{Data1: 0x000214E2, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIDispatch            = windows.GUID{Data1: 0x00020400, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIShellFolderViewDual = windows.GUID{Data1: 0xE7A1AF80, Data2: 0x4D96, Data3: 0x11CF, Data4: [8]byte{0x96, 0x0C, 0x00, 0x80, 0xC7, 0xF4, 0xEE, 0x85}}
	iidIShellDispatch2      = windows.GUID{Data1: 0xA4C6892C, Data2: 0x3BA9, Data3: 0x11D2, Data4: [8]byte{0x9D, 0xEA, 0x00, 0xC0, 0x4F, 0xB1, 0x61, 0x62}}
)

// variant is an OLE VARIANT on 64-bit Windows: 24 bytes.
type variant struct {
	vt  uint16
	_   [3]uint16
	val uintptr
	_   uintptr
}

// shellFrame holds every buffer COM reads or writes through a uintptr. It is
// stored in shellFrameAnchor, so it lives on the heap, where Go never moves an
// object, and stays reachable until the launch returns.
type shellFrame struct {
	location, root, arguments, directory, operation, show variant
	hwnd                                                  int32
	objects                                               [9]unsafe.Pointer
	pid                                                   uint32
}

var shellFrameAnchor atomic.Pointer[shellFrame]

func comCall(object unsafe.Pointer, index int, args ...uintptr) error {
	table := *(*unsafe.Pointer)(object)
	method := *(*uintptr)(unsafe.Add(table, index*int(unsafe.Sizeof(uintptr(0)))))
	hr, _, _ := syscall.SyscallN(method, append([]uintptr{uintptr(object)}, args...)...)
	if int32(hr) < 0 {
		return fmt.Errorf("COM method %d: HRESULT 0x%08X", index, uint32(hr))
	}
	return nil
}

func comRelease(object unsafe.Pointer) {
	if object != nil {
		comCall(object, 2)
	}
}

// shellIsThisAccount compares the desktop shell's token user with this
// process's, so a caller running as another account never launches into the
// desktop owner's session.
func shellIsThisAccount(frame *shellFrame) error {
	window, _, _ := getShellWindow.Call()
	if window == 0 {
		return errNoShell
	}
	getWindowThreadProcessID.Call(window, uintptr(unsafe.Pointer(&frame.pid)))
	process, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, frame.pid)
	if err != nil {
		return fmt.Errorf("%w: %v", errNoShell, err)
	}
	defer windows.CloseHandle(process)
	var token windows.Token
	if err := windows.OpenProcessToken(process, windows.TOKEN_QUERY, &token); err != nil {
		return fmt.Errorf("%w: %v", errNoShell, err)
	}
	defer token.Close()
	shellUser, err := token.GetTokenUser()
	if err != nil {
		return fmt.Errorf("%w: %v", errNoShell, err)
	}
	self, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	if !windows.EqualSid(shellUser.User.Sid, self.User.Sid) {
		return fmt.Errorf("%w: the desktop shell runs as %s", errNoShell, shellUser.User.Sid)
	}
	return nil
}

// launchThroughShell asks this session's desktop shell, explorer.exe out of
// process, to start image with args in dir, hidden. The started process gets
// the shell's token, environment and unvirtualized view of the profile, and
// leaves the caller's job object. It does not wait and returns no process:
// `start` observes readiness through the resolver.
func launchThroughShell(image, dir string, args ...string) error {
	return launchThroughShellMode(image, dir, 0, args...)
}

// launchThroughShellVisible uses the same account/session proof as runtime
// activation and asks the desktop shell to show an interactive application.
func launchThroughShellVisible(image, dir string, args ...string) error {
	const swShowNormal = 1
	return launchThroughShellMode(image, dir, swShowNormal, args...)
}

func launchThroughShellMode(image, dir string, show int, args ...string) error {
	frame := new(shellFrame)
	shellFrameAnchor.Store(frame)
	defer shellFrameAnchor.CompareAndSwap(frame, nil)
	if err := shellIsThisAccount(frame); err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED); err == nil {
		defer coUninitialize.Call()
	}
	release := func() {
		for i := len(frame.objects) - 1; i >= 0; i-- {
			comRelease(frame.objects[i])
		}
	}
	defer release()
	shellWindows, desktop, provider, browser, view, background, folderView, application, shell :=
		&frame.objects[0], &frame.objects[1], &frame.objects[2], &frame.objects[3], &frame.objects[4], &frame.objects[5], &frame.objects[6], &frame.objects[7], &frame.objects[8]
	const clsctxLocalServer = 4
	if hr, _, _ := coCreateInstance.Call(uintptr(unsafe.Pointer(&clsidShellWindows)), 0, clsctxLocalServer, uintptr(unsafe.Pointer(&iidIShellWindows)), uintptr(unsafe.Pointer(shellWindows))); int32(hr) < 0 {
		return fmt.Errorf("%w: ShellWindows: HRESULT 0x%08X", errNoShell, uint32(hr))
	}
	const swcDesktop, swfoNeedDispatch = 8, 1
	if err := comCall(*shellWindows, 15, uintptr(unsafe.Pointer(&frame.location)), uintptr(unsafe.Pointer(&frame.root)), swcDesktop, uintptr(unsafe.Pointer(&frame.hwnd)), swfoNeedDispatch, uintptr(unsafe.Pointer(desktop))); err != nil || *desktop == nil {
		return fmt.Errorf("%w: FindWindowSW: %v", errNoShell, err)
	}
	steps := []struct {
		name string
		do   func() error
	}{
		{"IServiceProvider", func() error {
			return comCall(*desktop, 0, uintptr(unsafe.Pointer(&iidIServiceProvider)), uintptr(unsafe.Pointer(provider)))
		}},
		{"QueryService", func() error {
			return comCall(*provider, 3, uintptr(unsafe.Pointer(&sidSTopLevelBrowser)), uintptr(unsafe.Pointer(&iidIShellBrowser)), uintptr(unsafe.Pointer(browser)))
		}},
		{"QueryActiveShellView", func() error { return comCall(*browser, 15, uintptr(unsafe.Pointer(view))) }},
		{"GetItemObject", func() error {
			const svgioBackground = 0
			return comCall(*view, 15, svgioBackground, uintptr(unsafe.Pointer(&iidIDispatch)), uintptr(unsafe.Pointer(background)))
		}},
		{"IShellFolderViewDual", func() error {
			return comCall(*background, 0, uintptr(unsafe.Pointer(&iidIShellFolderViewDual)), uintptr(unsafe.Pointer(folderView)))
		}},
		{"Application", func() error { return comCall(*folderView, 7, uintptr(unsafe.Pointer(application))) }},
		{"IShellDispatch2", func() error {
			return comCall(*application, 0, uintptr(unsafe.Pointer(&iidIShellDispatch2)), uintptr(unsafe.Pointer(shell)))
		}},
	}
	for _, step := range steps {
		if err := step.do(); err != nil {
			return fmt.Errorf("%w: %s: %v", errNoShell, step.name, err)
		}
	}
	bstr := func(s string) (uintptr, error) {
		text, err := windows.UTF16PtrFromString(s)
		if err != nil {
			return 0, err
		}
		b, _, _ := sysAllocString.Call(uintptr(unsafe.Pointer(text)))
		runtime.KeepAlive(text)
		if b == 0 {
			return 0, errors.New("SysAllocString failed")
		}
		return b, nil
	}
	quoted := make([]string, len(args))
	for i, a := range args {
		quoted[i] = windows.EscapeArg(a)
	}
	file, err := bstr(image)
	if err != nil {
		return err
	}
	defer sysFreeString.Call(file)
	for _, v := range []struct {
		slot *variant
		text string
	}{{&frame.arguments, strings.Join(quoted, " ")}, {&frame.directory, dir}, {&frame.operation, "open"}} {
		b, err := bstr(v.text)
		if err != nil {
			return err
		}
		defer sysFreeString.Call(b)
		*v.slot = variant{vt: 8, val: b}
	}
	const vtI4 = 3
	frame.show = variant{vt: vtI4, val: uintptr(show)}
	// IShellDispatch2::ShellExecute. x64 passes each 24-byte VARIANT by reference.
	if err := comCall(*shell, 31, file, uintptr(unsafe.Pointer(&frame.arguments)), uintptr(unsafe.Pointer(&frame.directory)), uintptr(unsafe.Pointer(&frame.operation)), uintptr(unsafe.Pointer(&frame.show))); err != nil {
		return fmt.Errorf("activation refused: shell launch %s %v: %w", image, args, err)
	}
	return nil
}
