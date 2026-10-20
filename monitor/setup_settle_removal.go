package main

import (
	"errors"
	"net/http"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// setupCancelStep is Cancel on a running Setup step: it stops the step the
// Panel names running for the component. A stopped removal continues on the
// next Remove or Install.
func (p *servicePanel) setupCancelStep(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	request, ok := decodeSetupChoice(w, r)
	if !ok {
		return
	}
	if request.ComponentID == "" || !p.cancelSetupStep(request.ComponentID) {
		http.Error(w, "no running step of this package can be cancelled", http.StatusConflict)
		return
	}
	panelJSON(w, map[string]bool{"cancelled": true})
}

// setupSettleRemoval ends a removal no Setup step runs (setup.SettleRemoval):
// an uninstall whose registration step refused or whose reply was lost, or
// one an update interrupted. A removal that runs now is refused, and the page
// waits for it. Install and Try again call it before they continue.
func (p *servicePanel) setupSettleRemoval(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	request, ok := decodeSetupChoice(w, r)
	if !ok {
		return
	}
	if request.ComponentID == "" || request.PlanID == "" {
		http.Error(w, "the component and its plan are required", http.StatusBadRequest)
		return
	}
	if _, running := p.setupActionsNow()[request.ComponentID]; running {
		http.Error(w, "the removal is running; it finishes in a moment", http.StatusConflict)
		return
	}
	if !p.setupTryStart(w) {
		return
	}
	started := false
	defer func() {
		if !started {
			p.setupActionMu.Unlock()
		}
	}()
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	current, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	if current.Plan.ID == "" || current.Plan.ID != request.PlanID {
		http.Error(w, "the approved setup plan changed; reload before continuing", http.StatusConflict)
		return
	}
	stepID := ""
	if _, err := runner.SettleRemoval(ctx, request.ComponentID); errors.Is(err, setup.ErrRemovalUnderway) {
		// The removal deleted part of the package; it finishes as its own
		// step, and the page waits for it.
		id, err := p.startNativeUninstall(runner, journal, request.ComponentID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		started, stepID = true, id
	} else if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	if stepID == "" {
		// A settled removal clears its kept failure.
		if name, _, err := nativeUninstallName(journal, request.ComponentID); err == nil {
			clearRemovalFailure(journal, name)
		}
	}
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	state.Actions, state.Outcomes, state.StepID = p.setupActionsNow(), p.setupOutcomesNow(), stepID
	panelJSON(w, state)
}
