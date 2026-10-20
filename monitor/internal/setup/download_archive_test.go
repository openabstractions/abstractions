package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	archive "github.com/openabstractions/abstraction-download/go/archive"
)

// The archive mechanism and its tests live in the abstraction-download
// archive package; these tests cover Setup's Settings row over it.

func TestArchiveSettingReadsBackAndClears(t *testing.T) {
	standInHome(t)
	if ArchiveFolder() != "" {
		t.Fatal("a new home has an archive folder")
	}
	folder := filepath.Join(t.TempDir(), "archive")
	if err := SetArchiveFolder(context.Background(), folder); err != nil {
		t.Fatal(err)
	}
	if got := ArchiveFolder(); got != folder {
		t.Fatalf("archive folder %q, want %q", got, folder)
	}
	// The runtime reads the same setting from its own state folder.
	home, _ := CurrentHome()
	if _, err := os.Stat(filepath.Join(home.Config, "download-archive.json")); err != nil {
		t.Fatalf("the setting is not in Setup's state folder: %v", err)
	}
	if err := SetArchiveFolder(context.Background(), ""); err != nil || ArchiveFolder() != "" {
		t.Fatalf("the archive was not cleared: %q, %v", ArchiveFolder(), err)
	}
}

// The Panel's Setup state for a runtime with its own state folder is the
// archive state that runtime reads.
func TestRuntimeStateArchiveIsSetupsState(t *testing.T) {
	state := t.TempDir()
	if err := UseRuntimeState(state); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	home, _ := CurrentHome()
	runtime, err := archive.StateFor(state)
	if err != nil || runtime != home.Config {
		t.Fatalf("runtime archive state %q, Setup state %q: %v", runtime, home.Config, err)
	}
}

// The Settings row's counts come from the stored listing: a reviewed model
// held under any name counts as found, and a model file that matches no
// pin as other.
func TestArchiveStatusCountsFoundAndOtherModelsFromTheListing(t *testing.T) {
	standInHome(t)
	folder := t.TempDir()
	coding := CodingModelPlan().Assets[0].Download
	if err := SetArchiveFolder(context.Background(), folder); err != nil {
		t.Fatal(err)
	}
	// The stored listing, as a walk of the owner's NAS saves it.
	listing, err := json.Marshal(map[string]any{"folder": folder, "walked_at": time.Now().UTC(), "files": []map[string]any{
		{"path": "llm/qwen.gguf", "size": coding.Bytes, "modified": 1, "sha256": coding.SHA256},
		{"path": "llm/unknown.gguf", "size": 4 << 20, "modified": 1},
		{"path": "notes/readme.txt", "size": 2 << 20, "modified": 1},
		{"path": "pictures/other.safetensors", "size": 5 << 20, "modified": 1},
	}})
	if err != nil {
		t.Fatal(err)
	}
	home, _ := CurrentHome()
	if err := os.WriteFile(filepath.Join(home.Config, "download-archive-index.json"), listing, 0o600); err != nil {
		t.Fatal(err)
	}
	state := ArchiveStatus()
	if !state.Listed || state.Found != 1 || state.Other != 2 || !state.Keep || state.Looking {
		t.Fatalf("status %+v", state)
	}
	if !ArchiveHolds(CodingModelPlan().Downloads()) {
		t.Fatal("ArchiveHolds misses the coding model under its own name")
	}
}
