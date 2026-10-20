package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type codingPageRunningTransfer struct {
	*codingPanelTransfer
	reserveFailure error
}

func (f *codingPageRunningTransfer) Reserve(ctx context.Context, pin setup.PinnedDownload, key string) (setup.WorkIdentity, error) {
	if f.reserveFailure != nil {
		return setup.WorkIdentity{}, f.reserveFailure
	}
	return f.codingPanelTransfer.Reserve(ctx, pin, key)
}

func (f *codingPageRunningTransfer) Observe(context.Context, setup.WorkIdentity) (setup.TransferObservation, error) {
	return setup.TransferObservation{State: "running", Done: 32768, Total: setup.CodingModelPlan().DownloadBytes}, nil
}

func TestCodingModelUnreservedConsentDoesNotClaimBackgroundWork(t *testing.T) {
	root := t.TempDir()
	model := setup.CodingModelPlan()
	transfer := &codingPageRunningTransfer{codingPanelTransfer: &codingPanelTransfer{panelTransfer: &panelTransfer{}}, reserveFailure: errors.New("fixture reservation failed")}
	controller := setup.ModelController{Plan: &model, JournalPath: filepath.Join(root, "coding.json"), StageDir: root, ProviderRoot: root, Transfer: transfer, LocalResults: true, FreeBytes: func(string) (uint64, error) { return 1 << 50, nil }}
	_, err := controller.StartAccepted(context.Background(), setup.ModelApproval{PlanID: model.ID, DownloadBytes: model.DownloadBytes, PeakBytes: model.PeakBytes, Licenses: model.Licenses})
	if err == nil || !strings.Contains(err.Error(), "fixture reservation failed") {
		t.Fatalf("reservation failure: %v", err)
	}
	panel := &servicePanel{codingController: &controller}
	got := installHandlerFor(panel.handler("setup-test-key"))("GET", "/setup/modelhost/coding", "")
	if got.Code != http.StatusOK {
		t.Fatalf("saved consent: %d %s", got.Code, got.Body.String())
	}
	var reply struct {
		Status setup.ModelStatus `json:"status"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.Status.Approved || len(reply.Status.Assets) != 1 || reply.Status.Assets[0].Phase != setup.Pending || reply.Status.Assets[0].Transfer != nil {
		t.Fatalf("fixture must retain approved local intent without a public job: %s", got.Body.String())
	}
	if transfer.reserves != 0 || transfer.submits != 0 || transfer.work.Key != "" {
		t.Fatal("failed reservation invented public work")
	}
}

// Run the page's real initial reads against the real saved
// Install/coding receivers. Only DOM presentation and unrelated engine preview
// are stand-ins; no app journal or fresh Review is supplied.
func TestModelOnlyPageReopensSavedInstallInEitherInitialReadOrder(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not installed")
	}
	for _, order := range []string{"state-first", "copies-first"} {
		t.Run(order, func(t *testing.T) {
			root := t.TempDir()
			journal := setup.Journal{Path: filepath.Join(root, "apps", "plan.json")}
			model := setup.CodingModelPlan()
			transfer := &codingPageRunningTransfer{codingPanelTransfer: &codingPanelTransfer{panelTransfer: &panelTransfer{}}}
			controller := setup.ModelController{Plan: &model, JournalPath: filepath.Join(root, "coding.json"), StageDir: root, ProviderRoot: root, Transfer: transfer, LocalResults: true, FreeBytes: func(string) (uint64, error) { return 1 << 50, nil }}
			panel := &servicePanel{setupJournal: &journal, codingController: &controller, maintenanceReview: func(context.Context) setup.MaintenanceReview { return setup.MaintenanceReview{} }}
			stubOverviewMachine(t)
			handler := panel.handler("setup-test-key")
			review, _ := codingOnlyReview(model.ID)
			raw, _ := json.Marshal(setupChoiceRequest{PlanID: review.ID, CodingModelPlanID: model.ID, Confirm: true})
			if got := installHandlerFor(handler)("POST", "/setup/install", string(raw)); got.Code != 200 {
				t.Fatalf("initial approval: %d %s", got.Code, got.Body.String())
			}
			work := transfer.work
			before := transfer.submits
			stateDone, copiesDone := make(chan struct{}), make(chan struct{})
			var stateOnce, copiesOnce sync.Once
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/setup/state":
					if order == "copies-first" {
						select {
						case <-copiesDone:
						case <-r.Context().Done():
							return
						}
					}
					handler.ServeHTTP(w, r)
					stateOnce.Do(func() { close(stateDone) })
				case "/setup/other-copies":
					if order == "state-first" {
						select {
						case <-stateDone:
						case <-r.Context().Done():
							return
						}
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{"copies":[]}`))
					copiesOnce.Do(func() { close(copiesDone) })
				case "/setup/modelhost/chat":
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(`{}`))
				default:
					handler.ServeHTTP(w, r)
				}
			}))
			t.Cleanup(server.Close)
			input, _ := json.Marshal(map[string]string{"base": server.URL})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, node, "-e", modelOnlyResumePageProbe)
			cmd.Stdin = bytes.NewReader(input)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("actual page resume: %v\n%s", err, out)
			}
			var report struct {
				Calls    []string `json:"calls"`
				Sentence string   `json:"sentence"`
				Button   string   `json:"button"`
				Line     string   `json:"line"`
			}
			if json.Unmarshal(out, &report) != nil {
				t.Fatalf("page result: %s", out)
			}
			// The page shows the saved install as stopped (owner,
			// 2026-11-09): the coding download the job service keeps shows
			// its bytes, and Install continues it; nothing is approved,
			// planned or started on opening.
			if report.Sentence != "Finish installing Qwen3.6 35B." || report.Button != "Install" || !strings.HasPrefix(report.Line, "Downloading · ") || !strings.HasSuffix(report.Line, " of 21.3 GB") {
				t.Fatalf("reopened background state is unclear: %s", out)
			}
			for _, call := range report.Calls {
				if strings.HasPrefix(call, "POST ") {
					t.Fatalf("reopen invented a fresh setup action: %s", out)
				}
			}
			if transfer.reserves != 1 || transfer.submits != before || !reflect.DeepEqual(transfer.work, work) {
				t.Fatalf("reopen changed work: %s reserves=%d submits=%d", out, transfer.reserves, transfer.submits)
			}
			if _, err := os.Stat(journal.Path); !os.IsNotExist(err) {
				t.Fatalf("page created an app plan: %v", err)
			}
		})
	}
}

