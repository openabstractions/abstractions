package main

import (
	"context"
	"encoding/json"
	"errors"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	jobwire "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	"io"
	"log"
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
	ModelPlanID       string `json:"model_plan_id,omitempty"`
	CodingModelPlanID string `json:"coding_model_plan_id,omitempty"`
	// PictureModelPlanIDs are the picture model toggles the person ticked
	// (setupPictureSelection); absent keeps the default, the recommended
	// model for an engine with none prepared, and [] ticks none.
	PictureModelPlanIDs []string `json:"picture_model_plan_ids,omitempty"`
	// PermissionsID is the digest of the permission rules the screen
	// stated (setupPermission.ID).
	PermissionsID string      `json:"permissions_id,omitempty"`
	PlanID        string      `json:"plan_id"`
	Confirm       bool        `json:"confirm"`
	ComponentID   string      `json:"component_id"`
	Engine        setup.AppID `json:"engine,omitempty"`
}

type setupPanelStatus struct {
	Catalog setup.Catalog `json:"catalog"`
	setup.Status
	Approved bool `json:"approved"`
	// Actions are the Setup steps running now, by component: a package
	// step runs for minutes without progress events, so the page's stall
	// clock waits for it (setup_actions.go).
	Actions map[string]setupAction `json:"actions,omitempty"`
	// Outcomes are how each component's last step ended (setup_actions.go);
	// StepID names the step a reply started.
	Outcomes map[string]setupOutcome `json:"outcomes,omitempty"`
	StepID   string                  `json:"step_id,omitempty"`
}

type setupPlanReview struct {
	setup.Plan
	PictureModel *setup.ModelPlan `json:"picture_model,omitempty"`
	// PictureModels are the picture models offered as separate toggles,
	// the recommended one first, each with its size, licence and whether
	// this review selects it. PictureModel is the selected recommended one;
	// the other selected ones are approved by their IDs.
	PictureModels []setupPictureOffer `json:"picture_models,omitempty"`
	CodingModel   *setup.ModelPlan    `json:"coding_model,omitempty"`
	// Replaces names each installed version this plan would update, so the
	// consent text can say so before approval.
	Replaces []setupReplacement `json:"replaces,omitempty"`
	// OtherCopies are installations without the OA mark that Setup leaves
	// as they are.
	OtherCopies []setup.OtherCopy `json:"other_copies,omitempty"`
	// Permissions are the permissions the plan's apps get at Install, as
	// sentences (setup_install.go).
	Permissions []setupPermission `json:"permissions,omitempty"`
	// Included are the companions the plan added to the person's ticks,
	// such as a picture app's engine; the page shows each dimmed under its
	// app.
	Included []setup.AppID `json:"included,omitempty"`
	// PictureEngine is the engine line under a picture app.
	PictureEngine *setupPictureEngine `json:"picture_engine,omitempty"`
	// Added are the packages the plan still installs, each with its sizes
	// (setup.Catalog.AddedComponents); AddDownloadBytes and AddDiskBytes are
	// their sums. The page's rows and its total read these, so they agree.
	Added            []setup.AddedComponent `json:"added"`
	AddDownloadBytes int64                  `json:"add_download_bytes"`
	AddDiskBytes     int64                  `json:"add_disk_bytes"`
	// Disk is the drive the packages go on and its free space, when Setup
	// could read it.
	Disk *setupDisk `json:"disk,omitempty"`
	// ArchiveLooked is whether this review listed the download archive for
	// a model it had no listed candidate for; the page then rereads which
	// models copy from the archive.
	ArchiveLooked bool `json:"archive_looked,omitempty"`
}

// setupArchiveWait bounds how long a plan review waits for a listing of
// the download archive: the walk's own time bound and a little more.
var setupArchiveWait = 35 * time.Second

// lookInArchive lists the download archive when one of the review's models
// has no listed candidate and the last listing is older than ten minutes
// (setup.ArchiveLookFor), and waits for it; the page shows "Checking what
// to install…" meanwhile.
func (review *setupPlanReview) lookInArchive(r *http.Request) {
	var wanted []setup.PinnedDownload
	if review.CodingModel != nil {
		wanted = append(wanted, review.CodingModel.Downloads()...)
	}
	for _, offer := range review.PictureModels {
		if offer.Selected {
			wanted = append(wanted, offer.Downloads()...)
		}
	}
	done := setup.ArchiveLookFor(wanted)
	if done == nil {
		return
	}
	timer := time.NewTimer(setupArchiveWait)
	defer timer.Stop()
	select {
	case <-done:
		review.ArchiveLooked = true
	case <-r.Context().Done():
	case <-timer.C:
	}
}

