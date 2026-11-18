//go:build !windows

package main

import (
	"fmt"
	"os"
	"testing"

	"github.com/openabstractions/abstractions/serve/internal/durabletemp"
	"github.com/openabstractions/abstractions/serve/internal/personfolders"
)

// TestMain runs this package's tests with the person's per-user folders
// inside a temporary folder (personfolders.Main); on Windows the TestMain
// of host_windows_test.go does. The temporary folder lies on a file system
// that keeps file IDs (durabletemp.Enter): the runtime's pairing and message
// peer state, and the fixtures, pin folders by stored file ID.
func TestMain(m *testing.M) {
	temp, err := durabletemp.Enter()
	if err != nil {
		fmt.Fprintln(os.Stderr, "durabletemp:", err)
		os.Exit(2)
	}
	code := withInstalledStoresPinned(func() int { return personfolders.Main(m) })
	durabletemp.Leave(temp)
	os.Exit(code)
}
