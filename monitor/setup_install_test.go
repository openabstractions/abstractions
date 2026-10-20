package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	lemonade "github.com/openabstractions/abstraction-provider-lemonade/go/setup"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestPictureOffersRequireApplicationOnlyForStagedAttachment(t *testing.T) {
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	for _, engine := range []setup.AppID{setup.Lemonade, setup.PyTorch, setup.TheNoise} {
		_, _, offers, err := setupPictureOffers(context.Background(), journal, engine, nil)
		if err != nil || len(offers) == 0 {
			t.Fatalf("%s offers: %v, %v", engine, offers, err)
		}
		for _, offer := range offers {
			var required setup.AppID
			if offer.ID == setup.ZImageComfyModelPlan().ID {
				required = officialPictureCatalogID()
			}
			if offer.RequiredApplication != required || offer.StagesPictureFilesOnly() != (required != "") {
				t.Fatalf("%s offer %s: stage-only %t, required %q, want %q", engine, offer.ID,
					offer.StagesPictureFilesOnly(), offer.RequiredApplication, required)
			}
		}
	}
}

const pictureEngineHost = product.Lemonade

func picturePermissionRules() ([]setup.ApprovedRule, error) {
	return picturePermissionRulesFor(pictureEngineHost)
}

// ComfyUI gets the grants package's picture feature set and nothing wider:
// complete on the local engine's host and the image output's write and read.
// Every rule has a sentence on the approval screen, and no sentence names a
// rule ID.
func TestPicturePermissionsAreTheFeatureSetAsSentences(t *testing.T) {
	rules, err := picturePermissionRules()
	if err != nil {
		t.Fatal(err)
	}
	want := []setup.ApprovedRule{
		{Action: "abstraction.inference/complete", Resource: "host:lemonade-image"},
		{Action: "abstraction.storage/content.write", Resource: "output:image"},
		{Action: "abstraction.storage/content.read", Resource: "output:image"},
	}
	if !slices.Equal(rules, want) {
		t.Fatalf("ComfyUI rules %+v, want %+v", rules, want)
	}
	ruleID := regexp.MustCompile(`abstraction\.|host:|output:|job:`)
	for _, rule := range rules {
		sentence, err := permissionSentence(rule, usePictures)
		if err != nil {
			t.Fatal(err)
		}
		if ruleID.MatchString(sentence) || !strings.HasSuffix(sentence, ".") || strings.Contains(sentence, "background") {
			t.Fatalf("%s on %s reads %q", rule.Action, rule.Resource, sentence)
		}
	}
	permissions, err := setupPermissions(catalogPlan(t, setup.ComfyUI, setup.Lemonade))
	if err != nil || len(permissions) != 1 || len(permissions[0].Sentences) != len(rules) || permissions[0].Title != "ComfyUI" ||
		permissions[0].App != setup.ComfyUI || permissions[0].ID != setup.RulesDigest(rules) {
		t.Fatalf("picture permissions: %+v %v", permissions, err)
	}
	if none, err := setupPermissions(catalogPlan(t, setup.Lemonade)); err != nil || none != nil {
		t.Fatalf("a plan without the picture goal lists permissions: %+v %v", none, err)
	}
}

func TestPicturePermissionReviewUsesTheChoiceThatSuppliesTheEngine(t *testing.T) {
	catalog := setup.Catalog{Choices: []setup.Choice{
		{ID: "earlier-picture-app", Application: true, Title: "Earlier choice", Use: setup.UseImage, Engines: []setup.AppID{setup.PyTorch}},
		{ID: "selected-picture-app", Application: true, Title: "Selected choice", Use: setup.UseImage, Engines: []setup.AppID{setup.Lemonade}},
	}}
	plan := setup.Plan{Selected: []setup.AppID{"earlier-picture-app", "selected-picture-app", setup.Lemonade}}
	permissions, err := setupPermissionsForCatalog(catalog, plan)
	rules, rulesErr := picturePermissionRules()
	if err != nil || rulesErr != nil || len(permissions) != 1 || permissions[0].App != "selected-picture-app" ||
		permissions[0].Title != "Selected choice" || permissions[0].ID != setup.RulesDigest(rules) {
		t.Fatalf("picture permissions = %+v, err = %v, rules err = %v", permissions, err, rulesErr)
	}

	catalog.Choices[1].Application = false
	if permissions, err := setupPermissionsForCatalog(catalog, plan); err == nil || permissions != nil {
		t.Fatalf("an undisplayable owner returned permissions = %+v, err = %v", permissions, err)
	}
	if _, rules, err := picturePermissionSelection(catalog, plan); err == nil || rules != nil {
		t.Fatalf("an undisplayable owner produced rules = %+v, err = %v", rules, err)
	}
}

