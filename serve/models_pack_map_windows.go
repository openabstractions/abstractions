//go:build windows

package main

import (
	"fmt"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// mapFile maps path read-only, so packing reads it through the file cache
// without copying it into the Go heap. unmap releases it.
func mapFile(path string) ([]byte, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	return mapOpen(f, path)
}

// mapLocked maps path read-only through a handle that shares reading only,
// as npudecode's readLocked opens a kit file: until unmap, no one can open
// the file to write, truncate or delete it, so the bytes a caller hashes
// are the bytes it later decodes against. A file another process holds
// open for writing is refused with a sharing violation.
func mapLocked(path string) ([]byte, func(), error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, nil, err
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING,
		windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return mapOpen(os.NewFile(uintptr(h), path), path)
}

// mapOpen maps the open file f whole; unmap releases the view and closes f.
func mapOpen(f *os.File, path string) ([]byte, func(), error) {
	st, err := f.Stat()
	if err != nil {
		//unchecked: a read-only handle on a failed stat; the stat error is returned
		f.Close()
		return nil, nil, err
	}
	size := st.Size()
	if size == 0 {
		return nil, func() {
			//unchecked: a read-only handle of an empty file
			f.Close()
		}, nil
	}
	h, err := windows.CreateFileMapping(windows.Handle(f.Fd()), nil, windows.PAGE_READONLY, uint32(size>>32), uint32(size), nil)
	if err != nil {
		//unchecked: a read-only handle; the mapping error is returned
		f.Close()
		return nil, nil, fmt.Errorf("mapping %s: %w", path, err)
	}
	addr, err := windows.MapViewOfFile(h, windows.FILE_MAP_READ, 0, 0, uintptr(size))
	if err != nil {
		//unchecked: closing this call's own mapping handle; the view error is returned
		windows.CloseHandle(h)
		//unchecked: as above
		f.Close()
		return nil, nil, fmt.Errorf("mapping %s: %w", path, err)
	}
	p := *(*unsafe.Pointer)(unsafe.Pointer(&addr))
	data := unsafe.Slice((*byte)(p), size)
	return data, func() {
		//unchecked: releasing a read-only view; nothing was written through it
		windows.UnmapViewOfFile(addr)
		//unchecked: as above
		windows.CloseHandle(h)
		//unchecked: as above
		f.Close()
	}, nil
}
