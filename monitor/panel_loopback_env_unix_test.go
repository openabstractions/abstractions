//go:build !windows

package main

import (
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strconv"
	"testing"
)

// A program that starts the installed Panel with -native=false also chooses
// its HOME. The guard finds ~/.local/bin under the home folder the account
// database records for this process's user, whatever HOME says: a Panel under
// a forged HOME is a stranger, and the recorded folder stays listed.
func TestInstalledPanelIgnoresForgedHome(t *testing.T) {
	account, err := user.LookupId(strconv.Itoa(os.Getuid()))
	if err != nil {
		t.Skip("this account has no account database entry here:", err)
	}
	want := filepath.Join(account.HomeDir, ".local", "bin")
	forged := t.TempDir()
	t.Setenv("HOME", forged)
	t.Setenv("XDG_DATA_HOME", filepath.Join(forged, ".local", "share"))
	folders, err := panelInstallFolders()
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(folders, want) {
		t.Errorf("install folders %q under a forged HOME miss %q", folders, want)
	}
	if slices.Contains(folders, filepath.Join(forged, ".local", "bin")) {
		t.Errorf("install folders %q follow the forged HOME", folders)
	}
	decoy := filepath.Join(forged, ".local", "bin", "Abstraction Panel")
	if err := os.MkdirAll(filepath.Dir(decoy), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(decoy, []byte("decoy panel"), 0o700); err != nil {
		t.Fatal(err)
	}
	if installedPanel(decoy) {
		t.Errorf("a Panel under the forged HOME was judged the installed Panel")
	}
}