// An installed engine whose starter model is already prepared adds no model
// to the next review; one with none adds it, so its Install finishes it.
func TestReviewAddsTheStarterModelOnlyWhileTheEngineHasNone(t *testing.T) {
	withOtherCopies(t, nil)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	call := installHandler(t, &journal)
	review := func() setupPlanReview {
		t.Helper()
		got := call("POST", "/setup/plan", `{"selected":["comfyui","pytorch"]}`)
		var r setupPlanReview
		if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &r) != nil {
			t.Fatalf("review: %d %s", got.Code, got.Body.String())
		}
		return r
	}
	if got := review(); got.PictureModel == nil || got.PictureModel.ID != setup.PyTorchModelPlan().ID {
		t.Fatalf("an engine without a model offers none: %+v", got.PictureModel)
	}
	if err := os.MkdirAll(filepath.Dir(journal.Path), 0o755); err != nil {
		t.Fatal(err)
	}
	writeStarterJournal(t, journal, setup.PyTorch, true)
	if got := review(); got.PictureModel != nil {
		t.Fatalf("an engine with its model ready offers it again: %+v", got.PictureModel)
	}
	writeStarterJournal(t, journal, setup.PyTorch, false)
	if got := review(); got.PictureModel == nil {
		t.Fatal("an approved but unprepared model is not offered to finish")
	}
}

