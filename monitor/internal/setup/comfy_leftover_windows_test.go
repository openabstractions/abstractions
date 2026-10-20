//go:build windows

package setup

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Owner's PC, R36: removing the ComfyUI integration product left
// custom_nodes\comfyui-openabstractions\oa_client\abstraction\{download,job,
// rights} with no files, and the new MSI refused that folder. A tree of empty
// folders is removed; a folder holding a file or a junction is left whole,
// and the junction's target is never entered.
func TestComfyLeftoverFolderRemovedOnlyWhenEmpty(t *testing.T) {
	tree := func(t *testing.T) string {
		folder := filepath.Join(t.TempDir(), "custom_nodes", "comfyui-openabstractions")
		for _, sub := range []string{"download", "job", "rights"} {
			if err := os.MkdirAll(filepath.Join(folder, "oa_client", "abstraction", sub), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		return folder
	}
	t.Run("empty folders", func(t *testing.T) {
		folder := tree(t)
		if err := removeEmptyFolderTree(folder); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(folder); !os.IsNotExist(err) {
			t.Fatal("the empty tree is still there", err)
		}
		if err := removeEmptyFolderTree(folder); err != nil {
			t.Fatal("an absent folder", err)
		}
	})
	t.Run("a file", func(t *testing.T) {
		folder := tree(t)
		file := filepath.Join(folder, "oa_client", "abstraction", "job", "kept.py")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := removeEmptyFolderTree(folder); err == nil || !strings.Contains(err.Error(), "kept.py") {
			t.Fatal("a folder holding a file", err)
		}
		if _, err := os.Stat(filepath.Join(folder, "oa_client", "abstraction", "download")); err != nil {
			t.Fatal("a folder holding a file lost its empty sibling", err)
		}
	})
	t.Run("a regular file at the cleanup location", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "leftover")
		if err := os.WriteFile(path, []byte("keep"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := removeEmptyFolderTree(path); err == nil {
			t.Fatal("directory-only cleanup accepted a regular file")
		}
		if raw, err := os.ReadFile(path); err != nil || string(raw) != "keep" {
			t.Fatalf("cleanup changed the file: %q, %v", raw, err)
		}
	})
	t.Run("a junction", func(t *testing.T) {
		folder := tree(t)
		outside := filepath.Join(t.TempDir(), "outside")
		if err := os.MkdirAll(filepath.Join(outside, "empty"), 0o700); err != nil {
			t.Fatal(err)
		}
		link := filepath.Join(folder, "oa_client", "linked")
		if out, err := exec.Command("cmd", "/c", "mklink", "/J", link, outside).CombinedOutput(); err != nil {
			t.Fatalf("mklink /J: %v %s", err, out)
		}
		t.Cleanup(func() { os.Remove(link) })
		if err := removeEmptyFolderTree(folder); err == nil {
			t.Fatal("a folder holding a junction was removed")
		}
		if _, err := os.Stat(filepath.Join(outside, "empty")); err != nil {
			t.Fatal("the junction's target changed", err)
		}
		if _, err := os.Lstat(link); err != nil {
			t.Fatal("the junction was removed", err)
		}
	})
}

func TestLastMSIMessageNamesTheInstallersWords(t *testing.T) {
	log := "=== Logging started: 12.11.2026  21:45:40 ===\r\n" +
		"Product: OpenAbstractions for ComfyUI -- custom_nodes already holds a comfyui-openabstractions folder this installer did not place; remove it, then run this installer again.\r\n" +
		"MSI (c) (CC:A0) [21:45:41:002]: Product: OpenAbstractions for ComfyUI -- Installation failed.\r\n" +
		"Product: OpenAbstractions for ComfyUI -- Installation failed.\r\n" +
		"=== Logging stopped: 12.11.2026  21:45:41 ===\r\n"
	if got := lastMSIMessage(log, 300); !strings.Contains(got, "already holds a comfyui-openabstractions folder") {
		t.Fatalf("%q", got)
	}
	if got := lastMSIMessage("=== Logging started ===\r\n", 300); got != "" {
		t.Fatalf("%q", got)
	}
}
