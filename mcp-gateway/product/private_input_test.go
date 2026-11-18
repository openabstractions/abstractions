package product

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// privateInput guards a cross-repository fixture, using the body pinned by
// scripts/oa.py. The private gate runs this check; a published checkout skips
// only when its separately published input is absent.
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
