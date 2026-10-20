//go:build !windows

package main

import (
	"os"
	"path/filepath"
)

// accountFolders are this account's local application data and Pictures
// folders: the user cache folder and ~/Pictures.
func accountFolders() (localAppData, pictures string, err error) {
	if localAppData, err = os.UserCacheDir(); err != nil {
		return "", "", err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", "", err
	}
	return localAppData, filepath.Join(home, "Pictures"), nil
}
