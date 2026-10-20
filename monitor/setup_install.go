package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
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
// permissions step. Native application startup is requested from Apps.

// setupPermission is one app's permissions, as the screen states them. ID is
// the digest of the exact rules; the Install click sends it back, and the
// approval records those rules.
type setupPermission struct {
	App       setup.AppID `json:"app"`
	Title     string      `json:"title"`
	Sentences []string    `json:"sentences"`
	ID        string      `json:"id"`
}

// picturePermissionRulesFor is the same rule set on another picture engine's
// host (setup.PictureHost), for the setup step's engine on this computer.
func picturePermissionRulesFor(host string) ([]setup.ApprovedRule, error) {
	if !isPictureEngineHost(host) {
		return nil, fmt.Errorf("%q is not a picture engine host", host)
	}
	rules, err := grants.FeatureRules(grants.Picture, grants.For{Hosts: []string{host}})
	if err != nil {
		return nil, err
	}
	approved := make([]setup.ApprovedRule, 0, len(rules))
	for _, rule := range rules {
		approved = append(approved, setup.ApprovedRule{Action: rule.Action, Resource: rule.Resource})
	}
	return approved, nil
}

// isPictureEngineHost is whether host is a picture engine choice's provider
// (setup.PictureHosts).
func isPictureEngineHost(host string) bool {
	return host != "" && slices.Contains(setup.PictureHosts(), host)
}

// pictureEngineResource is whether resource names a picture engine's host.
func pictureEngineResource(resource string) bool {
	return slices.ContainsFunc(setup.PictureHosts(), func(host string) bool { return resource == inference.ResourceHost(host) })
}

// permissionUse is the feature a rule serves: it words the rules whose
// sentence depends on the app's use of them.
type permissionUse int

const (
	// usePictures is ComfyUI making pictures through the local engine
	// (grants.Picture).
	usePictures permissionUse = iota
	// useChat is an app the setup step serves for code chatting with the
	// coding model Setup included (setup.ChatHost).
	useChat
)

// permissionSentence words one rule for an approval: the one place a rule
// becomes page text, so no page shows a rule ID. A rule with no sentence is
// an error: nothing is granted that the screen did not state.
func permissionSentence(rule setup.ApprovedRule, use permissionUse) (string, error) {
	switch {
	case use == usePictures && rule.Action == inference.ActionComplete && pictureEngineResource(rule.Resource):
		return asks.SetupPermissionWords("make_pictures"), nil
	case use == usePictures && rule.Action == grants.ContentWriteAction && rule.Resource == "output:image":
		return asks.SetupPermissionWords("write_pictures"), nil
	case use == usePictures && rule.Action == grants.ContentReadAction && rule.Resource == "output:image":
		return asks.SetupPermissionWords("read_pictures"), nil
	case use == useChat && rule.Action == inference.ActionComplete && rule.Resource == inference.ResourceHost(setup.ChatHost):
		return "Chat with the coding model Setup set up on this computer.", nil
	}
	return "", fmt.Errorf("no sentence states %s on %s", rule.Action, rule.Resource)
}

// setupPictureRules rebuilds the picture permission ceiling from the same
// compiled choice and engine that the approval screen presents.
func setupPictureRules(plan setup.Plan) ([]setup.ApprovedRule, error) {
	_, rules, err := picturePermissionSelection(setup.NewCatalog(), plan)
	return rules, err
}

func picturePermissionSelection(catalog setup.Catalog, plan setup.Plan) (setup.Choice, []setup.ApprovedRule, error) {
	choice, engine, ok := catalog.PicturePlanChoice(plan)
	if !ok {
		return setup.Choice{}, nil, nil
	}
	if !choice.Application || choice.ID == "" || choice.Name() == "" {
		return setup.Choice{}, nil, errors.New("the picture permissions have no Catalog application to review")
	}
	rules, err := picturePermissionRulesFor(setup.PictureHost(engine))
	return choice, rules, err
}

