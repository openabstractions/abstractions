package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"slices"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type setupChoiceRequest struct {
	Selected []setup.AppID `json:"selected"`
	// Beside names selected apps the person chose to install although another
	// copy without the OA mark is installed (setup_other_copies.go).
	Beside []setup.AppID `json:"beside,omitempty"`
	// ModelPlanID is the picture-model plan an Install click approves with
	// the app plan (setup_install.go).
	ModelPlanID string `json:"model_plan_id,omitempty"`
	// PermissionsID is the digest of the permission rules the screen
	// stated (setupPermission.ID).
	PermissionsID string      `json:"permissions_id,omitempty"`
	PlanID        string      `json:"plan_id"`
	Confirm       bool        `json:"confirm"`
	ComponentID   string      `json:"component_id"`
	Engine        setup.AppID `json:"engine,omitempty"`
}

type setupPanelStatus struct {
	setup.Status
	Approved bool `json:"approved"`
}

type setupPlanReview struct {
	setup.Plan
	PictureModel *setup.ModelPlan `json:"picture_model,omitempty"`
	// Replaces names each installed version this plan would update, so the
	// consent text can say so before approval.
	Replaces []setupReplacement `json:"replaces,omitempty"`
	// OtherCopies are installations without the OA mark that Setup leaves
	// as they are.
	OtherCopies []setup.OtherCopy `json:"other_copies,omitempty"`
	// Permissions are the permissions the plan's apps get at Install, as
	// sentences (setup_install.go).
	Permissions []setupPermission `json:"permissions,omitempty"`
}

type setupReplacement struct {
	ComponentID string `json:"component_id"`
	Title       string `json:"title"`
	From        string `json:"from"`
	To          string `json:"to"`
}

// setupReplacements compares a reviewed plan with the versions in use.
func setupReplacements(plan setup.Plan, installed map[string]setup.Artifact) []setupReplacement {
	var replaces []setupReplacement
	for _, component := range plan.Components {
		old, ok := installed[component.ID]
		if !ok || component.Artifact == nil || old.SHA256 == component.Artifact.SHA256 {
			continue
		}
		replaces = append(replaces, setupReplacement{ComponentID: component.ID, Title: component.Title, From: old.Version, To: component.Artifact.Version})
	}
	return replaces
}

type setupModelController interface {
	DiskPreflight() (setup.ModelDiskStatus, error)
	Status(context.Context) (setup.ModelStatus, error)
	Start(context.Context, setup.ModelApproval) (setup.ModelStatus, error)
	AdvanceWithProgress(context.Context, string, func(int64, int64) error) (setup.ModelStatus, error)
	Retry(context.Context, string) (setup.ModelStatus, error)
	Configure(context.Context, bool, func(int64, int64) error) (setup.ModelStatus, error)
	ReconcileProvider(context.Context) (setup.ModelStatus, error)
}

type modelProgress struct {
	Done  int64 `json:"done"`
	Total int64 `json:"total"`
}

type setupModelReply struct {
	EngineInstalled bool            `json:"engine_installed,omitempty"`
	Engine          setup.AppID     `json:"engine,omitempty"`
	Enabled         bool            `json:"enabled"`
	Available       bool            `json:"available"`
	Reason          string          `json:"reason,omitempty"`
	SetupPlanID     string          `json:"setup_plan_id,omitempty"`
	Plan            setup.ModelPlan `json:"plan"`
	// Choices are the other picture models the provider declares, shown
	// for review with their sizes and licences; none can be approved here.
	Choices []setup.ModelPlan `json:"choices,omitempty"`
	// ChoiceStatus is each offered plan's progress, keyed by plan ID.
	ChoiceStatus map[string]setup.ModelStatus `json:"choice_status,omitempty"`
	Disk         setup.ModelDiskStatus        `json:"disk"`
	Feature      setup.FeatureStatus          `json:"feature"`
	Status       setup.ModelStatus            `json:"status"`
	Progress     map[string]modelProgress     `json:"progress,omitempty"`
}

