package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"slices"

	cas "github.com/openabstractions/abstraction-cas/go"
	jobapi "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// ModelStager is the provider's private, verified BF16 staging and
// configuration operation, which moves each model download by rename. It
// must honor cancellation and refuse replacement by default.
type ModelStager func(context.Context, string, BF16StageFiles, func(int64, int64) error, bool) error
type ModelProbe func(context.Context, string) (bool, error)

type BF16StageFiles struct{ Main, Encoder, VAE, BackendArchive string }

type ModelApproval struct {
	PlanID        string   `json:"plan_id"`
	DownloadBytes int64    `json:"download_bytes"`
	PeakBytes     int64    `json:"peak_bytes"`
	Licenses      []string `json:"licenses"`
}

type ModelEffect struct {
	Phase      Phase         `json:"phase"`
	Work       *WorkIdentity `json:"work,omitempty"`
	StagedPath string        `json:"staged_path,omitempty"`
	Reason     string        `json:"reason,omitempty"`
	// Shared is an asset the provider already holds for a staged model with
	// the same SHA-256; it is verified without a download and the provider
	// links its staged file.
	Shared bool `json:"shared,omitempty"`
}

type ModelState struct {
	PlanID              string                 `json:"plan_id"`
	Approved            bool                   `json:"approved"`
	Effects             map[string]ModelEffect `json:"effects"`
	ProviderConfigured  bool                   `json:"provider_configured"`
	ProviderConfiguring bool                   `json:"provider_configuring"`
	ProviderFailure     string                 `json:"provider_failure,omitempty"`
}

type ModelAssetStatus struct {
	ID       string               `json:"id"`
	Phase    Phase                `json:"phase"`
	Reason   string               `json:"reason,omitempty"`
	Transfer *TransferObservation `json:"transfer,omitempty"`
}

type ModelStatus struct {
	Plan                ModelPlan          `json:"plan"`
	Approved            bool               `json:"approved"`
	Assets              []ModelAssetStatus `json:"assets"`
	ProviderConfigured  bool               `json:"provider_configured"`
	ProviderConfiguring bool               `json:"provider_configuring"`
	ProviderFailure     string             `json:"provider_failure,omitempty"`
	// Ready also requires the caller's public program-scoped selection read.
	Ready bool `json:"ready"`
}

type ModelDiskStatus struct {
	RequiredPeakBytes      int64  `json:"required_peak_bytes"`
	StageAvailableBytes    uint64 `json:"stage_available_bytes"`
	ProviderRequiredBytes  int64  `json:"provider_required_bytes"`
	ProviderAvailableBytes uint64 `json:"provider_available_bytes"`
	Enough                 bool   `json:"enough"`
}

type ModelController struct {
	JournalPath  string
	StageDir     string
	ProviderRoot string
	Transfer     Transfer
	Stage        ModelStager
	Probe        ModelProbe
	FreeBytes    func(string) (uint64, error)
	// Selected must read the public program-scoped selection policy and
	// confirm this exact model is selected for the target application.
	Selected func(context.Context) (bool, error)
	// Plan is an offered picture model's plan (PictureModelChoices); nil is
	// the starter plan (ApprovedBF16Plan). An offered plan is staged into the
	// configured provider through StageModel, and its assets whose SHA-256 a
	// model StagedModels names already has start verified and shared.
	Plan         *ModelPlan
	StagedModels func(context.Context) ([]string, error)
	StageModel   func(ctx context.Context, root, model string, files map[string]string, progress func(int64, int64) error) error
	// Guard revalidates the fixed engine's current approved package and
	// ownership before each action, including recovery and transfer retries.
	Guard func(context.Context) error
}

func (c ModelController) guard(ctx context.Context) error {
	if c.Guard != nil {
		return c.Guard(ctx)
	}
	return ctx.Err()
}

