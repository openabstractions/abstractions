package setup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	identity "github.com/openabstractions/abstraction-identity"
)

type ComponentStatus struct {
	ID       string               `json:"id"`
	Effect   Effect               `json:"effect"`
	Transfer *TransferObservation `json:"transfer,omitempty"`
}

type Status struct {
	Plan           Plan              `json:"plan"`
	Components     []ComponentStatus `json:"components"`
	Configuration  map[string]bool   `json:"configuration"`
	Ready          bool              `json:"ready"`
	CurrentCatalog bool              `json:"current_catalog"`
}

type Runner struct {
	Journal  Journal
	Transfer Transfer
	// RetireGuard is consulted before an earlier version's files are removed.
	// ComfyUI's earlier versions stay until a guard confirms no registered
	// application entry still names them.
	RetireGuard          RetireGuard
	featureCheck         func() (FeatureStatus, error)                // focused qualification seam only
	modelHostFeatureAdd  func(context.Context) (FeatureStatus, error) // focused maintenance seam
	modelHostHomeAllowed func() error                                 // focused isolation check seam
	healthCheck          HealthCheck                                  // test seam; packageHealth otherwise
	crashAfter           func(step string) error                      // test seam: stop between an effect and its journal record
}

func NewRunner(j Journal, transfer Transfer) Runner {
	if transfer == nil {
		transfer = PublicTransfer{}
	}
	return Runner{Journal: j, Transfer: transfer}
}

// Start records the selected fixed choices. It performs no network or install
// effect. Panel calls it only after the person confirms the displayed plan.
func (r Runner) Start(ctx context.Context, selected []AppID) (Status, error) {
	plan, err := r.Journal.catalog().Plan(selected)
	if err != nil {
		return Status{}, err
	}
	if _, err := r.Journal.SavePlan(ctx, plan); err != nil {
		return Status{}, err
	}
	return r.Status(ctx)
}

// Status reads the app's action journal and the public runtime's work status.
// A transport failure leaves work unknown; it never becomes a failed transfer.
func (r Runner) Status(ctx context.Context) (Status, error) {
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return Status{}, err
	}
	if state.Plan.ID == "" {
		return Status{Configuration: map[string]bool{}}, nil
	}
	if err := r.Journal.Authorize(state.Plan); err != nil {
		if priorErr := r.Journal.recognizePriorPlan(state.Plan); priorErr != nil {
			return Status{}, priorErr
		}
		return r.priorStatus(state), nil
	}
	return r.status(ctx, state), nil
}

// priorStatus reports old work without observing or advancing its job. An
// installed row is shown as installed only after its pinned receipt and files
// still pass verification. The returned plan is visibly review-only.
func (r Runner) priorStatus(state State) Status {
	plan := state.Plan
	plan.Preparable = false
	plan.Ready = false
	plan.Blockers = append(slices.Clone(plan.Blockers), "Review the current release before further app setup")
	status := Status{Plan: plan, Configuration: state.Configuration}
	for _, component := range state.Plan.Components {
		effect := state.Effects[component.ID]
		if effect.Phase == Installed {
			artifact, err := r.Journal.knownInstalledArtifact(state, component.ID)
			if err == nil {
				var destination string
				destination, err = installPath(artifact)
				if err == nil {
					var installed bool
					installed, err = archiveInstalled(artifact, destination)
					if err == nil && !installed {
						err = errors.New("installed package receipt or files changed")
					}
				}
			}
			if err != nil {
				effect.Phase = Uncertain
				effect.Reason = err.Error()
			}
		}
		status.Components = append(status.Components, ComponentStatus{ID: component.ID, Effect: effect})
	}
	return status
}

func (r Runner) status(ctx context.Context, state State) Status {
	status := Status{Plan: state.Plan, Configuration: state.Configuration, Ready: state.Ready(), CurrentCatalog: true}
	for _, component := range state.Plan.Components {
		effect := state.Effects[component.ID]
		row := ComponentStatus{ID: component.ID, Effect: effect}
		if effect.Phase == Requested && effect.Work != nil {
			observation, err := r.Transfer.Observe(ctx, *effect.Work)
			if err != nil {
				observation = TransferObservation{State: "unknown", Failure: err.Error()}
			}
			row.Transfer = &observation
		}
		status.Components = append(status.Components, row)
	}
	return status
}

