package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// choicePanel is a Panel with an approved picture plan, a starter
// controller fake and one fake controller per offered plan.
func choicePanel(t *testing.T, starterConfigured bool) (*servicePanel, map[string]*setupModelControllerFake, setup.Plan) {
	t.Helper()
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	mainPlan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.SavePlan(context.Background(), mainPlan); err != nil {
		t.Fatal(err)
	}
	starter := &setupModelControllerFake{status: setup.ModelStatus{Plan: setup.ApprovedBF16Plan(), Approved: starterConfigured, ProviderConfigured: starterConfigured}, disk: setup.ModelDiskStatus{Enough: true}}
	choices := map[string]*setupModelControllerFake{}
	controllers := map[string]setupModelController{}
	for _, plan := range setup.PictureModelChoices() {
		fake := &setupModelControllerFake{status: setup.ModelStatus{Plan: plan}, disk: setup.ModelDiskStatus{Enough: true}}
		choices[plan.ID], controllers[plan.ID] = fake, fake
	}
	panel := &servicePanel{setupJournal: &journal, modelController: starter, choiceControllers: controllers,
		setupFeatureCheck: func() (setup.FeatureStatus, error) { return setup.FeatureStatus{Available: true, Installed: true}, nil }}
	return panel, choices, mainPlan
}

func panelPost(t *testing.T, handler http.Handler, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("POST", path, strings.NewReader(body))
	req.Host = "127.0.0.1"
	req.Header.Set("X-Panel-Key", "setup-test-key")
	out := httptest.NewRecorder()
	handler.ServeHTTP(out, req)
	return out
}

// Setup's start route accepts each listed plan's ID, each starting only its
// own controller with its own reviewed approval, once the starter model is
// prepared; an unknown ID starts nothing.
func TestSetupStartsEachListedPlanAndRefusesAnUnknownOne(t *testing.T) {
	before, beforeChoices, mainPlan := choicePanel(t, false)
	choice := setup.PictureModelChoices()[0]
	early := panelPost(t, before.handler("setup-test-key"), "/setup/model/start", `{"plan_id":"`+choice.ID+`","setup_plan_id":"`+mainPlan.ID+`","confirm":true}`)
	if early.Code != http.StatusConflict || beforeChoices[choice.ID].starts != 0 || !strings.Contains(early.Body.String(), "starter picture model") {
		t.Fatalf("an offered plan started before the starter model: %d %s", early.Code, early.Body.String())
	}
	panel, choices, mainPlan := choicePanel(t, true)
	handler := panel.handler("setup-test-key")
	for _, plan := range setup.PictureModelChoices() {
		out := panelPost(t, handler, "/setup/model/start", `{"plan_id":"`+plan.ID+`","setup_plan_id":"`+mainPlan.ID+`","confirm":true}`)
		if out.Code != http.StatusOK {
			t.Fatalf("%s: %d %s", plan.Model, out.Code, out.Body.String())
		}
		fake := choices[plan.ID]
		if fake.starts != 1 || fake.approval.PlanID != plan.ID || fake.approval.DownloadBytes != plan.DownloadBytes || !slices.Equal(fake.approval.Licenses, plan.Licenses) {
			t.Fatalf("%s started %d times with %+v", plan.Model, fake.starts, fake.approval)
		}
		for id, other := range choices {
			if id != plan.ID && other.starts > 1 {
				t.Fatalf("starting %s started %s", plan.Model, other.status.Plan.Model)
			}
		}
	}
	unknown := panelPost(t, handler, "/setup/model/start", `{"plan_id":"`+strings.Repeat("0", 64)+`","setup_plan_id":"`+mainPlan.ID+`","confirm":true}`)
	if unknown.Code == http.StatusOK {
		t.Fatalf("an unknown plan started: %s", unknown.Body.String())
	}
	total := 0
	for _, fake := range choices {
		total += fake.starts
	}
	if total != len(choices) {
		t.Fatalf("an unknown plan started a controller: %d starts", total)
	}
}
