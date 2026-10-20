package setup

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// A junction in the place of a removal's moved-aside folder is no removal
// under way, and the removal does not follow it.
func TestRemovalUnderwayRefusesAJunction(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "0.9.0")
	target := filepath.Join(dir, "elsewhere")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(target, "keep.txt")
	if err := os.WriteFile(keep, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(dir, ".0.9.0.removing")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", aside, target).CombinedOutput(); err != nil {
		t.Fatalf("mklink: %v %s", err, out)
	}
	t.Cleanup(func() { os.Remove(aside) })
	if removalUnderway(root) {
		t.Fatal("a junction reads as a removal under way")
	}
	if _, err := removePackageTreeContext(context.Background(), Artifact{}, root); err == nil {
		t.Fatal("the removal followed a junction")
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("the junction's target lost its file")
	}
}
