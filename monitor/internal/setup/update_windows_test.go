package setup

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-inference/go/providerhost/enginefiles"
)

// The Lemonade provider gives its package folder the engine-files read-only
// DACL (inference CONTRACT.md INF-F3). Removing a retired version gives that
// folder back to the account first; without it the package files stay and
// the removal fails.
func TestRemovingAnEarlierVersionUnlocksItsEngineFolder(t *testing.T) {
	f := newUpdateFixture(t, "lemonade-engine", packageEntries("lemonade-engine", "2.0.0"))
	engine := filepath.Join(f.root1, "lemonade")
	sid, err := enginefiles.AccountSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := enginefiles.SetFolderACL(engine, sid, enginefiles.ReadOnly); err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(filepath.Dir(f.root1), "."+filepath.Base(f.root1)+".removing")
	t.Cleanup(func() {
		for _, root := range []string{f.root1, aside} {
			if err := enginefiles.UnlockWithin(root); err != nil {
				t.Errorf("unlock %s: %v", root, err)
			}
		}
	})
	kept, err := removePackageTree(f.v1, f.root1)
	if err != nil {
		t.Fatalf("the read-only engine folder blocked the removal: %v", err)
	}
	if kept != 0 {
		t.Fatalf("kept %d files of the package", kept)
	}
	for _, gone := range []string{f.root1, aside} {
		if _, err := os.Lstat(gone); !os.IsNotExist(err) {
			t.Fatalf("%s remains after the removal: %v", gone, err)
		}
	}
}
