//go:build windows

package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A stand-in for the installation's CLI answers `service upgrade-check` with
// the status written beside it, as serve's refuseDuringUpgrade does (3 while
// Setup's early stop holds the upgrade exclusion). The Panel and the
// presenter start beside it and exit only on that status.
func TestThePanelExitsWhileSetupReplacesItsInstallation(t *testing.T) {
	tools := t.TempDir()
	source := t.TempDir()
	program := `package main

import (
	"os"
	"path/filepath"
	"strconv"
)

func main() {
	if len(os.Args) != 3 || os.Args[1] != "service" || os.Args[2] != "upgrade-check" {
		os.Exit(2)
	}
	self, _ := os.Executable()
	raw, err := os.ReadFile(filepath.Join(filepath.Dir(self), "status.txt"))
	if err != nil {
		os.Exit(1)
	}
	code, _ := strconv.Atoi(string(raw))
	os.Exit(code)
}
`
	if err := os.WriteFile(filepath.Join(source, "main.go"), []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "go.mod"), []byte("module upgradecheck\n\ngo 1.21\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	build := exec.Command("go", "build", "-o", filepath.Join(tools, "openabstractions.exe"), ".")
	build.Dir = source
	build.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the stand-in CLI: %v\n%s", err, out)
	}
	panel := func() (string, error) { return filepath.Join(tools, "Abstraction Panel.exe"), nil }
	for _, c := range []struct {
		status string
		exits  bool
	}{{"3", true}, {"0", false}, {"1", false}, {"2", false}} {
		if err := os.WriteFile(filepath.Join(tools, "status.txt"), []byte(c.status), 0o600); err != nil {
			t.Fatal(err)
		}
		if got := upgradeReplacingThisInstallation(panel, runUpgradeCheck); got != c.exits {
			t.Fatalf("upgrade-check status %s: exits %v, want %v", c.status, got, c.exits)
		}
	}
	// A development build has no installed CLI beside it and starts normally.
	elsewhere := func() (string, error) { return filepath.Join(t.TempDir(), "Abstraction Panel.exe"), nil }
	asked := false
	if upgradeReplacingThisInstallation(elsewhere, func(string) (int, error) { asked = true; return 3, nil }) || asked {
		t.Fatal("a Panel without an installed CLI beside it checked or exited")
	}
}
