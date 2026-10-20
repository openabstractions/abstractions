package main

import (
	"context"
	"errors"
	"fmt"
)

type applicationLoginState struct {
	enabled bool
	changed <-chan struct{}
}

type runtimeLogonApply func(context.Context, bool) error

// This first source slice establishes the service-owned reconciliation. The
// package, platform integration and demand-activation companions must qualify
// their real backends before any platform can report successful reconciliation.
var errRuntimeLogonPrerequisite = errors.New("runtime login integration prerequisites are not qualified")

func runtimeLogonUnavailable(platform string) error {
	return fmt.Errorf("%w: %s package and activation companion required", errRuntimeLogonPrerequisite, platform)
}

// watchRuntimeLogon serializes platform operations over fresh service snapshots.
// A change during an operation is observed immediately afterwards; a previous
// removal therefore cannot finish after a newer registration's platform write.
// Failures are reported and retried only on another directory notification.
// No registration, permission, accepted work or process is rolled back here.
func watchRuntimeLogon(parent context.Context, read func(context.Context) (applicationLoginState, error), apply runtimeLogonApply, report func(error)) func() {
	ctx, cancel := context.WithCancel(parent)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var applied bool
		haveApplied := false
		for {
			if ctx.Err() != nil {
				return
			}
			state, err := read(ctx)
			if err != nil {
				if ctx.Err() == nil && report != nil {
					report(fmt.Errorf("runtime login snapshot: %w", err))
				}
				return
			}
			if !haveApplied || state.enabled != applied {
				if err := apply(ctx, state.enabled); err != nil {
					// A platform operation may have changed one of several entries
					// before failing. Its previous successful state is no longer a
					// trustworthy basis for skipping the next reconciliation.
					haveApplied = false
					if ctx.Err() == nil && report != nil {
						report(fmt.Errorf("runtime login reconciliation: %w", err))
					}
				} else {
					applied, haveApplied = state.enabled, true
				}
			}
			select {
			case <-ctx.Done():
				return
			case <-state.changed:
			}
		}
	}()
	return func() {
		cancel()
		<-done
	}
}
