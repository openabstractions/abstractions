package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

var nativeUninstallProgram = setup.NativeArchiveProgram // focused source-observation seam

func (p *servicePanel) setupUninstall(w http.ResponseWriter, r *http.Request) {
	if !p.setupTryStart(w) {
		return
	}
	started := false
	defer func() {
		if !started {
			p.setupActionMu.Unlock()
		}
	}()
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	request, ok := decodeSetupChoice(w, r)
	if !ok {
		return
	}
	if !request.Confirm || request.ComponentID == "" {
		http.Error(w, "explicit confirmation of the selected application uninstall is required", http.StatusBadRequest)
		return
	}
	runner, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	state, err := setupStatus(ctx, runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	if request.PlanID == "" || state.Plan.ID != request.PlanID || !state.Approved {
		http.Error(w, "the approved plan changed", http.StatusConflict)
		return
	}
	stepID, err := p.startNativeUninstall(runner, journal, request.ComponentID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	started = true
	state.Actions, state.Outcomes, state.StepID = p.setupActionsNow(), p.setupOutcomesNow(), stepID
	panelJSON(w, state)
}

// nativeUninstallStep runs the removal step; tests replace it.
var nativeUninstallStep = (*servicePanel).runNativeUninstall

// nativeUninstallTimeout bounds one removal step; tests shorten it.
var nativeUninstallTimeout = 2 * time.Hour

// startNativeUninstall starts the removal of one native application
// component as a running "remove" step with progress and Cancel: its
// registration and the package files verified by its receipt. The
// caller holds setupActionMu; on a nil error the step owns it and unlocks it
// when it ends. A large package can take longer than the page's request. A
// removal stopped part way continues from the folder it moved aside.
func (p *servicePanel) startNativeUninstall(runner setup.Runner, journal setup.Journal, componentID string) (string, error) {
	name, title, err := nativeUninstallName(journal, componentID)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), nativeUninstallTimeout)
	progress, end := p.beginSetupStep(componentID, "remove", ctx, cancel)
	stepID := p.setupActionsNow()[componentID].StepID
	go func() {
		var failure error
		defer p.setupActionMu.Unlock()
		defer cancel()
		defer end()
		// The outcome is kept before the step stops being listed, so a page
		// that finds it ended reads how it ended.
		defer func() {
			if r := recover(); r != nil {
				failure = fmt.Errorf("the removal stopped unexpectedly: %v", r)
				log.Printf("setup: remove %s: %v", componentID, r)
			}
			outcome := setupOutcome{Kind: "remove", StepID: stepID}
			switch {
			case failure == nil:
				clearRemovalFailure(journal, name)
			case errors.Is(failure, context.Canceled) || errors.Is(ctx.Err(), context.Canceled):
				// A Cancel by the person is no failure.
				outcome.Cancelled = true
			default:
				outcome.Failure, outcome.Detail = stepFailureReason(failure), failure.Error()
				recordRemovalFailure(journal, name, title, failure)
			}
			p.recordSetupOutcome(componentID, outcome)
		}()
		failure = nativeUninstallStep(p, setup.WithProgress(ctx, progress), runner, name, componentID)
		if failure != nil {
			log.Printf("setup: remove %s: %v", componentID, failure)
		}
	}()
	return stepID, nil
}

// removalReason is failure's first line without its closing period, at
// most 200 bytes.
func removalReason(failure error) string {
	reason := strings.TrimSuffix(strings.TrimSpace(strings.SplitN(failure.Error(), "\n", 2)[0]), ".")
	if len(reason) > 200 {
		reason = strings.ToValidUTF8(reason[:200], "")
	}
	return reason
}

// clearRemovalFailure drops name's kept removal failure once a later
// removal of it succeeded.
func clearRemovalFailure(journal setup.Journal, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	record, err := journal.LoadInstall(ctx)
	if err != nil || record.Failure == nil || record.Failure.Component != name {
		return
	}
	if _, err := journal.MarkInstall(ctx, record.PlanID, func(r *setup.InstallRecord) {
		if r.Failure != nil && r.Failure.Component == name {
			r.Failure = nil
		}
	}); err != nil {
		log.Printf("setup: remove %s: clear the kept failure: %v", name, err)
	}
}

