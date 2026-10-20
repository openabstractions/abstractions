//go:build windows

package setup

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/placetest"
)

func TestFolderProgramRunning(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if running, err := folderProgramRunning(filepath.Dir(program)); err != nil || !running {
		t.Fatalf("the test's own folder: %v, %v", running, err)
	}
	if running, err := folderProgramRunning(t.TempDir()); err != nil || running {
		t.Fatalf("an empty folder: %v, %v", running, err)
	}
}

func TestFolderProcessesUsesNativeDirectoryIdentity(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	alias := filepath.Join(root, "Alias")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", alias, filepath.Dir(program)).CombinedOutput(); err != nil {
		t.Fatalf("mklink /J: %v %s", err, out)
	}
	t.Cleanup(func() {
		if err := os.Remove(alias); err != nil {
			t.Error(err)
		}
	})
	began := time.Now()
	pids, err := folderProcesses([]string{alias, filepath.Dir(program), root})
	t.Logf("running-app scan over three local roots took %s", time.Since(began))
	if err != nil || !slices.Contains(pids, uint32(os.Getpid())) {
		t.Fatalf("process in the same native directory: %v, %v", pids, err)
	}
	running, err := folderProgramRunning(alias)
	if err != nil || !running {
		t.Fatalf("process reached through directory alias: %v, %v", running, err)
	}
	if pids, err := folderProcesses([]string{filepath.Join(root, "missing")}); len(pids) != 0 || err != nil {
		t.Fatalf("missing optional sibling: %v, %v", pids, err)
	}
	redirect := filepath.Join(root, "RemoteAlias")
	if err := placetest.ShareJunction(redirect); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Remove(redirect); err != nil {
			t.Error(err)
		}
	})
	if pids, err := folderProcesses([]string{redirect}); len(pids) != 0 || !errors.Is(err, fileid.ErrNotLocalPath) {
		t.Fatalf("configured folder with a remote junction target: %v, %v", pids, err)
	}
}

func TestMovedRunningImageLeavesRelevantAncestryIndeterminate(t *testing.T) {
	root := t.TempDir()
	folder, err := fileid.Open(t.Context(), fileid.Self, root, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	defer folder.Close()
	roots := []runningFolder{{path: root, held: folder}}
	path := filepath.Join(root, "image.exe")
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	image, err := fileid.Open(t.Context(), fileid.Self, path, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	moved := filepath.Join(t.TempDir(), "moved.exe")
	if err := os.Rename(path, moved); err != nil {
		t.Fatal(err)
	}
	if relation, err := image.Relate(folder); err != nil || relation.Kind != fileid.RelationOutside {
		t.Fatalf("moved image relation: %v, %v", relation, err)
	}
	if inside, err := relateRunningImage(image, moved, roots); inside || err == nil {
		t.Fatalf("indeterminate after move: %v, %v", inside, err)
	}
	other, err := fileid.Open(t.Context(), fileid.Self, moved, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	if inside, err := relateRunningImage(other, path, roots); inside || err == nil {
		t.Fatalf("indeterminate reported image: %v, %v", inside, err)
	}
	if inside, err := relateRunningImage(other, moved, roots); inside || err != nil {
		t.Fatalf("known unrelated image: %v, %v", inside, err)
	}
}

func TestInstalledPackageUnknownUpgradeCode(t *testing.T) {
	installed, err := installedPackage("{00000000-0000-5000-8000-0000000F1C0D}")
	if err != nil || installed {
		t.Fatalf("installed %v, err %v", installed, err)
	}
}