// plan is the controller's reviewed plan.
func (c ModelController) plan() ModelPlan {
	if c.Plan != nil {
		return *c.Plan
	}
	return ApprovedBF16Plan()
}

// assetRoles names the provider's checkpoint role of each model asset.
var assetRoles = map[string]string{"main": "main", "encoder": "text_encoder", "vae": "vae"}

// stagedDigests are the SHA-256 digests of every model file the provider
// holds for the models it has staged, from the reviewed plans of those
// models. The starter plan's backend archive is no model file.
func stagedDigests(models []string) map[string]bool {
	digests := map[string]bool{}
	plans := append([]ModelPlan{ApprovedBF16Plan()}, PictureModelChoices()...)
	for _, plan := range plans {
		if !slices.Contains(models, plan.Model) {
			continue
		}
		for _, a := range plan.Assets {
			if assetRoles[a.ID] != "" {
				digests[a.Download.SHA256] = true
			}
		}
	}
	return digests
}

func (c ModelController) transfer() Transfer {
	if c.Transfer != nil {
		return c.Transfer
	}
	return PublicTransfer{}
}

// DiskPreflight is a read-only preview for the consent screen. Start repeats
// the check immediately before approval because available space can change.
func (c ModelController) DiskPreflight() (ModelDiskStatus, error) {
	if c.StageDir == "" || c.ProviderRoot == "" {
		return ModelDiskStatus{}, errors.New("model stage or provider directory is empty")
	}
	free := c.FreeBytes
	if free == nil {
		free = modelDiskFree
	}
	plan := c.plan()
	stage, err := free(c.StageDir)
	if err != nil {
		return ModelDiskStatus{}, fmt.Errorf("cannot verify model setup disk space: %w", err)
	}
	provider, err := free(c.ProviderRoot)
	if err != nil {
		return ModelDiskStatus{}, fmt.Errorf("cannot verify provider model disk space: %w", err)
	}
	required := plan.DownloadBytes
	if c.Plan == nil {
		required += backendExpandedBytes
	}
	return ModelDiskStatus{RequiredPeakBytes: plan.PeakBytes, StageAvailableBytes: stage,
		ProviderRequiredBytes: required, ProviderAvailableBytes: provider,
		Enough: stage >= uint64(plan.PeakBytes) && provider >= uint64(required)}, nil
}

// Start records one approval for the complete reviewed model plan. The UI
// presents the source, license, download bytes, and conservative peak bytes.
func (c ModelController) Start(ctx context.Context, approval ModelApproval) (ModelStatus, error) {
	if err := c.guard(ctx); err != nil {
		return ModelStatus{}, err
	}
	plan := c.plan()
	if approval.PlanID != plan.ID || approval.DownloadBytes != plan.DownloadBytes || approval.PeakBytes != plan.PeakBytes || !slices.Equal(approval.Licenses, plan.Licenses) {
		return ModelStatus{}, errors.New("model approval differs from the reviewed plan")
	}
	if c.JournalPath == "" {
		return ModelStatus{}, errors.New("model journal path is empty")
	}
	disk, err := c.DiskPreflight()
	if err != nil {
		return ModelStatus{}, err
	}
	if disk.StageAvailableBytes < uint64(plan.PeakBytes) {
		return ModelStatus{}, fmt.Errorf("model setup needs %d free bytes for the public download and the provider's staged files; %d available", plan.PeakBytes, disk.StageAvailableBytes)
	}
	if disk.ProviderAvailableBytes < uint64(disk.ProviderRequiredBytes) {
		return ModelStatus{}, fmt.Errorf("provider model directory needs %d free bytes for its model files and backend expansion; %d available", disk.ProviderRequiredBytes, disk.ProviderAvailableBytes)
	}
	if err := os.MkdirAll(filepath.Dir(c.JournalPath), 0700); err != nil {
		return ModelStatus{}, err
	}
	shared := map[string]bool{}
	if c.Plan != nil && c.StagedModels != nil {
		models, err := c.StagedModels(ctx)
		if err != nil {
			return ModelStatus{}, err
		}
		shared = stagedDigests(models)
	}
	err = cas.ChangeLimitContext(ctx, c.JournalPath, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := c.decode(raw)
		if err != nil {
			return nil, err
		}
		if state.PlanID == plan.ID && state.Approved {
			return raw, nil
		}
		if state.PlanID != "" {
			return nil, errors.New("an existing model plan needs explicit recovery before replacement")
		}
		state = ModelState{PlanID: plan.ID, Approved: true, Effects: map[string]ModelEffect{}}
		for _, a := range plan.Assets {
			state.Effects[a.ID] = ModelEffect{Phase: Pending}
			if shared[a.Download.SHA256] {
				state.Effects[a.ID] = ModelEffect{Phase: Verified, Shared: true}
			}
		}
		return json.Marshal(state)
	})
	if err != nil {
		return ModelStatus{}, err
	}
	return c.Status(ctx)
}

