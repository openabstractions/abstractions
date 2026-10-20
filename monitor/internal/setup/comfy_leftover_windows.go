//go:build windows

package setup

import (
	"io/fs"
	"syscall"
)

// reparsePoint reports a junction, symbolic link or other reparse point.
func reparsePoint(info fs.FileInfo) bool {
	data, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || data.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
