package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// Repair on an app row puts back an adopter's fix-up that the app's own
// updater replaced, per the Adopter fix-ups and Repair contract in
// internal/setup/README.md. The catalogue names the
// file and its hashes (setup.Fixup); the Panel only reads the file, and the
// adopter's installer, repaired through Windows Installer, stays its only
// writer. Repair runs as a Setup step: it refuses while another step runs
// and while the app is open.

// setupFixupCheck reads the installed apps' fix-ups; setupFixupRepair runs
// one Repair. Tests replace both.
var (
	setupFixupCheck = func(ctx context.Context, catalog setup.Catalog) ([]setup.FixupStatus, error) {
		return catalog.CheckFixups(ctx)
	}
	setupFixupRepair = func(ctx context.Context, catalog setup.Catalog, componentID string) (setup.FixupStatus, error) {
		return catalog.RepairFixup(ctx, componentID)
	}
)

// setupRepairTimeout bounds one Repair; tests shorten it.
var setupRepairTimeout = 15 * time.Minute

// setupRepairIsolated refuses Repair from a Panel serving an isolated
// runtime: the app and its adopter package are this account's own.
var setupRepairIsolated = func() bool {
	home, err := setup.CurrentHome()
	return err != nil || home.Isolated
}

func (p *servicePanel) setupRepair(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
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
	request, ok := decodeSetupChoice(w, r)
	if !ok {
		return
	}
	if !request.Confirm || request.ComponentID == "" || request.PlanID != "" || len(request.Selected) != 0 || len(request.Beside) != 0 ||
		request.ModelPlanID != "" || request.CodingModelPlanID != "" || request.PermissionsID != "" || request.Engine != "" {
		http.Error(w, "confirm the repair of one component", http.StatusBadRequest)
		return
	}
	journal, err := p.setupJournalForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	catalog := journal.Catalog()
	ctx, cancel := panelCall(r)
	defer cancel()
	statuses, err := setupFixups(ctx, catalog)
	if err != nil {
		log.Printf("setup: repair %s: %v", request.ComponentID, err)
		http.Error(w, "Setup could not read what needs repair.", http.StatusConflict)
		return
	}
	var status *setup.FixupStatus
	for i := range statuses {
		if statuses[i].Component == request.ComponentID {
			status = &statuses[i]
		}
	}
	switch {
	case status == nil:
		http.Error(w, "Nothing here needs repair.", http.StatusConflict)
		return
	case setupRepairIsolated():
		http.Error(w, "Repair from the installed OpenAbstractions Panel.", http.StatusConflict)
		return
	case !status.Repair:
		http.Error(w, setupRepairSentence(*status), http.StatusConflict)
		return
	case status.Running:
		http.Error(w, status.Close, http.StatusConflict)
		return
	}
	stepID := p.startSetupRepair(catalog, *status)
	started = true
	panelJSON(w, map[string]any{"step_id": stepID, "actions": p.setupActionsNow(), "outcomes": p.setupOutcomesNow()})
}

// setupFixups are the installed apps' fix-ups as the page shows them: a
// Panel serving an isolated runtime offers no Repair.
func setupFixups(ctx context.Context, catalog setup.Catalog) ([]setup.FixupStatus, error) {
	fixes, err := setupFixupCheck(ctx, catalog)
	if err == nil && setupRepairIsolated() {
		for i := range fixes {
			fixes[i].Repair = false
		}
	}
	return fixes, err
}

// setupRepairSentence is the row's sentence for a fix-up Repair cannot fix.
func setupRepairSentence(status setup.FixupStatus) string {
	if status.Sentence == "" {
		return "Nothing here needs repair."
	}
	return status.Sentence + "."
}

// startSetupRepair runs the Repair as a "repair" step of the component. The
// caller holds setupActionMu; the step owns it and unlocks it when it ends.
func (p *servicePanel) startSetupRepair(catalog setup.Catalog, status setup.FixupStatus) string {
	ctx, cancel := context.WithTimeout(context.Background(), setupRepairTimeout)
	_, end := p.beginSetupStep(status.Component, "repair", ctx, nil)
	stepID := p.setupActionsNow()[status.Component].StepID
	go func() {
		var failure error
		var after setup.FixupStatus
		defer p.setupActionMu.Unlock()
		defer cancel()
		defer end()
		defer func() {
			if r := recover(); r != nil {
				failure = fmt.Errorf("the repair stopped unexpectedly: %v", r)
			}
			outcome := setupOutcome{Kind: "repair", StepID: stepID}
			if failure != nil {
				log.Printf("setup: repair %s: %v", status.Component, failure)
				outcome.Failure = setupRepairFailure(failure, status, after)
				outcome.Detail = failure.Error()
			}
			p.recordSetupOutcome(status.Component, outcome)
		}()
		after, failure = setupFixupRepair(ctx, catalog, status.Component)
	}()
	return stepID
}

// setupRepairFailure is the sentence a failed Repair leaves on its row; the
// step's error, with any Windows Installer code, goes to its technical
// details.
func setupRepairFailure(failure error, before, after setup.FixupStatus) string {
	switch {
	case after.Running && errors.Is(failure, setup.ErrFixupAppRunning):
		return before.Close
	case errors.Is(failure, setup.ErrFixupInstallerBusy):
		return "Another installation is running. Press Repair when it finishes."
	case after.State == setup.FixupUnknown && after.Sentence != "":
		return after.Sentence + "."
	}
	return "Repair did not finish."
}
