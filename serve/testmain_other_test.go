//go:build !windows

package main

import (
	"os"
	"testing"

	"github.com/openabstractions/abstractions/serve/internal/personfolders"
)

// TestMain runs this package's tests with the person's per-user folders
// inside a temporary folder (personfolders.Main); on Windows the TestMain
// of host_windows_test.go does.
func TestMain(m *testing.M) {
	os.Exit(withInstalledStoresPinned(func() int { return personfolders.Main(m) }))
}