// Status reads only the intent journal and public job observations. It does
// not rescan multi-gigabyte files on a UI poll.
func (c ModelController) Status(ctx context.Context) (ModelStatus, error) {
	state, err := c.load(ctx)
	if err != nil {
		return ModelStatus{}, err
	}
	status := ModelStatus{Plan: c.plan(), Approved: state.Approved,
		ProviderConfigured: state.ProviderConfigured, ProviderConfiguring: state.ProviderConfiguring, ProviderFailure: state.ProviderFailure}
	for _, a := range status.Plan.Assets {
		effect := state.Effects[a.ID]
		row := ModelAssetStatus{ID: a.ID, Phase: effect.Phase, Reason: effect.Reason}
		if effect.Phase == Requested && effect.Work != nil {
			observation, err := c.transfer().Observe(ctx, *effect.Work)
			if err != nil {
				observation = TransferObservation{State: "unknown", Failure: err.Error()}
			}
			row.Transfer = &observation
		}
		status.Assets = append(status.Assets, row)
	}
	if state.ProviderConfigured && c.Selected != nil {
		selected, err := c.Selected(ctx)
		if err != nil {
			return status, err
		}
		status.Ready = selected
	}
	return status, nil
}

// Advance makes at most one recoverable transition for one fixed asset.
// The public request identity is saved before submission.
func (c ModelController) Advance(ctx context.Context, assetID string) (ModelStatus, error) {
	return c.AdvanceWithProgress(ctx, assetID, nil)
}

// AdvanceWithProgress reports local transfer-result staging progress. The
// callback may return an error to stop the copy; the saved public job remains.
func (c ModelController) AdvanceWithProgress(ctx context.Context, assetID string, progress func(int64, int64) error) (ModelStatus, error) {
	if err := c.guard(ctx); err != nil {
		return ModelStatus{}, err
	}
	state, err := c.load(ctx)
	if err != nil {
		return ModelStatus{}, err
	}
	if !state.Approved {
		return ModelStatus{}, errors.New("model plan has not been approved")
	}
	a, ok := c.asset(assetID)
	if !ok {
		return ModelStatus{}, errors.New("unknown model asset")
	}
	effect := state.Effects[assetID]
	switch effect.Phase {
	case Pending:
		keySum := sha256.Sum256([]byte(state.PlanID + "\x00" + a.ID))
		work, err := c.transfer().Reserve(ctx, a.Download, "model-"+hex.EncodeToString(keySum[:16]))
		if err != nil {
			return c.statusWithError(ctx, err)
		}
		if err := c.change(ctx, assetID, effect, ModelEffect{Phase: Requested, Work: &work}); err != nil {
			return ModelStatus{}, err
		}
		if err := c.transfer().Submit(ctx, a.Download, work); err != nil {
			return c.statusWithError(ctx, err)
		}
	case Requested:
		if effect.Work == nil {
			return ModelStatus{}, errors.New("requested model transfer has no saved identity")
		}
		if err := c.transfer().Submit(ctx, a.Download, *effect.Work); err != nil {
			return c.statusWithError(ctx, err)
		}
		observation, err := c.transfer().Observe(ctx, *effect.Work)
		if err != nil {
			return c.statusWithError(ctx, err)
		}
		switch observation.State {
		case "complete":
			path, err := c.stage(ctx, a, *effect.Work, progress)
			if err != nil {
				return ModelStatus{}, err
			}
			if err := c.change(ctx, assetID, effect, ModelEffect{Phase: Verified, Work: effect.Work, StagedPath: path}); err != nil {
				return ModelStatus{}, err
			}
		case "failed", "cancelled":
			if err := c.change(ctx, assetID, effect, ModelEffect{Phase: Failed, Work: effect.Work, Reason: observation.Failure}); err != nil {
				return ModelStatus{}, err
			}
		}
	case Verified, Failed:
	default:
		return ModelStatus{}, errors.New("invalid model phase")
	}
	return c.Status(ctx)
}

