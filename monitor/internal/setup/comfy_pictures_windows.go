//go:build windows

package setup

import "golang.org/x/sys/windows"

func comfyPicturesDirectory() (string, error) {
	return windows.KnownFolderPath(windows.FOLDERID_Pictures, windows.KF_FLAG_DEFAULT)
}
