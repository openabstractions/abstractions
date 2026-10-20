package setup

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

// lemonadeArtifact is the catalogue's reviewed Lemonade package. The
// catalogue pins it for Windows x64 only; the Home placement these tests
// check is the same on every platform, so they read the Windows x64
// catalogue wherever they run.
func lemonadeArtifact(t *testing.T) Artifact {
	t.Helper()
	plan, err := catalogForTarget("windows", "amd64").Plan([]AppID{ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range plan.Components {
		if component.ID == "lemonade-engine" && component.Artifact != nil {
			return *component.Artifact
		}
	}
	t.Fatal("the catalogue names no Lemonade package")
	return Artifact{}
}

// Every Setup location follows the Home the Panel selected: the journals,
// installed apps, ComfyUI's local data and pictures sit under the runtime's
// state folder, and provider verbs are refused there (live walkthrough
// 2026-10-02, D6).
func TestRuntimeStateHomeHoldsEverySetupLocation(t *testing.T) {
	state := t.TempDir()
	if err := UseRuntimeState(state); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(UseUserState)
	root := filepath.Join(state, "setup")
	inside := func(what, path string) {
		t.Helper()
		if !strings.HasPrefix(path, root+string(filepath.Separator)) {
			t.Fatalf("%s %s is outside the runtime's Setup folder %s", what, path, root)
		}
	}
	journal, err := DefaultJournal()
	if err != nil {
		t.Fatal(err)
	}
	inside("plan journal", journal.Path)
	lemonade := lemonadeArtifact(t)
	app, err := installPath(lemonade)
	if err != nil {
		t.Fatal(err)
	}
	inside("installed app", app)
	comfy := lemonade
	comfy.InstallIdentity = "comfyui"
	paths, err := comfyPathsForArtifact(comfy)
	if err != nil {
		t.Fatal(err)
	}
	for what, path := range map[string]string{"ComfyUI install": paths.install, "ComfyUI user data": paths.user, "ComfyUI output": paths.output, "ComfyUI launch record": paths.launch} {
		inside(what, path)
	}
	if _, err := runLemonadeCLI(t.Context(), "setup-info", "--json"); !errors.Is(err, errIsolatedProvider) {
		t.Fatalf("a provider verb must be refused for a runtime's own state: %v", err)
	}
	if _, err := DefaultBF16Controller(func(context.Context) (bool, error) { return false, nil }); err == nil {
		t.Fatal("the picture-model controller must be refused for a runtime's own state")
	}
}

// A refused Home refuses every Setup location with its reason.
func TestNoStateHomeRefusesSetup(t *testing.T) {
	reason := errors.New("no state folder")
	UseNoState(reason)
	t.Cleanup(UseUserState)
	if _, err := DefaultJournal(); !errors.Is(err, reason) {
		t.Fatalf("journal: %v", err)
	}
	if _, err := installPath(lemonadeArtifact(t)); !errors.Is(err, reason) {
		t.Fatalf("install path: %v", err)
	}
}
