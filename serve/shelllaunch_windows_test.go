package main

import (
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
)

// From a caller inside a packaged app, a process started through the desktop
// shell sees the real profile. Outside a packaged app there is nothing to
// escape, so the check is skipped.
func TestShellLaunchEscapesAPackagedProfileView(t *testing.T) {
	view, err := bootstrap.CurrentProfileView()
	if err != nil || !view.Virtualized {
		t.Skipf("profile view %s %v: not inside a packaged app", view, err)
	}
	marker := filepath.Join(t.TempDir(), "profile-view")
	if err := launchThroughShell(os.Args[0], filepath.Dir(os.Args[0]), "-test.run=^TestShellHelperProcess$", "--", marker); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil && len(data) > 0 {
			if got := string(data); got != "real" {
				t.Fatalf("shell-launched process sees %q, want real", got)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the shell-launched helper wrote nothing to %s", marker)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestShellHelperProcess writes its own profile view to the marker named after
// "--". An ordinary test run passes no marker, and it does nothing.
func TestShellHelperProcess(t *testing.T) {
	args := flag.Args()
	if len(args) != 1 || !strings.HasSuffix(args[0], "profile-view") {
		return
	}
	view, err := bootstrap.CurrentProfileView()
	text := view.String()
	if err != nil {
		text = "error: " + err.Error()
	}
	os.WriteFile(args[0], []byte(text), 0o600)
}
