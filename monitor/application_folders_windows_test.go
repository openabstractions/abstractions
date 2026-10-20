//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A junction inside {app} that points outside it does not make the outside
// folder part of {app}: identity.Within resolves the junction first.
func TestAJunctionInsideTheAppFolderToOutsideIsRefused(t *testing.T) {
	folders, outside := testFolders(t)
	link := filepath.Join(folders["app"], "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	// Remove the link itself, never what it points to, before the temporary
	// folder is cleaned up.
	t.Cleanup(func() { _ = os.Remove(link) })
	for _, value := range []string{"{app}/link/evil.exe", "{app}/link/sub/evil.exe"} {
		if got, err := folders.resolve(value, true); err == nil {
			t.Errorf("%s resolved to %q through a junction out of {app}", value, got)
		}
		if got, err := folders.resolve(value, false); err == nil {
			t.Errorf("argument %s resolved to %q through a junction out of {app}", value, got)
		}
	}
	if _, err := folders.resolve("{app}/real/app.exe", true); err != nil {
		t.Fatalf("a path inside {app} beside the junction: %v", err)
	}
}