const modelOnlyResumePageProbe = `
const fs=require('node:fs'),h=require('./testdata/setup_page_test_helper.js');
const input=JSON.parse(fs.readFileSync(0,'utf8')),calls=[];
(async()=>{const p=await h.page(async(path,body)=>{calls.push((body?'POST ':'GET ')+path);const r=await fetch(input.base+path,{method:body?'POST':'GET',headers:{'X-Panel-Key':'setup-test-key','Content-Type':'application/json'},body:body?JSON.stringify(body):undefined});if(!r.ok)return Error(await r.text());return r.json()});
for(let i=0;i<100&&!/^(Installing|Catalog stopped)/.test(p.summary().sentence);i++){await new Promise(r=>setTimeout(r,100));await h.settled()}
const s=p.summary();process.stdout.write(JSON.stringify({calls,sentence:s.sentence,button:s.button,line:p.parts('coding').line}))})().catch(e=>{console.error(e);process.exitCode=1});
`

func TestGetStartedPageIsRoutedAndOffersFixedGoals(t *testing.T) {
	for _, join := range []string{"coding_model_plan_id:coding", "picture_model_plan_ids:", "if(record.coding_model_plan_id)steps.push('coding')", "setup_plan_id:planID,confirm:true,approval", "/setup/overview", "/setup/modelhost/coding"} {
		if !strings.Contains(setupPage, join) {
			t.Fatalf("Install is disconnected from %s", join)
		}
	}
	const key = "setup-test-key"
	handler := (&servicePanel{}).handler(key)
	req := httptest.NewRequest("GET", "/setup?k="+key, nil)
	req.Host = "127.0.0.1"
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, req)
	if got.Code != 200 {
		t.Fatalf("GET /setup: %d %s", got.Code, got.Body.String())
	}
	for _, text := range []string{
		"Setup", "Install apps and models built for OpenAbstractions.", "<h2>Apps</h2>", "<h2>Models</h2>", "Chat and coding", "Pictures", "Engines",
		"Create pictures", "Coding assistant", "Chosen for this computer: llama.cpp for chat.",
		"Checking what is installed…", `class="oa-primary"`, "Tick to install. Clear a tick to remove.",
	} {
		if !strings.Contains(got.Body.String(), text) {
			t.Errorf("setup page missing %q", text)
		}
	}
	for _, retired := range []string{"PyTorch", "ROCm", "picture engine", "gfx", "Model host"} {
		if strings.Contains(setupPageBody, retired) {
			t.Errorf("Setup shows %q to a person", retired)
		}
	}
	if strings.Contains(got.Body.String(), "type=\"url\"") || strings.Contains(got.Body.String(), "type=\"command\"") {
		t.Fatal("setup page accepts a caller-supplied artifact URL or command")
	}
	if nav := panelNav(key, "setup"); !strings.Contains(nav, `href="/settings?k=setup-test-key"`) {
		t.Fatalf("Setup does not reach the other pages: %s", nav)
	}
}

