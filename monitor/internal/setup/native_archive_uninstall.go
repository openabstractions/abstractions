package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"

	"github.com/openabstractions/abstraction-cas/go"
)

// UninstallNative removes one explicitly selected current native archive.
// unregister must observe the actual @3 registration and its owned instances;
// a lost reply leaves Removing set for the next explicit reconciliation.
func (r Runner) UninstallNative(ctx context.Context, componentID string, unregister func(context.Context, Artifact, string) error) error {
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return err
	}
	a, err := r.Journal.ComponentArtifact(state, componentID)
	if err != nil || a.NativeResources == nil || state.ApprovedPlanID != state.Plan.ID || state.Effects[componentID].Phase != Installed || unregister == nil {
		return errors.New("no approved current native archive installation")
	}
	root, err := installPath(a)
	if err != nil {
		return err
	}
	if !state.Effects[componentID].Removing {
		if _, err := matchingReceiptContext(ctx, a, root); err != nil {
			return err
		}
		state, err = r.Journal.recordNativeRemoval(ctx, state, componentID, false, 0)
		if err != nil {
			return err
		}
	}
	if err := unregister(ctx, a, root); err != nil {
		return err
	}
	kept, err := removePackageTreeContext(ctx, a, root)
	if err != nil {
		return err
	}
	_, err = r.Journal.recordNativeRemoval(ctx, state, componentID, true, kept)
	return err
}

func (j Journal) recordNativeRemoval(ctx context.Context, expected State, id string, finished bool, kept int) (State, error) {
	return j.replaceNativeEffect(ctx, expected, id, func(e Effect) (Effect, error) {
		return nativeRemovalEffect(e, finished, kept), nil
	})
}

// nativeRemovalEffect is e once a native archive's removal started, or
// finished keeping kept added or changed files.
func nativeRemovalEffect(e Effect, finished bool, kept int) Effect {
	e.Removing = !finished
	e.Revision++
	if finished {
		e.Phase = Removed
		e.StagedPath = ""
		e.Work = nil
		e.ResourceIndex = 0
		e.Retained = nil
		e.Reason = fmt.Sprintf("Application registration and package files removed; %d added or changed files kept. User data and permissions preserved", kept)
	} else {
		e.Reason = "Explicit uninstall in progress; registration and files require reconciliation"
	}
	return e
}

// ReinstallNative is called only after an explicit review and confirmation.
// Ordinary SavePlan retains Removed even for the same deterministic plan ID.
func (j Journal) ReinstallNative(ctx context.Context, expected State, id string, confirmed bool) (State, error) {
	if !confirmed {
		return State{}, errors.New("explicit reinstall confirmation required")
	}
	return j.replaceNativeEffect(ctx, expected, id, func(e Effect) (Effect, error) {
		if e.Phase != Removed || e.Removing {
			return e, errors.New("only a completed native uninstall can be reviewed for reinstall")
		}
		e.Phase = Pending
		e.Revision++
		e.Reason = "Explicit reinstall approved for the reviewed package"
		e.Work = nil
		e.StagedPath = ""
		e.ResourceIndex = 0
		e.Retained = nil
		return e, nil
	})
}

func (j Journal) replaceNativeEffect(ctx context.Context, expected State, id string, replace func(Effect) (Effect, error)) (State, error) {
	var result State
	err := cas.ChangeLimitContext(ctx, j.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		state, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if state.Plan.ID != expected.Plan.ID || state.ApprovedPlanID != state.Plan.ID || !reflect.DeepEqual(state.Effects[id], expected.Effects[id]) {
			return nil, errors.New("native uninstall journal changed")
		}
		if err := j.Authorize(state.Plan); err != nil {
			return nil, err
		}
		a, err := j.ComponentArtifact(state, id)
		prior, priorErr := j.ComponentArtifact(expected, id)
		if err != nil || priorErr != nil || a.NativeResources == nil || !reflect.DeepEqual(a, prior) {
			return nil, errors.New("native uninstall source changed")
		}
		e, err := replace(state.Effects[id])
		if err != nil {
			return nil, err
		}
		state.Effects[id] = e
		result = state
		return json.Marshal(state)
	})
	return result, err
}
