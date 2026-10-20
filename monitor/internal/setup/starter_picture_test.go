package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// Each picture engine has its own starter plan, in the format its
// controller stages; Lemonade alone is a chat server and gets none.
func TestPictureEngineOfAndItsStarterModel(t *testing.T) {
	// ComfyUI's engine follows the card; these rows describe a Strix Halo.
	old := machineGraphics
	machineGraphics = func() ([]string, error) { return []string{`PCI\VEN_1002&DEV_1586&SUBSYS_38291043&REV_C1`}, nil }
	t.Cleanup(func() { machineGraphics = old })
	plan := func(selected ...AppID) Plan {
		t.Helper()
		p, err := NewCatalog().Plan(selected)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	for _, c := range []struct {
		selected []AppID
		engine   AppID
		app      bool
		model    string
	}{
		{[]AppID{ComfyUI}, PyTorch, true, PyTorchModelPlan().ID},
		{[]AppID{ComfyUI, PyTorch}, PyTorch, true, PyTorchModelPlan().ID},
		{[]AppID{ComfyUI, TheNoise}, TheNoise, false, TheNoiseModelPlan().ID},
		{[]AppID{ComfyUI, Lemonade}, Lemonade, true, ApprovedBF16Plan().ID},
		{[]AppID{ComfyUI, Lemonade, PyTorch}, PyTorch, true, PyTorchModelPlan().ID},
		{[]AppID{TheNoise}, TheNoise, false, TheNoiseModelPlan().ID},
		{[]AppID{PyTorch}, PyTorch, false, PyTorchModelPlan().ID},
		{[]AppID{TheNoise, PyTorch}, PyTorch, false, PyTorchModelPlan().ID},
		{[]AppID{Lemonade}, "", false, ""},
		{[]AppID{OpenCode}, "", false, ""},
	} {
		engine, app := PictureEngineOf(plan(c.selected...))
		model, _ := StarterPictureModel(engine)
		if engine != c.engine || app != c.app || model.ID != c.model {
			t.Errorf("%v: engine %q app %v model %q", c.selected, engine, app, model.ID)
		}
		if c.model != "" && StarterPictureEngine(c.model) != c.engine {
			t.Errorf("%v: plan %s maps to %q", c.selected, c.model, StarterPictureEngine(c.model))
		}
	}
	if StarterPictureEngine("") != "" || StarterPictureEngine(CodingModelPlan().ID) != "" {
		t.Fatal("a plan without a starter picture model names an engine")
	}
}

// Configured reads the engine's own model journal beside Setup's plan
// journal: absent and unprepared are false, prepared is true.
func TestStarterPictureConfiguredReadsTheEngineModelJournal(t *testing.T) {
	ctx := context.Background()
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	for _, engine := range []AppID{Lemonade, PyTorch, TheNoise} {
		read := func() (bool, error) { return starterPictureConfigured(ctx, journal, engine) }
		if got, err := read(); err != nil || got {
			t.Fatalf("%s without a journal: %v %v", engine, got, err)
		}
		plan, _ := StarterPictureModel(engine)
		for _, configured := range []bool{false, true} {
			state := ModelState{PlanID: plan.ID, Approved: true, Effects: map[string]ModelEffect{}, ProviderConfigured: configured}
			for _, a := range plan.Assets {
				state.Effects[a.ID] = ModelEffect{Phase: Verified}
			}
			raw, _ := json.Marshal(state)
			if err := os.WriteFile(StarterModelJournal(journal, engine), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if got, err := read(); err != nil || got != configured {
				t.Fatalf("%s configured=%v reads %v %v", engine, configured, got, err)
			}
		}
	}
	if StarterModelJournal(journal, Lemonade) != filepath.Join(filepath.Dir(journal.Path), "model.json") ||
		StarterModelJournal(journal, PyTorch) != filepath.Join(filepath.Dir(journal.Path), "model-pytorch-compact.json") ||
		PictureModelJournal(journal, PyTorchFullModelPlan()) != filepath.Join(filepath.Dir(journal.Path), "model-pytorch.json") {
		t.Fatal("starter model journals moved from the controllers' paths")
	}
}

// The default is pending while the starter model is prepared and unmarked;
// marking it keeps the rest of the journal, and every controller write keeps
// the mark.
func TestStarterDefaultPendingUntilMarked(t *testing.T) {
	ctx := context.Background()
	journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	if pending, err := StarterDefaultPending(ctx, journal, PyTorch); err != nil || pending {
		t.Fatalf("no journal reads pending: %v %v", pending, err)
	}
	if err := MarkStarterDefaultWritten(ctx, journal, PyTorch); err == nil {
		t.Fatal("marked a starter model with no journal")
	}
	plan := PyTorchModelPlan()
	state := ModelState{PlanID: plan.ID, Approved: true, Effects: map[string]ModelEffect{}, ProviderConfigured: true}
	for _, a := range plan.Assets {
		state.Effects[a.ID] = ModelEffect{Phase: Verified}
	}
	raw, _ := json.Marshal(state)
	if err := os.WriteFile(StarterModelJournal(journal, PyTorch), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if pending, err := StarterDefaultPending(ctx, journal, PyTorch); err != nil || !pending {
		t.Fatalf("a prepared starter model is not pending: %v %v", pending, err)
	}
	if err := MarkStarterDefaultWritten(ctx, journal, PyTorch); err != nil {
		t.Fatal(err)
	}
	if pending, err := StarterDefaultPending(ctx, journal, PyTorch); err != nil || pending {
		t.Fatalf("a marked default is still pending: %v %v", pending, err)
	}
	c, _ := starterController(journal, PyTorch)
	id := plan.Assets[0].ID
	if err := c.change(ctx, id, state.Effects[id], ModelEffect{Phase: Verified, Reason: "rechecked"}); err != nil {
		t.Fatal(err)
	}
	if after, err := c.load(ctx); err != nil || !after.DefaultWritten || !after.Approved {
		t.Fatalf("a controller write lost the mark: %+v %v", after, err)
	}
}

func TestInstalledPictureModelUsesEligibleOffersAndRecordedServedModel(t *testing.T) {
	prepare := func(t *testing.T, journal Journal, plan ModelPlan) {
		t.Helper()
		state := ModelState{PlanID: plan.ID, Approved: true, ProviderConfigured: true, Effects: map[string]ModelEffect{}}
		for _, asset := range plan.Assets {
			state.Effects[asset.ID] = ModelEffect{Phase: Verified}
		}
		raw, err := json.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(PictureModelJournal(journal, plan), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	check := func(t *testing.T, journal Journal, want string) {
		t.Helper()
		model, ok, err := InstalledPictureModel(t.Context(), journal)
		if model != want || ok != (want != "") || err != nil {
			t.Fatalf("installed model = %q, %v, %v; want %q", model, ok, err, want)
		}
	}
	t.Run("eligible offers in default order", func(t *testing.T) {
		journal := Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
		prepare(t, journal, TheNoiseModelPlan())
		prepare(t, journal, ZImageComfyModelPlan())
		check(t, journal, "")
		lemonade := ApprovedBF16Plan()
		prepare(t, journal, lemonade)
		check(t, journal, lemonade.Model)
		full := PyTorchFullModelPlan()
		prepare(t, journal, full)
		check(t, journal, full.Model)
		if err := os.WriteFile(journal.Path, []byte("invalid served model journal"), 0o600); err != nil {
			t.Fatal(err)
		}
		check(t, journal, full.Model)
		compact := PyTorchModelPlan()
		prepare(t, journal, compact)
		check(t, journal, compact.Model)
	})
	t.Run("registered set overrides prepared offer", func(t *testing.T) {
		fixture := newPairFixture(t)
		check(t, fixture.j, "")
		prepare(t, fixture.j, PyTorchFullModelPlan())
		check(t, fixture.j, PyTorchModelPlan().Model)
	})
}
