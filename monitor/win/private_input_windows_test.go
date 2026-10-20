package win

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// privateInput is a path, relative to this package, that a test reads from
// the private source workspace because its check spans repositories: the
// file or module lives in a repository scripts/split.manifest publishes apart
// from this one. A published checkout lacks it and the test skips there; the
// private gate runs it (scripts/gate_select.py selects it when either side
// changes). scripts/fixture_reads_manifest.py refuses a test path outside the
// package that private source holds and this repository does not publish
// unless it is named here, and refuses a name here that private source lacks.
func privateInput(t *testing.T, parts ...string) string {
	t.Helper()
	path := filepath.Join(parts...)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			t.Skipf("%s is private-workspace input this repository does not publish; the private gate runs this check", filepath.ToSlash(path))
		}
		t.Fatal(err)
	}
	return path
}