// writeStarterJournal writes engine's starter model journal, approved with
// every asset verified and configured as given.
func writeStarterJournal(t *testing.T, journal setup.Journal, engine setup.AppID, configured bool) {
	t.Helper()
	plan, _ := setup.StarterPictureModel(engine)
	effects := map[string]any{}
	for _, a := range plan.Assets {
		effects[a.ID] = map[string]any{"phase": "verified"}
	}
	raw, _ := json.Marshal(map[string]any{"plan_id": plan.ID, "approved": true, "effects": effects, "provider_configured": configured})
	if err := os.WriteFile(setup.StarterModelJournal(journal, engine), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// catalogPlan is the compiled catalog's plan of selected.
func catalogPlan(t *testing.T, selected ...setup.AppID) setup.Plan {
	t.Helper()
	plan, err := setup.NewCatalog().Plan(selected)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func installHandler(t *testing.T, journal *setup.Journal) func(method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	handler := (&servicePanel{setupJournal: journal}).handler("setup-test-key")
	return func(method, path, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		return got
	}
}

func TestInstallStartsOnlyItsExplicitCodingChoiceAndReopensWork(t *testing.T) {
	withOtherCopies(t, nil)
	root := t.TempDir()
	journal := setup.Journal{Path: filepath.Join(root, "apps", "plan.json")}
	model := setup.CodingModelPlan()
	transfer := &codingPanelTransfer{panelTransfer: &panelTransfer{}}
	c := setup.ModelController{Plan: &model, JournalPath: filepath.Join(root, "coding.json"), StageDir: root, ProviderRoot: root, Transfer: transfer, LocalResults: true, FreeBytes: func(string) (uint64, error) { return 1 << 50, nil }}
	panel := &servicePanel{setupJournal: &journal, codingController: &c}
	call := func(p *servicePanel, path string, value any) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(value)
		req := httptest.NewRequest("POST", path, strings.NewReader(string(raw)))
		req.Host = "127.0.0.1"
		req.Header.Set("X-Panel-Key", "setup-test-key")
		got := httptest.NewRecorder()
		p.handler("setup-test-key").ServeHTTP(got, req)
		return got
	}
	choice := setupChoiceRequest{Selected: []setup.AppID{setup.OpenCode}}
	review := call(panel, "/setup/plan", choice)
	var plan setupPlanReview
	if review.Code != 200 || json.Unmarshal(review.Body.Bytes(), &plan) != nil || plan.CodingModel != nil {
		t.Fatalf("unchosen review: %d %s", review.Code, review.Body.String())
	}
	choice.PlanID, choice.Confirm = plan.ID, true
	if got := call(panel, "/setup/install", choice); got.Code != 200 || transfer.submits != 0 {
		t.Fatalf("OpenCode inferred coding consent: %d %s", got.Code, got.Body.String())
	}
	choice.CodingModelPlanID = "stale"
	if got := call(panel, "/setup/install", choice); got.Code != 409 || transfer.submits != 0 {
		t.Fatal("stale coding approval submitted work")
	}
	choice.CodingModelPlanID = model.ID
	choice.Selected = nil
	review = call(panel, "/setup/plan", choice)
	if review.Code != 200 || json.Unmarshal(review.Body.Bytes(), &plan) != nil || plan.CodingModel == nil || plan.CodingModel.ID != model.ID || plan.CodingModel.DownloadBytes != 22853663008 {
		t.Fatalf("coding review: %d %s", review.Code, review.Body.String())
	}
	choice.PlanID = plan.ID
	// The first public submission loses its reply after the identity is saved.
	transfer.failure = errors.New("reply lost")
	if got := call(panel, "/setup/install", choice); got.Code != 409 {
		t.Fatalf("uncertain install: %d %s", got.Code, got.Body.String())
	}
	record, err := journal.LoadInstall(context.Background())
	if err != nil || record.CodingModelPlanID != model.ID {
		t.Fatalf("lost coding approval: %+v %v", record, err)
	}
	transfer.failure = nil
	reopened := &servicePanel{setupJournal: &journal, codingController: &c}
	approval := setup.ModelApproval{PlanID: model.ID, DownloadBytes: model.DownloadBytes, PeakBytes: model.PeakBytes, Licenses: model.Licenses}
	request := map[string]any{"action": "start", "confirm": true, "setup_plan_id": record.PlanID, "approval": approval}
	if got := call(reopened, "/setup/modelhost/coding", request); got.Code != 200 || !strings.Contains(got.Body.String(), `"accepted":true`) || transfer.reserves != 1 {
		t.Fatalf("reopened coding work: %d %s reserves=%d", got.Code, got.Body.String(), transfer.reserves)
	}
	request["setup_plan_id"] = "foreign"
	before := transfer.submits
	if got := call(reopened, "/setup/modelhost/coding", request); got.Code != 409 || transfer.submits != before {
		t.Fatal("foreign install submitted coding work")
	}
	// Replacing the approval with a legacy/no-coding install cannot replay it.
	if _, err := journal.ApproveInstall(context.Background(), plan.ID, "", nil, time.Now()); err != nil {
		t.Fatal(err)
	}
	request["setup_plan_id"] = plan.ID
	if got := call(reopened, "/setup/modelhost/coding", request); got.Code != 409 || transfer.submits != before {
		t.Fatal("missing coding approval submitted work")
	}
	combined := setupChoiceRequest{Selected: []setup.AppID{setup.Lemonade}, CodingModelPlanID: model.ID, Confirm: true}
	review = call(reopened, "/setup/plan", combined)
	if review.Code != 200 || json.Unmarshal(review.Body.Bytes(), &plan) != nil || plan.CodingModel == nil {
		t.Fatalf("combined review: %d %s", review.Code, review.Body.String())
	}
	combined.PlanID = plan.ID
	if got := call(reopened, "/setup/install", combined); got.Code != 200 || transfer.reserves != 1 {
		t.Fatalf("combined Install: %d %s", got.Code, got.Body.String())
	}
}

func TestModelOnlyInstallUsesNoAppPlanAndRequiresCurrentReview(t *testing.T) {
	root := t.TempDir()
	journal := setup.Journal{Path: filepath.Join(root, "apps", "plan.json")}
	model := setup.CodingModelPlan()
	transfer := &codingPanelTransfer{panelTransfer: &panelTransfer{}}
	c := setup.ModelController{Plan: &model, JournalPath: filepath.Join(root, "coding.json"), StageDir: root, ProviderRoot: root, Transfer: transfer, LocalResults: true, FreeBytes: func(string) (uint64, error) { return 1 << 50, nil }}
	handler := (&servicePanel{setupJournal: &journal, codingController: &c}).handler("setup-test-key")
	call := installHandlerFor(handler)
	for _, body := range []string{`{}`, `{"coding_model_plan_id":"stale"}`} {
		if got := call("POST", "/setup/plan", body); got.Code != 409 {
			t.Fatalf("invalid model-only review: %d %s", got.Code, got.Body.String())
		}
	}
	raw, _ := json.Marshal(setupChoiceRequest{CodingModelPlanID: model.ID})
	got := call("POST", "/setup/plan", string(raw))
	var review setupPlanReview
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &review) != nil || !review.Preparable || len(review.Selected) != 0 || len(review.Components) != 0 {
		t.Fatalf("model-only review: %d %s", got.Code, got.Body.String())
	}
	choice := setupChoiceRequest{PlanID: review.ID, CodingModelPlanID: model.ID, Confirm: true}
	choice.PlanID = "stale"
	raw, _ = json.Marshal(choice)
	if got := call("POST", "/setup/install", string(raw)); got.Code != 409 || transfer.submits != 0 {
		t.Fatal("unreviewed model-only install submitted")
	}
	choice.PlanID = review.ID
	raw, _ = json.Marshal(choice)
	got = call("POST", "/setup/install", string(raw))
	if got.Code != 200 || transfer.submits != 1 {
		t.Fatalf("model-only Install: %d %s", got.Code, got.Body.String())
	}
	if _, err := os.Stat(journal.Path); !os.IsNotExist(err) {
		t.Fatalf("model-only Install fabricated app plan: %v", err)
	}
	before := transfer.submits
	reopened := installHandler(t, &journal)("GET", "/setup/install", "")
	var reply setupInstallReply
	if reopened.Code != 200 || json.Unmarshal(reopened.Body.Bytes(), &reply) != nil || reply.Install.CodingModelPlanID != model.ID || !reply.Approved || transfer.submits != before {
		t.Fatalf("model-only reopen: %d %s", reopened.Code, reopened.Body.String())
	}
}

// One Install approves the reviewed plan, its picture model and its
// permissions together, and the record survives a restart of the Panel.
func TestOneInstallRecordsTheWholeApprovalAndResumes(t *testing.T) {
	withOtherCopies(t, nil)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	call := installHandler(t, &journal)
	review := call("POST", "/setup/plan", `{"selected":["comfyui","lemonade"]}`)
	var plan setupPlanReview
	if review.Code != 200 || json.Unmarshal(review.Body.Bytes(), &plan) != nil {
		t.Fatalf("review: %d %s", review.Code, review.Body.String())
	}
	if plan.PictureModel == nil || len(plan.Permissions) != 1 || len(plan.Permissions[0].Sentences) == 0 {
		t.Fatalf("the screen lacks the model or the permissions: %+v", plan)
	}
	modelID, permissionsID := setup.ApprovedBF16Plan().ID, plan.Permissions[0].ID
	body := func(model string, confirm bool) string {
		raw, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI, setup.Lemonade}, PlanID: plan.ID, ModelPlanID: model, PermissionsID: permissionsID, Confirm: confirm})
		return string(raw)
	}
	if got := call("POST", "/setup/install", body(modelID, false)); got.Code != http.StatusBadRequest {
		t.Fatalf("Install without the click's confirmation: %d %s", got.Code, got.Body.String())
	}
	if got := call("POST", "/setup/install", body("another-model", true)); got.Code != http.StatusConflict {
		t.Fatalf("Install of a model the screen did not show: %d %s", got.Code, got.Body.String())
	}
	permissionsID = "0000"
	if got := call("POST", "/setup/install", body(modelID, true)); got.Code != http.StatusConflict {
		t.Fatalf("Install of permissions the screen did not show: %d %s", got.Code, got.Body.String())
	}
	permissionsID = plan.Permissions[0].ID
	installed := call("POST", "/setup/install", body(modelID, true))
	var reply setupInstallReply
	if installed.Code != 200 || json.Unmarshal(installed.Body.Bytes(), &reply) != nil {
		t.Fatalf("Install: %d %s", installed.Code, installed.Body.String())
	}
	wantRules, _ := picturePermissionRules()
	if !reply.Approved || reply.Install.PlanID != plan.ID || reply.Install.ModelPlanID != modelID || !slices.Equal(reply.Install.Rules, wantRules) ||
		reply.Install.RulesDigest != permissionsID || reply.Install.Granted || reply.Install.Opened {
		t.Fatalf("Install recorded %+v", reply)
	}
	// A new Panel process reads the same record and resumes from it.
	resumed := installHandler(t, &journal)("GET", "/setup/install", "")
	var again setupInstallReply
	if resumed.Code != 200 || json.Unmarshal(resumed.Body.Bytes(), &again) != nil || again.Install.RulesDigest != reply.Install.RulesDigest || again.Install.PlanID != reply.Install.PlanID {
		t.Fatalf("resume after a restart: %d %s", resumed.Code, resumed.Body.String())
	}
}