func (c ModelController) statusWithError(ctx context.Context, cause error) (ModelStatus, error) {
	status, _ := c.Status(ctx)
	return status, cause
}

// Configure consumes four locally verified stages. The provider rechecks
// its own pins before publishing private model/config data. Selection remains
// a separate public operation and is checked by Status before Ready.
func (c ModelController) Configure(ctx context.Context, replace bool, progress func(int64, int64) error) (ModelStatus, error) {
	if err := c.guard(ctx); err != nil {
		return ModelStatus{}, err
	}
	if c.Plan == nil && c.Stage == nil || c.Plan != nil && c.StageModel == nil || c.ProviderRoot == "" {
		return ModelStatus{}, errors.New("provider staging is unavailable")
	}
	err := c.withProviderOperation(ctx, func() error {
		if err := c.guard(ctx); err != nil {
			return err
		}
		state, err := c.load(ctx)
		if err != nil {
			return err
		}
		if !state.Approved {
			return errors.New("approve the fixed model plan before configuration")
		}
		if state.ProviderConfigured {
			if c.Guard != nil && c.Selected != nil {
				ready, err := c.Selected(ctx)
				if err != nil {
					return err
				}
				if !ready {
					return errors.New("the configured provider no longer matches the approved engine; inspect its registration before recovery")
				}
			}
			return nil
		}
		if state.ProviderConfiguring {
			return c.reconcileProviderLocked(ctx)
		}
		if c.Plan != nil {
			return c.configureChoice(ctx, state, progress)
		}
		files := BF16StageFiles{}
		for _, a := range ApprovedBF16Plan().Assets {
			e := state.Effects[a.ID]
			if e.Phase != Verified || e.StagedPath == "" {
				return errors.New("all four image assets must be verified first")
			}
			switch a.ID {
			case "backend":
				files.BackendArchive = e.StagedPath
			case "main":
				files.Main = e.StagedPath
			case "encoder":
				files.Encoder = e.StagedPath
			case "vae":
				files.VAE = e.StagedPath
			}
		}
		if err := c.setConfiguring(ctx, true); err != nil {
			return err
		}
		if err := c.Stage(ctx, c.ProviderRoot, files, progress, replace); err != nil {
			// The exact probe must resolve a possible lost reply or crash.
			return err
		}
		if err := c.setConfigured(ctx); err != nil {
			return err
		}
		// The provider moved each model file into its models folder by
		// rename; only the expanded backend archive is left to remove.
		_ = os.Remove(files.BackendArchive)
		return nil
	})
	if err != nil {
		return c.statusWithError(ctx, err)
	}
	return c.Status(ctx)
}