// Advance performs at most one recoverable transfer transition. It never
// executes a package; the fixed portable handler consumes Verified later.
func (r Runner) Advance(ctx context.Context, componentID string) (Status, error) {
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return Status{}, err
	}
	if state.ApprovedPlanID == "" || state.ApprovedPlanID != state.Plan.ID {
		return Status{}, errors.New("setup plan has not been approved")
	}
	a, err := r.Journal.ComponentArtifact(state, componentID)
	if err != nil {
		return r.status(ctx, state), err
	}
	if err := r.requireLemonadeFeature(a); err != nil {
		return r.status(ctx, state), err
	}
	if !state.Plan.Preparable {
		return r.status(ctx, state), errors.New("setup packages are not fully pinned and reviewed")
	}
	component, _ := planComponent(state.Plan, componentID)
	for _, dependency := range component.Dependencies {
		if state.Effects[dependency].Phase != Installed {
			return r.status(ctx, state), fmt.Errorf("%s waits for %s to be installed", componentID, dependency)
		}
	}
	effect := state.Effects[componentID]
	pin := a.DownloadPin()
	// An update publishes the new version beside the retained one and stops
	// at Staged; the health check and activation follow as separate steps.
	// A first ComfyUI installation passes the same steps.
	published := Installed
	if effect.Retained != nil || firstInstallChecked(componentID) {
		published = Staged
	}
	if (effect.Phase == Installed || effect.Phase == Failed) && len(effect.Retired) > 0 {
		return r.retire(ctx, state, componentID)
	}
	switch effect.Phase {
	case Pending:
		if a.Action != "archive_per_user" {
			return r.status(ctx, state), errors.New("this package has no fixed installer handler")
		}
		if effect.Retained != nil {
			if err := r.checkChange(componentID, a, *effect.Retained); err != nil {
				return r.status(ctx, state), err
			}
		}
		destination, err := installPath(a)
		if err != nil {
			return r.status(ctx, state), err
		}
		if installed, err := archiveInstalled(a, destination); err != nil {
			return r.status(ctx, state), err
		} else if installed {
			state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: published, Reason: "already installed at the pinned identity"})
			if err != nil {
				return Status{}, err
			}
			return r.status(ctx, state), nil
		}
		if err := absent(destination); err != nil {
			return r.status(ctx, state), fmt.Errorf("existing app data is preserved: %w", err)
		}
		if componentID == "pytorch-engine" {
			staged, err := r.stageInstalledPyTorchInputs(ctx, a)
			if err != nil {
				return r.status(ctx, state), err
			}
			state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Verified, StagedPath: staged})
			return r.status(ctx, state), err
		}
		key := workKey(componentID, a)
		work, err := r.Transfer.Reserve(ctx, pin, key)
		if err != nil {
			return r.status(ctx, state), err
		}
		state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Requested, Work: &work})
		if err != nil {
			return Status{}, err
		}
		if err := r.Transfer.Submit(ctx, pin, work); err != nil {
			return r.status(ctx, state), err // exact identity remains for reconcile
		}
	case Requested:
		if effect.Work == nil {
			return r.status(ctx, state), errors.New("requested transfer has no public job identity")
		}
		if err := r.Transfer.Submit(ctx, pin, *effect.Work); err != nil {
			return r.status(ctx, state), err
		}
		observation, err := r.Transfer.Observe(ctx, *effect.Work)
		if err != nil {
			return r.status(ctx, state), err
		}
		switch observation.State {
		case "complete":
			staged, err := r.stage(ctx, a, *effect.Work)
			if err != nil {
				return r.status(ctx, state), err
			}
			state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Verified, StagedPath: staged})
			if err != nil {
				return Status{}, err
			}
		case "failed", "cancelled":
			state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Failed, Reason: observation.Failure})
			if err != nil {
				return Status{}, err
			}
		}
	case Verified:
		if a.Action != "archive_per_user" {
			return r.status(ctx, state), errors.New("this package has no fixed installer handler")
		}
		if err := verifyFile(effect.StagedPath, a); err != nil {
			return r.status(ctx, state), err
		}
		destination, err := installPath(a)
		if err != nil {
			return r.status(ctx, state), err
		}
		state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Installing})
		if err != nil {
			return Status{}, err
		}
		// Installing is on record before any file is extracted.
		if r.crashAfter != nil {
			if err := r.crashAfter("installing"); err != nil {
				return Status{}, err
			}
		}
		installErr := InstallArchive(ctx, a, effect.StagedPath, destination)
		if r.crashAfter != nil {
			if err := r.crashAfter("install"); err != nil {
				return Status{}, err
			}
		}
		if errors.Is(installErr, identity.ErrNotLocalPath) {
			// InstallArchive refused the folder before writing anything; it
			// is not probed either, since a probe would open it.
			reason := "archive installation refused: " + installErr.Error()
			state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, state.Effects[componentID], Effect{Phase: Uncertain, Reason: reason})
			if err != nil {
				return Status{}, err
			}
			return r.status(ctx, state), installErr
		}
		installed, probeErr := archiveInstalled(a, destination)
		if probeErr == nil && installed {
			state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, state.Effects[componentID], Effect{Phase: published})
			if err != nil {
				return Status{}, err
			}
			return r.status(ctx, state), nil
		}
		reason := "archive installation outcome needs inspection"
		if installErr != nil {
			reason += ": " + installErr.Error()
		}
		if probeErr != nil {
			reason += "; identity check: " + probeErr.Error()
		}
		state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, state.Effects[componentID], Effect{Phase: Uncertain, Reason: reason})
		if err != nil {
			return Status{}, err
		}
		return r.status(ctx, state), errors.New(reason)
	case Installing:
		// A process may have died after publishing the tree but before writing
		// its final journal transition. Reconcile the exact install identity.
		destination, err := installPath(a)
		if err != nil {
			return r.status(ctx, state), err
		}
		installed, err := archiveInstalled(a, destination)
		if err != nil {
			return r.status(ctx, state), err
		}
		if installed {
			state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: published})
		} else {
			state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Uncertain, Reason: "installation not confirmed; retry requires explicit recovery"})
		}
		if err != nil {
			return Status{}, err
		}
	case Staged:
		return r.checkHealth(ctx, state, componentID, a, effect)
	case Checked:
		return r.activate(ctx, state, componentID, a, effect)
	case Installed, Uncertain, Failed:
		// A status read never retries a potentially executed installer.
	default:
		return r.status(ctx, state), errors.New("invalid component phase")
	}
	return r.status(ctx, state), nil
}