// Setup is one page of ticks built from the shared components (architect
// ruling 2026-11-09): the page head, Apps, Models, Engines (a heading like
// Apps and Models with the engine choice as its caption, owner 2026-11-09)
// and the OpenAbstractions row, then one sticky summary row whose button is
// the page's only one. The retired sections, the engine chooser and the This
// device group are gone.
func TestGetStartedStatesUseTheSharedComponents(t *testing.T) {
	head := `<header class="oa-pagehead">`
	if !strings.HasPrefix(setupPage, head) || strings.Count(setupPage, head) != 1 || strings.Contains(setupPage, "<style>") {
		t.Fatal("Setup does not open with the one page head, or keeps page-local CSS")
	}
	order := []string{`id="setup-choices"`, `id="setup-models"`, `id="setup-engines"`, `id="setup-oa"`, `id="setup-summary-row"`}
	for i := 1; i < len(order); i++ {
		if a, b := strings.Index(setupPage, order[i-1]), strings.Index(setupPage, order[i]); a < 0 || b < a {
			t.Fatalf("%s does not follow %s", order[i], order[i-1])
		}
	}
	for _, heading := range []string{`id="setup-choices" class="oa-group"><h2>Apps</h2>`, `id="setup-models" class="oa-group"><h2>Models</h2>`,
		`<section id="setup-engines" class="oa-group"><h2>Engines</h2><p id="setup-engine-choice" class="oa-caption">`} {
		if !strings.Contains(setupPage, heading) {
			t.Errorf("Setup lacks the section heading %q", heading)
		}
	}
	if strings.Contains(setupPage, `<details id="setup-engines"`) {
		t.Error("Engines is still a folded row; it is a heading like Apps and Models")
	}
	summary := setupPage[strings.Index(setupPage, `<div id="setup-summary-row"`):]
	summary = summary[:strings.Index(summary, "\n")]
	if !strings.Contains(summary, `class="oa-row oa-footer"`) || !strings.Contains(summary, `<button id="setup-install" class="oa-primary" type="button" disabled>Install</button>`) || !strings.Contains(summary, `role="status"`) {
		t.Error("the summary row is not the sticky footer with the status and the one button")
	}
	if strings.Count(setupPage, "<button") != 1 || strings.Count(setupPage, "oa-primary") != 1 {
		t.Error("the page markup holds more than the one button")
	}
	for _, gone := range []string{"<select", "setup-engine-pick", "Picture engine", "setup-device", "This device", "setup-maintenance", "App setup", "Review app setup",
		"OpenAbstractions installer\u003c", "setup-coding-consent", "setup-model-approve", "Show details", "Open the picture workspace", "Local picture support",
		"earlier catalog", "from an earlier build", "Coming in a later preview", "Licences: "} {
		if strings.Contains(setupPage, gone) {
			t.Errorf("Setup still carries %q", gone)
		}
	}
}