// ReconcileProvider inspects the fixed private model identity after a crash
// or ambiguous staging result. A false result permits an explicit retry;
// a partial or foreign identity must make Probe return an error.
func (c ModelController) ReconcileProvider(ctx context.Context) (ModelStatus, error) {
	if err := c.guard(ctx); err != nil {
		return ModelStatus{}, err
	}
	if c.Probe == nil || c.ProviderRoot == "" {
		return ModelStatus{}, errors.New("provider model probe is unavailable")
	}
	err := c.withProviderOperation(ctx, func() error { return c.reconcileProviderLocked(ctx) })
	if err != nil {
		return c.statusWithError(ctx, err)
	}
	return c.Status(ctx)
}

func (c ModelController) reconcileProviderLocked(ctx context.Context) error {
	if c.Probe == nil {
		return errors.New("provider model probe is unavailable")
	}
	state, err := c.load(ctx)
	if err != nil {
		return err
	}
	if !state.ProviderConfiguring {
		return nil
	}
	ready, err := c.Probe(ctx, c.ProviderRoot)
	if err != nil {
		return err
	}
	if ready {
		return c.setConfigured(ctx)
	}
	return c.setConfigureFailure(ctx, errors.New("private model preparation was not completed"))
}

// A separate CAS record holds the native cross-process lock throughout the
// private staging or exact probe. Journal status reads remain responsive.
func (c ModelController) withProviderOperation(ctx context.Context, operation func() error) error {
	if c.JournalPath == "" {
		return errors.New("model journal path is empty")
	}
	return cas.ChangeLimitContext(ctx, c.JournalPath+".provider-op", 64, func([]byte) ([]byte, error) {
		if err := operation(); err != nil {
			return nil, err
		}
		return []byte("complete"), nil
	})
}

// Retry resubmits only an exact public attempt confirmed failed or cancelled.
func (c ModelController) Retry(ctx context.Context, assetID string) (ModelStatus, error) {
	if err := c.guard(ctx); err != nil {
		return ModelStatus{}, err
	}
	state, err := c.load(ctx)
	if err != nil {
		return ModelStatus{}, err
	}
	if !state.Approved {
		return ModelStatus{}, errors.New("model plan has not been approved")
	}
	a, ok := c.asset(assetID)
	if !ok {
		return ModelStatus{}, errors.New("unknown model asset")
	}
	effect := state.Effects[assetID]
	if effect.Phase != Failed || effect.Work == nil {
		return ModelStatus{}, errors.New("only a confirmed failed model transfer can be retried")
	}
	prior, err := c.transfer().Observe(ctx, *effect.Work)
	if err != nil {
		return c.statusWithError(ctx, err)
	}
	if prior.State != "failed" && prior.State != "cancelled" {
		return ModelStatus{}, errors.New("public service has not confirmed terminal failure")
	}
	next := *effect.Work
	next.Attempt++
	if err := c.change(ctx, assetID, effect, ModelEffect{Phase: Requested, Work: &next}); err != nil {
		return ModelStatus{}, err
	}
	if err := c.transfer().Submit(ctx, a.Download, next); err != nil {
		return c.statusWithError(ctx, err)
	}
	return c.Status(ctx)
}

// configureChoice stages an offered plan's verified downloads into the
// configured provider, which moves each by rename; a shared asset passes no
// file, and the provider links its staged file. The caller holds the
// provider operation.
func (c ModelController) configureChoice(ctx context.Context, state ModelState, progress func(int64, int64) error) error {
	if c.StageModel == nil {
		return errors.New("provider staging is unavailable")
	}
	plan := c.plan()
	files := map[string]string{}
	for _, a := range plan.Assets {
		e := state.Effects[a.ID]
		if e.Phase != Verified || e.StagedPath == "" && !e.Shared {
			return errors.New("every picture model file must be verified first")
		}
		if !e.Shared {
			role := assetRoles[a.ID]
			if a.Role != "" {
				role = a.Role
			}
			files[role] = e.StagedPath
		}
	}
	if err := c.setConfiguring(ctx, true); err != nil {
		return err
	}
	// The provider moves each download into its models folder by rename,
	// so nothing is left here to remove.
	if err := c.StageModel(ctx, c.ProviderRoot, plan.Model, files, progress); err != nil {
		return err
	}
	return c.setConfigured(ctx)
}

