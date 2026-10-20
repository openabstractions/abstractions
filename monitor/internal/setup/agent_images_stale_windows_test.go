package setup

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/placetest"
)

func writeAgentFixtureFiles(t *testing.T, root string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(root, name), []byte("fixture "+name), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func agentFixtureExists(t *testing.T, root, name string) bool {
	t.Helper()
	_, err := os.Lstat(filepath.Join(root, name))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return err == nil
}

func TestRemoveStaleAgentImageCopiesKeepsCurrentLockAndHeldFiles(t *testing.T) {
	root := t.TempDir()
	program := filepath.Join(root, "oa-claude-integration.exe")
	keep := "oa-claude-integration.exe.ccc.tmp.previous"
	stale := []string{
		"oa-claude-integration.exe.aaa.tmp",
		"oa-claude-integration.exe.aaa.tmp.previous",
		"oa-claude-integration.exe.bbb.tmp.previous.previous",
		"OA-CLAUDE-INTEGRATION.EXE.installer-0011.TMP",
	}
	stays := []string{
		"oa-claude-integration.exe",
		"oa-claude-integration.exe.lock",
		keep,
		"oa-claude-integration.exe.held.tmp",
		"oa-codex-integration.exe.ddd.tmp",
		"oa-codex-integration.exe.ddd.tmp.previous",
		"oa-claude-integration.exe.tmp",
		".oa-agent-update-batch.lock",
	}
	writeAgentFixtureFiles(t, root, append(slices.Clone(stale), stays...)...)
	if err := os.Mkdir(filepath.Join(root, "oa-claude-integration.exe.dir.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	stays = append(stays, "oa-claude-integration.exe.dir.tmp")
	// A staged image another process still holds cannot be deleted; it stays
	// and the cleanup reports no failure.
	held, err := openArchiveSource(context.Background(), filepath.Join(root, "oa-claude-integration.exe.held.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()

	removed := removeStaleAgentImageCopies(t.Context(), root, program, filepath.Join(root, strings.ToUpper(keep)))
	slices.Sort(removed)
	want := slices.Clone(stale)
	slices.Sort(want)
	if !slices.Equal(removed, want) {
		t.Fatalf("removed %v, want %v", removed, want)
	}
	for _, name := range stale {
		if agentFixtureExists(t, root, name) {
			t.Fatal("stale copy kept", name)
		}
	}
	for _, name := range stays {
		if !agentFixtureExists(t, root, name) {
			t.Fatal("kept file removed", name)
		}
	}
	if removeStaleAgentImageCopies(t.Context(), filepath.Join(root, "missing"), program) != nil {
		t.Fatal("missing directory reported removals")
	}
}

func TestRemoveStaleAgentImageCopiesChecksTheVolumeNameAndRoot(t *testing.T) {
	const current = "oa-claude-integration.exe"
	const stale = current + ".stage.tmp"
	t.Run("case-sensitive", func(t *testing.T) {
		root := t.TempDir()
		if err := placetest.CaseSensitive(root); err != nil {
			t.Skip(err)
		}
		other := strings.ToUpper(current)
		foreign := other + ".stage.tmp"
		backup := current + ".stage.tmp.previous"
		distinct := current + ".STAGE.TMP.PREVIOUS"
		writeAgentFixtureFiles(t, root, current, other, stale, foreign, backup, distinct)
		removed := removeStaleAgentImageCopies(t.Context(), root, filepath.Join(root, current), filepath.Join(root, backup))
		if want := []string{distinct, stale}; !slices.Equal(removed, want) {
			t.Fatalf("removed %v, want %v", removed, want)
		}
		for _, name := range []string{current, other, foreign, backup} {
			if !agentFixtureExists(t, root, name) {
				t.Fatal("foreign or protected name removed", name)
			}
		}
	})
	t.Run("redirected root", func(t *testing.T) {
		root, actual := t.TempDir(), t.TempDir()
		writeAgentFixtureFiles(t, actual, current, stale)
		alias := filepath.Join(root, "alias")
		if err := placetest.Junction(alias, actual); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.Remove(alias); err != nil {
				t.Error(err)
			}
		})
		if removed := removeStaleAgentImageCopies(t.Context(), alias, filepath.Join(alias, current)); len(removed) != 0 {
			t.Fatalf("redirected root removed %v", removed)
		}
		if !agentFixtureExists(t, actual, stale) {
			t.Fatal("file in the redirected folder was removed")
		}
	})
}

func TestAgentUpdateBatchRemovesStaleCopiesAfterPublication(t *testing.T) {
	root := t.TempDir()
	s := batchImageFixtureAt(t, root, AgentClaude)
	backup := s.Image.Program() + ".previous"
	stale := []string{"oa-claude-integration.exe.old1.tmp", "oa-claude-integration.exe.old1.tmp.previous", "oa-claude-integration.exe.old2.tmp.previous"}
	foreign := "oa-codex-integration.exe.old3.tmp.previous"
	writeAgentFixtureFiles(t, root, append(slices.Clone(stale), foreign)...)
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{s}); err != nil {
		t.Fatal(err)
	}
	if got := replacementPin(t, s.Old.Program()); !sameAgentPin(got, s.imagePin) {
		t.Fatal("staged image not published")
	}
	if got := replacementPin(t, backup); !sameAgentPin(got, s.oldPin) {
		t.Fatal("Undo backup of this update removed or changed")
	}
	for _, name := range stale {
		if agentFixtureExists(t, root, name) {
			t.Fatal("stale copy kept after publication", name)
		}
	}
	if !agentFixtureExists(t, root, foreign) {
		t.Fatal("another connector's copy removed")
	}
}

func TestAgentUpdateBatchRestartKeepsStagedCopies(t *testing.T) {
	root := t.TempDir()
	s := batchImageFixtureAt(t, root, AgentCodex)
	stale := "oa-codex-integration.exe.old1.tmp.previous"
	writeAgentFixtureFiles(t, root, stale)
	busy, err := openArchiveSource(context.Background(), s.Old.Program())
	if err != nil {
		t.Fatal(err)
	}
	defer busy.Close()
	if err := PublishAgentUpdates(context.Background(), []*StagedAgentUpdate{s}); err == nil {
		t.Fatal("busy connector published")
	}
	if !agentFixtureExists(t, root, filepath.Base(s.Image.Program())) || !agentFixtureExists(t, root, stale) {
		t.Fatal("a waiting update removed staged copies")
	}
}
