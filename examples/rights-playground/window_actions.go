package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"

	wire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	api "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

// Private typed outcomes keep UI guidance independent of diagnostic prose.
// A refusal proves no pending occurrence; the capability owns that lifecycle.
type readRefusalError struct {
	Label   string
	Outcome wire.SecretReadOutcome
}

func (e *readRefusalError) Error() string {
	return fmt.Sprintf("Read %s refused: %s", e.Label, e.Outcome)
}

type useRefusalError struct {
	Outcome api.AcceptanceOutcome
	Reason  string
}

func (e *useRefusalError) Error() string {
	return fmt.Sprintf("Use refused: %s %s", e.Outcome, e.Reason)
}

type useFailureError struct {
	Label string
	State api.WorkState
	Cause api.FailureCause
}

func (e *useFailureError) Error() string {
	if e.Cause != "" {
		return fmt.Sprintf("Use %s: %s (%s)", e.Label, e.State, e.Cause)
	}
	return fmt.Sprintf("Use %s: %s", e.Label, e.State)
}

type windowAction struct{ Verb, Label string }
type windowCompletion struct {
	Action windowAction
	Err    error
}

// One user click makes one adapter call in this process. Completion is consumed
// by the UI thread before another click is admitted. Closing cancels the call
// and suppresses its late result; Use's adapter retains uncertain identities.
type windowActions struct {
	mu      sync.Mutex
	closed  bool
	busy    bool
	cancel  context.CancelFunc
	result  *windowCompletion
	execute func(context.Context, windowAction) error
	notify  func()
}

func newWindowActions(c connection, notify func()) *windowActions {
	return &windowActions{execute: func(ctx context.Context, a windowAction) error {
		if a.Verb == "read" {
			return readSecret(ctx, c, a.Label, io.Discard)
		}
		return use(ctx, c, a.Label, false, io.Discard)
	}, notify: notify}
}

func (a *windowActions) start(action windowAction) bool {
	if action.Verb != "read" && action.Verb != "use" {
		return false
	}
	if _, err := fixtureFor(action.Label); err != nil {
		return false
	}
	a.mu.Lock()
	if a.closed || a.busy {
		a.mu.Unlock()
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	a.busy, a.cancel = true, cancel
	a.mu.Unlock()
	go func() {
		err := a.execute(ctx, action)
		cancel()
		a.mu.Lock()
		if a.closed {
			a.mu.Unlock()
			return
		}
		a.result = &windowCompletion{Action: action, Err: err}
		a.mu.Unlock()
		a.notify()
	}()
	return true
}

func (a *windowActions) take() (windowCompletion, bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.result == nil {
		return windowCompletion{}, false
	}
	result := *a.result
	a.result, a.cancel, a.busy = nil, nil, false
	return result, true
}

func (a *windowActions) close() {
	a.mu.Lock()
	a.closed, a.result = true, nil
	cancel := a.cancel
	a.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func windowResult(c windowCompletion) string {
	name := "Read"
	if c.Action.Verb == "use" {
		name = "Use"
	}
	prefix := name + " " + c.Action.Label
	if c.Err == nil {
		if c.Action.Verb == "read" {
			return prefix + " succeeded. This app verified the dummy value and cleared its returned copy."
		}
		return prefix + " succeeded. Dummy artifact verified; the secret stayed in the runtime and receiver."
	}
	if errors.Is(c.Err, errAcceptanceUncertain) {
		return prefix + " has an uncertain outcome and may still run. Click Use secret for the same name to reconcile the retained operation."
	}
	var read *readRefusalError
	if errors.As(c.Err, &read) {
		if read.Outcome == wire.SecretReadOutcomeForbidden {
			return prefix + " was refused. Check the OA permission decision or Panel access, then click Read secret again."
		}
		return prefix + " was unavailable (" + read.Outcome.String() + "). Check the dummy secret in the Panel before trying again."
	}
	var refused *useRefusalError
	if errors.As(c.Err, &refused) {
		if refused.Outcome == api.AcceptanceOutcomeForbidden {
			return prefix + " was refused. Check the OA permission decision or Panel access, then click Use secret again."
		}
		return prefix + " was not accepted. Check the runtime and dummy setup before trying again."
	}
	var failed *useFailureError
	if errors.As(c.Err, &failed) {
		if failed.Cause == api.FailureCauseCredential {
			return prefix + " failed because the credential was unavailable or refused. Check OA or the Panel, then click Use secret again."
		}
		if failed.State == api.WorkStateCancelled {
			return prefix + " was cancelled."
		}
		return prefix + " failed. Check the isolated runtime and dummy receiver before trying again."
	}
	// Do not turn arbitrary transport/provider diagnostics into a permission
	// status, or display data returned in those diagnostics in the app window.
	return prefix + " could not complete. Check that this pack's runtime is running before trying again."
}
