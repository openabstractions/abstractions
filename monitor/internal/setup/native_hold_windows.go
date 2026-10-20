package setup

import (
	"os"

	"golang.org/x/sys/windows"
)

// openWriteExcluded opens path for reading and shares it for reading only:
// no process opens it for writing or deleting while the handle is open. A
// link at path is refused.
func openWriteExcluded(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return nil, err
	}
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		windows.CloseHandle(handle)
		return nil, err
	}
	if info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT|windows.FILE_ATTRIBUTE_DEVICE) != 0 {
		windows.CloseHandle(handle)
		return nil, os.ErrInvalid
	}
	return os.NewFile(uintptr(handle), path), nil
}
