//go:build !windows

package main

// installedApplicationManifest: only the Windows Setup packages record an
// installed application's manifest.
func installedApplicationManifest(string) (string, bool) { return "", false }

// installedApplicationRecords: only the Windows Setup packages record one.
func installedApplicationRecords() map[string]string { return map[string]string{} }

// setupAppFolder: only the Windows Setup packages install into a fixed folder.
func setupAppFolder(string) string { return "" }
