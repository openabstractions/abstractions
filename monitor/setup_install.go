package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	inference "github.com/openabstractions/abstraction-inference/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// One Install (hostile critic A1): the Setup page shows the app packages,
// the picture model, the disk need and the permissions ComfyUI gets on one
// screen, and one Install click approves them together. The page then runs
// every mechanical step itself; this file holds the approval record and the
// two steps that had no endpoint: writing ComfyUI's approved permissions and
// opening ComfyUI with the picture recipe.

// comfyRecipeURL is the fixed address the Panel opens once ComfyUI answers.
// The OA sidebar loads the picture recipe for oa_recipe=picture through the
// click-only path (VISION 2026-10-02): no OA question.
const comfyRecipeURL = "http://127.0.0.1:8188/?oa_recipe=picture"

// openComfyPage opens a URL in the person's browser. Tests replace it.
var openComfyPage = launch

// pictureEngineHost is the inference host of the local picture engine that
// ComfyUI's picture node calls through image@1: the Lemonade provider's
// setup.ProviderID. The Panel does not depend on the provider module, so a
// test pins the two equal.
const pictureEngineHost = "lemonade-image"

// setupPermission is one app's permissions, as the screen states them. ID is
// the digest of the exact rules; the Install click sends it back, and the
// approval records those rules.
type setupPermission struct {
	App       setup.AppID `json:"app"`
	Title     string      `json:"title"`
	Sentences []string    `json:"sentences"`
	ID        string      `json:"id"`
}

// picturePermissionRules are the exact rules ComfyUI's picture node needs:
// the grants package's picture feature set on the local engine's host. The
// node calls image@1 directly; it reads no router and submits no job.
func picturePermissionRules() ([]setup.ApprovedRule, error) {
	rules, err := grants.FeatureRules(grants.Picture, grants.For{Hosts: []string{pictureEngineHost}})
	if err != nil {
		return nil, err
	}
	approved := make([]setup.ApprovedRule, 0, len(rules))
	for _, rule := range rules {
		approved = append(approved, setup.ApprovedRule{Action: rule.Action, Resource: rule.Resource})
	}
	return approved, nil
}

// permissionUse is the feature a rule serves: it words the rules whose
// sentence depends on the app's use of them.
type permissionUse int

const (
	// usePictures is ComfyUI making pictures through the local engine
	// (grants.Picture).
	usePictures permissionUse = iota
)

// permissionSentence words one rule for an approval: the one place a rule
// becomes page text, so no page shows a rule ID. A rule with no sentence is
// an error: nothing is granted that the screen did not state.
func permissionSentence(rule setup.ApprovedRule, use permissionUse) (string, error) {
	switch {
	case use == usePictures && rule.Action == inference.ActionComplete && rule.Resource == inference.ResourceHost(pictureEngineHost):
		return "Make pictures with the local picture engine.", nil
	case use == usePictures && rule.Action == grants.ContentWriteAction && rule.Resource == "output:image":
		return "Save the pictures it makes.", nil
	case use == usePictures && rule.Action == grants.ContentReadAction && rule.Resource == "output:image":
		return "Read back the pictures it made.", nil
	}
	return "", fmt.Errorf("no sentence states %s on %s", rule.Action, rule.Resource)
}

// setupPermissions lists the permissions a plan's apps get at Install. Only
// the picture goal's ComfyUI gets any.
func setupPermissions(selected []setup.AppID) ([]setupPermission, error) {
	if !slices.Contains(selected, setup.ComfyUI) {
		return nil, nil
	}
	rules, err := picturePermissionRules()
	if err != nil {
		return nil, err
	}
	permission := setupPermission{App: setup.ComfyUI, Title: "ComfyUI", ID: setup.RulesDigest(rules)}
	for _, rule := range rules {
		sentence, err := permissionSentence(rule, usePictures)
		if err != nil {
			return nil, err
		}
		permission.Sentences = append(permission.Sentences, sentence)
	}
	return []setupPermission{permission}, nil
}

type setupInstallReply struct {
	setupPanelStatus
	Install setup.InstallRecord `json:"install"`
}

// setupInstall approves the reviewed plan, its picture model and its
// permissions with one click, then records the approval for the steps.
func (p *servicePanel) setupInstall(w http.ResponseWriter, r *http.Request) {
	p.setupActionMu.Lock()
	defer p.setupActionMu.Unlock()
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	request, ok := decodeSetupChoice(w, r)
	if !ok {
		return
	}
	if !request.Confirm {
		http.Error(w, "the Install click is the approval; it was not sent", http.StatusBadRequest)
		return
	}
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	plan, err := journal.Catalog().Plan(request.Selected)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if request.PlanID == "" || request.PlanID != plan.ID {
		http.Error(w, "reviewed setup plan changed; review it again", http.StatusConflict)
		return
	}
	picture := slices.Contains(plan.Selected, setup.ComfyUI)
	wantModel := ""
	if picture {
		wantModel = setup.ApprovedBF16Plan().ID
	}
	if request.ModelPlanID != wantModel {
		http.Error(w, "the reviewed picture model changed; review it again", http.StatusConflict)
		return
	}
	var rules []setup.ApprovedRule
	if picture {
		if rules, err = picturePermissionRules(); err != nil {
			panelError(w, err)
			return
		}
	}
	if request.PermissionsID != setup.RulesDigest(rules) {
		http.Error(w, "the permissions on the screen changed; review them again", http.StatusConflict)
		return
	}
	if refuseOtherCopies(w, request, otherCopiesNow()) {
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	status, err := runner.Start(ctx, request.Selected)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if status.Plan.ID != plan.ID {
		http.Error(w, "setup catalog changed; review it again", http.StatusConflict)
		return
	}
	record, err := journal.ApproveInstall(ctx, plan.ID, wantModel, rules, time.Now())
	if err != nil {
		panelError(w, err)
		return
	}
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, setupInstallReply{setupPanelStatus: state, Install: record})
}

