package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// Each picture model ComfyUI's PyTorch engine offers is its own toggle in
// the plan review: an app with no model arrives with the recommended
// compact Klein ticked, the person may tick the full-precision Klein and
// Z-Image-Turbo (ComfyUI only) instead or beside it, and an engine that
// already has a model arrives with none ticked.
func TestPictureModelTogglesSelectThePlansToPrepare(t *testing.T) {
	ctx := context.Background()
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	plan, err := journal.Catalog().Plan([]setup.AppID{setup.ComfyUI, setup.PyTorch})
	if err != nil {
		t.Fatal(err)
	}
	compact, full, zimage := setup.PyTorchModelPlan(), setup.PyTorchFullModelPlan(), setup.ZImageComfyModelPlan()
	selected := func(offers []setupPictureOffer) []string {
		var ids []string
		for _, o := range offers {
			if o.Selected {
				ids = append(ids, o.ID)
			}
		}
		return ids
	}

	starter, others, offers, err := setupPictureSelection(ctx, journal, plan, nil)
	if err != nil || starter == nil || starter.ID != compact.ID || len(others) != 0 {
		t.Fatalf("default selection %v %v %v", starter, others, err)
	}
	if len(offers) != 3 || !offers[0].Recommended || offers[1].Recommended || offers[2].RequiredApplication != setup.ComfyUI ||
		offers[0].RequiredApplication != "" || offers[1].RequiredApplication != "" || !slices.Equal(selected(offers), []string{compact.ID}) {
		t.Fatalf("default offers %+v", offers)
	}
	for i, want := range []string{"Recommended for this computer", "Full precision, larger", "Photorealistic, for ComfyUI"} {
		if got := offers[i].Caption; got != want {
			t.Fatalf("offer %d caption %q, want %q", i, got, want)
		}
	}
	raw, _ := json.Marshal(offers[0])
	var row map[string]any
	if err := json.Unmarshal(raw, &row); err != nil || row["id"] != compact.ID || row["recommended"] != true || row["selected"] != true ||
		row["title"] != "FLUX.2 Klein 4B compact" || row["download_bytes"].(float64) != float64(compact.DownloadBytes) {
		t.Fatalf("offer row %s", raw)
	}

	starter, others, _, err = setupPictureSelection(ctx, journal, plan, []string{full.ID, zimage.ID})
	if err != nil || starter != nil || !slices.Equal(others, []string{full.ID, zimage.ID}) {
		t.Fatalf("larger models only: %v %v %v", starter, others, err)
	}
	if starter, others, _, err = setupPictureSelection(ctx, journal, plan, []string{}); err != nil || starter != nil || len(others) != 0 {
		t.Fatalf("no ticks selects %v %v %v", starter, others, err)
	}
	if _, _, _, err = setupPictureSelection(ctx, journal, plan, []string{"not-an-offer"}); err == nil {
		t.Fatal("an unknown picture model was accepted")
	}

	// A prepared full-precision Klein: nothing is ticked by default and the
	// prepared model is never selected again.
	state := setup.ModelState{PlanID: full.ID, Approved: true, ProviderConfigured: true, Effects: map[string]setup.ModelEffect{}}
	for _, a := range full.Assets {
		state.Effects[a.ID] = setup.ModelEffect{Phase: setup.Verified}
	}
	raw, _ = json.Marshal(state)
	if err := os.WriteFile(setup.PictureModelJournal(journal, full), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	starter, others, offers, err = setupPictureSelection(ctx, journal, plan, nil)
	if err != nil || starter != nil || len(others) != 0 || !offers[1].Prepared {
		t.Fatalf("an engine with a model: %v %v %+v %v", starter, others, offers, err)
	}
	if _, others, _, _ = setupPictureSelection(ctx, journal, plan, []string{full.ID, compact.ID}); len(others) != 0 {
		t.Fatalf("a prepared model was selected again: %v", others)
	}
	if starter, _, _, _ := setupPictureSelection(ctx, journal, plan, nil); starter != nil {
		t.Fatal("the starter is offered beside a prepared model")
	}
	if err := os.WriteFile(setup.PictureModelJournal(journal, zimage), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if starter, others, offers, err := setupPictureSelection(ctx, journal, plan, []string{compact.ID}); err != nil ||
		starter != nil || others != nil || offers != nil {
		t.Fatalf("unreadable offer returned partial approval: %v %v %v %v", starter, others, offers, err)
	}
}

// The model endpoints drive each PyTorch toggle by its plan ID: the status
// lists the other toggles as the engine's choices.
func TestPyTorchToggleChoicesAreTheEngineChoices(t *testing.T) {
	choices := engineChoices(setup.PyTorch, setup.PyTorchModelPlan().ID)
	if len(choices) != 2 || choices[0].ID != setup.PyTorchFullModelPlan().ID || choices[1].ID != setup.ZImageComfyModelPlan().ID {
		t.Fatalf("PyTorch choices %v", choices)
	}
	// With the full-precision Klein served, the compact one is a choice.
	served := engineChoices(setup.PyTorch, setup.PyTorchFullModelPlan().ID)
	if len(served) != 2 || served[0].ID != setup.PyTorchModelPlan().ID {
		t.Fatalf("choices beside the full Klein %v", served)
	}
	if !isEngineChoice(setup.PyTorch, choices[1].ID) || !isEngineChoice(setup.PyTorch, setup.PyTorchModelPlan().ID) || isEngineChoice(setup.PyTorch, "x") || isEngineChoice(setup.Lemonade, choices[0].ID) {
		t.Fatal("choice membership")
	}
	if len(engineChoices(setup.Lemonade, "")) != len(setup.PictureModelChoices()) || engineChoices(setup.TheNoise, "") != nil {
		t.Fatal("other engines' choices changed")
	}
}

// A prepared full-precision Klein is the PyTorch engine's picture model:
// the one-question step offers it as configured and never prepares the
// compact starter beside it; an engine with no model prepares the starter.
func TestStepPicturesCountAPreparedFullKleinAsConfigured(t *testing.T) {
	ctx := context.Background()
	personFolders(t)
	setup.UseUserState()
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	panel := &servicePanel{setupJournal: &journal}
	offer := func() (setup.StepPictureOffer, string) {
		t.Helper()
		model, err := panel.stepPictureModel(setup.PyTorch)
		if err != nil {
			t.Fatal(err)
		}
		o := stepPictureOwners{model: model,
			feature: func(setup.AppID) (setup.FeatureStatus, error) {
				return setup.FeatureStatus{Installed: true, Available: true}, nil
			},
			engineOffer: func(context.Context, setup.FeatureStatus, setup.AppID) (setup.StepPictureOffer, error) {
				return setup.StepPictureOffer{}, nil
			}}
		got, err := o.offer(ctx, setup.StepApp{Program: filepath.Join(t.TempDir(), "ComfyUI.exe"), Engine: setup.PyTorch, Title: "ComfyUI", Name: "comfyui"})
		if err != nil {
			t.Fatal(err)
		}
		return got, model.ReviewedPlan().ID
	}
	if got, plan := offer(); got.Configured || plan != setup.PyTorchModelPlan().ID {
		t.Fatalf("an engine without a model: configured %v plan %s", got.Configured, plan)
	}
	full := setup.PyTorchFullModelPlan()
	state := setup.ModelState{PlanID: full.ID, Approved: true, ProviderConfigured: true, Effects: map[string]setup.ModelEffect{}}
	for _, a := range full.Assets {
		state.Effects[a.ID] = setup.ModelEffect{Phase: setup.Verified}
	}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(setup.PictureModelJournal(journal, full), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// The full Klein's controller is the engine's; with no engine serving
	// it here, its recorded preparation does not read prepared.
	if got, plan := offer(); got.Configured || plan != full.ID {
		t.Fatalf("a prepared full Klein: configured %v plan %s", got.Configured, plan)
	}
}
