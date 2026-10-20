package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/openabstractions/abstraction-identity/fileid"
	"os"
	"path/filepath"
	"testing"
)

func TestNativeInstalledPlanRequiresBoundOriginalRoot(t *testing.T) {
	_, a, _, plan := nativeArchiveFixture(t)
	root := t.TempDir()
	plan.InstalledRoot = root
	raw, _ := json.Marshal(plan)
	if _, err := decodeNativeInstallPlan(raw, a); err == nil {
		t.Fatal("raw extraction accepted installed roster")
	}
	if _, err := decodeNativeInstallPlanAt(raw, a, root); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeNativeInstallPlanAt(raw, a, filepath.Join(root, "aside")); err == nil {
		t.Fatal("wrong original root accepted")
	}
}

func TestNativeRemovalResumesConfiguredFilesAndPreservesChangedBytes(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	source, a, _, plan := nativeArchiveFixture(t)
	root, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(context.Background(), a, source, root); err != nil {
		t.Fatal(err)
	}
	original, err := fileid.Pending(context.Background(), fileid.Self, root)
	if err != nil {
		t.Fatal(err)
	}
	configured := []byte("home = " + filepath.Join(original.Path(), "oa-backend", "standalone-env") + "\n")
	sum := sha256.Sum256(configured)
	plan.Files = append(plan.Files, archiveFileRecord{Path: "helper/pyvenv.cfg", Size: int64(len(configured)), SHA256: hex.EncodeToString(sum[:])})
	for name, data := range map[string][]byte{"oa-backend/manifest.json": []byte("{\"fixture\":true}"), "oa-backend/helper/python.exe": []byte("changed personal program"), "oa-backend/helper/pyvenv.cfg": configured, "personal.txt": []byte("keep")} {
		p := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	old := nativeArchiveInstalledCommand
	t.Cleanup(func() { nativeArchiveInstalledCommand = old })
	nativeArchiveInstalledCommand = func(_ context.Context, _ sevenZipPin, bound string) ([]byte, error) {
		if bound != original.Path() {
			t.Fatal("aside used as configuration root")
		}
		plan.InstalledRoot = bound
		return json.Marshal(plan)
	}
	aside := filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+".removing")
	if err := os.Rename(root, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(aside, a.ApplicationProgram)); err != nil {
		t.Fatal(err)
	}
	kept, err := removePackageTree(a, root)
	if err != nil {
		t.Fatal(err)
	}
	if kept != 2 {
		t.Fatalf("kept %d", kept)
	}
	if _, err := os.Stat(filepath.Join(root, "oa-backend/helper/pyvenv.cfg")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("configured source file was not removed")
	}
	for _, name := range []string{"personal.txt", "oa-backend/helper/python.exe"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Fatal("changed file lost")
		}
	}
}

func TestNativeRemovalUsesSourceImageAndConfiguredRoster(t *testing.T) {
	if err := UseRuntimeState(t.TempDir()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	source, a, _, plan := nativeArchiveFixture(t)
	root, err := installPath(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := InstallArchive(context.Background(), a, source, root); err != nil {
		t.Fatal(err)
	}
	canonical, err := fileid.Pending(context.Background(), fileid.Self, root)
	if err != nil {
		t.Fatal(err)
	}
	expectedRoot := canonical.Path()
	if _, err := NativeArchiveProgram(context.Background(), a, root); err != nil {
		t.Fatalf("exact source image refused: %v", err)
	}
	old := nativeArchiveInstalledCommand
	t.Cleanup(func() { nativeArchiveInstalledCommand = old })
	calls := 0
	nativeArchiveInstalledCommand = func(ctx context.Context, image sevenZipPin, original string) ([]byte, error) {
		calls++
		if err := verifyFileContext(ctx, image.Path, Artifact{Bytes: image.Bytes, SHA256: image.SHA256}); err != nil {
			return nil, err
		}
		if filepath.Dir(image.Path) == root {
			t.Fatal("executed mutable installed image")
		}
		if original != expectedRoot {
			t.Fatalf("original root %q want %q", original, expectedRoot)
		}
		plan.InstalledRoot = original
		return json.Marshal(plan)
	}
	// A changed installed image is preserved; metadata comes from the retained
	// independently pinned source image, including on an interrupted aside.
	if err := os.WriteFile(filepath.Join(root, a.ApplicationProgram), []byte("person changed image"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := NativeArchiveProgram(context.Background(), a, root); err == nil {
		t.Fatal("changed image retained registration-removal authority")
	}
	got, err := matchingReceipt(a, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Files) != 5 {
		t.Fatalf("merged roster has %d files", len(got.Files))
	}
	aside := filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+".removing")
	if err := os.Rename(root, aside); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(aside, a.ApplicationProgram)); err != nil {
		t.Fatal(err)
	}
	if _, err := matchingReceipt(a, aside); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("metadata calls %d", calls)
	}
	if err := os.WriteFile(source, []byte("wrong source"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := matchingReceipt(a, aside); err == nil {
		t.Fatal("wrong source accepted")
	}
	if calls != 2 {
		t.Fatal("executed metadata after source refusal")
	}
	nativeArchiveInstalledCommand = func(context.Context, sevenZipPin, string) ([]byte, error) { return nil, errors.New("unused") }
}
