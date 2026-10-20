package main

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// personFolders points this test's per-user configuration and cache folders
// at a temporary directory, so a Setup write to the person's own state shows.
func personFolders(t *testing.T) string {
	t.Helper()
	person := t.TempDir()
	t.Setenv("APPDATA", filepath.Join(person, "Roaming"))
	t.Setenv("LOCALAPPDATA", filepath.Join(person, "Local"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(person, "config"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(person, "cache"))
	t.Setenv("HOME", person)
	return person
}

// bindTestRuntime binds a named runtime as -runtime-endpoint does, and
// restores the installed runtime's selection afterward.
func bindTestRuntime(t *testing.T, stateDir string) {
	t.Helper()
	machine, explicit := panelMachine, panelExplicit
	t.Cleanup(func() {
		panelMachine, panelExplicit = machine, explicit
		setup.UseUserState()
	})
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if err := bindExplicitRuntime(`\\.\pipe\oa-setup-state-test-runtime`, filepath.Clean(program), stateDir); err != nil {
		t.Fatal(err)
	}
}

func saveComfyIntent(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI}, PlanID: plan.ID, Confirm: true})
	req := httptest.NewRequest("POST", "/setup/intent", strings.NewReader(string(body)))
	req.Host = "127.0.0.1"
	req.Header.Set("X-Panel-Key", "setup-state-key")
	got := httptest.NewRecorder()
	(&servicePanel{}).handler("setup-state-key").ServeHTTP(got, req)
	return got
}

func assertEmptyTree(t *testing.T, root string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !entry.IsDir() {
			t.Errorf("Setup wrote the person's own state: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// A Panel serving a runtime with its own state folder keeps Setup's saved
// plan there and writes nothing to the person's own Setup state (live
// walkthrough 2026-10-02, D6). The picture-model controller is refused: the
// Lemonade provider's folders are the person's own.
func TestNamedRuntimeKeepsSetupStateInItsStateFolder(t *testing.T) {
	person := personFolders(t)
	state := t.TempDir()
	bindTestRuntime(t, state)
	if saved := saveComfyIntent(t); saved.Code != 200 {
		t.Fatalf("confirmed intent: %d %s", saved.Code, saved.Body.String())
	}
	if _, err := os.Stat(filepath.Join(state, "setup", "plan.json")); err != nil {
		t.Fatalf("the plan is not in the runtime's state folder: %v", err)
	}
	if _, err := (&servicePanel{}).setupModelForPanel(); err == nil || !strings.Contains(err.Error(), "own state folder") {
		t.Fatalf("the picture-model controller must be refused for an isolated runtime: %v", err)
	}
	assertEmptyTree(t, person)
}

// A Panel serving a named runtime without its state folder refuses Setup
// instead of falling back to the person's own Setup state.
func TestNamedRuntimeWithoutStateFolderRefusesSetup(t *testing.T) {
	person := personFolders(t)
	bindTestRuntime(t, "")
	saved := saveComfyIntent(t)
	if saved.Code == 200 || !strings.Contains(saved.Body.String(), "-runtime-state-dir") {
		t.Fatalf("Setup without the runtime's state folder: %d %s", saved.Code, saved.Body.String())
	}
	assertEmptyTree(t, person)
}
