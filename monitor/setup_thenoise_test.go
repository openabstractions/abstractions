package main

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type boundEngineController struct {
	setupModelControllerFake
	actions int
}

func (f *boundEngineController) AdvanceWithProgress(context.Context, string, func(int64, int64) error) (setup.ModelStatus, error) {
	f.actions++
	return f.status, nil
}
func (f *boundEngineController) Retry(context.Context, string) (setup.ModelStatus, error) {
	f.actions++
	return f.status, nil
}
func (f *boundEngineController) Configure(context.Context, bool, func(int64, int64) error) (setup.ModelStatus, error) {
	f.actions++
	return f.status, nil
}
func (f *boundEngineController) ReconcileProvider(context.Context) (setup.ModelStatus, error) {
	f.actions++
	return f.status, nil
}

func TestTheNoisePanelBindsEveryModelActionToItsReviewedEngineAndPlan(t *testing.T) {
	testBoundEnginePanel(t, setup.TheNoise, setup.TheNoiseModelPlan())
}

func TestPyTorchPanelBindsEveryModelActionToItsReviewedEngineAndPlan(t *testing.T) {
	testBoundEnginePanel(t, setup.PyTorch, setup.PyTorchModelPlan())
}

func TestPyTorchRemovalAvailableAcrossOwnedInterruptedPhases(t *testing.T) {
	for _, phase := range []setup.Phase{setup.Staged, setup.Checked, setup.Installed, setup.Pending, setup.Verified} {
		for _, key := range []string{"", "fixture-birth-key"} {
			for _, removing := range []bool{false, true} {
				status := setupPanelStatus{Status: setup.Status{Components: []setup.ComponentStatus{{ID: "pytorch-engine", Effect: setup.Effect{Phase: phase, ComponentRootKey: key, Removing: removing}}}}}
				want := key != "" && (phase == setup.Staged || phase == setup.Checked || phase == setup.Installed)
				if installedSetupEngine(status, setup.PyTorch) != want {
					t.Fatal("interrupted removal eligibility", phase, key, removing)
				}
			}
		}
	}
}

func testBoundEnginePanel(t *testing.T, engine setup.AppID, model setup.ModelPlan) {
	t.Helper()
	j := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	plan, err := j.Catalog().Plan([]setup.AppID{engine})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.SavePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	fake := &boundEngineController{setupModelControllerFake: setupModelControllerFake{status: setup.ModelStatus{Plan: model}, disk: setup.ModelDiskStatus{Enough: true}}}
	for _, asset := range model.Assets {
		fake.status.Assets = append(fake.status.Assets, setup.ModelAssetStatus{ID: asset.ID, Phase: setup.Verified})
	}
	lemonade := &boundEngineController{}
	p := &servicePanel{setupJournal: &j, modelController: lemonade, engineControllers: map[setup.AppID]setupModelController{engine: fake}, setupFeatureCheck: func() (setup.FeatureStatus, error) { return setup.FeatureStatus{Installed: true, Available: true}, nil }}
	h := p.handler("setup-test-key")
	post := func(action string, body setupModelRequest) int {
		raw, _ := json.Marshal(body)
		return panelPost(t, h, "/setup/model/"+action, string(raw)).Code
	}
	for _, action := range []string{"start", "advance", "retry", "configure", "reconcile"} {
		body := setupModelRequest{Engine: engine, PlanID: model.ID, SetupPlanID: plan.ID, AssetID: model.Assets[0].ID, Confirm: true}
		for _, change := range []func(*setupModelRequest){
			func(b *setupModelRequest) { b.Engine = "unknown" },
			func(b *setupModelRequest) { b.PlanID = setup.ApprovedBF16Plan().ID },
			func(b *setupModelRequest) { b.SetupPlanID = "stale" },
		} {
			wrong := body
			change(&wrong)
			if post(action, wrong) == http.StatusOK {
				t.Fatalf("%s accepted a stale engine/plan", action)
			}
		}
		if fake.starts != 0 && action == "start" || fake.actions != 0 && action == "start" {
			t.Fatal("denied model action ran")
		}
		if code := post(action, body); code != http.StatusOK {
			t.Fatalf("%s fixed request: %d", action, code)
		}
	}
	if fake.starts != 1 || fake.actions != 4 || lemonade.starts != 0 || lemonade.actions != 0 {
		t.Fatalf("controller routing: TheNoise %d/%d Lemonade %d/%d", fake.starts, fake.actions, lemonade.starts, lemonade.actions)
	}
	other, err := j.Catalog().Plan([]setup.AppID{setup.Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.SavePlan(context.Background(), other); err != nil {
		t.Fatal(err)
	}
	if post("retry", setupModelRequest{Engine: engine, PlanID: model.ID, SetupPlanID: plan.ID, AssetID: model.Assets[0].ID}) == http.StatusOK || fake.actions != 4 {
		t.Fatal("old approved package view retained engine authority")
	}
}

func TestTheNoiseRemovalPanelRequiresFixedChoiceAndCurrentApproval(t *testing.T) {
	j := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	plan, err := j.Catalog().Plan([]setup.AppID{setup.TheNoise})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.SavePlan(context.Background(), plan); err != nil {
		t.Fatal(err)
	}
	h := (&servicePanel{setupJournal: &j}).handler("setup-test-key")
	for _, body := range []setupChoiceRequest{{Engine: setup.Lemonade, PlanID: plan.ID, Confirm: true}, {Engine: setup.TheNoise, PlanID: plan.ID}, {Engine: setup.TheNoise, PlanID: "stale", Confirm: true}} {
		raw, _ := json.Marshal(body)
		if out := panelPost(t, h, "/setup/engine/remove", string(raw)); out.Code == http.StatusOK {
			t.Fatal("unapproved removal accepted")
		}
	}
}