type setupModelRequest struct {
	Engine      setup.AppID `json:"engine,omitempty"`
	PlanID      string      `json:"plan_id"`
	SetupPlanID string      `json:"setup_plan_id"`
	AssetID     string      `json:"asset_id"`
	Confirm     bool        `json:"confirm"`
}

func (p *servicePanel) setupJournalForPanel() (setup.Journal, error) {
	if p.setupJournal != nil {
		return *p.setupJournal, nil
	}
	return setup.DefaultJournal()
}

func (p *servicePanel) setupRunnerForPanel() (setup.Runner, setup.Journal, error) {
	journal, err := p.setupJournalForPanel()
	if err != nil {
		return setup.Runner{}, setup.Journal{}, err
	}
	runner := setup.NewRunner(journal, nil)
	runner.RetireGuard = p.comfyRetireGuard
	return runner, journal, nil
}

func (p *servicePanel) setupModelForPanel() (setupModelController, error) {
	p.modelControllerMu.Lock()
	defer p.modelControllerMu.Unlock()
	if p.modelController != nil {
		return p.modelController, nil
	}
	controller, err := setup.DefaultBF16Controller(p.selectedComfyImagePolicy)
	if err != nil {
		return nil, err
	}
	p.modelController = controller
	return controller, nil
}

func fixedSetupEngine(engine setup.AppID) (setup.AppID, error) {
	if engine == "" {
		engine = setup.Lemonade
	}
	if engine != setup.Lemonade && engine != setup.TheNoise && engine != setup.PyTorch {
		return "", errors.New("unknown fixed picture engine")
	}
	return engine, nil
}

func (p *servicePanel) setupModelForEngine(engine setup.AppID) (setupModelController, setup.ModelPlan, error) {
	if engine == setup.Lemonade {
		controller, err := p.setupModelForPanel()
		return controller, setup.ApprovedBF16Plan(), err
	}
	plan := setup.TheNoiseModelPlan()
	if engine == setup.PyTorch {
		plan = setup.PyTorchModelPlan()
	}
	p.modelControllerMu.Lock()
	defer p.modelControllerMu.Unlock()
	if controller := p.engineControllers[engine]; controller != nil {
		return controller, plan, nil
	}
	journal, err := p.setupJournalForPanel()
	if err != nil {
		return nil, setup.ModelPlan{}, err
	}
	var controller setup.ModelController
	if engine == setup.PyTorch {
		controller, err = setup.DefaultPyTorchController(journal)
	} else {
		controller, err = setup.DefaultTheNoiseController(journal)
	}
	if err != nil {
		return nil, setup.ModelPlan{}, err
	}
	if p.engineControllers == nil {
		p.engineControllers = map[setup.AppID]setupModelController{}
	}
	p.engineControllers[engine] = controller
	return controller, plan, nil
}

func engineInPlan(plan setup.Plan, engine setup.AppID) bool {
	return slices.Contains(plan.Selected, engine) || engine == setup.Lemonade && slices.Contains(plan.Selected, setup.ComfyUI)
}

func installedSetupEngine(status setupPanelStatus, engine setup.AppID) bool {
	for _, component := range status.Components {
		if component.ID == string(engine)+"-engine" {
			if engine == setup.PyTorch {
				return slices.Contains([]setup.Phase{setup.Staged, setup.Checked, setup.Installed}, component.Effect.Phase) && component.Effect.ComponentRootKey != ""
			}
			return component.Effect.Phase == setup.Installed && !component.Effect.Removing
		}
	}
	return false
}

func (p *servicePanel) setupRemoveEngine(w http.ResponseWriter, r *http.Request) {
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
	if request.Engine != setup.TheNoise && request.Engine != setup.PyTorch || !request.Confirm {
		http.Error(w, "explicit confirmation of the fixed engine removal is required", http.StatusBadRequest)
		return
	}
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Minute)
	defer cancel()
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	if request.PlanID == "" || state.Plan.ID != request.PlanID || !state.Approved || !engineInPlan(state.Plan, request.Engine) {
		http.Error(w, "the approved engine plan changed; review it again", http.StatusConflict)
		return
	}
	if request.Engine == setup.PyTorch {
		err = runner.RemovePyTorch(ctx)
	} else {
		err = runner.RemoveTheNoise(ctx)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	state, err = setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, state)
}

