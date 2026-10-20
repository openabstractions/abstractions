package main

import (
	"path/filepath"

	identity "github.com/openabstractions/abstraction-identity"
)

// Fixtures name files under t.TempDir, which may carry an 8.3 short spelling;
// stores keep the long spelling. These compare through the identity owner.

// samePanelProgram reports whether two subject programs name one program.
func samePanelProgram(a, b string) bool { return identity.SameSubjectProgram(a, b) }

// samePanelPath reports whether two existing paths name one file or folder.
func samePanelPath(a, b string) bool {
	same, err := identity.SameFile(a, b)
	return err == nil && same
}

// samePanelArgs compares command arguments, with absolute paths compared by
// file identity.
func samePanelArgs(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] == want[i] {
			continue
		}
		if !filepath.IsAbs(got[i]) || !filepath.IsAbs(want[i]) || !samePanelPath(got[i], want[i]) {
			return false
		}
	}
	return true
}
