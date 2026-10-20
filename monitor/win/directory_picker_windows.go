//go:build windows

package win

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"unsafe"

	"golang.org/x/sys/windows"
)

// IFileDialog/IShellItem slot order is the Windows SDK 10.0.26100.0
// ShObjIdl_core.h IFileDialogVtbl/IShellItemVtbl. The existing COM call owner
// remains comProc. This is the Panel's native UI, not a Facade or C ABI API.
// https://learn.microsoft.com/windows/win32/shell/common-file-dialog
type directoryDialogVtbl struct {
	iUnknownVtbl
	Show, SetFileTypes, SetFileTypeIndex, GetFileTypeIndex, Advise, Unadvise comProc
	SetOptions, GetOptions, SetDefaultFolder, SetFolder, GetFolder           comProc
	GetCurrentSelection, SetFileName, GetFileName, SetTitle                  comProc
	SetOkButtonLabel, SetFileNameLabel, GetResult, AddPlace                  comProc
	SetDefaultExtension, Close                                               comProc
}

type directoryDialog struct{ vtbl *directoryDialogVtbl }
type directoryItemVtbl struct {
	iUnknownVtbl
	BindToHandler, GetParent, GetDisplayName, GetAttributes, Compare comProc
}
type directoryItem struct{ vtbl *directoryItemVtbl }

var directoryCoCreate = ole32.NewProc("CoCreateInstance")
var directoryClass = windows.GUID{Data1: 0xdc1c5a9c, Data2: 0xe88a, Data3: 0x4dde, Data4: [8]byte{0xa5, 0xa1, 0x60, 0xf8, 0x2a, 0x20, 0xae, 0xf7}}
var directoryInterface = windows.GUID{Data1: 0xd57c7288, Data2: 0xd4ad, Data3: 0x4768, Data4: [8]byte{0xbe, 0x02, 0x9d, 0x96, 0x95, 0x32, 0xd9, 0x60}}

type directoryPickResult struct {
	path string
	err  error
}

// A request has one window lifetime and one queued picker. Cancellation never
// publishes a late selection, including when the window closed before dispatch.
func windowDirectoryPicker(window context.Context, do func(func()), choose func(context.Context) (string, error)) func(context.Context) (string, error) {
	var mu sync.Mutex
	busy := false
	return func(parent context.Context) (string, error) {
		ctx, cancel := context.WithCancel(parent)
		stop := context.AfterFunc(window, cancel)
		defer stop()
		defer cancel()
		if window.Err() != nil || ctx.Err() != nil {
			return "", context.Canceled
		}
		mu.Lock()
		if busy {
			mu.Unlock()
			return "", errors.New("a folder picker is already open")
		}
		busy = true
		mu.Unlock()
		result := make(chan directoryPickResult, 1)
		do(func() {
			defer func() { mu.Lock(); busy = false; mu.Unlock() }()
			if ctx.Err() != nil {
				result <- directoryPickResult{err: ctx.Err()}
				return
			}
			path, err := choose(ctx)
			if ctx.Err() != nil {
				path, err = "", ctx.Err()
			}
			result <- directoryPickResult{path: path, err: err}
		})
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case picked := <-result:
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			return picked.path, picked.err
		}
	}
}

// chooseDirectory runs on the existing STA window thread. IFileDialog owns the
// modal UI; cancellation queues Close on that same thread through Window.Do.
func chooseDirectory(ctx context.Context, owner *Window) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var dialog *directoryDialog
	hr, _, _ := directoryCoCreate.Call(uintptr(unsafe.Pointer(&directoryClass)), 0, 1, uintptr(unsafe.Pointer(&directoryInterface)), uintptr(unsafe.Pointer(&dialog))) //unchecked: COM returns the checked HRESULT in hr, not GetLastError
	if int32(hr) < 0 || dialog == nil {
		return "", errors.New("the folder picker is unavailable")
	}
	self := uintptr(unsafe.Pointer(dialog))
	defer dialog.vtbl.Release.call(self) //unchecked: Release returns the remaining reference count
	var options uint32
	if int32(dialog.vtbl.GetOptions.call(self, uintptr(unsafe.Pointer(&options)))) < 0 {
		return "", errors.New("the folder picker options are unavailable")
	}
	// FOS_PICKFOLDERS | FORCEFILESYSTEM | NOCHANGEDIR | PATHMUSTEXIST |
	// DONTADDTORECENT, preserving the dialog's existing default options.
	if int32(dialog.vtbl.SetOptions.call(self, uintptr(options|0x20|0x40|0x8|0x800|0x02000000))) < 0 ||
		int32(dialog.vtbl.SetTitle.call(self, uintptr(unsafe.Pointer(utf16("Choose a folder"))))) < 0 {
		return "", errors.New("the folder picker could not be prepared")
	}
	var active, closeFailed atomic.Bool
	active.Store(true)
	finished := make(chan struct{})
	watchDone := make(chan struct{})
	go func() {
		defer close(watchDone)
		select {
		case <-finished:
		case <-ctx.Done():
			owner.Do(func() {
				if active.Load() {
					closeFailed.Store(int32(dialog.vtbl.Close.call(self, 0x800704c7)) < 0)
				}
			})
		}
	}()
	hr = dialog.vtbl.Show.call(self, owner.Handle())
	active.Store(false)
	close(finished)
	<-watchDone
	if closeFailed.Load() {
		return "", errors.New("the folder picker could not confirm cancellation")
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if uint32(hr) == 0x800704c7 { // HRESULT_FROM_WIN32(ERROR_CANCELLED)
		return "", nil
	}
	if int32(hr) < 0 {
		return "", errors.New("the folder picker did not complete")
	}
	var item *directoryItem
	if int32(dialog.vtbl.GetResult.call(self, uintptr(unsafe.Pointer(&item)))) < 0 || item == nil {
		return "", errors.New("the project folder selection is unavailable")
	}
	itemSelf := uintptr(unsafe.Pointer(item))
	defer item.vtbl.Release.call(itemSelf) //unchecked: Release returns the remaining reference count
	var path *uint16
	if int32(item.vtbl.GetDisplayName.call(itemSelf, 0x80058000, uintptr(unsafe.Pointer(&path)))) < 0 || path == nil { // SIGDN_FILESYSPATH
		return "", errors.New("the selected item is not a filesystem folder")
	}
	defer coTaskMemFree.Call(uintptr(unsafe.Pointer(path))) //unchecked: CoTaskMemFree has no return value
	return windows.UTF16PtrToString(path), nil
}