type setupDisk struct {
	Volume    string `json:"volume"`
	FreeBytes uint64 `json:"free_bytes"`
}

// setupInstallDisk is setup.InstallDisk; tests replace it.
var setupInstallDisk = setup.InstallDisk

// setupPictureEngine is a picture app's engine in a plan: the one the plan
// uses, the one that fits this computer best, and the choices Change offers.
type setupPictureEngine struct {
	App     setup.AppID   `json:"app"`
	Engine  setup.AppID   `json:"engine"`
	Best    setup.AppID   `json:"best"`
	Options []setup.AppID `json:"options"`
}

// setupPlanCompanions fills the review's companions and engine line from
// the catalog's picture apps.
func setupPlanCompanions(catalog setup.Catalog, requested []setup.AppID, review *setupPlanReview) {
	for _, id := range review.Selected {
		if !slices.Contains(requested, id) {
			review.Included = append(review.Included, id)
		}
	}
	for _, choice := range catalog.Choices {
		if len(choice.Engines) == 0 || !slices.Contains(review.Selected, choice.ID) {
			continue
		}
		line := &setupPictureEngine{App: choice.ID, Best: catalog.PictureEngine(choice.ID), Options: slices.Clone(choice.Engines)}
		for _, engine := range choice.Engines {
			if slices.Contains(review.Selected, engine) {
				line.Engine = engine
				break
			}
		}
		review.PictureEngine = line
		return
	}
}

// A model-only review has a stable approval identity and no app installation.
// It reuses the compiled coding plan and the existing Install record.
func codingOnlyReview(id string) (setupPlanReview, error) {
	model := setup.CodingModelPlan()
	if id == "" || id != model.ID {
		return setupPlanReview{}, errors.New("the selected coding model changed; review it again")
	}
	return setupPlanReview{Plan: setup.Plan{ID: "coding-model-" + model.ID, Preparable: true}, CodingModel: &model}, nil
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

type modelProgress = jobwire.WorkProgress

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
	Progress     map[string]string            `json:"progress,omitempty"`
	// DefaultPending is a ready starter model Setup has not yet made the
	// account's picture default (setup.StarterDefaultPending); the page's
	// model step then asks once more.
	DefaultPending bool `json:"default_pending,omitempty"`
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
	if p.setupTransfer != nil {
		runner = setup.NewRunner(journal, *p.setupTransfer)
	}
	runner.RetireGuard = p.comfyRetireGuard
	return runner, journal, nil
}

func (p *servicePanel) setupModelForPanel() (setupModelController, error) {
	p.modelControllerMu.Lock()
	defer p.modelControllerMu.Unlock()
	if p.modelController != nil {
		return p.modelController, nil
	}
	controller, err := setup.DefaultBF16Controller(nil)
	if err != nil {
		return nil, err
	}
	p.modelController = controller
	return controller, nil
}

func fixedSetupEngine(engine setup.AppID) (setup.AppID, error) {
	return setup.FixedPictureEngine(engine)
}