// An Install without the picture goal approves no picture model and no
// permissions.
func TestOneInstallWithoutPicturesApprovesNoModel(t *testing.T) {
	withOtherCopies(t, nil)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	call := installHandler(t, &journal)
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.Lemonade}, PlanID: plan.ID, ModelPlanID: setup.ApprovedBF16Plan().ID, Confirm: true})
	if got := call("POST", "/setup/install", string(raw)); got.Code != http.StatusConflict {
		t.Fatalf("an engine-only plan approved a picture model: %d %s", got.Code, got.Body.String())
	}
	raw, _ = json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.Lemonade}, PlanID: plan.ID, Confirm: true})
	got := call("POST", "/setup/install", string(raw))
	var reply setupInstallReply
	if got.Code != 200 || json.Unmarshal(got.Body.Bytes(), &reply) != nil || reply.Install.ModelPlanID != "" || len(reply.Install.Rules) != 0 {
		t.Fatalf("engine-only Install: %d %s", got.Code, got.Body.String())
	}
}

// The A5 rule holds at Install: another copy keeps OA's build out unless the
// person chose it.
func TestOneInstallKeepsTheOtherCopyRule(t *testing.T) {
	withOtherCopies(t, []setup.OtherCopy{{App: setup.ComfyUI, Title: "ComfyUI", Location: `C:\Users\person\ComfyUI_windows_portable`, Source: "folder"}})
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	call := installHandler(t, &journal)
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI, setup.Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI, setup.Lemonade}, PlanID: plan.ID, ModelPlanID: setup.ApprovedBF16Plan().ID, PermissionsID: pictureID(t), Confirm: true})
	if got := call("POST", "/setup/install", string(raw)); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "already installed") {
		t.Fatalf("Install beside an unchosen copy: %d %s", got.Code, got.Body.String())
	}
}

