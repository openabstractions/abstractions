//go:build !windows

package setup

// readUninstallEntries has no uninstall registry to read off Windows.
func readUninstallEntries() ([]UninstallEntry, error) { return nil, nil }