func TestSetupDiscoveryFromApps(t *testing.T) {
	if !strings.Contains(servicePage, `id="apps-setup"`) || !strings.Contains(servicePage, "Browse the Catalog") {
		t.Fatal("Apps must expose the Catalog before registration")
	}
	if strings.Index(servicePage, `id="apps-setup"`) > strings.Index(servicePage, `id="apps"`) {
		t.Fatal("Setup must be visible before the apps list")
	}
	if !strings.Contains(setupPage, `<section id="setup-choices" class="oa-group"><h2>Apps</h2>`) {
		t.Fatal("Setup must lead with the apps Apps links to")
	}
	if !strings.Contains(setupPage, `<span id="setup-status" class="oa-row-label" role="status"`) {
		t.Fatal("Setup must show installation state on arrival")
	}
}

type setupModelControllerFake struct {
	status   setup.ModelStatus
	disk     setup.ModelDiskStatus
	starts   int
	approval setup.ModelApproval
}

func (f *setupModelControllerFake) DiskPreflight() (setup.ModelDiskStatus, error) { return f.disk, nil }
func (f *setupModelControllerFake) Status(context.Context) (setup.ModelStatus, error) {
	return f.status, nil
}
func (f *setupModelControllerFake) Start(_ context.Context, approval setup.ModelApproval) (setup.ModelStatus, error) {
	f.starts++
	f.approval = approval
	f.status.Approved = true
	return f.status, nil
}
func (f *setupModelControllerFake) AdvanceWithProgress(context.Context, string, func(int64, int64) error) (setup.ModelStatus, error) {
	return f.status, nil
}
func (f *setupModelControllerFake) Retry(context.Context, string) (setup.ModelStatus, error) {
	return f.status, nil
}
func (f *setupModelControllerFake) Configure(context.Context, bool, func(int64, int64) error) (setup.ModelStatus, error) {
	return f.status, nil
}
func (f *setupModelControllerFake) ReconcileProvider(context.Context) (setup.ModelStatus, error) {
	return f.status, nil
}

func TestSetupModelReviewAndApprovalAreBoundToFixedPlan(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	// Consent/controller fixture: the person explicitly selected the engine.
	// The feature check below is a test double, not package readiness proof.
	mainPlan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI, setup.Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.SavePlan(context.Background(), mainPlan); err != nil {
		t.Fatal(err)
	}
	fake := &setupModelControllerFake{status: setup.ModelStatus{Plan: setup.ApprovedBF16Plan()}, disk: setup.ModelDiskStatus{Enough: true}}
	panel := &servicePanel{setupJournal: &journal, modelController: fake,
		setupFeatureCheck: func() (setup.FeatureStatus, error) { return setup.FeatureStatus{Available: true, Installed: true}, nil }}
	handler := panel.handler("setup-test-key")
	get := httptest.NewRecorder()
	getReq := httptest.NewRequest("GET", "/setup/model", nil)
	getReq.Host = "127.0.0.1"
	getReq.Header.Set("X-Panel-Key", "setup-test-key")
	handler.ServeHTTP(get, getReq)
	if get.Code != http.StatusOK {
		t.Fatalf("model status: %d %s", get.Code, get.Body.String())
	}
	var reply setupModelReply
	if err := json.Unmarshal(get.Body.Bytes(), &reply); err != nil {
		t.Fatal(err)
	}
	if !reply.Enabled || !reply.Available || reply.SetupPlanID != mainPlan.ID || reply.Plan.ID != setup.ApprovedBF16Plan().ID {
		t.Fatalf("model preview was not bound to the approved picture plan: %+v", reply)
	}
	choices := setup.PictureModelChoices()
	if len(reply.Choices) != len(choices) || len(choices) != 2 {
		t.Fatalf("model reply offers %d other picture models, want %d", len(reply.Choices), len(choices))
	}
	for i := range choices {
		if reply.Choices[i].ID != choices[i].ID || reply.Choices[i].DownloadBytes != choices[i].DownloadBytes || reply.Choices[i].LicenseText == "" {
			t.Fatalf("choice %d = %+v", i, reply.Choices[i])
		}
	}
	chosen := httptest.NewRequest("POST", "/setup/model/start", strings.NewReader(`{"plan_id":"`+choices[0].ID+`","setup_plan_id":"`+mainPlan.ID+`","confirm":true}`))
	chosen.Host = "127.0.0.1"
	chosen.Header.Set("X-Panel-Key", "setup-test-key")
	refused := httptest.NewRecorder()
	handler.ServeHTTP(refused, chosen)
	if refused.Code == http.StatusOK || fake.starts != 0 {
		t.Fatalf("an offered model plan started a download: %d starts=%d", refused.Code, fake.starts)
	}
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/setup/model/start", strings.NewReader(body))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		out := httptest.NewRecorder()
		handler.ServeHTTP(out, req)
		return out
	}
	modelPlan := setup.ApprovedBF16Plan()
	unconfirmed := post(`{"plan_id":"` + modelPlan.ID + `","setup_plan_id":"` + mainPlan.ID + `"}`)
	if unconfirmed.Code != http.StatusConflict || fake.starts != 0 {
		t.Fatalf("unconfirmed model plan started: %d starts=%d %s", unconfirmed.Code, fake.starts, unconfirmed.Body.String())
	}
	wrongOuter := post(`{"plan_id":"` + modelPlan.ID + `","setup_plan_id":"wrong","confirm":true}`)
	if wrongOuter.Code != http.StatusConflict || fake.starts != 0 {
		t.Fatalf("stale outer plan started model setup: %d starts=%d", wrongOuter.Code, fake.starts)
	}
	confirmed := post(`{"plan_id":"` + modelPlan.ID + `","setup_plan_id":"` + mainPlan.ID + `","confirm":true}`)
	if confirmed.Code != http.StatusOK || fake.starts != 1 {
		t.Fatalf("confirmed model plan did not start: %d starts=%d %s", confirmed.Code, fake.starts, confirmed.Body.String())
	}
	if fake.approval.PlanID != modelPlan.ID || fake.approval.DownloadBytes != modelPlan.DownloadBytes || fake.approval.PeakBytes != modelPlan.PeakBytes || !slices.Equal(fake.approval.Licenses, modelPlan.Licenses) {
		t.Fatalf("server did not derive the fixed model approval: %+v", fake.approval)
	}
}