// setupPictureOffer is one picture model toggle of a plan review: the
// reviewed plan with its sizes and licences, whether it is the engine's
// recommended model, any application it requires, preparation state and
// whether this review selects it.
type setupPictureOffer struct {
	setup.ModelPlan
	Recommended         bool        `json:"recommended"`
	RequiredApplication setup.AppID `json:"required_application,omitempty"`
	Prepared            bool        `json:"prepared"`
	Selected            bool        `json:"selected"`
	// Caption is the toggle's one caption: what the model is for, or the
	// trade it makes against the recommended one.
	Caption string `json:"caption"`
	// Archived is whether the download archive holds every file, so
	// preparing the model copies from it.
	Archived bool `json:"archived,omitempty"`
}

// setupPictureSelection is the picture models a plan's Install prepares, as
// separate toggles (setup.PictureModelOffers of the engine the plan runs
// pictures on). requested is the review's picture_model_plan_ids; nil is
// the default: the recommended model while the engine has no prepared
// model, the owner's rule that an app with no model arrives with its
// recommended model ticked. A prepared model is never selected again. The
// starter is the selected recommended plan, which the Install record keeps
// as its model plan; others are the other selected plans. An ID no offer
// has is refused. A model journal Setup cannot read selects nothing; the
// engine's own model controller reports it.
func setupPictureSelection(ctx context.Context, journal setup.Journal, plan setup.Plan, requested []string) (starter *setup.ModelPlan, others []string, offers []setupPictureOffer, err error) {
	engine, _ := setup.PictureEngineOf(plan)
	return setupPictureOffers(ctx, journal, engine, requested)
}

// setupPictureOffers is setupPictureSelection for engine's offers.
func setupPictureOffers(ctx context.Context, journal setup.Journal, engine setup.AppID, requested []string) (starter *setup.ModelPlan, others []string, offers []setupPictureOffer, err error) {
	all := setup.PictureModelOffers(engine)
	for _, id := range requested {
		if id == "" || !slices.ContainsFunc(all, func(offer setup.PictureOffer) bool { return offer.Plan.ID == id }) {
			return nil, nil, nil, errors.New("the reviewed picture models changed; review them again")
		}
	}
	hasModel := false
	for _, offer := range all {
		prepared, err := setup.PictureModelConfigured(ctx, journal, offer.Plan)
		if err != nil {
			log.Printf("setup: %s picture model journal: %v", engine, err)
			return nil, nil, nil, nil
		}
		hasModel = hasModel || prepared
		var requiredApplication setup.AppID
		if offer.Plan.StagesPictureFilesOnly() {
			requiredApplication = officialPictureCatalogID()
		}
		offers = append(offers, setupPictureOffer{
			ModelPlan: offer.Plan, Recommended: offer.Recommended, RequiredApplication: requiredApplication,
			Prepared: prepared, Caption: offer.ReviewCaption(),
		})
	}
	for i := range offers {
		row := &offers[i]
		if requested == nil {
			row.Selected = row.Recommended && !hasModel
		} else {
			row.Selected = slices.Contains(requested, row.ID) && !row.Prepared
		}
		if row.Selected && row.Recommended {
			model := row.ModelPlan
			starter = &model
		} else if row.Selected {
			others = append(others, row.ID)
		}
	}
	return starter, others, offers, nil
}

// installPictureChoice is the selected picture application from the Catalog.
func installPictureChoice(catalog setup.Catalog, plan setup.Plan) (setup.Choice, bool) {
	for _, choice := range catalog.StepChoices() {
		if choice.Use == setup.UseImage && len(choice.Engines) > 0 && slices.Contains(plan.Selected, choice.ID) {
			return choice, true
		}
	}
	return setup.Choice{}, false
}

// setupPermissions lists the permissions a plan's apps get at Install: the
// picture app's rules on its engine. The coding model's chat permission is
// the setup step's (setup_step_chat.go): this page's coding-model plan
// selects no app, so nothing here could name a program for it.
func setupPermissions(plan setup.Plan) ([]setupPermission, error) {
	return setupPermissionsForCatalog(setup.NewCatalog(), plan)
}

