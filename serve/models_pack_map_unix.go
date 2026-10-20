//go:build unix

package main

import (
	"fmt"
	"os"
	"syscall"
)

// mapLocked reads path into memory. Unix has no mandatory lock that keeps
// writers out of a file, and a shared mapping shows their writes; a
// private copy keeps the bytes a caller hashes the bytes it later decodes
// against.
func mapLocked(path string) ([]byte, func(), error) {
	data, err := os.ReadFile(path)
	return data, func() {}, err
}

// mapFile maps path read-only, so packing reads it through the page cache
// without copying it into the Go heap. unmap releases it.
func mapFile(path string) ([]byte, func(), error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	//unchecked: the mapping keeps the pages; the descriptor is not needed after mmap
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, nil, err
	}
	if st.Size() == 0 {
		return nil, func() {}, nil
	}
	if int64(int(st.Size())) != st.Size() {
		return nil, nil, fmt.Errorf("%s is too large to map here", path)
	}
	data, err := syscall.Mmap(int(f.Fd()), 0, int(st.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, nil, fmt.Errorf("mapping %s: %w", path, err)
	}
	return data, func() {
		//unchecked: releasing a read-only mapping; nothing was written through it
		syscall.Munmap(data)
	}, nil
}
