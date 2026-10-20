//go:build windows

package setup

import (
	"path/filepath"
	"testing"
)

// The retire guard keeps a version when the registered program or the version
// folder is a path identity refuses: it cannot be shown outside (ID-F1).
func TestProgramWithinKeepsVersionsItCannotCompare(t *testing.T) {
	root := t.TempDir()
	share := `\\oa-test-host.invalid\share\ComfyUI`
	if !ProgramWithin(root, share+`\python.exe`) || !ProgramWithin(share, filepath.Join(root, "python.exe")) {
		t.Fatal("a refused path let the retire guard remove a version")
	}
	if ProgramWithin(root, filepath.Join(t.TempDir(), "python.exe")) {
		t.Fatal("a local program outside the version counted as inside")
	}
	if !ProgramWithin(root, filepath.Join(root, "python.exe")) {
		t.Fatal("a local program inside the version did not count")
	}
}