// Native startup uses the registered application rather than a recipe URL.
func TestOneInstallRetiredRecipeActionIsFinite(t *testing.T) {
	p := &servicePanel{}
	w := httptest.NewRecorder()
	p.setupInstallOpen(w, httptest.NewRequest(http.MethodPost, "/setup/install/open", strings.NewReader(`{}`)))
	if w.Code != http.StatusGone {
		t.Fatalf("retired open: %d %s", w.Code, w.Body.String())
	}
}

func pictureID(t *testing.T) string {
	t.Helper()
	rules, err := picturePermissionRules()
	if err != nil {
		t.Fatal(err)
	}
	return setup.RulesDigest(rules)
}

// The grant step writes exactly the rules the Install click approved, once,
// even when this Panel version would compute other rules.
func TestGrantStepWritesTheApprovedRulesOnce(t *testing.T) {
	withOtherCopies(t, nil)
	var written [][]setup.ApprovedRule
	var programs []string
	priorWriter, priorProgram := grantWriter, comfyProgram
	grantWriter = func(_ context.Context, program string, rules []setup.ApprovedRule) error {
		programs, written = append(programs, program), append(written, rules)
		return nil
	}
	comfyProgram = func(*servicePanel, context.Context, setup.Journal) (string, error) {
		return `C:\OA\ComfyUI\python_embeded\python.exe`, nil
	}
	t.Cleanup(func() { grantWriter, comfyProgram = priorWriter, priorProgram })
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	call := installHandler(t, &journal)
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI, setup.Lemonade})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI, setup.Lemonade}, PlanID: plan.ID, ModelPlanID: setup.ApprovedBF16Plan().ID, PermissionsID: pictureID(t), Confirm: true})
	if got := call("POST", "/setup/install", string(raw)); got.Code != 200 {
		t.Fatalf("Install: %d %s", got.Code, got.Body.String())
	}
	// An approval an earlier Panel recorded with other rules: the step writes
	// those, not the rules this version computes.
	earlier := []setup.ApprovedRule{{Action: "abstraction.inference/complete", Resource: "host:lemonade-image"}}
	if _, err := journal.ApproveInstall(context.Background(), plan.ID, setup.ApprovedBF16Plan().ID, earlier, time.Now()); err != nil {
		t.Fatal(err)
	}
	step := `{"setup_plan_id":"` + plan.ID + `"}`
	for range 2 {
		if got := call("POST", "/setup/install/grants", step); got.Code != 200 {
			t.Fatalf("grants: %d %s", got.Code, got.Body.String())
		}
	}
	if len(written) != 1 || !slices.Equal(written[0], earlier) || programs[0] != `C:\OA\ComfyUI\python_embeded\python.exe` {
		t.Fatalf("written %+v for %v, want the approved %+v once", written, programs, earlier)
	}
	// A record whose rules no longer match their digest writes nothing.
	tampered := journal
	if _, err := tampered.MarkInstall(context.Background(), plan.ID, func(r *setup.InstallRecord) {
		r.Granted = false
		r.Rules = append(r.Rules, setup.ApprovedRule{Action: "abstraction.router/inventory.read", Resource: "abstraction.router/inventory"})
	}); err != nil {
		t.Fatal(err)
	}
	if got := call("POST", "/setup/install/grants", step); got.Code != http.StatusConflict || len(written) != 1 {
		t.Fatalf("a tampered approval: %d %s, writes %d", got.Code, got.Body.String(), len(written))
	}
	// A record whose rules and digest were both widened passes the digest
	// and stops at the ceiling the Panel rebuilds: nothing is written.
	widened := append(slices.Clone(pictureRulesFor(t)), setup.ApprovedRule{Action: "abstraction.storage/content.read", Resource: "output:video"})
	if _, err := journal.MarkInstall(context.Background(), plan.ID, func(r *setup.InstallRecord) {
		r.Granted, r.Rules, r.RulesDigest = false, widened, setup.RulesDigest(widened)
	}); err != nil {
		t.Fatal(err)
	}
	const refused = "the recorded permissions reach beyond what ComfyUI's pictures need; review and install again\n"
	if got := call("POST", "/setup/install/grants", step); got.Code != http.StatusConflict || len(written) != 1 || got.Body.String() != refused {
		t.Fatalf("a widened approval with a matching digest: %d %s, writes %d", got.Code, got.Body.String(), len(written))
	}
}

func pictureRulesFor(t *testing.T) []setup.ApprovedRule {
	t.Helper()
	rules, err := picturePermissionRules()
	if err != nil {
		t.Fatal(err)
	}
	return rules
}

// The engine host ComfyUI's permission names is the Lemonade provider's own
// provider ID, read from the provider module this module requires, so the
// published Panel checks it too.
func TestPictureEngineHostIsTheLemonadeProviderID(t *testing.T) {
	if lemonade.ProviderID != pictureEngineHost {
		t.Fatalf("the Lemonade provider's ProviderID is %q, the Panel names %q", lemonade.ProviderID, pictureEngineHost)
	}
}
