//go:build windows

package main

import (
	"path/filepath"

	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/monitor/win"
)

func init() {
	// The account's Start menu Programs folder lies in its roaming
	// application data, which storage's layout resolves.
	startMenuPrograms = func() (string, error) {
		_, roaming, err := layout.WindowsAccountFolders(layout.Current())
		if err != nil {
			return "", err
		}
		return filepath.Join(roaming, "Microsoft", "Windows", "Start Menu", "Programs"), nil
	}
	readStartShortcut = func(path string) (startShortcut, error) {
		s, err := win.ReadShortcut(path)
		return startShortcut(s), err
	}
	writeStartShortcut = func(path string, s startShortcut) error {
		return win.WriteShortcut(path, win.Shortcut(s))
	}
}
