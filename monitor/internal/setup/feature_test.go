package setup

import (
	"context"
	"errors"
	"path/filepath"
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
