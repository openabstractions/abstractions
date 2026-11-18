//go:build windows

package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// The installed Panel's developer page only reads, whichever runtime it is
// pointed at (rights RIGHTS-O17, monitor/panel_loopback_guard.go). The
// journeys answer questions and press Panel buttons through that page, and an
// isolated runtime admits the Panel beside its own program. A run against an
// installed tools folder therefore starts the isolated runtime and its Panel
// from private copies of that folder's programs: the same bytes, a different
// file, which the isolated runtime admits as its Panel and the guard leaves
// writable. The driver stays the runtime's --operator-program.

// installedToolFolders are the folders an installed Panel sits in, the list
// the Panel's guard reads (product.InstalledToolFolders). A folder it could
// not read stays out; the guard then guards every Panel and the journey says
// so. Tests replace it.
var installedToolFolders = func() []string {
	//unchecked: retain every readable folder; on a record error the Panel guard refuses changes for every Panel and the journey reports that refusal
	folders, _ := product.InstalledToolFolders()
	return folders
}

// holdsInstalledPanel reports whether tools' Panel is an installed Panel, by
// file identity as the guard compares it: a link to the installed Panel is
// the installed Panel.
func holdsInstalledPanel(tools string) bool {
	panel, err := os.Stat(filepath.Join(tools, "Abstraction Panel.exe"))
	if err != nil {
		return false
	}
	for _, folder := range installedToolFolders() {
		if folder == "" {
			continue
		}
		if installed, err := os.Stat(filepath.Join(folder, "Abstraction Panel.exe")); err == nil && os.SameFile(panel, installed) {
			return true
		}
	}
	return false
}

// runtimeTools is the folder the isolated runtime and its Panel start from:
// tools itself, or, when tools holds the installed Panel, dir holding copies
// of tools' top-level files. Subfolders (engines) stay where they are; the
// journeys name them through -tools.
func runtimeTools(tools, dir string) (string, error) {
	if !holdsInstalledPanel(tools) {
		return tools, nil
	}
	entries, err := os.ReadDir(tools)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, entry := range entries {
		if !entry.Type().IsRegular() {
			continue
		}
		if err := copyFile(filepath.Join(tools, entry.Name()), filepath.Join(dir, entry.Name())); err != nil {
			return "", fmt.Errorf("copy %s: %w", entry.Name(), err)
		}
	}
	if holdsInstalledPanel(dir) {
		return "", fmt.Errorf("the Panel copy in %s is still the installed Panel", dir)
	}
	return dir, nil
}
