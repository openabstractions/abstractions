package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"slices"

	cas "github.com/openabstractions/abstraction-cas/go"
)

// ReconcilePrior settles one unfinished step of an approved plan from an
// earlier release, on the person's explicit request. Nothing of the old plan
// resumes: an open download is cancelled, an interrupted installation is
// inspected and recorded as installed or not, and a staged update that was
// never activated is removed while the version it would have replaced stays
// active. Afterwards the current release's plan can replace the old one.
func (r Runner) ReconcilePrior(ctx context.Context, componentID string) (Status, error) {
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return Status{}, err
	}
	if state.Plan.ID == "" || state.ApprovedPlanID != state.Plan.ID {
		return Status{}, errors.New("setup plan has not been approved")
	}
	if r.Journal.Authorize(state.Plan) == nil {
		return Status{}, errors.New("this plan belongs to the current release; continue it with its own steps")
	}
	if err := r.Journal.recognizePriorPlan(state.Plan); err != nil {
		return Status{}, err
	}
	component, ok := planComponent(state.Plan, componentID)
	if !ok {
		return Status{}, errors.New("component is not part of the earlier plan")
	}
	effect := state.Effects[componentID]
	next := Effect{Phase: Failed}
	var retire *Artifact
	switch effect.Phase {
	case Requested:
		reason, err := r.closePriorDownload(ctx, effect)
		if err != nil {
			return r.priorStatus(state), err
		}
		next.Reason = reason
	case Installing, Uncertain, Staged, Checked:
		if component.Artifact == nil {
			next.Reason = "the earlier release had no package for this step"
			break
		}
		a := *component.Artifact
		root, err := installPath(a)
		if err != nil {
			return r.priorStatus(state), err
		}
		installed, err := ArchiveInstalled(a, root)
		if err != nil {
			return r.priorStatus(state), err
		}
		switch {
		case installed && effect.Retained == nil && (effect.Phase == Installing || effect.Phase == Uncertain):
			next = Effect{Phase: Installed, Reason: "the earlier release's installation had finished"}
		case installed:
			retire = &a
			next.Reason = "the earlier release's staged package was never activated and is removed"
			if effect.Retained != nil {
				next.Reason += "; the installed version stays active"
			}
		case absent(root) == nil:
			next.Reason = "the earlier release's installation left nothing behind"
		default:
			return r.priorStatus(state), fmt.Errorf("the folder %s differs from the earlier release's package and was preserved; move it elsewhere, then reconcile again", root)
		}
	default:
		if len(effect.Retired) == 0 {
			return r.priorStatus(state), errors.New("this step of the earlier plan has nothing to reconcile")
		}
	}
	if slices.Contains([]Phase{Requested, Installing, Uncertain, Staged, Checked}, effect.Phase) {
		if state, err = r.Journal.reconcilePrior(ctx, state.Plan.ID, componentID, effect, next, retire); err != nil {
			return Status{}, err
		}
	}
	if len(state.Effects[componentID].Retired) > 0 {
		// The status the retire step builds would observe old downloads;
		// an earlier plan's status never does.
		if _, err := r.retire(ctx, state, componentID); err != nil {
			return r.priorStatus(state), err
		}
		if state, err = r.Journal.Load(ctx); err != nil {
			return Status{}, err
		}
	}
	return r.priorStatus(state), nil
}

// closePriorDownload ends the old plan's public job. A job the service already
// ended needs nothing; an open one is cancelled explicitly. Its bytes stay
// with the job service, which owns their retention.
func (r Runner) closePriorDownload(ctx context.Context, effect Effect) (string, error) {
	if effect.Work == nil {
		return "the earlier release's download had no job identity", nil
	}
	observation, err := r.Transfer.Observe(ctx, *effect.Work)
	if err != nil {
		return "", fmt.Errorf("the earlier release's download could not be observed; nothing changed: %w", err)
	}
	switch observation.State {
	case "complete", "failed", "cancelled":
		return "the earlier release's download had ended (" + observation.State + "); it was not installed", nil
	}
	canceller, ok := r.Transfer.(transferCanceller)
	if !ok {
		return "", errors.New("this setup cannot cancel the earlier release's download")
	}
	if err := canceller.Cancel(ctx, *effect.Work); err != nil {
		return "", fmt.Errorf("the earlier release's download was not cancelled; nothing changed: %w", err)
	}
	return "the earlier release's download was cancelled; it was not installed", nil
}

// reconcilePrior records one reconciliation of an earlier release's plan.
// Only settling transitions are accepted; none starts an effect.
func (j Journal) reconcilePrior(ctx context.Context, planID, componentID string, expected, next Effect, retire *Artifact) (State, error) {
	var saved State
	err := cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.Plan.ID == "" || state.Plan.ID != planID || state.ApprovedPlanID != planID {
			return nil, errors.New("setup plan changed; reload before continuing")
		}
		if j.Authorize(state.Plan) == nil {
			return nil, errors.New("this plan belongs to the current release")
		}
		if err := j.recognizePriorPlan(state.Plan); err != nil {
			return nil, err
		}
		current, ok := state.Effects[componentID]
		if !ok || !reflect.DeepEqual(current, expected) {
			return nil, fmt.Errorf("setup component %q changed; reload before continuing", componentID)
		}
		switch {
		case next.Phase == Failed && slices.Contains([]Phase{Requested, Installing, Uncertain, Staged, Checked}, current.Phase):
		case next.Phase == Installed && current.Retained == nil && (current.Phase == Installing || current.Phase == Uncertain):
		default:
			return nil, fmt.Errorf("an earlier plan cannot move from %s to %s", current.Phase, next.Phase)
		}
		next.Work, next.StagedPath = current.Work, current.StagedPath
		next.Retained, next.Retired = current.Retained, slices.Clone(current.Retired)
		if retire != nil {
			next.Retired = append(next.Retired, *retire)
		}
		next.Revision = current.Revision + 1
		state.Effects[componentID] = next
		saved = state
		return json.Marshal(state)
	})
	return saved, err
}