func setupPermissionsForCatalog(catalog setup.Catalog, plan setup.Plan) ([]setupPermission, error) {
	choice, rules, err := picturePermissionSelection(catalog, plan)
	if err != nil || len(rules) == 0 {
		return nil, err
	}
	permission := setupPermission{App: choice.ID, Title: choice.Name(), ID: setup.RulesDigest(rules)}
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
	// ModelEngine is the engine the approved picture model is prepared in,
	// so the page drives that engine's model steps.
	ModelEngine setup.AppID `json:"model_engine,omitempty"`
	// Workspace is comfyWorkspaceState for the record's plan: an install
	// whose ComfyUI is not in Apps or the Start menu has not finished.
	Workspace string `json:"workspace,omitempty"`
	// WorkspaceNote is the sentence for Workspace "other": another
	// program's entry holds ComfyUI's name and stays.
	WorkspaceNote string `json:"workspace_note,omitempty"`
}

func newSetupInstallReply(state setupPanelStatus, record setup.InstallRecord) setupInstallReply {
	engine := setup.StarterPictureEngine(record.ModelPlanID)
	if engine == "" && len(record.PictureModelPlanIDs) > 0 {
		engine, _ = setup.PictureEngineOf(state.Plan)
	}
	return setupInstallReply{setupPanelStatus: state, Install: record, ModelEngine: engine}
}