func (c ModelController) asset(id string) (ModelAsset, bool) {
	for _, a := range c.plan().Assets {
		if a.ID == id {
			return a, true
		}
	}
	return ModelAsset{}, false
}

// decode reads the controller's journal, which records one approval of its
// own plan.
func (c ModelController) decode(raw []byte) (ModelState, error) {
	if raw == nil {
		return ModelState{}, nil
	}
	var state ModelState
	if err := json.Unmarshal(raw, &state); err != nil {
		return ModelState{}, err
	}
	plan := c.plan()
	if state.PlanID != plan.ID || !state.Approved || len(state.Effects) != len(plan.Assets) {
		return ModelState{}, errors.New("model journal differs from reviewed plan")
	}
	return state, nil
}

func (c ModelController) load(ctx context.Context) (ModelState, error) {
	if c.JournalPath == "" {
		return ModelState{}, errors.New("model journal path is empty")
	}
	raw, err := cas.ReadLimitContext(ctx, c.JournalPath, maxJournalBytes)
	if err != nil {
		return ModelState{}, err
	}
	return c.decode(raw)
}

func (c ModelController) change(ctx context.Context, id string, before, after ModelEffect) error {
	return cas.ChangeLimitContext(ctx, c.JournalPath, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := c.decode(raw)
		if err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(state.Effects[id], before) {
			return nil, errors.New("model asset changed; reload before continuing")
		}
		if after.Phase == Requested && !validWorkIdentity(after.Work) {
			return nil, errors.New("requested model transfer needs its complete public job identity")
		}
		state.Effects[id] = after
		return json.Marshal(state)
	})
}

func (c ModelController) setConfiguring(ctx context.Context, configuring bool) error {
	return cas.ChangeLimitContext(ctx, c.JournalPath, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := c.decode(raw)
		if err != nil {
			return nil, err
		}
		if state.ProviderConfigured || state.ProviderConfiguring {
			return nil, errors.New("provider staging is already active or complete")
		}
		state.ProviderConfiguring = configuring
		state.ProviderFailure = ""
		return json.Marshal(state)
	})
}

func (c ModelController) setConfigureFailure(ctx context.Context, cause error) error {
	return cas.ChangeLimitContext(ctx, c.JournalPath, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := c.decode(raw)
		if err != nil {
			return nil, err
		}
		if !state.ProviderConfiguring || state.ProviderConfigured {
			return nil, errors.New("provider staging state changed")
		}
		state.ProviderConfiguring = false
		state.ProviderFailure = cause.Error()
		return json.Marshal(state)
	})
}

func (c ModelController) setConfigured(ctx context.Context) error {
	return cas.ChangeLimitContext(ctx, c.JournalPath, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := c.decode(raw)
		if err != nil {
			return nil, err
		}
		if !state.ProviderConfiguring {
			return nil, errors.New("provider staging was not reserved")
		}
		state.ProviderConfiguring = false
		state.ProviderConfigured = true
		return json.Marshal(state)
	})
}

func (c ModelController) modelStagePath(a ModelAsset) string {
	if c.plan().Engine == PyTorch {
		return filepath.Join(c.StageDir, filepath.FromSlash(a.Path))
	}
	return filepath.Join(c.StageDir, a.Download.SHA256+filepath.Ext(a.Path))
}