func TestSetupComponentActionPhasesAreConstrained(t *testing.T) {
	tests := []struct {
		name     string
		phase    setup.Phase
		retry    bool
		recovery bool
		want     bool
	}{
		{"advance pending", setup.Pending, false, false, true},
		{"advance requested", setup.Requested, false, false, true},
		{"advance verified", setup.Verified, false, false, true},
		{"advance installing", setup.Installing, false, false, false},
		{"retry failed", setup.Failed, true, false, true},
		{"retry uncertain", setup.Uncertain, true, false, true},
		{"retry requested", setup.Requested, true, false, false},
		{"recover interrupted install", setup.Installing, false, true, true},
		{"recover active transfer", setup.Requested, false, true, false},
		{"advance staged update to its health check", setup.Staged, false, false, true},
		{"advance checked update to activation", setup.Checked, false, false, true},
		{"advance installed", setup.Installed, false, false, false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := setupPhaseAllowsAction(test.phase, test.retry, test.recovery); got != test.want {
				t.Fatalf("setupPhaseAllowsAction(%q, retry=%t, recovery=%t) = %t, want %t", test.phase, test.retry, test.recovery, got, test.want)
			}
		})
	}
	retired := []setup.Artifact{{Version: "1.0.0"}}
	for _, test := range []struct {
		effect setup.Effect
		want   bool
	}{
		{setup.Effect{Phase: setup.Installed, Retired: retired}, true},
		{setup.Effect{Phase: setup.Failed, Retired: retired}, true},
		{setup.Effect{Phase: setup.Installed}, false},
		{setup.Effect{Phase: setup.Requested, Retired: retired}, true},
	} {
		if got := setupEffectAllowsAction(test.effect, false, false); got != test.want {
			t.Fatalf("advance of %+v allowed=%t, want %t", test.effect, got, test.want)
		}
	}
	if setupActionTimeout(setup.Checked, false) != 90*time.Minute || setupActionTimeout(setup.Installed, false) != 90*time.Minute {
		t.Fatal("activation rehash and earlier-version removal need the long deadline")
	}
}

