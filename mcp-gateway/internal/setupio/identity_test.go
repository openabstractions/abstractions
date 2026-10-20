package setupio

import (
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/openabstractions/abstraction-storage/go/layout"
)

// ClaudeConfigDirFor is .claude in the home folder without an override, the
// override itself when one is absolute, and a refusal for a relative one.
func TestClaudeConfigDirFor(t *testing.T) {
	home := t.TempDir()
	platform := layout.Platform{
		GOOS:    runtime.GOOS,
		Known:   func(string) (string, error) { return home, nil },
		Temp:    func() (string, error) { return home, nil },
		Profile: func() (string, error) { return "", nil },
		Getenv: func(name string) string {
			if name == "HOME" {
				return home
			}
			return ""
		},
	}
	override := filepath.Join(t.TempDir(), "config")
	homeDir := func() (string, error) { return home, nil }
	if got, err := ClaudeConfigDirFor(platform, "", homeDir); err != nil || got != filepath.Join(home, ".claude") {
		t.Fatalf("default = %q, %v", got, err)
	}
	if got, err := ClaudeConfigDirFor(platform, override, homeDir); err != nil || got != override {
		t.Fatalf("override = %q, %v", got, err)
	}
	if got, err := ClaudeConfigDirFor(platform, filepath.Join("relative", "config"), homeDir); err == nil || got != "" {
		t.Fatalf("relative override = %q, %v", got, err)
	}
	failed := errors.New("no home")
	if got, err := ClaudeConfigDirFor(platform, "", func() (string, error) { return "", failed }); !errors.Is(err, failed) || got != "" {
		t.Fatalf("missing home = %q, %v", got, err)
	}
}
