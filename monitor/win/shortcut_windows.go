//go:build windows

package win

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Shortcut is one shell link (.lnk): the program it starts, that program's
// arguments and the description Windows shows as its tooltip. The link shows
// its target's icon.
type Shortcut struct {
	Target      string
	Arguments   string
	Description string
}

// IShellLinkW and IPersistFile slot order is the Windows SDK 10.0.26100.0
// ShObjIdl_core.h IShellLinkWVtbl and ObjIdl.h IPersistFileVtbl.
// https://learn.microsoft.com/windows/win32/api/shobjidl_core/nn-shobjidl_core-ishelllinkw
type shellLinkVtbl struct {
	iUnknownVtbl
	GetPath, GetIDList, SetIDList, GetDescription, SetDescription                   comProc
	GetWorkingDirectory, SetWorkingDirectory, GetArguments, SetArguments            comProc
	GetHotkey, SetHotkey, GetShowCmd, SetShowCmd, GetIconLocation, SetIconLocation comProc
	SetRelativePath, Resolve, SetPath                                               comProc
}
type shellLink struct{ vtbl *shellLinkVtbl }

type persistFileVtbl struct {
	iUnknownVtbl
	GetClassID, IsDirty, Load, Save, SaveCompleted, GetCurFile comProc
}
type persistFile struct{ vtbl *persistFileVtbl }

var (
	shellLinkClass     = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	shellLinkInterface = windows.GUID{Data1: 0x000214f9, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	persistFileIID     = windows.GUID{Data1: 0x0000010b, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
	shortcutCoCreate   = ole32.NewProc("CoCreateInstance")
)

// shortcutTextLimit bounds each string read back from a link; a Start-menu
// link's target and arguments fit well inside it.
const shortcutTextLimit = 4096

// withShellLink runs f on one locked thread inside its own single-threaded
// apartment, with a fresh ShellLink object and its IPersistFile.
func withShellLink(f func(link *shellLink, file *persistFile) error) error {
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		if err := windows.CoInitializeEx(0, windows.COINIT_APARTMENTTHREADED|windows.COINIT_DISABLE_OLE1DDE); err != nil {
			done <- fmt.Errorf("CoInitializeEx: %w", err)
			return
		}
		defer windows.CoUninitialize()
		var link *shellLink
		hr, _, _ := shortcutCoCreate.Call(uintptr(unsafe.Pointer(&shellLinkClass)), 0, 1, uintptr(unsafe.Pointer(&shellLinkInterface)), uintptr(unsafe.Pointer(&link))) //unchecked: COM returns the checked HRESULT in hr, not GetLastError
		if int32(hr) < 0 || link == nil {
			done <- fmt.Errorf("the shell link object is unavailable: %#x", uint32(hr))
			return
		}
		self := uintptr(unsafe.Pointer(link))
		defer link.vtbl.Release.call(self) //unchecked: Release returns the remaining reference count
		var file *persistFile
		if hr := link.vtbl.QueryInterface.call(self, uintptr(unsafe.Pointer(&persistFileIID)), uintptr(unsafe.Pointer(&file))); int32(hr) < 0 || file == nil {
			done <- fmt.Errorf("the shell link cannot be saved: %#x", uint32(hr))
			return
		}
		defer file.vtbl.Release.call(uintptr(unsafe.Pointer(file))) //unchecked: Release returns the remaining reference count
		done <- f(link, file)
	}()
	return <-done
}

func checked(hr uintptr, what string) error {
	if int32(hr) < 0 {
		return fmt.Errorf("%s: %#x", what, uint32(hr))
	}
	return nil
}

// WriteShortcut saves s as the shell link at path, replacing a link there.
func WriteShortcut(path string, s Shortcut) error {
	if s.Target == "" {
		return errors.New("a shortcut needs a target program")
	}
	return withShellLink(func(link *shellLink, file *persistFile) error {
		self := uintptr(unsafe.Pointer(link))
		if err := checked(link.vtbl.SetPath.call(self, uintptr(unsafe.Pointer(utf16(s.Target)))), "SetPath"); err != nil {
			return err
		}
		if err := checked(link.vtbl.SetArguments.call(self, uintptr(unsafe.Pointer(utf16(s.Arguments)))), "SetArguments"); err != nil {
			return err
		}
		if err := checked(link.vtbl.SetDescription.call(self, uintptr(unsafe.Pointer(utf16(s.Description)))), "SetDescription"); err != nil {
			return err
		}
		return checked(file.vtbl.Save.call(uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(utf16(path))), 1), "Save")
	})
}

// ReadShortcut reads the shell link at path. A missing file is the error
// os.Lstat returns for it (fs.ErrNotExist).
func ReadShortcut(path string) (Shortcut, error) {
	if _, err := os.Lstat(path); err != nil {
		return Shortcut{}, err
	}
	var s Shortcut
	err := withShellLink(func(link *shellLink, file *persistFile) error {
		// STGM_READ.
		if err := checked(file.vtbl.Load.call(uintptr(unsafe.Pointer(file)), uintptr(unsafe.Pointer(utf16(path))), 0), "Load"); err != nil {
			return err
		}
		self := uintptr(unsafe.Pointer(link))
		buffer := make([]uint16, shortcutTextLimit)
		read := func(method comProc, what string, extra ...uintptr) (string, error) {
			clear(buffer)
			args := append([]uintptr{self, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer))}, extra...)
			if err := checked(method.call(args...), what); err != nil {
				return "", err
			}
			return windows.UTF16ToString(buffer), nil
		}
		var err error
		// SLGP_RAWPATH: the path as stored, environment strings unexpanded.
		if s.Target, err = read(link.vtbl.GetPath, "GetPath", 0, 4); err != nil {
			return err
		}
		if s.Arguments, err = read(link.vtbl.GetArguments, "GetArguments"); err != nil {
			return err
		}
		s.Description, err = read(link.vtbl.GetDescription, "GetDescription")
		return err
	})
	return s, err
}