func (p *servicePanel) setupEngineFeatureState(engine setup.AppID) (setup.FeatureStatus, error) {
	if p.setupFeatureCheck != nil {
		return p.setupFeatureCheck()
	}
	if engine == setup.Lemonade {
		return p.setupFeatureState()
	}
	return setup.EngineFeatureStatus(engine)
}

func (p *servicePanel) openInstalledComfy(ctx context.Context, journal setup.Journal) (setup.ComfyPaths, error) {
	p.comfyPathsMu.Lock()
	defer p.comfyPathsMu.Unlock()
	// Explicit actions must reopen the pinned receipt. Cached paths are only a
	// display hint and cannot establish that the files are still intact.
	paths, err := setup.OpenInstalledComfy(ctx, journal)
	if err != nil {
		return setup.ComfyPaths{}, err
	}
	p.comfyPaths = &paths
	return paths, nil
}

func (p *servicePanel) setupFeatureState() (setup.FeatureStatus, error) {
	if p.setupFeatureCheck != nil {
		return p.setupFeatureCheck()
	}
	return setup.LemonadeFeatureStatus()
}

func (p *servicePanel) modelProgressSnapshot() map[string]modelProgress {
	p.modelProgressMu.Lock()
	defer p.modelProgressMu.Unlock()
	result := make(map[string]modelProgress, len(p.modelProgress))
	for key, value := range p.modelProgress {
		result[key] = value
	}
	return result
}

func (p *servicePanel) setModelProgress(key string, done, total int64) {
	p.modelProgressMu.Lock()
	defer p.modelProgressMu.Unlock()
	if p.modelProgress == nil {
		p.modelProgress = make(map[string]modelProgress)
	}
	p.modelProgress[key] = modelProgress{Done: done, Total: total}
}

func (p *servicePanel) clearModelProgress(key string) {
	p.modelProgressMu.Lock()
	defer p.modelProgressMu.Unlock()
	delete(p.modelProgress, key)
}

func setupStatus(ctx context.Context, runner setup.Runner, journal setup.Journal) (setupPanelStatus, error) {
	status, err := runner.Status(ctx)
	if err != nil {
		return setupPanelStatus{}, err
	}
	state, err := journal.Load(ctx)
	if err != nil {
		return setupPanelStatus{}, err
	}
	return setupPanelStatus{Status: status, Approved: status.CurrentCatalog && state.Plan.ID != "" && state.ApprovedPlanID == state.Plan.ID}, nil
}

func decodeSetupChoice(w http.ResponseWriter, r *http.Request) (setupChoiceRequest, bool) {
	var request setupChoiceRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid setup choices", http.StatusBadRequest)
		return setupChoiceRequest{}, false
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		http.Error(w, "one setup choice request required", http.StatusBadRequest)
		return setupChoiceRequest{}, false
	}
	return request, true
}

func (p *servicePanel) setupPlan(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	request, ok := decodeSetupChoice(w, r)
	if !ok {
		return
	}
	journal, err := p.setupJournalForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	plan, err := journal.Catalog().Plan(request.Selected)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	others := otherCopiesNow()
	if refuseOtherCopies(w, request, others) {
		return
	}
	reply := setupPlanReview{Plan: plan, OtherCopies: others.Copies}
	ctx, cancel := panelCall(r)
	defer cancel()
	installed, err := journal.InstalledVersions(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	reply.Replaces = setupReplacements(plan, installed)
	if reply.Permissions, err = setupPermissions(plan.Selected); err != nil {
		panelError(w, err)
		return
	}
	if slices.Contains(plan.Selected, setup.ComfyUI) {
		model := setup.ApprovedBF16Plan()
		reply.PictureModel = &model
	}
	panelJSON(w, reply)
}

// setupIntent records approval for this exact catalog-backed plan. The Panel
// then advances safe actions under that plan-wide approval.
func (p *servicePanel) setupIntent(w http.ResponseWriter, r *http.Request) {
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
		http.Error(w, "explicit plan confirmation required", http.StatusBadRequest)
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
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, state)
}