func TestSetupActionDeadlinesMatchLongStages(t *testing.T) {
	if got := setupActionTimeout(setup.Requested, false); got != 30*time.Minute {
		t.Fatalf("completed download staging deadline = %s, want 30m", got)
	}
	if got := setupActionTimeout(setup.Verified, false); got != 90*time.Minute {
		t.Fatalf("archive installation deadline = %s, want 90m", got)
	}
	if got := setupActionTimeout(setup.Installing, true); got != 90*time.Minute {
		t.Fatalf("interrupted installation recovery deadline = %s, want 90m", got)
	}
	if got := setupActionTimeout(setup.Pending, false); got != 2*time.Minute {
		t.Fatalf("short submit deadline = %s, want 2m", got)
	}
}

func TestSetupFeatureMaintenanceNeedsAnApprovedPlanAndExplicitConfirmation(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	handler := (&servicePanel{setupJournal: &journal}).handler("setup-test-key")
	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/setup/feature/add", strings.NewReader(body))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		return got
	}
	if got := post(`{"plan_id":"plan"}`); got.Code != http.StatusBadRequest {
		t.Fatalf("missing explicit confirmation: %d %s", got.Code, got.Body.String())
	}
	if got := post(`{"plan_id":"plan","confirm":true}`); got.Code != http.StatusConflict {
		t.Fatalf("missing approved plan must stop before Windows Installer: %d %s", got.Code, got.Body.String())
	}
}

