package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/fileid"
)

// An orphaned staged copy is deleted only when the name still holds the exact
// pinned single-link file; anything else at the name stays with an error.
func TestDeleteAgentImagePinnedLeavesSwappedOrLinkedNames(t *testing.T) {
	stagedAt := func(t *testing.T) (string, fileid.FileID) {
		t.Helper()
		program := filepath.Join(t.TempDir(), "oa-opencode-integration.exe.stage.tmp")
		if err := os.WriteFile(program, []byte("staged connector"), 0600); err != nil {
			t.Fatal(err)
		}
		id, err := fileid.Resolve(t.Context(), fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			t.Fatal(err)
		}
		return program, id
	}
	t.Run("pinned", func(t *testing.T) {
		program, id := stagedAt(t)
		if err := deleteAgentImagePinned(t.Context(), program, id); err != nil {
			t.Fatal(err)
		}
		if _, err := os.Lstat(program); !os.IsNotExist(err) {
			t.Fatal("pinned staged copy stayed", err)
		}
	})
	t.Run("cancelled", func(t *testing.T) {
		program, id := stagedAt(t)
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := deleteAgentImagePinned(ctx, program, id); !errors.Is(err, context.Canceled) {
			t.Fatal("cancelled delete did not refuse", err)
		}
		if _, err := os.Lstat(program); err != nil {
			t.Fatal("cancelled delete removed the staged copy", err)
		}
	})
	t.Run("swapped", func(t *testing.T) {
		program, id := stagedAt(t)
		if err := os.Rename(program, program+".moved"); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(program, []byte("other file"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := deleteAgentImagePinned(t.Context(), program, id); err == nil {
			t.Fatal("swapped name deleted")
		}
		if body, err := os.ReadFile(program); err != nil || string(body) != "other file" {
			t.Fatal("swapped file changed", string(body), err)
		}
	})
	t.Run("hard-linked", func(t *testing.T) {
		program, id := stagedAt(t)
		if err := os.Link(program, program+".link"); err != nil {
			t.Fatal(err)
		}
		if err := deleteAgentImagePinned(t.Context(), program, id); err == nil {
			t.Fatal("hard-linked copy deleted")
		}
		if _, err := os.Lstat(program); err != nil {
			t.Fatal("hard-linked copy removed", err)
		}
	})
	t.Run("symlink", func(t *testing.T) {
		target, id := stagedAt(t)
		program := filepath.Join(filepath.Dir(target), "named.tmp")
		if err := os.Symlink(target, program); err != nil {
			t.Skip("symbolic links unavailable to this account:", err)
		}
		if err := deleteAgentImagePinned(t.Context(), program, id); err == nil {
			t.Fatal("link at the staged name deleted")
		}
		if _, err := os.Lstat(program); err != nil {
			t.Fatal("link removed", err)
		}
		if _, err := os.Lstat(target); err != nil {
			t.Fatal("link target removed", err)
		}
	})
}
