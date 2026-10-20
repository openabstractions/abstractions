package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestGetStartedPageIsRoutedAndOffersFixedGoals(t *testing.T) {
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
		"Get started", "Choose what to set up.", `value="comfyui"`, `value="opencode"`, `value="lemonade"`,
		"Create pictures", "Coding assistant", "More picture engines", "Included with ComfyUI", "setup-install", "setup-install-progress",
		"Nothing downloads until you press Install.", `class="oa-primary"`,
		"Open the picture workspace", "Load picture recipe", "setupRenderOtherCopies", "/setup/other-copies",
		"edit the prompt", "press “Run”", `web.href=safeWebURL`,
	} {
		if !strings.Contains(got.Body.String(), text) {
			t.Errorf("setup page missing %q", text)
		}
	}
	if strings.Contains(got.Body.String(), "type=\"url\"") || strings.Contains(got.Body.String(), "type=\"command\"") {
		t.Fatal("setup page accepts a caller-supplied artifact URL or command")
	}
	if nav := panelNav(key, "setup"); !strings.Contains(nav, `href="/settings?k=setup-test-key"`) {
		t.Fatalf("Get started does not reach the other pages: %s", nav)
	}
}

// Get started's four states use the shared components (research/panel-
// redesign/SPEC.md §4, S7): a page head with one caption; the apps as rows
// with switches, ComfyUI chosen on arrival; the other engines behind "More
// picture engines"; a review with one primary action, Install; then the
// install steps and the per-component rows that carry Retry.
func TestGetStartedStatesUseTheSharedComponents(t *testing.T) {
	head := `<header class="oa-pagehead">`
	if !strings.HasPrefix(setupPage, head) || strings.Count(setupPage, head) != 1 || strings.Contains(setupPage, "<style>") {
		t.Fatal("Get started does not open with the one page head, or keeps page-local CSS")
	}
	section := func(id string) string {
		start := strings.Index(setupPage, `<section id="`+id+`"`)
		if start < 0 {
			t.Fatalf("no section %s", id)
		}
		return setupPage[start : start+strings.Index(setupPage[start:], "</section>")]
	}
	// Every disclosure has oaExpander's summary: its title and the row chevron.
	chevron := `<span class="oa-row-chevron" aria-hidden="true"></span></summary>`
	if !strings.Contains(oaExpander("x", ""), `<summary>x`+chevron) {
		t.Fatalf("oaExpander's summary changed: %s", oaExpander("x", ""))
	}
	if n := strings.Count(setupPage, "<summary>"); n != strings.Count(setupPage, chevron) || n != 2 {
		t.Errorf("a disclosure on Get started lacks the row chevron (%d summaries)", n)
	}
	choices := section("setup-choices")
	engines := choices[strings.Index(choices, `<details class="oa-expander" id="setup-engines"><summary>More picture engines`+chevron):]
	for _, app := range []string{"comfyui", "opencode", "lemonade", "thenoise", "pytorch"} {
		box := `<input type="checkbox" class="switch" id="setup-app-` + app + `" name="setup-app" value="` + app + `"`
		if !strings.Contains(choices, box) {
			t.Errorf("%s is no switch in an app row", app)
		}
		if behind := strings.Contains(engines, box); behind != (app != "comfyui" && app != "opencode") {
			t.Errorf("%s: behind More picture engines = %v", app, behind)
		}
	}
	// Other-copy rows go straight into the Apps group (the script places them
	// before More picture engines); no wrapper or loose sentence sits between
	// rows. The Review row has one caption; its status line shows only the
	// copy check while it runs (S7 follow-up).
	if strings.Contains(setupPage, "setup-other-copies") {
		t.Error("a wrapper still holds the other-copy notes")
	}
	reviewRow := choices[strings.Index(choices, `<span class="oa-row-label">Review and install</span>`):]
	if strings.Count(reviewRow, `class="oa-row-caption"`) != 1 || !strings.Contains(reviewRow, `<p id="setup-status" class="oa-status" role="status" aria-live="polite" data-state="loading">Checking for other copies of ComfyUI.</p>`) {
		t.Error("the Review row does not carry one caption and the copy check's status line")
	}
	for _, p := range regexp.MustCompile(`(?s)<section id="setup-choices".*?</section>`).FindAllString(setupPage, 1) {
		if loose := regexp.MustCompile(`(?m)^\s*<p`).FindString(p); loose != "" {
			t.Errorf("a loose paragraph sits between the Apps group's rows: %q", loose)
		}
	}
	if strings.Count(choices, " checked>") != 1 || !strings.Contains(choices, `value="comfyui" checked>`) {
		t.Error("a first run does not start with ComfyUI chosen and nothing else")
	}
	review := section("setup-plan")
	if !strings.Contains(review, `class="oa-group" hidden`) || strings.Count(setupPage, `class="oa-primary"`) < 1 || strings.Count(review, `oa-primary`) != 1 || !strings.Contains(review, `<button id="setup-install" class="oa-primary"`) {
		t.Error("the review does not have exactly one primary action, Install")
	}
	install, progress := strings.Index(setupPage, `<section id="setup-install-progress"`), strings.Index(setupPage, `<section id="setup-progress"`)
	if install < 0 || progress < install || progress > strings.Index(setupPage, `<section id="setup-engine-choice"`) {
		t.Error("per-component progress does not follow the install steps")
	}
	if !strings.Contains(section("setup-install-progress"), `id="setup-install-again"`) {
		t.Error("the stopped install step has no Try again")
	}
	// Models › Add a model owns local chat setup and links to the pinned ZIP
	// recovery at #setup-modelhost; Models' per-app rows own the picture
	// model; the app page owns Start and Remove (S7 review, decided for 0.4.0).
	if !strings.Contains(setupPage, `<section id="setup-modelhost" class="oa-group" hidden>`) || !strings.Contains(section("setup-modelhost"), `id="setup-modelhost-engine-stage"`) {
		t.Error("the pinned ZIP recovery is not at #setup-modelhost")
	}
	for _, moved := range []string{"setup-modelhost-root", "setup-modelhost-bundled", "setup-modelhost-feature", "setup-picture", "/setup/model/picture", "setup-comfy-activate", "setup-comfy-remove", "/setup/comfy/remove", "oa-note", "toLowerCase()"} {
		if strings.Contains(setupPage, moved) {
			t.Errorf("Get started still carries %q", moved)
		}
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
	mainPlan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI})
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
	preview := post("/setup/plan", `{"selected":["comfyui","opencode"]}`)
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
	embedded, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI, setup.OpenCode})
	if err != nil {
		t.Fatal(err)
	}
	pending := !embedded.Preparable
	if plan.Preparable != embedded.Preparable || plan.Ready || len(plan.Configuration) != 2 {
		t.Fatalf("catalog reported unavailable apps as ready: %+v", plan)
	}
	model := setup.ApprovedBF16Plan()
	if review.PictureModel == nil || review.PictureModel.ID != model.ID || review.PictureModel.DownloadBytes != model.DownloadBytes || review.PictureModel.PeakBytes != model.PeakBytes || !slices.Equal(review.PictureModel.Licenses, model.Licenses) {
		t.Fatalf("picture preview did not expose the canonical separate model requirement: %+v", review.PictureModel)
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
	if len(state.Plan.Selected) != 1 || state.Plan.Selected[0] != setup.ComfyUI || state.Ready || !state.Approved {
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