func (p *servicePanel) setupState(w http.ResponseWriter, r *http.Request) {
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
	panelJSON(w, state)
}

func (p *servicePanel) setupComponentAction(w http.ResponseWriter, r *http.Request, retry, recovery bool) {
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
	if request.PlanID == "" || request.ComponentID == "" {
		http.Error(w, "reviewed plan and component are required", http.StatusBadRequest)
		return
	}
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	statusCtx, statusCancel := panelCall(r)
	current, err := setupStatus(statusCtx, runner, journal)
	statusCancel()
	if err != nil {
		panelError(w, err)
		return
	}
	if recovery && current.Plan.ID != "" && !current.CurrentCatalog {
		p.setupReconcilePrior(w, r, runner, journal, request, current)
		return
	}
	if current.Plan.ID == "" || current.Plan.ID != request.PlanID || !current.Approved {
		http.Error(w, "approved setup plan changed; review and approve it again", http.StatusConflict)
		return
	}
	if !current.Plan.Preparable {
		http.Error(w, "reviewed packages are not available yet", http.StatusConflict)
		return
	}
	var effect setup.Effect
	found := false
	for _, component := range current.Components {
		if component.ID == request.ComponentID {
			effect = component.Effect
			found = true
			break
		}
	}
	if !found {
		http.Error(w, "component is not part of the approved plan", http.StatusBadRequest)
		return
	}
	phase := effect.Phase
	if !setupEffectAllowsAction(effect, retry, recovery) {
		if retry {
			http.Error(w, "retry is available only for a failed transfer or an uncertain install", http.StatusConflict)
		} else if recovery {
			http.Error(w, "recovery is available only for an interrupted installation", http.StatusConflict)
		} else {
			http.Error(w, "this setup step is already active or complete", http.StatusConflict)
		}
		return
	}
	ctx, cancel := setupActionCall(r, phase, recovery)
	defer cancel()
	if retry {
		_, err = runner.Retry(ctx, request.ComponentID)
	} else {
		_, err = runner.Advance(ctx, request.ComponentID)
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, state)
}

func setupPhaseAllowsAction(phase setup.Phase, retry, recovery bool) bool {
	if retry {
		return phase == setup.Failed || phase == setup.Uncertain
	}
	if recovery {
		return phase == setup.Installing
	}
	// Staged and Checked are the health check and activation of an update.
	return phase == setup.Pending || phase == setup.Requested || phase == setup.Verified ||
		phase == setup.Staged || phase == setup.Checked
}

// setupEffectAllowsAction also lets Advance remove versions an update
// retired once the component is installed or its staged version failed.
func setupEffectAllowsAction(effect setup.Effect, retry, recovery bool) bool {
	if !retry && !recovery && len(effect.Retired) > 0 && (effect.Phase == setup.Installed || effect.Phase == setup.Failed) {
		return true
	}
	return setupPhaseAllowsAction(effect.Phase, retry, recovery)
}

// setupReconcilePrior settles one unfinished step of an approved plan from an
// earlier release. It is the explicit recovery action on such a plan; nothing
// of that plan resumes.
func (p *servicePanel) setupReconcilePrior(w http.ResponseWriter, r *http.Request, runner setup.Runner, journal setup.Journal, request setupChoiceRequest, current setupPanelStatus) {
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Minute)
	defer cancel()
	state, err := journal.Load(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	if current.Plan.ID != request.PlanID || state.ApprovedPlanID != state.Plan.ID || state.Plan.ID != request.PlanID {
		http.Error(w, "the earlier setup plan changed; reload before continuing", http.StatusConflict)
		return
	}
	if _, err := runner.ReconcilePrior(ctx, request.ComponentID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	reply, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, reply)
}

