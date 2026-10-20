package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestFixedPictureControllersKeepPanelDefaultCacheAndLivePlan(t *testing.T) {
	personFolders(t)
	setup.UseUserState()
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	injected := &setupModelControllerFake{}
	panel := &servicePanel{setupJournal: &journal, modelController: injected}

	engine, err := fixedSetupEngine("")
	if err != nil || engine != setup.Lemonade {
		t.Fatalf("default engine = %q, %v", engine, err)
	}
	if got, err := fixedSetupEngine(setup.ComfyUI); got != "" || err == nil || err.Error() != "unknown fixed picture engine" {
		t.Fatalf("non-engine selection = %q, %v", got, err)
	}
	if got, plan, err := panel.setupModelForEngine(engine); got != injected || plan.ID != setup.ApprovedBF16Plan().ID || err != nil {
		t.Fatalf("default controller = %T, plan %q, error %v", got, plan.ID, err)
	}

	refused := errors.New("the factory has no Setup home")
	setup.UseNoState(refused)
	t.Cleanup(setup.UseUserState)
	if got, plan, err := panel.setupModelForEngine(setup.TheNoise); got != nil || plan.ID != "" || !errors.Is(err, refused) {
		t.Fatalf("cached engine factory error = %v, controller %T, plan %q", err, got, plan.ID)
	}
	cached := &setupModelControllerFake{}
	panel.engineControllers = map[setup.AppID]setupModelController{setup.TheNoise: cached}
	if got, plan, err := panel.setupModelForEngine(setup.TheNoise); got != cached || plan.ID != setup.TheNoiseModelPlan().ID || err != nil {
		t.Fatalf("cached engine reopened its factory: controller %T, plan %q, error %v", got, plan.ID, err)
	}
	setup.UseUserState()

	readLive := func(want setup.ModelPlan) {
		t.Helper()
		got, plan, err := panel.setupModelForEngine(setup.PyTorch)
		if err != nil || got == nil || plan.ID != want.ID || panel.engineControllers[setup.PyTorch] != nil {
			t.Fatalf("live engine controller = %T, plan %q, want %q, error %v", got, plan.ID, want.ID, err)
		}
	}
	readLive(setup.PyTorchModelPlan())
	full := setup.PyTorchFullModelPlan()
	preparePictureModel(t, journal, full)
	readLive(full)
}

func TestPreparedEngineDoesNotRequireOrChangeApplicationSelection(t *testing.T) {
	for _, engine := range []setup.AppID{setup.Lemonade, setup.TheNoise, setup.PyTorch} {
		for _, action := range []string{"configure", "reconcile"} {
			t.Run(string(engine)+"/"+action, func(t *testing.T) {
				ctx := context.Background()
				journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
				plan, err := journal.Catalog().Plan([]setup.AppID{setup.ComfyUI, engine})
				if err != nil {
					t.Fatal(err)
				}
				if _, err = journal.SavePlan(ctx, plan); err != nil {
					t.Fatal(err)
				}
				model := setup.TheNoiseModelPlan()
				if engine == setup.Lemonade {
					model = setup.ApprovedBF16Plan()
				}
				if engine == setup.PyTorch {
					model = setup.PyTorchModelPlan()
				}
				controller := &setupModelControllerFake{status: setup.ModelStatus{Plan: model, Approved: true, Ready: true, ProviderConfigured: true}}
				panel := &servicePanel{setupJournal: &journal, modelController: controller, engineControllers: map[setup.AppID]setupModelController{engine: controller}}
				raw, _ := json.Marshal(setupModelRequest{Engine: engine, PlanID: model.ID, SetupPlanID: plan.ID})
				out := panelPost(t, panel.handler("setup-test-key"), "/setup/model/"+action, string(raw))
				if out.Code != http.StatusOK {
					t.Fatalf("prepared engine required app receipt/policy: %d %s", out.Code, out.Body.String())
				}
				state, err := journal.Load(ctx)
				if err != nil {
					t.Fatal(err)
				}
				if state.Configuration["comfyui-image-policy"] {
					t.Fatal("engine preparation confirmed an application choice")
				}
				if !state.Configuration[string(engine)+"-model"] {
					t.Fatal("engine preparation was not confirmed")
				}
			})
		}
	}
}

