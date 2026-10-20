//go:build windows

package setup

import (
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

func modelDiskFree(path string) (uint64, error) {
	parent := filepath.Clean(path)
	for {
		if _, err := os.Stat(parent); err == nil {
			break
		}
		next := filepath.Dir(parent)
		if next == parent {
			return 0, os.ErrNotExist
		}
		parent = next
	}
	name, err := windows.UTF16PtrFromString(parent)
	if err != nil {
		return 0, err
	}
	var available uint64
	err = windows.GetDiskFreeSpaceEx(name, &available, nil, nil)
	return available, err
}
