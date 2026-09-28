package main

import (
	"os"
	"path/filepath"

	identity "github.com/openabstractions/abstraction-identity"
)

// selfProgramPath resolves this process's own image path to the canonical
// long form identity.SubjectProgram now derives for the same peer connection
// (identity.CanonicalProgramPath): a short DOS 8.3 launch alias no longer
// makes a self decide's displayed subject diverge from the subject the
// runtime actually compares against.
func selfProgramPath(fallback string) string {
	exe, err := os.Executable()
	if err != nil {
		return fallback
	}
	return identity.CanonicalProgramPath(filepath.Clean(exe))
}