// comfyRetireGuard keeps an earlier ComfyUI version while the registered
// comfyui entry starts it, or while the Panel cannot read which program the
// entry starts.
func (p *servicePanel) comfyRetireGuard(ctx context.Context, componentID, root string) error {
	if componentID != "comfyui-app" {
		return nil
	}
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		return err
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return err
	}
	registered, found, err := findComfyDescriptor(page)
	if err != nil {
		return err
	}
	if !found {
		p.comfyAppsMu.Lock()
		mine := p.comfyRegisteredDescriptor
		p.comfyAppsMu.Unlock()
		if mine == nil {
			return errors.New("the earlier ComfyUI version is kept until the Panel can read which program the comfyui entry starts")
		}
		registered = *mine
	}
	if setup.ProgramWithin(root, registered.Program) {
		return errors.New("the registered comfyui entry still starts the earlier version; register the updated ComfyUI first")
	}
	return nil
}

func (p *servicePanel) setupAdvance(w http.ResponseWriter, r *http.Request) {
	p.setupComponentAction(w, r, false, false)
}

func (p *servicePanel) setupRetry(w http.ResponseWriter, r *http.Request) {
	p.setupComponentAction(w, r, true, false)
}

func (p *servicePanel) setupRecover(w http.ResponseWriter, r *http.Request) {
	p.setupComponentAction(w, r, false, true)
}

