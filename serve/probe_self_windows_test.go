package main

import (
	"os"
	"path/filepath"
	"testing"

	identity "github.com/openabstractions/abstraction-identity"
)

func TestSelfSubjectCanonicalizesTheWindowsInvocationPath(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	want := identity.CanonicalProgramPath(filepath.Clean(exe))
	if got := probeSelf().Program; got != want {
		t.Fatalf("self program = %q, want the canonical invocation path %q", got, want)
	}
}
