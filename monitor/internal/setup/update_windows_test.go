package setup

import (
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
)

// Antivirus or an indexer holds a file of the earlier version for a moment
// (TestRemovingAnEarlierVersionKeepsFilesOutsideItsReceipt failed once under
// load with "Access is denied"). The removal waits it out and finishes.
func TestRemovingAnEarlierVersionWaitsOutABriefFileHold(t *testing.T) {
	f := newUpdateFixture(t, "lemonade-engine", packageEntries("lemonade-engine", "2.0.0"))
	var held string
	if err := filepath.WalkDir(f.root1, func(path string, entry fs.DirEntry, err error) error {
		if err == nil && held == "" && entry.Type().IsRegular() {
			held = path
		}
		return err
	}); err != nil || held == "" {
		t.Fatalf("no file to hold in %s: %v", f.root1, err)
	}
	holder, err := os.Open(held) // Go opens without FILE_SHARE_DELETE: the folder cannot move.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(f.root1, f.root1+".probe"); err == nil {
		t.Fatal("the folder moved while a file in it was held")
	}
	released := make(chan error, 1)
	go func() {
		time.Sleep(300 * time.Millisecond)
		released <- holder.Close()
	}()
	kept, err := removePackageTree(f.v1, f.root1)
	if closeErr := <-released; closeErr != nil {
		t.Fatal(closeErr)
	}
	if err != nil {
		t.Fatalf("a brief hold failed the removal: %v", err)
	}
	if kept != 0 {
		t.Fatalf("kept %d files of the package", kept)
	}
	if _, err := os.Lstat(f.root1); !os.IsNotExist(err) {
		t.Fatalf("the earlier version remains: %v", err)
	}
}

// The Lemonade provider gives its package folder the engine-files read-only
// DACL (inference CONTRACT.md INF-F3). Removing a retired version gives that
// folder back to the account first; without it the package files stay and
// the removal fails.
func TestRemovingAnEarlierVersionUnlocksItsEngineFolder(t *testing.T) {
	f := newUpdateFixture(t, "lemonade-engine", packageEntries("lemonade-engine", "2.0.0"))
	engine := filepath.Join(f.root1, "lemonade")
	sid, err := programfiles.AccountSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := programfiles.SetFolderACL(engine, sid, programfiles.ReadOnly); err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(filepath.Dir(f.root1), "."+filepath.Base(f.root1)+".removing")
	t.Cleanup(func() {
		for _, root := range []string{f.root1, aside} {
			if err := programfiles.UnlockWithin(root); err != nil {
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
