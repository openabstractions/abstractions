package gateway

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// privateInput is documented in serve/private_input_test.go.
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