func (c ModelController) stage(ctx context.Context, a ModelAsset, work WorkIdentity, progress func(int64, int64) error) (string, error) {
	if c.StageDir == "" {
		return "", errors.New("model stage directory is empty")
	}
	if err := os.MkdirAll(c.StageDir, 0700); err != nil {
		return "", err
	}
	target := c.modelStagePath(a)
	if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
		return "", err
	}
	if err := checkModelStage(ctx, target, a.Download); err == nil {
		return target, nil
	} else if _, statErr := os.Lstat(target); statErr == nil {
		// A name that does not verify is this folder's own leftover.
		if err := os.Remove(target); err != nil {
			return "", err
		}
	}
	// The job service hard-links its verified result here (VISION
	// 2026-10-01, JOB-A17): the disk holds one file per download, and the
	// service keeps its own name.
	linker, links := c.transfer().(ResultLinker)
	if !links {
		return "", errors.New("the download service cannot link a model into the picture engine's folder")
	}
	outcome, err := linker.Link(ctx, work, target)
	if err != nil {
		return "", err
	}
	switch outcome {
	case jobapi.LinkOutcomeLinked:
		if err := checkModelStage(ctx, target, a.Download); err != nil {
			//unchecked: the refusal is primary; this removes only Setup's own name
			_ = os.Remove(target)
			return "", fmt.Errorf("model asset %s differs from reviewed size or digest", a.ID)
		}
		if progress != nil {
			if err := progress(a.Download.Bytes, a.Download.Bytes); err != nil {
				return "", err
			}
		}
		return target, nil
	case jobapi.LinkOutcomeRemote:
		// A remote download service keeps streaming (bindings never cross
		// machines).
	case jobapi.LinkOutcomeOtherVolume:
		return "", errors.New("the download service keeps its downloads on another volume than the picture engine's folder; keep OpenAbstractions' data and the picture engine on one NTFS volume and download again")
	case jobapi.LinkOutcomeNoHardLinks:
		return "", errors.New("the volume of the picture engine's folder makes no hard links; keep it on an NTFS volume and download again")
	case jobapi.LinkOutcomeForbidden:
		return "", errors.New("the download service refused to name the model in the picture engine's folder for this Setup")
	default:
		return "", fmt.Errorf("the download service did not link model asset %s (%s)", a.ID, outcome)
	}
	partial, err := os.CreateTemp(c.StageDir, ".model-partial-")
	if err != nil {
		return "", err
	}
	defer os.Remove(partial.Name())
	defer partial.Close()
	h := sha256.New()
	limited := &boundedWriter{writer: io.MultiWriter(partial, h), remaining: a.Download.Bytes}
	writer := &modelProgressWriter{ctx: ctx, dst: limited, total: a.Download.Bytes, report: progress}
	n, err := c.transfer().Copy(ctx, work, writer)
	if err != nil {
		return "", err
	}
	if n != a.Download.Bytes || limited.remaining != 0 || hex.EncodeToString(h.Sum(nil)) != a.Download.SHA256 {
		return "", fmt.Errorf("model asset %s differs from reviewed size or digest", a.ID)
	}
	if err := partial.Sync(); err != nil {
		return "", err
	}
	if err := partial.Close(); err != nil {
		return "", err
	}
	if err := os.Link(partial.Name(), target); err != nil {
		if checkModelStage(ctx, target, a.Download) == nil {
			return target, nil
		}
		return "", err
	}
	return target, nil
}

type modelProgressWriter struct {
	ctx         context.Context
	dst         io.Writer
	done, total int64
	report      func(int64, int64) error
}

func (w *modelProgressWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	n, err := w.dst.Write(p)
	w.done += int64(n)
	if w.report != nil {
		if reportErr := w.report(w.done, w.total); reportErr != nil {
			return n, reportErr
		}
	}
	return n, err
}

func checkModelStage(ctx context.Context, path string, pin PinnedDownload) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() != pin.Bytes {
		return errors.New("model stage has wrong size")
	}
	h := sha256.New()
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != pin.SHA256 {
		return errors.New("model stage digest differs from pin")
	}
	return nil
}
