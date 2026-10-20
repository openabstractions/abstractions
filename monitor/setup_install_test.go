package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	lemonade "github.com/openabstractions/abstraction-provider-lemonade/go/setup"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

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
	permissions, err := setupPermissions([]setup.AppID{setup.ComfyUI, setup.Lemonade})
	if err != nil || len(permissions) != 1 || len(permissions[0].Sentences) != len(rules) || permissions[0].Title != "ComfyUI" ||
		permissions[0].ID != setup.RulesDigest(rules) {
		t.Fatalf("picture permissions: %+v %v", permissions, err)
	}
	if none, err := setupPermissions([]setup.AppID{setup.Lemonade}); err != nil || none != nil {
		t.Fatalf("a plan without the picture goal lists permissions: %+v %v", none, err)
	}
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

// One Install approves the reviewed plan, its picture model and its
// permissions together, and the record survives a restart of the Panel.
func TestOneInstallRecordsTheWholeApprovalAndResumes(t *testing.T) {
	withOtherCopies(t, nil)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	call := installHandler(t, &journal)
	review := call("POST", "/setup/plan", `{"selected":["comfyui"]}`)
	var plan setupPlanReview
	if review.Code != 200 || json.Unmarshal(review.Body.Bytes(), &plan) != nil {
		t.Fatalf("review: %d %s", review.Code, review.Body.String())
	}
	if plan.PictureModel == nil || len(plan.Permissions) != 1 || len(plan.Permissions[0].Sentences) == 0 {
		t.Fatalf("the screen lacks the model or the permissions: %+v", plan)
	}
	modelID, permissionsID := setup.ApprovedBF16Plan().ID, plan.Permissions[0].ID
	body := func(model string, confirm bool) string {
		raw, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI}, PlanID: plan.ID, ModelPlanID: model, PermissionsID: permissionsID, Confirm: confirm})
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
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI}, PlanID: plan.ID, ModelPlanID: setup.ApprovedBF16Plan().ID, PermissionsID: pictureID(t), Confirm: true})
	if got := call("POST", "/setup/install", string(raw)); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "already installed") {
		t.Fatalf("Install beside an unchosen copy: %d %s", got.Code, got.Body.String())
	}
}

// ComfyUI opens with the picture recipe once per approval; the steps refuse
// without one.
func TestOneInstallOpensComfyOnceAndStepsNeedTheApproval(t *testing.T) {
	withOtherCopies(t, nil)
	var opened []string
	prior := openComfyPage
	openComfyPage = func(url string) { opened = append(opened, url) }
	t.Cleanup(func() { openComfyPage = prior })
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	call := installHandler(t, &journal)
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	step := `{"setup_plan_id":"` + plan.ID + `"}`
	for _, path := range []string{"/setup/install/open", "/setup/install/grants"} {
		if got := call("POST", path, step); got.Code != http.StatusConflict {
			t.Fatalf("%s without an approval: %d %s", path, got.Code, got.Body.String())
		}
	}
	raw, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI}, PlanID: plan.ID, ModelPlanID: setup.ApprovedBF16Plan().ID, PermissionsID: pictureID(t), Confirm: true})
	if got := call("POST", "/setup/install", string(raw)); got.Code != 200 {
		t.Fatalf("Install: %d %s", got.Code, got.Body.String())
	}
	for range 2 {
		if got := call("POST", "/setup/install/open", step); got.Code != 200 {
			t.Fatalf("open: %d %s", got.Code, got.Body.String())
		}
	}
	if len(opened) != 1 || opened[0] != comfyRecipeURL {
		t.Fatalf("ComfyUI opened %v, want once at %s", opened, comfyRecipeURL)
	}
	// The permissions step names the installed ComfyUI program; with no
	// receipt-verified install it writes nothing.
	if got := call("POST", "/setup/install/grants", step); got.Code != http.StatusConflict {
		t.Fatalf("grants before ComfyUI is installed: %d %s", got.Code, got.Body.String())
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
	plan, err := setup.NewCatalog().Plan([]setup.AppID{setup.ComfyUI})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(setupChoiceRequest{Selected: []setup.AppID{setup.ComfyUI}, PlanID: plan.ID, ModelPlanID: setup.ApprovedBF16Plan().ID, PermissionsID: pictureID(t), Confirm: true})
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
	if got := call("POST", "/setup/install/grants", step); got.Code != http.StatusConflict || len(written) != 1 {
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
