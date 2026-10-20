package main

import (
	"context"
	"fmt"

	facade "github.com/openabstractions/abstraction-facade/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// pictureDefaultStore reads and writes the account's selection policies; the
// inference selection operator is one. Tests replace it.
type pictureDefaultStore interface {
	ReadAccountDefault(ctx context.Context, name string) (iclient.SelectionReadV2, error)
	PutAccountDefault(ctx context.Context, revision string, policy iclient.SelectionPolicyV2) (iclient.SelectionChange, error)
}

var setupPictureDefaultStore = func(ctx context.Context) (pictureDefaultStore, error) {
	return panelMachine().ResolveInferenceSelectionOperatorV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// setupFillImageDefault names a prepared picture model in the account's
// picture policy when the policy has no candidate. A policy the person set
// stays as it is. Setup runs it whenever it reads its overview and after a
// picture model becomes ready, which heals installs that never wrote one.
func setupFillImageDefault(ctx context.Context, journal setup.Journal) error {
	model, ok, err := setup.InstalledPictureModel(ctx, journal)
	if err != nil || !ok {
		return err
	}
	store, err := setupPictureDefaultStore(ctx)
	if err != nil {
		return err
	}
	read, err := store.ReadAccountDefault(ctx, imagePolicy)
	if err != nil {
		return err
	}
	switch read.Outcome {
	case iwire.SelectionReadOutcomeFound:
		if read.Policy != nil && len(read.Policy.Candidates) > 0 {
			return nil
		}
	case iwire.SelectionReadOutcomeUnknown:
	default:
		return fmt.Errorf("the picture default cannot be read: %s", read.Outcome)
	}
	change, err := store.PutAccountDefault(ctx, read.Revision, localFirstPolicy(imagePolicy, read, model))
	if err != nil {
		return err
	}
	if change.Outcome != iwire.EditOutcomeApplied {
		return fmt.Errorf("the picture default was not written: %s %s", change.Outcome, change.Reason)
	}
	return nil
}