// setupInstall approves the reviewed plan, its picture model and its
// permissions with one click, then records the approval for the steps.
func (p *servicePanel) setupInstall(w http.ResponseWriter, r *http.Request) {
	if !p.setupTryStart(w) {
		return
	}
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
	if len(request.Selected) == 0 {
		review, reviewErr := codingOnlyReview(request.CodingModelPlanID)
		if reviewErr != nil || request.PlanID != review.ID || request.ModelPlanID != "" || len(request.PictureModelPlanIDs) != 0 || request.PermissionsID != "" || len(request.Beside) != 0 {
			http.Error(w, "the reviewed model-only installation changed; review it again", http.StatusConflict)
			return
		}
		ctx, cancel := panelCall(r)
		defer cancel()
		record, saveErr := journal.ApproveInstall(ctx, review.ID, "", nil, time.Now(), request.CodingModelPlanID)
		if saveErr != nil {
			panelError(w, saveErr)
			return
		}
		if startErr := p.startInstallCoding(ctx, record); startErr != nil {
			http.Error(w, "Coding download acceptance is pending: "+startErr.Error()+". Reopen Setup to reconcile the saved approval.", http.StatusConflict)
			return
		}
		panelJSON(w, codingOnlyInstallReply(review, record))
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
	if refuseOtherCopies(w, request, otherCopiesNow()) {
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	wantModel := ""
	starter, pictures, _, err := setupPictureSelection(ctx, journal, plan, request.PictureModelPlanIDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if starter != nil {
		wantModel = starter.ID
	}
	if request.ModelPlanID != wantModel {
		http.Error(w, "the reviewed picture model changed; review it again", http.StatusConflict)
		return
	}
	if request.CodingModelPlanID != "" && request.CodingModelPlanID != setup.CodingModelPlan().ID {
		http.Error(w, "the reviewed coding model changed; review it again", http.StatusConflict)
		return
	}
	rules, err := setupPictureRules(plan)
	if err != nil {
		panelError(w, err)
		return
	}
	if request.PermissionsID != setup.RulesDigest(rules) {
		http.Error(w, "the permissions on the screen changed; review them again", http.StatusConflict)
		return
	}
	status, err := runner.Start(ctx, request.Selected)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if status.Plan.ID != plan.ID {
		http.Error(w, "setup catalog changed; review it again", http.StatusConflict)
		return
	}
	record, err := journal.ApproveInstallRecord(ctx, setup.InstallRecord{PlanID: plan.ID, ModelPlanID: wantModel, PictureModelPlanIDs: pictures,
		CodingModelPlanID: request.CodingModelPlanID, Rules: rules}, time.Now())
	if err != nil {
		panelError(w, err)
		return
	}
	if record.CodingModelPlanID != "" {
		if err := p.startInstallCoding(ctx, record); err != nil {
			http.Error(w, "Coding download acceptance is pending: "+err.Error()+". Reopen Setup to reconcile the saved approval.", http.StatusConflict)
			return
		}
	}
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, newSetupInstallReply(state, record))
}

func codingOnlyInstallReply(review setupPlanReview, record setup.InstallRecord) setupInstallReply {
	return setupInstallReply{setupPanelStatus: setupPanelStatus{Status: setup.Status{Plan: review.Plan, CurrentCatalog: true}, Approved: true}, Install: record}
}

func currentCodingOnlyRecord(record setup.InstallRecord) (setupPlanReview, error) {
	review, err := codingOnlyReview(record.CodingModelPlanID)
	if err != nil || record.PlanID != review.ID || record.ModelPlanID != "" || len(record.Rules) != 0 || record.RulesDigest != "" {
		return setupPlanReview{}, errors.New("the approved coding model changed; review and install again")
	}
	return review, nil
}

// startInstallCoding reuses the coding controller's durable public work. The
// install record is written first, so a lost reply can reopen the same choice.
func (p *servicePanel) startInstallCoding(ctx context.Context, record setup.InstallRecord) error {
	plan := setup.CodingModelPlan()
	if record.CodingModelPlanID == "" || record.CodingModelPlanID != plan.ID {
		return errors.New("the installation has no current coding-model approval")
	}
	c, err := p.codingModelController()
	if err != nil {
		return err
	}
	_, err = c.StartAccepted(ctx, setup.ModelApproval{PlanID: plan.ID, DownloadBytes: plan.DownloadBytes, PeakBytes: plan.PeakBytes, Licenses: plan.Licenses})
	return err
}

// setupInstallState reports the approval record beside the plan state, so
// the page shows an unfinished install as stopped after a restart. A record
// another Panel build approved is marked interrupted by the update; nothing
// starts until the person presses Install (/setup/install/continue).
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
	record, err := journal.NoteInstallUpdate(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	p.writeInstallState(w, ctx, runner, journal, record)
}

// setupInstallContinue is the person's Install on a stopped install: the
// same approval continues from each step's journal, with no new approval
// and no second download of a verified file.
func (p *servicePanel) setupInstallContinue(w http.ResponseWriter, r *http.Request) {
	if !p.setupTryStart(w) {
		return
	}
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
	journal, _, err := p.approvedInstall(ctx, step.SetupPlanID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	record, err := journal.ContinueInstall(ctx, step.SetupPlanID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	p.writeInstallState(w, ctx, runner, journal, record)
}

// setupInstallFailed keeps a refused step's component and reason on the
// install record, so the page names them again after a reload.
func (p *servicePanel) setupInstallFailed(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var request struct {
		SetupPlanID string `json:"setup_plan_id"`
		setup.InstallFailure
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&request); err != nil || request.SetupPlanID == "" {
		http.Error(w, "the approved setup plan is required", http.StatusBadRequest)
		return
	}
	_, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	record, err := journal.RecordInstallFailure(ctx, request.SetupPlanID, request.InstallFailure)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	panelJSON(w, record)
}

// writeInstallState answers record beside the plan state.
func (p *servicePanel) writeInstallState(w http.ResponseWriter, ctx context.Context, runner setup.Runner, journal setup.Journal, record setup.InstallRecord) {
	if strings.HasPrefix(record.PlanID, "coding-model-") {
		review, recordErr := currentCodingOnlyRecord(record)
		if recordErr != nil {
			http.Error(w, recordErr.Error(), http.StatusConflict)
			return
		}
		panelJSON(w, codingOnlyInstallReply(review, record))
		return
	}
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	if record.PlanID == "" || record.PlanID != state.Plan.ID {
		record = setup.InstallRecord{}
	} else if !state.Approved {
		// An approval of an earlier release's plan (almost every update
		// changes the catalogue) stays readable: the page shows it stopped,
		// and Install plans the same choices against this release.
		prior, err := journal.Load(ctx)
		if err != nil {
			panelError(w, err)
			return
		}
		if state.CurrentCatalog || prior.ApprovedPlanID != record.PlanID {
			record = setup.InstallRecord{}
		}
	}
	state.Actions, state.Outcomes = p.setupActionsNow(), p.setupOutcomesNow()
	reply := newSetupInstallReply(state, record)
	// A plan a removal approved has no install record of its own; its
	// ComfyUI is read the same way. A release that installs no ComfyUI
	// adds none to Apps: the OA build an earlier plan installed is only
	// removed (setupRemoveOABuild).
	if state.Plan.ID != "" {
		catalog := journal.Catalog()
		if choice, ok := installPictureChoice(catalog, state.Plan); ok && catalog.Available(choice.ID) {
			reply.Workspace, reply.WorkspaceNote = officialPicture.workspaceState(p, ctx, journal, state.Plan)
		}
	}
	panelJSON(w, reply)
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
	record, err := journal.LoadInstall(ctx)
	if err != nil {
		return journal, record, err
	}
	if strings.HasPrefix(record.PlanID, "coding-model-") {
		_, err = currentCodingOnlyRecord(record)
		if err == nil && record.PlanID != planID {
			err = errors.New("the approved installation changed; review and install again")
		}
		return journal, record, err
	}
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		return journal, setup.InstallRecord{}, err
	}
	if !state.Approved || state.Plan.ID != planID || record.PlanID != planID {
		return journal, setup.InstallRecord{}, errors.New("the approved installation changed; review and install again")
	}
	return journal, record, nil
}

// comfyProgram is the receipt-verified program the permissions name:
// the installed paths' Program, the program OpenAbstractions records for the
// application's calls and the one its registration names, which the setup
// step's picture rules (setup_step_pictures.go) also name. Tests replace it.
var comfyProgram = func(p *servicePanel, ctx context.Context, journal setup.Journal) (string, error) {
	paths, err := p.openInstalledComfy(ctx, journal)
	if err != nil {
		return "", err
	}
	return paths.Program(), nil
}

// grantWriter writes the approved rules for one program. Tests replace it.
var grantWriter = func(ctx context.Context, program string, approved []setup.ApprovedRule) error {
	caller, err := panelMachine().ObserveCaller(ctx)
	if err != nil || caller.Outcome.String() != "observed" || caller.Account == "" {
		return errors.New("the Panel's own account could not be verified")
	}
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	_, err = writeApprovedPermissions(ctx, operator, rightsclient.Subject{Account: caller.Account, Program: program}, approved)
	return err
}

// approvedInSetup is the reason the picture permissions Setup writes
// record.
const approvedInSetup = "approved in Catalog"

// writeApprovedPermissions writes the permissions the person approved in
// Setup as product grants (grants.WriteProduct): a rule the person already
// holds for the same program, action and resource stays as they set it.
func writeApprovedPermissions(ctx context.Context, operator grants.ProductOperator, subject rightsclient.Subject, approved []setup.ApprovedRule) (grants.ProductResult, error) {
	rules := make([]grants.Rule, 0, len(approved))
	for _, rule := range approved {
		rules = append(rules, grants.Rule{Action: rule.Action, Resource: rule.Resource})
	}
	return grants.WriteProduct(ctx, operator, subject, rules, 0, approvedInSetup)
}

// setupInstallGrants writes ComfyUI's approved picture permissions for the
// installed, receipt-verified ComfyUI program, once.
func (p *servicePanel) setupInstallGrants(w http.ResponseWriter, r *http.Request) {
	if !p.setupTryStart(w) {
		return
	}
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
	state, err := journal.Load(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	ceiling, err := setupPictureRules(state.Plan)
	if err != nil {
		panelError(w, err)
		return
	}
	for _, rule := range record.Rules {
		if !slices.Contains(ceiling, rule) {
			http.Error(w, "the recorded permissions reach beyond what "+officialPictureAppTitle+"'s pictures need; review and install again", http.StatusConflict)
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

// setupInstallOpen retains a finite refusal for the retired recipe URL action.
func (p *servicePanel) setupInstallOpen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	http.Error(w, "Use the registered application on Apps to request startup", http.StatusGone)
}