// readyingController is a model controller whose model becomes ready at
// its Configure.
type readyingController struct {
	setupModelControllerFake
}

func (f *readyingController) Configure(context.Context, bool, func(int64, int64) error) (setup.ModelStatus, error) {
	f.status.ProviderConfigured, f.status.Ready = true, true
	return f.status, nil
}

// The starter model that becomes ready in Setup is marked in the plan's
// configuration and becomes the account's picture model, once.
func TestReadyStarterModelBecomesThePictureDefaultOnce(t *testing.T) {
	ctx := context.Background()
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	plan, err := journal.Catalog().Plan([]setup.AppID{setup.ComfyUI, setup.PyTorch})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.SavePlan(ctx, plan); err != nil {
		t.Fatal(err)
	}
	model := setup.PyTorchModelPlan()
	controller := &readyingController{setupModelControllerFake{status: setup.ModelStatus{Plan: model, Approved: true}, disk: setup.ModelDiskStatus{Enough: true}}}
	for _, asset := range model.Assets {
		controller.status.Assets = append(controller.status.Assets, setup.ModelAssetStatus{ID: asset.ID, Phase: setup.Verified})
	}
	// The engine's model journal records the prepared model, as the real
	// controller's Configure leaves it.
	writeStarterJournal(t, journal, setup.PyTorch, true)
	var defaults []string
	fail := true
	previous := setupImageDefault
	setupImageDefault = func(_ context.Context, name string) error {
		defaults = append(defaults, name)
		if fail {
			fail = false
			return errors.New("selection service unavailable")
		}
		return nil
	}
	t.Cleanup(func() { setupImageDefault = previous })
	panel := &servicePanel{setupJournal: &journal, engineControllers: map[setup.AppID]setupModelController{setup.PyTorch: controller},
		setupFeatureCheck: func() (setup.FeatureStatus, error) { return setup.FeatureStatus{Installed: true, Available: true}, nil }}
	h := panel.handler("setup-test-key")
	raw, _ := json.Marshal(setupModelRequest{Engine: setup.PyTorch, PlanID: model.ID, SetupPlanID: plan.ID})
	if out := panelPost(t, h, "/setup/model/configure", string(raw)); out.Code != http.StatusConflict {
		t.Fatalf("a failed default write reads as done: %d %s", out.Code, out.Body.String())
	}
	pending := func() bool {
		t.Helper()
		req := httptest.NewRequest("GET", "/setup/model?engine=pytorch", nil)
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		got := httptest.NewRecorder()
		h.ServeHTTP(got, req)
		var reply setupModelReply
		if got.Code != http.StatusOK || json.Unmarshal(got.Body.Bytes(), &reply) != nil {
			t.Fatalf("model status: %d %s", got.Code, got.Body.String())
		}
		return reply.DefaultPending
	}
	if !pending() {
		t.Fatal("a failed default write is not pending for the next step")
	}
	// The page's model step asks again with reconcile; then the default is
	// written once and the person's later choice is never replaced.
	for _, action := range []string{"reconcile", "configure", "reconcile"} {
		if out := panelPost(t, h, "/setup/model/"+action, string(raw)); out.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", action, out.Code, out.Body.String())
		}
	}
	if len(defaults) != 2 || defaults[1] != "flux2-klein-4b-compact" || pending() {
		t.Fatalf("picture default writes: %v", defaults)
	}
	state, err := journal.Load(ctx)
	if err != nil || !state.Configuration["pytorch-model"] {
		t.Fatalf("the plan does not mark the PyTorch model: %v %v", state.Configuration, err)
	}
}
