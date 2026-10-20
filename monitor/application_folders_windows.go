//go:build windows

package main

import (
	"github.com/openabstractions/abstraction-storage/go/layout"
	"golang.org/x/sys/windows"
)

// accountFolders are this account's local application data folder, where an
// application manifest's {local_app_data} names the application's own
// files, and the person's Pictures folder (storage layout, STO-L5).
func accountFolders() (localAppData, pictures string, err error) {
	if localAppData, err = windows.KnownFolderPath(windows.FOLDERID_LocalAppData, windows.KF_FLAG_DEFAULT); err != nil {
		return "", "", err
	}
	if pictures, err = layout.ResolvePerson(layout.Current(), layout.Pictures); err != nil {
		return "", "", err
	}
	return localAppData, pictures, nil
}
