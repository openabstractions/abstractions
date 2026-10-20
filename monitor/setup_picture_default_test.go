package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type fakePictureDefaults struct {
	read iclient.SelectionReadV2
	puts []iclient.SelectionPolicyV2
}

func (f *fakePictureDefaults) ReadAccountDefault(_ context.Context, name string) (iclient.SelectionReadV2, error) {
	return f.read, nil
}

func (f *fakePictureDefaults) PutAccountDefault(_ context.Context, _ string, policy iclient.SelectionPolicyV2) (iclient.SelectionChange, error) {
	f.puts = append(f.puts, policy)
	return iclient.SelectionChange{Outcome: iwire.EditOutcomeApplied}, nil
}

func preparePictureModel(t *testing.T, journal setup.Journal, plan setup.ModelPlan) {
	t.Helper()
	state := setup.ModelState{PlanID: plan.ID, Approved: true, Effects: map[string]setup.ModelEffect{}, ProviderConfigured: true}
	for _, a := range plan.Assets {
		state.Effects[a.ID] = setup.ModelEffect{Phase: setup.Verified}
	}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(setup.PictureModelJournal(journal, plan), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

func useFakePictureDefaults(t *testing.T, f *fakePictureDefaults) {
	previous := setupPictureDefaultStore
	setupPictureDefaultStore = func(context.Context) (pictureDefaultStore, error) { return f, nil }
	t.Cleanup(func() { setupPictureDefaultStore = previous })
}

// R21: FLUX.2 Klein prepared with no picture policy gets the policy; a
// policy the person set stays; with no picture model nothing is written.
func TestSetupFillImageDefault(t *testing.T) {
	ctx := context.Background()
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	f := &fakePictureDefaults{read: iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeUnknown}}
	useFakePictureDefaults(t, f)

	if err := setupFillImageDefault(ctx, journal); err != nil || len(f.puts) != 0 {
		t.Fatalf("no picture model wrote %v: %v", f.puts, err)
	}
	preparePictureModel(t, journal, setup.ZImageComfyModelPlan())
	if err := setupFillImageDefault(ctx, journal); err != nil || len(f.puts) != 0 {
		t.Fatalf("staging-only picture model wrote %v: %v", f.puts, err)
	}

	klein := setup.PyTorchModelPlan()
	preparePictureModel(t, journal, klein)
	if err := setupFillImageDefault(ctx, journal); err != nil {
		t.Fatal(err)
	}
	if len(f.puts) != 1 || f.puts[0].Name != imagePolicy || len(f.puts[0].Candidates) != 1 ||
		f.puts[0].Candidates[0].Model != klein.Model || f.puts[0].Candidates[0].Placement != iwire.CandidatePlacementLocal {
		t.Fatalf("missing policy not filled: %+v", f.puts)
	}

	f.puts = nil
	f.read = iclient.SelectionReadV2{Outcome: iwire.SelectionReadOutcomeFound, Policy: &iclient.SelectionPolicyV2{Name: imagePolicy,
		Candidates: []iclient.SelectionCandidateV2{{Model: "chosen-by-person", Placement: iwire.CandidatePlacementLocal}}}}
	if err := setupFillImageDefault(ctx, journal); err != nil || len(f.puts) != 0 {
		t.Fatalf("the person's policy was overwritten: %+v %v", f.puts, err)
	}
}
