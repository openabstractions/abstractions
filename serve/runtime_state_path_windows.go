//go:build windows

package main

import (
	"strings"

	"golang.org/x/sys/windows"
)

// finalPath is an existing path as the file system resolves it: junctions,
// symbolic links, 8.3 short names and mapped network drives followed,
// through the open handle's final name (GetFinalPathNameByHandle).
// filepath.EvalSymlinks leaves a junction unresolved, since Go reports a
// junction as an irregular file. A variable so tests can stand in a mapped
// drive without creating one.
var finalPath = func(path string) (string, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return "", err
	}
	handle, err := windows.CreateFile(name, 0, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return "", err
	}
	//unchecked: the handle carries no data access
	defer windows.CloseHandle(handle)
	buf := make([]uint16, windows.MAX_PATH)
	for {
		n, err := windows.GetFinalPathNameByHandle(handle, &buf[0], uint32(len(buf)), 0)
		if err != nil {
			return "", err
		}
		if int(n) < len(buf) {
			final := windows.UTF16ToString(buf[:n])
			if rest, ok := strings.CutPrefix(final, `\\?\UNC\`); ok {
				return `\\` + rest, nil
			}
			return strings.TrimPrefix(final, `\\?\`), nil
		}
		buf = make([]uint16, n+1)
	}
}