// setupInstallState reports the approval record beside the plan state, so
// the page resumes the approved steps after a restart.
func (p *servicePanel) setupInstallState(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	record, err := journal.LoadInstall(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	if !state.Approved || record.PlanID != state.Plan.ID {
		record = setup.InstallRecord{}
	}
	panelJSON(w, setupInstallReply{setupPanelStatus: state, Install: record})
}

type setupInstallStep struct {
	SetupPlanID string `json:"setup_plan_id"`
}

func decodeInstallStep(w http.ResponseWriter, r *http.Request) (setupInstallStep, bool) {
	var step setupInstallStep
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&step); err != nil || step.SetupPlanID == "" {
		http.Error(w, "the approved setup plan is required", http.StatusBadRequest)
		return step, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "one install step request required", http.StatusBadRequest)
		return step, false
	}
	return step, true
}

// approvedInstall loads the record of the approved plan the step names.
func (p *servicePanel) approvedInstall(ctx context.Context, planID string) (setup.Journal, setup.InstallRecord, error) {
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		return journal, setup.InstallRecord{}, err
	}
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		return journal, setup.InstallRecord{}, err
	}
	record, err := journal.LoadInstall(ctx)
	if err != nil {
		return journal, setup.InstallRecord{}, err
	}
	if !state.Approved || state.Plan.ID != planID || record.PlanID != planID {
		return journal, setup.InstallRecord{}, errors.New("the approved installation changed; review and install again")
	}
	return journal, record, nil
}

// comfyProgram is the receipt-verified ComfyUI program the permissions name.
// Tests replace it.
var comfyProgram = func(p *servicePanel, ctx context.Context, journal setup.Journal) (string, error) {
	paths, err := p.openInstalledComfy(ctx, journal)
	if err != nil {
		return "", err
	}
	return paths.CallerProgram(), nil
}

// grantWriter writes the approved rules for one program. Tests replace it.
var grantWriter = func(ctx context.Context, program string, approved []setup.ApprovedRule) error {
	rules := make([]grants.Rule, 0, len(approved))
	for _, rule := range approved {
		rules = append(rules, grants.Rule{Action: rule.Action, Resource: rule.Resource})
	}
	caller, err := panelMachine().ObserveCaller(ctx)
	if err != nil || caller.Outcome.String() != "observed" || caller.Account == "" {
		return errors.New("the Panel's own account could not be verified")
	}
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	page, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil {
		return err
	}
	if page.Outcome.String() != "page" {
		return fmt.Errorf("the permission list is %s", page.Outcome.String())
	}
	result, err := grants.Write(ctx, operator, page.Revision, rightsclient.Subject{Account: caller.Account, Program: program}, rules, 0, "approved in Setup")
	if err != nil {
		return err
	}
	if result.Outcome != "applied" {
		return fmt.Errorf("the permission service answered %s; reload Setup to try again", result.Outcome)
	}
	return nil
}

// setupInstallGrants writes ComfyUI's approved picture permissions for the
// installed, receipt-verified ComfyUI program, once.
func (p *servicePanel) setupInstallGrants(w http.ResponseWriter, r *http.Request) {
	p.setupActionMu.Lock()
	defer p.setupActionMu.Unlock()
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	step, ok := decodeInstallStep(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Minute)
	defer cancel()
	journal, record, err := p.approvedInstall(ctx, step.SetupPlanID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if len(record.Rules) == 0 || setup.RulesDigest(record.Rules) != record.RulesDigest {
		http.Error(w, "this installation approved no app permissions", http.StatusConflict)
		return
	}
	// install.json is a file any program of this account can edit, digest
	// included. The operator side rebuilds its own ceiling and writes only
	// approved rules inside it, so a newer Panel can only narrow an approval.
	ceiling, err := picturePermissionRules()
	if err != nil {
		panelError(w, err)
		return
	}
	for _, rule := range record.Rules {
		if !slices.Contains(ceiling, rule) {
			http.Error(w, "the recorded permissions reach beyond what ComfyUI's pictures need; review and install again", http.StatusConflict)
			return
		}
	}
	if !record.Granted {
		program, err := comfyProgram(p, ctx, journal)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		// Exactly the rules the Install click approved, whatever this Panel
		// version would compute now.
		if err := grantWriter(ctx, program, record.Rules); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if record, err = journal.MarkInstall(ctx, step.SetupPlanID, func(r *setup.InstallRecord) { r.Granted = true }); err != nil {
			panelError(w, err)
			return
		}
	}
	panelJSON(w, record)
}

// setupInstallOpen opens ComfyUI with the picture recipe, once per approval.
func (p *servicePanel) setupInstallOpen(w http.ResponseWriter, r *http.Request) {
	p.setupActionMu.Lock()
	defer p.setupActionMu.Unlock()
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	step, ok := decodeInstallStep(w, r)
	if !ok {
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	journal, record, err := p.approvedInstall(ctx, step.SetupPlanID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if !record.Opened {
		openComfyPage(comfyRecipeURL)
		if record, err = journal.MarkInstall(ctx, step.SetupPlanID, func(r *setup.InstallRecord) { r.Opened = true }); err != nil {
			panelError(w, err)
			return
		}
	}
	panelJSON(w, record)
}
