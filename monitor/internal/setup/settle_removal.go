package setup

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"github.com/openabstractions/abstraction-cas/go"
)

// ErrRemovalUnderway is SettleRemoval's answer for a removal that already
// moved the package aside and deleted part of it: the Panel continues the
// removal (Runner.UninstallNative), which resumes from the moved folder.
var ErrRemovalUnderway = errors.New("the removal already deleted part of the package; it continues")

// removalUnderway reports whether root's removal moved it aside
// (removePackageTreeContext's "."+name+".removing"). The moved folder is a
// real folder; a junction or link in its place is no removal.
func removalUnderway(root string) bool {
	if _, err := os.Lstat(root); err == nil {
		return false
	}
	info, err := os.Lstat(filepath.Join(filepath.Dir(root), "."+filepath.Base(root)+".removing"))
	return err == nil && info.Mode().Type() == fs.ModeDir
}

// rootGone reports whether root no longer exists.
func rootGone(root string) bool {
	_, err := os.Lstat(root)
	return errors.Is(err, fs.ErrNotExist)
}

// SettleRemoval ends a removal no step is running: an uninstall whose
// registration step refused, whose reply was lost, or that an update
// interrupted. The caller checks that no Setup step for the component runs.
// An installed or uncertain package whose receipt and registered program
// are intact is installed. An installed one whose files changed is refused,
// and the person removes or reinstalls it; an uncertain one stays uncertain
// for its own retry. Any other phase keeps its phase. Nothing is deleted and
// nothing is registered.
func (r Runner) SettleRemoval(ctx context.Context, componentID string) (Status, error) {
	state, err := r.Journal.Load(ctx)
	if err != nil {
		return Status{}, err
	}
	if state.Plan.ID == "" || state.ApprovedPlanID != state.Plan.ID {
		return Status{}, errors.New("setup plan has not been approved")
	}
	effect := state.Effects[componentID]
	if !effect.Removing {
		return r.status(ctx, state), nil
	}
	reason := "The interrupted removal was stopped; the package stays as it was"
	phase := effect.Phase
	if phase == Installed || phase == Uncertain {
		component, ok := planComponent(state.Plan, componentID)
		if !ok || component.Artifact == nil {
			return r.status(ctx, state), errors.New("component has no reviewed release artifact")
		}
		root, err := installPath(*component.Artifact)
		if err != nil {
			return r.status(ctx, state), err
		}
		// The receipt UninstallNative checks before it records a removal, and
		// the program the registration names.
		_, receiptErr := matchingReceiptContext(ctx, *component.Artifact, root)
		intact := receiptErr == nil && (component.Artifact.ApplicationProgram == "" || verifyInstalledPath(root, component.Artifact.ApplicationProgram) == nil)
		switch {
		case intact:
			phase = Installed
			reason = "The interrupted removal was stopped; the installed files are intact"
		case phase == Installed && (removalUnderway(root) || rootGone(root)):
			// An uncertain one keeps its own retry below.
			return r.status(ctx, state), ErrRemovalUnderway
		case phase == Installed:
			return r.status(ctx, state), errors.New("the package's files changed during the interrupted removal; remove it again or reinstall it")
		default:
			reason = "The interrupted removal was stopped; Try again checks the package again"
		}
	}
	var settled State
	err = cas.ChangeLimitContext(ctx, r.Journal.Path, maxJournalBytes, func(raw []byte) ([]byte, error) {
		current, err := decodeState(raw)
		if err != nil {
			return nil, err
		}
		if current.Plan.ID != state.Plan.ID || current.ApprovedPlanID != state.ApprovedPlanID || !reflect.DeepEqual(current.Effects[componentID], effect) {
			return nil, errors.New("the removal changed while it was settled; reload and try again")
		}
		e := current.Effects[componentID]
		e.Removing = false
		e.Phase = phase
		e.Revision++
		e.Reason = reason
		current.Effects[componentID] = e
		settled = current
		return json.Marshal(current)
	})
	if err != nil {
		return r.status(ctx, state), err
	}
	return r.status(ctx, settled), nil
}
