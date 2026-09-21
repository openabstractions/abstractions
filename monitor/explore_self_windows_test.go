package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExploreSelfPreservesTheWindowsInvocationPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if got := exploreSelf().Program; got != filepath.Clean(exe) {
		t.Fatalf("explore self program = %q, want native invocation path %q", got, filepath.Clean(exe))
	}
}
