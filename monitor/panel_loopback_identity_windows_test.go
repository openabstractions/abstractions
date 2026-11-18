//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// fakeInstall puts an "installed" Panel file in its own install folder and
// makes that folder the only one the guard reads.
func fakeInstall(t *testing.T) (folder, panel string) {
	t.Helper()
	folder = filepath.Join(t.TempDir(), "tools")
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	panel = filepath.Join(folder, "Abstraction Panel.exe")
	if err := os.WriteFile(panel, []byte("installed panel"), 0o700); err != nil {
		t.Fatal(err)
	}
	saved := panelInstallFolders
	panelInstallFolders = func() ([]string, error) { return []string{folder}, nil }
	t.Cleanup(func() { panelInstallFolders = saved })
	return folder, panel
}

// Security review 2026-11-22, F1: the guard decides by file identity, as the
// runtime admits the Panel. A hard link of the installed Panel in another
// folder is the installed Panel.
func TestInstalledPanelHardLinkIsGuarded(t *testing.T) {
	_, panel := fakeInstall(t)
	link := filepath.Join(t.TempDir(), "Copy.exe")
	if err := os.Link(panel, link); err != nil {
		t.Skip("hard links unavailable here:", err)
	}
	if !installedPanel(link) {
		t.Fatal("a hard link of the installed Panel was not guarded")
	}
	copyFile := filepath.Join(t.TempDir(), "Abstraction Panel.exe")
	if err := os.WriteFile(copyFile, []byte("installed panel"), 0o700); err != nil {
		t.Fatal(err)
	}
	if installedPanel(copyFile) {
		t.Fatal("a separate copy was taken for the installed Panel")
	}
	if !installedPanel(filepath.Join(t.TempDir(), "missing.exe")) {
		t.Fatal("an unreadable own program was not guarded")
	}
}

// A start through a junction to the install folder is the installed Panel.
func TestInstalledPanelThroughJunctionIsGuarded(t *testing.T) {
	folder, _ := fakeInstall(t)
	junction := filepath.Join(t.TempDir(), "via")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", junction, folder).CombinedOutput(); err != nil {
		t.Skip("junctions unavailable here:", err, string(out))
	}
	// The junction goes before its parent folder is deleted.
	t.Cleanup(func() { os.Remove(junction) })
	if !installedPanel(filepath.Join(junction, "Abstraction Panel.exe")) {
		t.Fatal("a start through a junction was not guarded")
	}
}