// recordRemovalFailure keeps a removal step's failure on the install record,
// which the page shows when it reloads: the step ends after its request has
// answered.
func recordRemovalFailure(journal setup.Journal, name, title string, failure error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	record, err := journal.LoadInstall(ctx)
	if err != nil || record.PlanID == "" {
		log.Printf("setup: remove %s: no install record keeps the failure: %v", name, err)
		return
	}
	sentence := "Removing " + title + " stopped: " + removalReason(failure) + "."
	if plain := serviceFailureSentence(failure); plain != "" {
		sentence = "Removing " + title + " stopped. " + plain
	}
	if _, err := journal.RecordInstallFailure(ctx, record.PlanID, setup.InstallFailure{Component: name, Sentence: sentence, Detail: failure.Error()}); err != nil {
		log.Printf("setup: remove %s: keep the failure: %v", name, err)
	}
}

// nativeUninstallName is the application choice componentID belongs to,
// and its title.
func nativeUninstallName(journal setup.Journal, componentID string) (string, string, error) {
	name, title := "", ""
	for _, choice := range journal.Catalog().Choices {
		if choice.Application && slices.Contains(choice.Components, componentID) {
			if name != "" {
				return "", "", errors.New("ambiguous application component")
			}
			name, title = string(choice.ID), choice.Title
		}
	}
	if name == "" {
		return "", "", errors.New("no selected native application component")
	}
	if title == "" {
		title = name
	}
	return name, title, nil
}

func (p *servicePanel) runNativeUninstall(ctx context.Context, runner setup.Runner, name, componentID string) error {
	return runner.UninstallNative(ctx, componentID, func(ctx context.Context, a setup.Artifact, root string) error {
		return p.unregisterNativeInstallation(ctx, name, a, root)
	})
}

func (p *servicePanel) setupReinstall(w http.ResponseWriter, r *http.Request) {
	if !p.setupTryStart(w) {
		return
	}
	defer p.setupActionMu.Unlock()
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	request, ok := decodeSetupChoice(w, r)
	if !ok {
		return
	}
	if !request.Confirm || request.ComponentID == "" {
		http.Error(w, "review and confirm the selected package reinstall", http.StatusBadRequest)
		return
	}
	_, journal, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	state, err := journal.Load(r.Context())
	if err != nil {
		panelError(w, err)
		return
	}
	if request.PlanID == "" || request.PlanID != state.Plan.ID {
		http.Error(w, "reviewed plan changed", http.StatusConflict)
		return
	}
	if _, err := journal.ReinstallNative(r.Context(), state, request.ComponentID, true); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	runner, _, err := p.setupRunnerForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	status, err := setupStatus(r.Context(), runner, journal)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, status)
}

func (p *servicePanel) unregisterNativeInstallation(ctx context.Context, name string, a setup.Artifact, root string) error {
	apps, err := p.nativeApplicationsForPanel(ctx, name)
	if err != nil {
		return err
	}
	native, ok := apps.(*nativeApplications)
	if !ok {
		return errors.New("native uninstall requires actual application ownership observations")
	}
	page, err := native.Observe(ctx, "", 0)
	if err != nil {
		return err
	}
	if page.Outcome != fwire.ApplicationOutcomePage {
		return errors.New("application ownership observation refused")
	}
	if native.owned {
		return errors.New("close the application's owned instance before uninstalling")
	}
	if len(page.Applications) == 0 {
		return nil
	}
	actual := page.Applications[0].Descriptor.Program
	expected, err := fileid.Pending(ctx, fileid.Self, filepath.Join(root, filepath.FromSlash(a.ApplicationProgram)))
	if err != nil {
		return err
	}
	observed, err := fileid.Pending(ctx, fileid.Self, actual)
	if err != nil {
		return err
	}
	if !fileid.SamePathName(expected.Path(), observed.Path()) {
		if setup.ProgramWithin(root, actual) {
			return errors.New("a user-replaced registration still names this installation; files preserved")
		}
		return nil // A different version/foreign registration stays registered.
	}
	if _, err := nativeUninstallProgram(ctx, a, root); err != nil {
		return err
	}
	change, err := native.Remove(ctx, name)
	if err != nil {
		return err
	}
	if change.Outcome != fwire.ApplicationOutcomeApplied {
		return errors.New("application registration removal was not confirmed; retry explicit uninstall")
	}
	page, err = native.Observe(ctx, "", 0)
	if err != nil {
		return err
	}
	if page.Outcome != fwire.ApplicationOutcomePage || native.owned {
		return errors.New("application removal readback is uncertain; files preserved")
	}
	for _, row := range page.Applications {
		if setup.ProgramWithin(root, row.Descriptor.Program) {
			return errors.New("a concurrent registration names this installation; files preserved")
		}
	}
	return nil
}
