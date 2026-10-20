package main

import (
	"context"
	"fmt"
	"strconv"
	"time"
)

// A Setup package step (setupActionCall) can run for many minutes with no
// progress event: extracting a large archive, rehashing an activation,
// verifying a staging copy, or checking every file before removal.
// The Panel state names each step running now,
// what it does and how far it is, so the page says so and offers Cancel; the
// page's stall clock waits for it and Try again does not queue behind it.
// The step's own timeout bounds it.

// setupAction is one running Setup step: what it does ("remove" or
// "install"), when it started, when its timeout ends it, and the count of
// its items done and in all with the time of the last change.
type setupAction struct {
	Kind       string `json:"kind,omitempty"`
	StartedAt  string `json:"started_at"`
	Deadline   string `json:"deadline,omitempty"`
	Done       int    `json:"done,omitempty"`
	Total      int    `json:"total,omitempty"`
	ProgressAt string `json:"progress_at,omitempty"`
	// Cancellable is set when Cancel (setupCancelStep) can stop the step.
	Cancellable bool   `json:"cancellable,omitempty"`
	StepID      string `json:"step_id,omitempty"`
}

// setupOutcome is how a component's last step ended: its kind, end time
// and id, and its failure, or Cancelled for a Cancel by the person. A step
// can end before the page reads the state; the page reads the outcome then.
type setupOutcome struct {
	Kind    string `json:"kind,omitempty"`
	EndedAt string `json:"ended_at"`
	StepID  string `json:"step_id"`
	Failure string `json:"failure,omitempty"`
	// Detail is the failure's technical text, for the row's technical
	// details.
	Detail    string `json:"detail,omitempty"`
	Cancelled bool   `json:"cancelled,omitempty"`
}

// recordSetupOutcome keeps componentID's step outcome.
func (p *servicePanel) recordSetupOutcome(componentID string, outcome setupOutcome) {
	outcome.EndedAt = setupNow().UTC().Format(time.RFC3339)
	p.setupActionsMu.Lock()
	defer p.setupActionsMu.Unlock()
	if p.setupOutcomes == nil {
		p.setupOutcomes = map[string]setupOutcome{}
	}
	p.setupOutcomes[componentID] = outcome
}

// setupOutcomesNow is a copy of the step outcomes; nil when none ended.
func (p *servicePanel) setupOutcomesNow() map[string]setupOutcome {
	p.setupActionsMu.Lock()
	defer p.setupActionsMu.Unlock()
	if len(p.setupOutcomes) == 0 {
		return nil
	}
	out := make(map[string]setupOutcome, len(p.setupOutcomes))
	for id, o := range p.setupOutcomes {
		out[id] = o
	}
	return out
}

// setupNow is the clock of the running-step records; tests replace it.
// setupStepPrefix keeps step ids unique across Panel restarts.
var setupStepPrefix = strconv.FormatInt(time.Now().UnixNano(), 36)

var setupNow = time.Now

// beginSetupAction records componentID's step as running until the returned
// function is called.
func (p *servicePanel) beginSetupAction(componentID string, ctx context.Context) func() {
	_, end := p.beginSetupStep(componentID, "", ctx, nil)
	return end
}

// beginSetupStep records componentID's step of kind as running until end is
// called. progress records its items done and in all; cancel, when given,
// is what Cancel (setupCancelStep) calls.
func (p *servicePanel) beginSetupStep(componentID, kind string, ctx context.Context, cancel context.CancelFunc) (progress func(done, total int), end func()) {
	now := setupNow().UTC().Format(time.RFC3339)
	action := setupAction{Kind: kind, StartedAt: now, ProgressAt: now, Cancellable: cancel != nil}
	if deadline, ok := ctx.Deadline(); ok {
		action.Deadline = deadline.UTC().Format(time.RFC3339)
	}
	p.setupActionsMu.Lock()
	p.setupStepSeq++
	action.StepID = fmt.Sprintf("step-%s-%d", setupStepPrefix, p.setupStepSeq)
	if p.setupActions == nil {
		p.setupActions = map[string]setupAction{}
	}
	if p.setupCancels == nil {
		p.setupCancels = map[string]context.CancelFunc{}
	}
	p.setupActions[componentID] = action
	if cancel != nil {
		p.setupCancels[componentID] = cancel
	}
	p.setupActionsMu.Unlock()
	progress = func(done, total int) {
		p.setupActionsMu.Lock()
		defer p.setupActionsMu.Unlock()
		a, ok := p.setupActions[componentID]
		if !ok || a.StartedAt != action.StartedAt {
			return
		}
		if done != a.Done || total != a.Total {
			a.Done, a.Total, a.ProgressAt = done, total, setupNow().UTC().Format(time.RFC3339)
			p.setupActions[componentID] = a
		}
	}
	end = func() {
		p.setupActionsMu.Lock()
		delete(p.setupActions, componentID)
		delete(p.setupCancels, componentID)
		p.setupActionsMu.Unlock()
	}
	return progress, end
}

// startPackageStep owns the Panel action lock after the route has admitted
// the operation. The route supplies its package effects and failure wording.
func (p *servicePanel) startPackageStep(component, kind, panicMessage string, timeout time.Duration, cancellable bool,
	release func(), run func(context.Context, func(int, int)) error, settle func(context.Context, error, *setupOutcome)) string {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	var stepCancel context.CancelFunc
	if cancellable {
		stepCancel = cancel
	}
	progress, end := p.beginSetupStep(component, kind, ctx, stepCancel)
	stepID := p.setupActionsNow()[component].StepID
	go func() {
		var failure error
		defer p.setupActionMu.Unlock()
		if release != nil {
			defer release()
		}
		defer cancel()
		defer end()
		defer func() {
			if recovered := recover(); recovered != nil {
				failure = fmt.Errorf("%s: %v", panicMessage, recovered)
			}
			outcome := setupOutcome{Kind: kind, StepID: stepID}
			settle(ctx, failure, &outcome)
			p.recordSetupOutcome(component, outcome)
		}()
		failure = run(ctx, progress)
	}()
	return stepID
}

// cancelSetupStep cancels componentID's running step when it can be
// cancelled; it reports whether it could.
func (p *servicePanel) cancelSetupStep(componentID string) bool {
	p.setupActionsMu.Lock()
	cancel := p.setupCancels[componentID]
	p.setupActionsMu.Unlock()
	if cancel == nil {
		return false
	}
	cancel()
	return true
}

// setupActionsNow is a copy of the running steps; nil when none runs.
func (p *servicePanel) setupActionsNow() map[string]setupAction {
	p.setupActionsMu.Lock()
	defer p.setupActionsMu.Unlock()
	if len(p.setupActions) == 0 {
		return nil
	}
	out := make(map[string]setupAction, len(p.setupActions))
	for id, action := range p.setupActions {
		out[id] = action
	}
	return out
}