func (p *servicePanel) setupModelForEngine(engine setup.AppID) (setupModelController, setup.ModelPlan, error) {
	plan, open, live, err := setup.FixedPictureController(engine)
	if err != nil {
		return nil, setup.ModelPlan{}, err
	}
	if open == nil {
		controller, err := p.setupModelForPanel()
		return controller, plan, err
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
	controller, err := open(journal)
	if err != nil {
		return nil, setup.ModelPlan{}, err
	}
	if live {
		return controller, controller.ReviewedPlan(), nil
	}
	if p.engineControllers == nil {
		p.engineControllers = map[setup.AppID]setupModelController{}
	}
	p.engineControllers[engine] = controller
	return controller, plan, nil
}

// engineInPlan is whether plan selects engine or installs its engine
// component, as ComfyUI installs the shared PyTorch engine.
func engineInPlan(plan setup.Plan, engine setup.AppID) bool {
	return slices.Contains(plan.Selected, engine) || slices.ContainsFunc(plan.Components, func(c setup.PlannedComponent) bool { return c.ID == string(engine)+"-engine" })
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
	remove := setup.PictureEngineRemoval(request.Engine)
	if remove == nil || !request.Confirm {
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
	if err := remove(runner, ctx); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if err := setup.SyncRetainedArchiveModelPaths(ctx, journal); err != nil {
		log.Printf("setup: ComfyUI model folders: %v", err)
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

func (p *servicePanel) modelProgressSnapshot() map[string]string {
	p.modelProgressMu.Lock()
	defer p.modelProgressMu.Unlock()
	result := make(map[string]string, len(p.modelProgress))
	for key, value := range p.modelProgress {
		result[key] = string(jobwire.EncodeWorkProgress(&value))
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
	return setupPanelStatus{Catalog: journal.Catalog(), Status: status, Approved: status.CurrentCatalog && state.Plan.ID != "" && state.ApprovedPlanID == state.Plan.ID}, nil
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
	if len(request.Selected) == 0 {
		review, err := codingOnlyReview(request.CodingModelPlanID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		if volume, free, diskErr := setupInstallDisk(); diskErr == nil {
			review.Disk = &setupDisk{Volume: volume, FreeBytes: free}
		}
		review.lookInArchive(r)
		panelJSON(w, review)
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
	if request.CodingModelPlanID != "" {
		model := setup.CodingModelPlan()
		if request.CodingModelPlanID != model.ID {
			http.Error(w, "the selected coding model changed; review it again", http.StatusConflict)
			return
		}
		reply.CodingModel = &model
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	installed, err := journal.InstalledVersions(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	reply.Replaces = setupReplacements(plan, installed)
	if reply.Added, err = journal.Catalog().AddedComponents(plan, installed); err != nil {
		panelError(w, err)
		return
	}
	for _, added := range reply.Added {
		reply.AddDownloadBytes += added.DownloadBytes
		reply.AddDiskBytes += added.DiskBytes
	}
	setupPlanCompanions(journal.Catalog(), request.Selected, &reply)
	if volume, free, diskErr := setupInstallDisk(); diskErr == nil {
		reply.Disk = &setupDisk{Volume: volume, FreeBytes: free}
	}
	if reply.Permissions, err = setupPermissions(plan); err != nil {
		panelError(w, err)
		return
	}
	reply.PictureModel, _, reply.PictureModels, err = setupPictureSelection(ctx, journal, plan, request.PictureModelPlanIDs)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	reply.lookInArchive(r)
	panelJSON(w, reply)
}

// setupIntent records approval for this exact catalog-backed plan. The Panel
// then advances safe actions under that plan-wide approval.
func (p *servicePanel) setupIntent(w http.ResponseWriter, r *http.Request) {
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

// setupCatalog is the release catalogue alone. The page draws its choices
// from it before the saved state arrives, so a slow or failed state read
// never leaves Setup without switches.
func (p *servicePanel) setupCatalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	journal, err := p.setupJournalForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, journal.Catalog())
}

var setupReconcileProviderRecords = setup.ReconcileInstalledPictureProviderRecords

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
	state.Actions, state.Outcomes = p.setupActionsNow(), p.setupOutcomesNow()
	panelJSON(w, state)
	// The provider owner can restore a missing installed record after an
	// upgrade. The status reply never waits for that recovery.
	reconcile := setupReconcileProviderRecords
	go func() {
		if err := reconcile(context.Background()); err != nil {
			log.Printf("setup: %v", err)
		}
	}()
}

func (p *servicePanel) setupComponentAction(w http.ResponseWriter, r *http.Request, retry, recovery bool) {
	// Try again waits for a running step: it never queues behind one,
	// including one that took the lock a moment ago.
	if (retry || recovery) && len(p.setupActionsNow()) > 0 {
		http.Error(w, "a Setup step is still running; try again when it finishes", http.StatusConflict)
		return
	}
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
	if !(retry && setupSameRootRetry(request.ComponentID, effect)) && !setupEffectAllowsAction(effect, retry, recovery) {
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
	defer p.beginSetupAction(request.ComponentID, ctx)()
	if retry {
		_, err = runner.Retry(ctx, request.ComponentID)
	} else {
		_, err = runner.Advance(ctx, request.ComponentID)
	}
	if err != nil {
		setupActionFailed(w, err)
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

// setupActionFailed answers 503 for downloads unavailable and 409 for any other package step error.
func setupActionFailed(w http.ResponseWriter, err error) {
	if errors.Is(err, setup.ErrDownloadsUnavailable) {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}
	http.Error(w, err.Error(), http.StatusConflict)
}

// setupSameRootRetry is an interrupted PyTorch component setup: it stays
// staged on its allocated root, and Runner.Retry owns its explicit
// same-root retry.
func setupSameRootRetry(componentID string, effect setup.Effect) bool {
	return componentID == "pytorch-engine" && effect.Phase == setup.Staged && effect.ComponentRootKey != "" && !effect.Removing
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

// comfyRetireGuard checks the current registered program before retirement.
// Native owned channels remain distinct from ordinary presence.
func (p *servicePanel) comfyRetireGuard(ctx context.Context, componentID, root string) error {
	archive := officialPicture.archive
	component, registration := archive.Component(), archive.Registration()
	if componentID != component {
		return nil
	}
	apps, err := p.nativeApplicationsForPanel(ctx, registration)
	if err != nil {
		return err
	}
	page, err := apps.Observe(ctx, "", 0)
	if err != nil {
		return err
	}
	registered, found, err := officialPicture.findWorkspaceDescriptor(page)
	if err != nil {
		return err
	}
	if !found {
		if native, ok := apps.(*nativeApplications); ok {
			if page.Outcome != fwire.ApplicationOutcomePage || native.owned {
				return errors.New("native ownership could not be read; package preserved")
			}
			return nil
		}
		p.comfyAppsMu.Lock()
		mine := p.comfyRegisteredDescriptor
		p.comfyAppsMu.Unlock()
		if mine == nil {
			return errors.New("the earlier ComfyUI version is kept until the Panel can read which program the comfyui entry starts")
		}
		registered = *mine
	}
	if setup.ProgramWithin(root, registered.Program) {
		if native, ok := apps.(*nativeApplications); ok && native.owned {
			return errors.New("a registered native owned instance still names this version; close it and register the updated application first")
		}
		return errors.New("the registered comfyui entry still starts the earlier version; register the updated ComfyUI first")
	}
	return nil
}

// setupAdvanceNextReply is the plan state after one automatic transition
// and the component it advanced, empty when none could advance now.
type setupAdvanceNextReply struct {
	setupPanelStatus
	Advanced string `json:"advanced,omitempty"`
}

// setupAdvanceNext makes the approved plan's next automatic transition. The
// selection is setup.Status.NextAutomatic, the one package driver the setup
// step also uses (architect review 25, A2); the page names only the plan.
func (p *servicePanel) setupAdvanceNext(w http.ResponseWriter, r *http.Request) {
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
	if request.PlanID == "" || request.ComponentID != "" {
		http.Error(w, "the reviewed plan is required, and Setup chooses the component", http.StatusBadRequest)
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
	if current.Plan.ID == "" || current.Plan.ID != request.PlanID || !current.Approved {
		http.Error(w, "approved setup plan changed; review and approve it again", http.StatusConflict)
		return
	}
	next := current.Status.NextAutomatic()
	cancelCtx, cancelDone := panelCall(r)
	cancelled := setupInstallCancelled(cancelCtx, journal, current.Plan.ID)
	cancelDone()
	if next == "" || cancelled {
		panelJSON(w, setupAdvanceNextReply{setupPanelStatus: current})
		return
	}
	var phase setup.Phase
	for _, component := range current.Components {
		if component.ID == next {
			phase = component.Effect.Phase
		}
	}
	ctx, cancel := setupActionCall(r, phase, false)
	defer cancel()
	defer p.beginSetupAction(next, ctx)()
	if _, err := runner.Advance(ctx, next); err != nil {
		setupActionFailed(w, err)
		return
	}
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, setupAdvanceNextReply{setupPanelStatus: state, Advanced: next})
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
	if engine == setup.PyTorch {
		if _, fixed, fixedErr := p.setupModelForEngine(engine); fixedErr == nil {
			plan = fixed
		}
	}
	reply := setupModelReply{Engine: engine, EngineInstalled: installedSetupEngine(state, engine), Enabled: selected && state.Approved, SetupPlanID: state.Plan.ID, Plan: plan, Progress: p.modelProgressSnapshot()}
	reply.Choices = engineChoices(engine, plan.ID)
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
	reply.DefaultPending, _ = setup.StarterDefaultPending(ctx, journal, engine)
	if engine == setup.PyTorch {
		reply.DefaultPending, _ = setup.PictureDefaultPending(ctx, journal, plan)
	}
	if len(reply.Choices) > 0 {
		reply.ChoiceStatus = p.choiceStatuses(ctx, engine, plan.ID)
	}
	panelJSON(w, reply)
}

func (p *servicePanel) setupModelAction(w http.ResponseWriter, r *http.Request, action string) {
	if !p.setupTryStart(w) {
		return
	}
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
	if engine != setup.Lemonade && request.PlanID != fixedPlan.ID && !isEngineChoice(engine, request.PlanID) {
		http.Error(w, "the reviewed model plan is bound to a different engine", http.StatusConflict)
		return
	}
	// An offered plan's request names its plan; the starter plan's may not.
	controller, plan, offered := starter, fixedPlan, request.PlanID != "" && request.PlanID != fixedPlan.ID
	if offered {
		if !isEngineChoice(engine, request.PlanID) {
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
		// The PyTorch engine's offers are separate toggles; a Lemonade
		// offer is staged into the provider its starter model configured.
		if offered && engine == setup.Lemonade {
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
		status, err = controller.Configure(ctx, false, func(done, total int64) error { p.setModelProgress("provider", done, total); return ctx.Err() })
		p.clearModelProgress("provider")
	case "reconcile":
		status, err = controller.ReconcileProvider(ctx)
	default:
		http.Error(w, "unknown picture model action", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// A ready PyTorch toggle stands on its own, as the starter does; the
	// engine serves one Klein set (the full-precision one once prepared)
	// and ComfyUI reads every prepared toggle.
	pyTorchToggle := engine == setup.PyTorch
	if status.ProviderConfigured && status.Ready && (!offered || pyTorchToggle) {
		stagedOnly := plan.StagesPictureFilesOnly()
		if !stagedOnly {
			if _, err := journal.ConfirmConfiguration(ctx, mainState.Plan.ID, string(engine)+"-model"); err != nil {
				http.Error(w, err.Error(), http.StatusConflict)
				return
			}
		}
		if pyTorchToggle {
			// ComfyUI's own loaders find the model at their next start.
			if err := setup.SyncRetainedArchiveModelPaths(ctx, journal); err != nil {
				log.Printf("setup: ComfyUI model folders: %v", err)
			}
		}
		// The ready model becomes the picture model of the account's apps
		// once; a failed write stays pending for the next action, and the
		// person's later choice stays as they set it. For the PyTorch
		// engine it is the set its registration serves.
		pending, err := setup.StarterDefaultPending(ctx, journal, engine)
		model := plan.Model
		if pyTorchToggle {
			pending, err = setup.PictureDefaultPending(ctx, journal, plan)
			pending = pending && !stagedOnly
			if err == nil && pending {
				// Without a recorded registration the plan's own set is
				// the one Setup registers.
				if served, servedErr := setup.PyTorchServedModel(ctx, journal); servedErr == nil && served != "" {
					model = served
				}
				// The default is the account's: a second Klein toggle
				// changes it only when its set became the served one.
				var due bool
				if due, err = setup.PictureDefaultDue(ctx, journal, plan, model); err == nil && !due {
					pending = false
					if markErr := setup.MarkPictureDefaultWritten(ctx, journal, plan); markErr != nil {
						log.Printf("setup: %s picture default: %v", engine, markErr)
					}
				}
			}
		}
		if err != nil {
			log.Printf("setup: %s picture default: %v", engine, err)
		}
		if pending {
			if err := setupImageDefault(ctx, model); err != nil {
				http.Error(w, "the picture model is ready; making it the picture model for your apps failed: "+err.Error(), http.StatusConflict)
				return
			}
			mark := setup.MarkStarterDefaultWritten(ctx, journal, engine)
			if pyTorchToggle {
				mark = setup.MarkPictureDefaultWritten(ctx, journal, plan)
			}
			if mark != nil {
				log.Printf("setup: %s picture default: %v", engine, mark)
			}
		}
		if err := setupFillImageDefault(ctx, journal); err != nil {
			log.Printf("setup: %s picture default: %v", engine, err)
		}
	}
	reply := setupModelReply{Engine: engine, EngineInstalled: installedSetupEngine(mainState, engine), Enabled: true, Available: true, SetupPlanID: mainState.Plan.ID, Plan: fixedPlan, Status: status, Progress: p.modelProgressSnapshot()}
	if offered {
		// The reply's status is the starter plan's; the offered plan's
		// progress is in ChoiceStatus.
		reply.Status, _ = starter.Status(ctx)
	}
	if reply.Choices = engineChoices(engine, fixedPlan.ID); len(reply.Choices) > 0 {
		reply.ChoiceStatus = p.choiceStatuses(ctx, engine, fixedPlan.ID)
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

// setupActionCall outlives the page's request: a step that installs or
// checks a package finishes and records its phase when the person closes
// the Panel or leaves the page. On 2026-11-08 a closed page cancelled the
// PyTorch component setup's journal transition after the component itself
// was ready, and the plan stayed at staged.
func setupActionCall(r *http.Request, phase setup.Phase, recovery bool) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(r.Context()), setupActionTimeout(phase, recovery))
}
