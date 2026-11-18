//go:build windows

package main

import "golang.org/x/sys/windows"

func readLocalDownloadDiskSpace(folder string) (free, total uint64, err error) {
	path, err := windows.UTF16PtrFromString(folder)
	if err != nil {
		return 0, 0, err
	}
	err = windows.GetDiskFreeSpaceEx(path, &free, &total, nil)
	return
}