// Retry advances only a known failed public transfer. The job service judges
// attempt eligibility. An uncertain installer is never relaunched here.
func (r Runner) Retry(ctx context.Context, componentID string) (Status, error) {
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return Status{}, err
	}
	if state.ApprovedPlanID == "" || state.ApprovedPlanID != state.Plan.ID {
		return Status{}, errors.New("setup plan has not been approved")
	}
	a, err := r.Journal.ComponentArtifact(state, componentID)
	if err != nil {
		return Status{}, err
	}
	if componentID == "pytorch-engine" && state.Effects[componentID].Phase == Staged {
		if err := r.requireLemonadeFeature(a); err != nil {
			return r.status(ctx, state), err
		}
		inputs, err := installPath(a)
		if err != nil {
			return r.status(ctx, state), err
		}
		state, err = r.installPyTorchComponent(ctx, state, a, inputs, true)
		return r.status(ctx, state), err
	}
	if err := r.requireLemonadeFeature(a); err != nil {
		return r.status(ctx, state), err
	}
	if !state.Plan.Preparable {
		return r.status(ctx, state), errors.New("setup packages are not fully pinned and reviewed")
	}
	component, _ := planComponent(state.Plan, componentID)
	for _, dependency := range component.Dependencies {
		if state.Effects[dependency].Phase != Installed {
			return r.status(ctx, state), fmt.Errorf("%s waits for %s to be installed", componentID, dependency)
		}
	}
	effect := state.Effects[componentID]
	published := Installed
	if effect.Retained != nil || firstInstallChecked(componentID) {
		published = Staged
	}
	if effect.Phase == Failed && (effect.Retained != nil || firstInstallChecked(componentID)) && effect.StagedPath != "" && len(effect.Retired) == 0 && a.Action == "archive_per_user" {
		// A staged update that failed its check or changed before activation
		// was removed. Its verified download remains; an explicit retry
		// installs it again beside the still-active earlier version.
		destination, err := installPath(a)
		if err != nil {
			return r.status(ctx, state), err
		}
		if err := absent(destination); err != nil {
			return r.status(ctx, state), fmt.Errorf("existing app data is preserved: %w", err)
		}
		if err := verifyFile(effect.StagedPath, a); err != nil {
			return r.status(ctx, state), err
		}
		state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Verified, StagedPath: effect.StagedPath, Reason: "explicit retry of the update"})
		return r.status(ctx, state), err
	}
	if effect.Phase == Uncertain && a.Action == "archive_per_user" {
		destination, err := installPath(a)
		if err != nil {
			return r.status(ctx, state), err
		}
		installed, err := archiveInstalled(a, destination)
		if err != nil {
			return r.status(ctx, state), err
		}
		if installed {
			state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: published})
			return r.status(ctx, state), err
		}
		if err := absent(destination); err != nil {
			return r.status(ctx, state), fmt.Errorf("existing app data is preserved: %w", err)
		}
		if err := verifyFile(effect.StagedPath, a); err != nil {
			return r.status(ctx, state), err
		}
		state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Verified, StagedPath: effect.StagedPath, Reason: "explicit archive retry after absent identity"})
		return r.status(ctx, state), err
	}
	if effect.Phase != Failed || effect.Work == nil {
		return r.status(ctx, state), errors.New("only a known failed transfer can be retried")
	}
	prior, err := r.Transfer.Observe(ctx, *effect.Work)
	if err != nil || prior.State != "failed" && prior.State != "cancelled" {
		return r.status(ctx, state), errors.New("the public service has not confirmed a failed transfer")
	}
	nextWork := *effect.Work
	nextWork.Attempt++
	state, err = r.Journal.Update(ctx, state.Plan.ID, componentID, effect, Effect{Phase: Requested, Work: &nextWork})
	if err != nil {
		return Status{}, err
	}
	if err := r.Transfer.Submit(ctx, a.DownloadPin(), nextWork); err != nil {
		return r.status(ctx, state), err
	}
	return r.status(ctx, state), nil
}

