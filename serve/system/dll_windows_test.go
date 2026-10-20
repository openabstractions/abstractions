package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/windows"
)

// TestLibrarySearchSkipsAPlantedLibrary: after the restriction a library
// named without a path is looked up in the system directory only, so one
// planted in the current folder is never found.
func TestLibrarySearchSkipsAPlantedLibrary(t *testing.T) {
	dir := t.TempDir()
	const name = "oa-planted-probe.dll"
	if err := os.WriteFile(filepath.Join(dir, name), []byte("not a library"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	if err := restrictLibrarySearch(); err != nil {
		t.Fatal(err)
	}
	h, err := windows.LoadLibraryEx(name, 0, 0)
	if err == nil {
		windows.FreeLibrary(h)
		t.Fatal("a library planted in the current folder loaded")
	}
	if !errors.Is(err, windows.ERROR_MOD_NOT_FOUND) {
		t.Fatalf("the planted file was found and opened: %v", err)
	}
	if h, err := windows.LoadLibraryEx("version.dll", 0, 0); err != nil {
		t.Fatalf("a system library no longer loads: %v", err)
	} else {
		windows.FreeLibrary(h)
	}
}
