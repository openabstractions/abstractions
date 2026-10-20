package setup

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/programfiles"
)

// A partial native install may hold a read-only sealed tree; clearing it lifts
// the ACL and removes the whole folder, receipt last.
func TestClearPartialNativeLiftsReadOnlyTrees(t *testing.T) {
	root := filepath.Join(t.TempDir(), "0.8.0")
	backend := filepath.Join(root, "oa-backend", "helper")
	if err := os.MkdirAll(backend, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(backend, "python.exe"), []byte("python"), 0600); err != nil {
		t.Fatal(err)
	}
	a := Artifact{SHA256: "ab", Version: "0.8.0", InstallIdentity: "oa-comfyui-native"}
	receipt, err := json.Marshal(archiveReceipt{Format: 2, ArtifactSHA256: a.SHA256, Version: a.Version, InstallIdentity: a.InstallIdentity})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, archiveMarker), receipt, 0600); err != nil {
		t.Fatal(err)
	}
	sid, err := programfiles.AccountSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := programfiles.SetFolderACL(filepath.Join(root, "oa-backend"), sid, programfiles.ReadOnly); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(backend, "python.exe")); err == nil {
		t.Fatal("fixture tree is not read-only")
	}
	other := a
	other.SHA256 = "cd"
	if err := clearPartialNative(context.Background(), other, root); err == nil {
		t.Fatal("another package's folder was removed")
	}
	if err := clearPartialNative(context.Background(), a, root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial folder kept: %v", err)
	}
	entries, err := os.ReadDir(filepath.Dir(root))
	if err != nil || len(entries) != 0 {
		t.Fatalf("set-aside folder left behind: %v %v", entries, err)
	}
}

// A folder an interrupted clear set aside is removed at the next retry or
// install, read-only trees included; the installation itself is untouched.
func TestSweepClearedNativeRemovesSetAsideFolders(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "0.8.0")
	if err := os.MkdirAll(root, 0700); err != nil {
		t.Fatal(err)
	}
	locked := filepath.Join(parent, ".0.8.0.clear-123", "0.8.0", "oa-backend")
	if err := os.MkdirAll(locked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "manifest.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	sid, err := programfiles.AccountSID()
	if err != nil {
		t.Fatal(err)
	}
	if err := programfiles.SetFolderACL(locked, sid, programfiles.ReadOnly); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(parent, ".0.9.0.clear-456")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	sweepClearedNative(root)
	if _, err := os.Stat(filepath.Join(parent, ".0.8.0.clear-123")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("set-aside folder kept: %v", err)
	}
	for _, kept := range []string{root, other} {
		if _, err := os.Stat(kept); err != nil {
			t.Fatalf("%s removed: %v", kept, err)
		}
	}
}

// A set-aside folder holding a junction is left alone, and so is the folder
// the junction names.
func TestSweepClearedNativeKeepsAFolderWithAJunction(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "0.8.0")
	target := filepath.Join(t.TempDir(), "person")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "picture.png"), []byte("kept"), 0600); err != nil {
		t.Fatal(err)
	}
	aside := filepath.Join(parent, ".0.8.0.clear-789")
	if err := os.MkdirAll(aside, 0700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(aside, "link")
	if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, target).CombinedOutput(); err != nil {
		t.Fatalf("junction: %v %s", err, out)
	}
	sweepClearedNative(root)
	if _, err := os.Lstat(link); err != nil {
		t.Fatalf("the folder with a junction was removed: %v", err)
	}
	if raw, err := os.ReadFile(filepath.Join(target, "picture.png")); err != nil || string(raw) != "kept" {
		t.Fatalf("the junction's target changed: %q %v", raw, err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
}
