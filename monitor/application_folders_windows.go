//go:build windows

package main

import "golang.org/x/sys/windows"

// accountFolders are this account's local application data and Pictures
// folders, as the shell names them.
func accountFolders() (localAppData, pictures string, err error) {
	if localAppData, err = windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT); err != nil {
		return "", "", err
	}
	if pictures, err = windows.KnownFolderPath(windows.FOLDERID_Pictures, windows.KF_FLAG_DEFAULT); err != nil {
		return "", "", err
	}
	return localAppData, pictures, nil
}