func installPath(a Artifact) (string, error) {
	if err := a.validate(); err != nil {
		return "", err
	}
	if a.Action != "archive_per_user" {
		return "", errors.New("no portable archive identity for this action")
	}
	home, err := CurrentHome()
	if err != nil {
		return "", err
	}
	destination := filepath.Join(home.Data, "OpenAbstractions", "apps", a.InstallIdentity, a.Version)
	if a.InstallIdentity == "tn" {
		destination = filepath.Join(home.Data, "OA", "e", "tn", a.Version)
		if err := theNoiseEnginePath(filepath.Join(destination, "e")); err != nil {
			return "", err
		}
	} else if a.InstallIdentity == "pt" {
		destination = filepath.Join(home.Data, "OA", "e", "pt", a.Version, "i")
	}
	// Every step that installs, probes or recovers the folder takes it from
	// here, so a folder off a local drive is refused before any of them opens
	// it (identity.LocalPath).
	if err := identity.LocalPath(destination); err != nil {
		return "", fmt.Errorf("the install folder must be on a local drive: %w", err)
	}
	return destination, nil
}

// archiveInstalled is ArchiveInstalled; tests count the runner's probes.
var archiveInstalled = ArchiveInstalled

func workKey(componentID string, a Artifact) string {
	sum := sha256.Sum256([]byte(componentID + "\x00" + a.SHA256))
	return "setup-" + hex.EncodeToString(sum[:16])
}

// stage copies service-owned completed bytes to an app-owned path and verifies
// the copied hash. A crash leaves a disposable partial; reopening reuses only
// a complete, correctly hashed artifact.
func (r Runner) stage(ctx context.Context, a Artifact, work WorkIdentity) (string, error) {
	dir := filepath.Join(filepath.Dir(r.Journal.Path), "downloads")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	target := filepath.Join(dir, a.SHA256+".zip")
	if _, err := os.Stat(target); err == nil {
		if err := verifyFile(target, a); err != nil {
			return "", err
		}
		return target, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	partial, err := os.CreateTemp(dir, ".partial-")
	if err != nil {
		return "", err
	}
	defer os.Remove(partial.Name())
	defer partial.Close()
	written, err := r.Transfer.Copy(ctx, work, &boundedWriter{writer: partial, remaining: a.Bytes})
	if err != nil {
		return "", err
	}
	if written != a.Bytes {
		return "", fmt.Errorf("download yielded %d bytes, expected %d", written, a.Bytes)
	}
	if err := partial.Sync(); err != nil {
		return "", err
	}
	if err := verifyFile(partial.Name(), a); err != nil {
		return "", err
	}
	if err := partial.Close(); err != nil {
		return "", err
	}
	// A link publishes only if target is absent. A second window cannot
	// replace a staged file while the first verifies or consumes it.
	if err := os.Link(partial.Name(), target); err != nil {
		if verifyFile(target, a) == nil { // another window published same bytes
			return target, nil
		}
		return "", err
	}
	return target, nil
}

type boundedWriter struct {
	writer    io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("download exceeds pinned artifact size")
	}
	n, err := w.writer.Write(p)
	w.remaining -= int64(n)
	return n, err
}

func verifyFile(path string, a Artifact) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || info.Size() != a.Bytes {
		return errors.New("staged artifact size differs from release pin")
	}
	sum := sha256.New()
	if _, err := io.Copy(sum, file); err != nil {
		return err
	}
	if !strings.EqualFold(hex.EncodeToString(sum.Sum(nil)), a.SHA256) {
		return errors.New("staged artifact digest differs from release pin")
	}
	return nil
}
