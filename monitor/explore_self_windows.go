package main

import (
	"os"
	"path/filepath"

	identity "github.com/openabstractions/abstraction-identity"
)

// exploreSelfProgram resolves the Panel's own image path to the canonical
// long form identity.SubjectProgram now derives for the same native service
// connection (identity.CanonicalProgramPath): a short DOS 8.3 launch alias no
// longer names a different rights subject from the program path observed
// there.
func exploreSelfProgram(fallback string) string {
	exe, err := os.Executable()
	if err != nil {
		return fallback
	}
	return identity.CanonicalProgramPath(filepath.Clean(exe))
}