func TestSetupPlanAndExplicitSavedIntentUseFixedCatalog(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	handler := (&servicePanel{setupJournal: &journal}).handler("setup-test-key")
	post := func(path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", path, strings.NewReader(body))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		return got
	}
	preview := post("/setup/plan", `{"selected":["comfyui","lemonade","opencode"]}`)
	if preview.Code != 200 {
		t.Fatalf("plan preview: %d %s", preview.Code, preview.Body.String())
	}
	var review setupPlanReview
	if err := json.Unmarshal(preview.Body.Bytes(), &review); err != nil {
		t.Fatal(err)
	}
	plan := review.Plan
	// The apps are pending until the embedded pins document lists them, and
	// on every platform it does not pin (setup_adopter_assets_test.go covers
	// the pinned review).
	embedded, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI, setup.Lemonade, setup.OpenCode})
	if err != nil {
		t.Fatal(err)
	}
	pending := !embedded.Preparable
	if plan.Preparable != embedded.Preparable || plan.Ready || !slices.Equal(plan.Configuration, embedded.Configuration) {
		t.Fatalf("catalog reported unavailable apps as ready: %+v", plan)
	}
	model := setup.ApprovedBF16Plan()
	// ComfyUI beside other apps runs on the engine the plan selects for it.
	if review.PictureModel == nil || review.PictureModel.ID != model.ID {
		t.Fatalf("multi-application review lacks its picture engine's starter model: %+v", review.PictureModel)
	}
	picturePreview := post("/setup/plan", `{"selected":["comfyui","lemonade"]}`)
	var pictureReview setupPlanReview
	if picturePreview.Code != 200 || json.Unmarshal(picturePreview.Body.Bytes(), &pictureReview) != nil || pictureReview.PictureModel == nil || pictureReview.PictureModel.ID != model.ID || pictureReview.PictureModel.DownloadBytes != model.DownloadBytes || pictureReview.PictureModel.PeakBytes != model.PeakBytes || !slices.Equal(pictureReview.PictureModel.Licenses, model.Licenses) {
		t.Fatalf("explicit picture preview did not expose the canonical separate model requirement: %+v", pictureReview.PictureModel)
	}
	plainPreview := post("/setup/plan", `{"selected":["comfyui"]}`)
	var plainReview setupPlanReview
	if plainPreview.Code != 200 || json.Unmarshal(plainPreview.Body.Bytes(), &plainReview) != nil || plainReview.PictureModel == nil || len(plainReview.Permissions) != 1 {
		t.Fatalf("ComfyUI alone lacks its included engine's starter model: %d %+v", plainPreview.Code, plainReview)
	}
	if engine, _ := setup.PictureEngineOf(plainReview.Plan); plainReview.PictureModel.PictureEngine() != engine {
		t.Fatalf("ComfyUI alone offers the model of another engine: %s, runs on %s", plainReview.PictureModel.PictureEngine(), engine)
	}
	assistantPreview := post("/setup/plan", `{"selected":["opencode"]}`)
	var assistantReview setupPlanReview
	if assistantPreview.Code != 200 || json.Unmarshal(assistantPreview.Body.Bytes(), &assistantReview) != nil || assistantReview.PictureModel != nil {
		t.Fatalf("assistant-only review should not include picture-model requirements: %d %+v", assistantPreview.Code, assistantReview.PictureModel)
	}
	// The Get started OpenCode row plans the official-installer seam, which
	// stays pending until Setup runs that route; OA ships no OpenCode build.
	if len(assistantReview.Components) != 1 || assistantReview.Components[0].ID != setup.OpenCodeOfficialComponent ||
		assistantReview.Components[0].Artifact != nil || assistantReview.Preparable {
		t.Fatalf("the OpenCode row does not plan the pending official-installer component: %+v", assistantReview.Components)
	}
	if _, err := os.Stat(journal.Path); !os.IsNotExist(err) {
		t.Fatalf("reviewing a plan wrote the intent journal: stat error=%v", err)
	}
	comfyPlan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	stale := post("/setup/intent", `{"selected":["comfyui"],"plan_id":"old-catalog-plan","confirm":true}`)
	if stale.Code != 409 {
		t.Fatalf("stale reviewed plan = %d %s, want 409", stale.Code, stale.Body.String())
	}
	unconfirmedBody, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI}, PlanID: comfyPlan.ID, Confirm: false})
	unconfirmed := post("/setup/intent", string(unconfirmedBody))
	if unconfirmed.Code != 400 {
		t.Fatalf("unconfirmed intent = %d %s, want 400", unconfirmed.Code, unconfirmed.Body.String())
	}
	if _, err := os.Stat(journal.Path); !os.IsNotExist(err) {
		t.Fatalf("unconfirmed plan wrote the intent journal: stat error=%v", err)
	}
	confirmedBody, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI}, PlanID: comfyPlan.ID, Confirm: true})
	saved := post("/setup/intent", string(confirmedBody))
	if saved.Code != 200 {
		t.Fatalf("confirmed intent: %d %s", saved.Code, saved.Body.String())
	}
	var state setupPanelStatus
	if err := json.Unmarshal(saved.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(state.Plan.Selected, comfyPlan.Selected) || !slices.Contains(state.Plan.Selected, setup.ComfyUI) || state.Ready || !state.Approved {
		t.Fatalf("saved state = %+v; expected a pending Comfy plan", state)
	}
	for _, component := range state.Components {
		if component.Effect.Phase != setup.Pending {
			t.Fatalf("plan approval started work: %+v", component)
		}
	}
	read := httptest.NewRecorder()
	get := httptest.NewRequest("GET", "/setup/state", nil)
	get.Host = "127.0.0.1"
	get.Header.Set("X-Panel-Key", "setup-test-key")
	handler.ServeHTTP(read, get)
	if read.Code != 200 {
		t.Fatalf("read setup state: %d %s", read.Code, read.Body.String())
	}
	var restored setupPanelStatus
	if err := json.Unmarshal(read.Body.Bytes(), &restored); err != nil {
		t.Fatal(err)
	}
	if !restored.Approved || restored.Plan.ID != comfyPlan.ID {
		t.Fatalf("saved approval did not restore: %+v", restored)
	}
	staleAdvance := post("/setup/advance", `{"plan_id":"old-plan","component_id":"comfyui-app"}`)
	if staleAdvance.Code != 409 {
		t.Fatalf("stale advance = %d %s, want 409", staleAdvance.Code, staleAdvance.Body.String())
	}
	// The one driver: the page names the plan, Setup chooses the component.
	if stale := post("/setup/advance-next", `{"plan_id":"old-plan"}`); stale.Code != 409 {
		t.Fatalf("stale advance-next = %d %s, want 409", stale.Code, stale.Body.String())
	}
	if chosen := post("/setup/advance-next", `{"plan_id":"`+comfyPlan.ID+`","component_id":"comfyui-app"}`); chosen.Code != 400 {
		t.Fatalf("a page-chosen component = %d %s, want 400", chosen.Code, chosen.Body.String())
	}
	if pending {
		// A plan whose package is not available yet advances nothing.
		idle := post("/setup/advance-next", `{"plan_id":"`+comfyPlan.ID+`"}`)
		var reply setupAdvanceNextReply
		if idle.Code != 200 || json.Unmarshal(idle.Body.Bytes(), &reply) != nil || reply.Advanced != "" || reply.Plan.ID != comfyPlan.ID {
			t.Fatalf("idle advance-next = %d %s", idle.Code, idle.Body.String())
		}
	}
	staleRetry := post("/setup/retry", `{"plan_id":"old-plan","component_id":"comfyui-app"}`)
	if staleRetry.Code != 409 {
		t.Fatalf("stale retry = %d %s, want 409", staleRetry.Code, staleRetry.Body.String())
	}
	if !pending {
		// An advance of a pinned package would reach the job service.
		return
	}
	unavailableAdvance := post("/setup/advance", `{"plan_id":"`+comfyPlan.ID+`","component_id":"comfyui-app"}`)
	if unavailableAdvance.Code != 409 || !strings.Contains(unavailableAdvance.Body.String(), "not available yet") {
		t.Fatalf("unavailable advance = %d %s, want package-pending conflict", unavailableAdvance.Code, unavailableAdvance.Body.String())
	}
	unavailableRetry := post("/setup/retry", `{"plan_id":"`+comfyPlan.ID+`","component_id":"comfyui-app"}`)
	if unavailableRetry.Code != 409 || !strings.Contains(unavailableRetry.Body.String(), "not available yet") {
		t.Fatalf("unavailable retry = %d %s, want package-pending conflict", unavailableRetry.Code, unavailableRetry.Body.String())
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(journal.Path), "downloads")); !os.IsNotExist(err) {
		t.Fatalf("pending package advance created download staging path: %v", err)
	}
}

