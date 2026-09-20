package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// The installed Panel sets and revokes rights rules and manages credentials, so
// the runtime must name it an operator when it is installed beside the runtime;
// an absent sibling is never named.
func TestOperatorSiblingsNameTheInstalledPanel(t *testing.T) {
	dir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	exe := filepath.Join(dir, "openabstractions"+suffix)
	panel := filepath.Join(dir, "Abstraction Panel"+suffix)
	for _, path := range []string{exe, panel} {
		if err := os.WriteFile(path, nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got := operatorSiblings(exe)
	if !slices.Contains(got, panel) {
		t.Fatalf("operators %v do not name the installed Panel %s", got, panel)
	}
	if slices.Contains(got, filepath.Join(dir, "openabstractionsw"+suffix)) {
		t.Fatalf("operators %v name an absent windowless link", got)
	}
	if n := len(got); n != 2 {
		t.Fatalf("operators %v: want the runtime and the Panel only", got)
	}
}