func (p *servicePanel) setupFeatureStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	engine, err := fixedSetupEngine(setup.AppID(r.URL.Query().Get("engine")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	feature, err := p.setupEngineFeatureState(engine)
	if err != nil {
		panelError(w, err)
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
	panelJSON(w, struct {
		setup.FeatureStatus
		PlanID   string `json:"plan_id"`
		Approved bool   `json:"approved"`
	}{FeatureStatus: feature, PlanID: state.Plan.ID, Approved: state.Approved})
}

func (p *servicePanel) setupAddFeature(w http.ResponseWriter, r *http.Request) {
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
	if !request.Confirm || request.PlanID == "" {
		http.Error(w, "explicit approved plan confirmation required", http.StatusBadRequest)
		return
	}
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	checkCtx, checkCancel := panelCall(r)
	current, err := setupStatus(checkCtx, runner, journal)
	checkCancel()
	if err != nil {
		panelError(w, err)
		return
	}
	if current.Plan.ID == "" || current.Plan.ID != request.PlanID || !current.Approved {
		http.Error(w, "approved setup plan changed; review and approve it again", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 45*time.Minute)
	defer cancel()
	engine, err := fixedSetupEngine(request.Engine)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	feature, err := runner.AddEngineFeature(ctx, engine)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	panelJSON(w, struct {
		setup.FeatureStatus
		PlanID   string `json:"plan_id"`
		Approved bool   `json:"approved"`
	}{FeatureStatus: feature, PlanID: current.Plan.ID, Approved: true})
}

func (p *servicePanel) setupModelStatus(w http.ResponseWriter, r *http.Request) {
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
	engine, err := fixedSetupEngine(setup.AppID(r.URL.Query().Get("engine")))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	selected := engineInPlan(state.Plan, engine)
	plan := setup.ApprovedBF16Plan()
	if engine == setup.TheNoise {
		plan = setup.TheNoiseModelPlan()
	} else if engine == setup.PyTorch {
		plan = setup.PyTorchModelPlan()
	}
	reply := setupModelReply{Engine: engine, EngineInstalled: installedSetupEngine(state, engine), Enabled: selected && state.Approved, SetupPlanID: state.Plan.ID, Plan: plan, Progress: p.modelProgressSnapshot()}
	if engine == setup.Lemonade {
		reply.Choices = setup.PictureModelChoices()
	}
	if !reply.Enabled {
		panelJSON(w, reply)
		return
	}
	reply.Feature, err = p.setupEngineFeatureState(engine)
	if err != nil {
		reply.Reason = err.Error()
		panelJSON(w, reply)
		return
	}
	controller, _, err := p.setupModelForEngine(engine)
	if err != nil {
		reply.Reason = err.Error()
		panelJSON(w, reply)
		return
	}
	if controller == nil {
		reply.Reason = "The trusted local model setup bridge is not available yet."
		panelJSON(w, reply)
		return
	}
	reply.Available = true
	reply.Disk, err = controller.DiskPreflight()
	if err != nil {
		reply.Available = false
		reply.Reason = err.Error()
		panelJSON(w, reply)
		return
	}
	reply.Status, err = controller.Status(ctx)
	if err != nil {
		reply.Available = false
		reply.Reason = err.Error()
		panelJSON(w, reply)
		return
	}
	if engine == setup.Lemonade {
		reply.ChoiceStatus = p.choiceStatuses(ctx)
	}
	panelJSON(w, reply)
}

func (p *servicePanel) setupModelAction(w http.ResponseWriter, r *http.Request, action string) {
	p.setupActionMu.Lock()
	defer p.setupActionMu.Unlock()
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var request setupModelRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil {
		http.Error(w, "invalid picture model setup request", http.StatusBadRequest)
		return
	}
	var trailing any
	if decoder.Decode(&trailing) != io.EOF {
		http.Error(w, "one picture model setup request required", http.StatusBadRequest)
		return
	}
	engine, err := fixedSetupEngine(request.Engine)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	starter, fixedPlan, err := p.setupModelForEngine(engine)
	if err != nil {
		panelError(w, err)
		return
	}
	if starter == nil {
		http.Error(w, "trusted picture model setup is not available", http.StatusServiceUnavailable)
		return
	}
	if engine != setup.Lemonade && request.PlanID != fixedPlan.ID {
		http.Error(w, "the reviewed model plan is bound to a different engine", http.StatusConflict)
		return
	}
	// An offered plan's request names its plan; the starter plan's may not.
	controller, plan, offered := starter, fixedPlan, request.PlanID != "" && request.PlanID != fixedPlan.ID
	if offered {
		if engine != setup.Lemonade {
			http.Error(w, "model plan is bound to a different fixed engine", http.StatusConflict)
			return
		}
		plan, controller, err = p.choiceControllerFor(request.PlanID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	checkCtx, checkCancel := panelCall(r)
	mainState, err := setupStatus(checkCtx, runner, journal)
	checkCancel()
	if err != nil {
		panelError(w, err)
		return
	}
	if mainState.Plan.ID == "" || mainState.Plan.ID != request.SetupPlanID || !mainState.Approved || !engineInPlan(mainState.Plan, engine) {
		http.Error(w, "the approved picture setup plan changed; review and approve it again", http.StatusConflict)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 3*time.Hour)
	defer cancel()
	var status setup.ModelStatus
	switch action {
	case "start":
		if !request.Confirm || request.PlanID != plan.ID {
			http.Error(w, "explicit confirmation of the current fixed model plan is required", http.StatusConflict)
			return
		}
		feature, featureErr := p.setupEngineFeatureState(engine)
		if featureErr != nil {
			panelError(w, featureErr)
			return
		}
		if !feature.Installed {
			http.Error(w, "add local picture support before preparing its model", http.StatusConflict)
			return
		}
		if offered {
			current, statusErr := starter.Status(ctx)
			if statusErr != nil || !current.ProviderConfigured {
				http.Error(w, "Prepare the starter picture model first.", http.StatusConflict)
				return
			}
		}
		status, err = controller.Start(ctx, setup.ModelApproval{PlanID: plan.ID, DownloadBytes: plan.DownloadBytes, PeakBytes: plan.PeakBytes, Licenses: slices.Clone(plan.Licenses)})
	case "advance":
		if !planAsset(plan, request.AssetID) {
			http.Error(w, "unknown fixed model asset", http.StatusBadRequest)
			return
		}
		status, err = controller.AdvanceWithProgress(ctx, request.AssetID, func(done, total int64) error { p.setModelProgress(request.AssetID, done, total); return ctx.Err() })
		p.clearModelProgress(request.AssetID)
	case "retry":
		if !planAsset(plan, request.AssetID) {
			http.Error(w, "unknown fixed model asset", http.StatusBadRequest)
			return
		}
		status, err = controller.Retry(ctx, request.AssetID)
	case "configure":
		current, statusErr := controller.Status(ctx)
		if statusErr != nil {
			http.Error(w, statusErr.Error(), http.StatusConflict)
			return
		}
		for _, asset := range current.Assets {
			if asset.Phase != setup.Verified {
				http.Error(w, "all reviewed picture model files must be verified before configuration", http.StatusConflict)
				return
			}
		}
		if offered || engine != setup.Lemonade || !slices.Contains(mainState.Plan.Selected, setup.ComfyUI) {
			status, err = controller.Configure(ctx, false, func(done, total int64) error { p.setModelProgress("provider", done, total); return ctx.Err() })
			p.clearModelProgress("provider")
			break
		}
		_, journal, journalErr := p.setupRunnerForPanel()
		if journalErr != nil {
			panelError(w, journalErr)
			return
		}
		paths, pathErr := p.openInstalledComfy(ctx, journal)
		if pathErr != nil {
			http.Error(w, pathErr.Error(), http.StatusConflict)
			return
		}
		if pathErr = p.ensureComfyImagePolicy(ctx, paths); pathErr != nil {
			http.Error(w, pathErr.Error(), http.StatusConflict)
			return
		}
		status, err = controller.Configure(ctx, false, func(done, total int64) error { p.setModelProgress("provider", done, total); return ctx.Err() })
		p.clearModelProgress("provider")
	case "reconcile":
		if offered || engine != setup.Lemonade || !slices.Contains(mainState.Plan.Selected, setup.ComfyUI) {
			status, err = controller.ReconcileProvider(ctx)
			break
		}
		_, journal, journalErr := p.setupRunnerForPanel()
		if journalErr != nil {
			panelError(w, journalErr)
			return
		}
		paths, pathErr := p.openInstalledComfy(ctx, journal)
		if pathErr != nil {
			http.Error(w, pathErr.Error(), http.StatusConflict)
			return
		}
		if pathErr = p.ensureComfyImagePolicy(ctx, paths); pathErr != nil {
			http.Error(w, pathErr.Error(), http.StatusConflict)
			return
		}
		status, err = controller.ReconcileProvider(ctx)
	default:
		http.Error(w, "unknown picture model action", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if status.ProviderConfigured && status.Ready && engine != setup.Lemonade {
		if _, err := journal.ConfirmConfiguration(ctx, mainState.Plan.ID, string(engine)+"-model"); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
	}
	reply := setupModelReply{Engine: engine, EngineInstalled: installedSetupEngine(mainState, engine), Enabled: true, Available: true, SetupPlanID: mainState.Plan.ID, Plan: fixedPlan, Status: status, Progress: p.modelProgressSnapshot()}
	if offered {
		// The reply's status is the starter plan's; the offered plan's
		// progress is in ChoiceStatus.
		reply.Status, _ = starter.Status(ctx)
	}
	if engine == setup.Lemonade {
		reply.Choices = setup.PictureModelChoices()
		reply.ChoiceStatus = p.choiceStatuses(ctx)
	}
	reply.Feature, _ = p.setupEngineFeatureState(engine)
	reply.Disk, err = starter.DiskPreflight()
	if err != nil {
		reply.Available = false
		reply.Reason = err.Error()
	}
	panelJSON(w, reply)
}

// planAsset reports one of plan's reviewed assets.
func planAsset(plan setup.ModelPlan, id string) bool {
	for _, asset := range plan.Assets {
		if id == asset.ID {
			return true
		}
	}
	return false
}

func setupActionTimeout(phase setup.Phase, recovery bool) time.Duration {
	if recovery || phase == setup.Verified || phase == setup.Installing || phase == setup.Uncertain ||
		phase == setup.Staged || phase == setup.Checked || phase == setup.Installed || phase == setup.Failed {
		// A reviewed portable app contains about 62k files and 6 GB expanded.
		// Give extraction, interrupted-install reconciliation, an update's
		// activation rehash and an earlier version's removal a finite window
		// that fits slow user disks without tying it to the short status timeout.
		return 90 * time.Minute
	}
	if phase == setup.Requested {
		// A completed service download may still need a large verified staging copy.
		return 30 * time.Minute
	}
	return 2 * time.Minute
}

func setupActionCall(r *http.Request, phase setup.Phase, recovery bool) (context.Context, context.CancelFunc) {
	return context.WithTimeout(r.Context(), setupActionTimeout(phase, recovery))
}
