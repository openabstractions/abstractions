//go:build !windows

package main

import "github.com/openabstractions/abstraction-storage/go/layout"

// accountFolders are this account's local application data folder, where an
// application manifest's {local_app_data} names the application's own
// files (layout.LocalAppData), and the person's Pictures folder
// (storage layout, STO-L5).
func accountFolders() (localAppData, pictures string, err error) {
	if localAppData, err = layout.LocalAppData(); err != nil {
		return "", "", err
	}
	if pictures, err = layout.ResolvePerson(layout.Current(), layout.Pictures); err != nil {
		return "", "", err
	}
	return localAppData, pictures, nil
}
