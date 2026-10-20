package setup

import (
	"io/fs"
	"syscall"

	"golang.org/x/sys/windows"
)

func discoveryReparse(info fs.FileInfo) bool {
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return !ok || attributes.FileAttributes&windows.FILE_ATTRIBUTE_REPARSE_POINT != 0
}
