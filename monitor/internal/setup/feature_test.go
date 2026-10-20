package setup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAddModelHostFeatureNeedsAnApprovedPlan(t *testing.T) {
	ctx := context.Background()
	called := false
	add := func(context.Context) (FeatureStatus, error) {
		called = true
		return FeatureStatus{Available: true, Installed: true}, nil
	}
	allowedHome := func() error { return nil }

	journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	runner := NewRunner(journal, nil)
	runner.modelHostHomeAllowed = allowedHome
	runner.modelHostFeatureAdd = add
	if _, err := runner.AddModelHostFeature(ctx); err == nil {
		t.Fatal("feature add without an approved plan succeeded")
	}
	if called {
		t.Fatal("feature add ran before plan approval")
	}

	plan, err := NewCatalog().Plan([]AppID{ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	// Chat hosting is OA's own: any approved plan reaches it.
	status, err := runner.AddModelHostFeature(ctx)
	if err != nil || !status.Installed || !called {
		t.Fatalf("an approved plan did not reach feature maintenance: %+v %v called=%v", status, err, called)
	}
}

func TestAddModelHostFeatureRefusesAnIsolatedSetupHome(t *testing.T) {
	ctx := context.Background()
	called := false
	plan, err := NewCatalog().Plan([]AppID{ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	journal := Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	if _, err := journal.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	runner := NewRunner(journal, nil)
	runner.modelHostHomeAllowed = func() error { return errors.New("isolated home") }
	runner.modelHostFeatureAdd = func(context.Context) (FeatureStatus, error) {
		called = true
		return FeatureStatus{Installed: true}, nil
	}
	if _, err := runner.AddModelHostFeature(ctx); err == nil || err.Error() != "isolated home" || called {
		t.Fatalf("isolated home feature result: %v; maintenance called=%v", err, called)
	}
}

// Every x64 core installs the Lemonade and PyTorch adapters (abstraction.wxs
// Level 1). Setup reads the adapter program beside the Panel and never asks
// Windows Installer for it; a missing one names how to restore it.
func TestAdapterPresenceReadsTheProgramBesideThePanel(t *testing.T) {
	tools := filepath.Join(t.TempDir(), "OpenAbstractions", "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	panel := filepath.Join(tools, "Abstraction Panel.exe")
	missing := adapterPresence(panel, "pytorchd.exe", "PyTorch Image")
	if missing.Installed || missing.Available || missing.Reason != "Run the latest OpenAbstractions Setup to add PyTorch Image." {
		t.Fatalf("missing adapter = %+v", missing)
	}
	if err := os.Mkdir(filepath.Join(tools, "pytorchd.exe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if folder := adapterPresence(panel, "pytorchd.exe", "PyTorch Image"); folder.Installed {
		t.Fatalf("a folder counted as the adapter: %+v", folder)
	}
	if err := os.WriteFile(filepath.Join(tools, "thenoise-provider.exe"), []byte("inert"), 0o644); err != nil {
		t.Fatal(err)
	}
	if present := adapterPresence(panel, "thenoise-provider.exe", "TheNoise Image"); !present.Installed || !present.Available || present.Reason != "" {
		t.Fatalf("present adapter = %+v", present)
	}
	elsewhere := filepath.Join(t.TempDir(), "Abstraction Panel.exe")
	if outside := adapterPresence(elsewhere, "thenoise-provider.exe", "TheNoise Image"); outside.Installed || !strings.Contains(outside.Reason, "installed OpenAbstractions Panel") {
		t.Fatalf("a Panel outside tools = %+v", outside)
	}
}

// The picture step's feature transition for an adapter engine is a read: an
// installed adapter passes, and a missing one stops Setup with the restore
// sentence. No plan or Windows Installer maintenance is involved.
func TestAddEngineFeatureOnlyReadsAnAdapter(t *testing.T) {
	ctx := context.Background()
	runner := NewRunner(Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}, nil)
	for _, engine := range []AppID{Lemonade, PyTorch, TheNoise} {
		reads := 0
		runner.featureCheck = func() (FeatureStatus, error) { reads++; return FeatureStatus{Available: true, Installed: true}, nil }
		if status, err := runner.AddEngineFeature(ctx, engine); err != nil || !status.Installed || reads != 1 {
			t.Fatalf("%s installed adapter = %+v, %v, reads %d", engine, status, err, reads)
		}
		runner.featureCheck = func() (FeatureStatus, error) {
			return FeatureStatus{Reason: adapterRestoreSentence(engineFeatureTitle(engine))}, nil
		}
		status, err := runner.AddEngineFeature(ctx, engine)
		want := "Run the latest OpenAbstractions Setup to add " + engineFeatureTitle(engine) + "."
		if err == nil || err.Error() != want || status.Installed {
			t.Fatalf("%s missing adapter = %+v, %v", engine, status, err)
		}
	}
}
