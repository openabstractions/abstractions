package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSelfSubjectPreservesTheWindowsInvocationPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got := probeSelf().Program; got != filepath.Clean(exe) {
		t.Fatalf("self program = %q, want native invocation path %q", got, filepath.Clean(exe))
	}
}