func TestPictureAppDoesNotKeepAnUnselectedEngine(t *testing.T) {
	if engineInPlan(setup.Plan{Selected: []setup.AppID{setup.ComfyUI}}, setup.Lemonade) {
		t.Fatal("an off Lemonade row remains selected through ComfyUI")
	}
}

func TestSetupStateExposesCurrentCatalogBeforeAnySelection(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	handler := (&servicePanel{setupJournal: &journal}).handler("setup-test-key")
	req := httptest.NewRequest("GET", "/setup/state", nil)
	req.Host = "127.0.0.1"
	req.Header.Set("X-Panel-Key", "setup-test-key")
	got := httptest.NewRecorder()
	handler.ServeHTTP(got, req)
	if got.Code != http.StatusOK {
		t.Fatalf("fresh state: %d %s", got.Code, got.Body.String())
	}
	var state setupPanelStatus
	if err := json.Unmarshal(got.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if len(state.Catalog.Choices) == 0 || state.Plan.ID != "" || state.Approved {
		t.Fatalf("fresh catalogue fabricated a selected/approved plan: %+v", state)
	}
	// The page reads the Lemonade engine row from the catalogue: on Windows
	// x64 its pinned package, elsewhere the sentence of what it needs.
	offered := runtime.GOOS == "windows" && runtime.GOARCH == "amd64"
	for _, component := range state.Catalog.Components {
		if component.ID == "lemonade-engine" && (offered != (component.Artifact != nil && component.Artifact.Version == "v2026.39.1") || offered == (component.Pending != "")) {
			t.Fatalf("fresh catalogue's Lemonade engine row: %+v", component)
		}
	}
}

// Setup speaks to the person: no release-process wording and no raw
// journal reason on the page.
func TestGetStartedWordingIsForThePerson(t *testing.T) {
	for _, release := range []string{"qualification pending", "producer receipt", "Engine staging not confirmed", "Native archive apps uninstall", "See what each app holds"} {
		if strings.Contains(setupPage, release) {
			t.Fatalf("Setup shows release or raw wording %q", release)
		}
	}
	for _, component := range setup.NewCatalog().Components {
		if strings.Contains(component.Pending, "reviewed") || strings.Contains(component.Pending, "receipt") {
			t.Fatalf("catalogue pending text is release wording: %q", component.Pending)
		}
	}
}
