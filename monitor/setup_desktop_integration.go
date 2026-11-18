package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"path/filepath"
	"time"

	"github.com/openabstractions/abstraction-storage/go/layout"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

const desktopIntegrationStep = "desktop-integration"
const desktopIntegrationTimeout = 12 * time.Minute

// unsupportedDesktopRelease names the supported releases when the official
// program is another one; tests replace it.
var unsupportedDesktopRelease = setup.Catalog.UnsupportedOfficialRelease

func decodeSetupConfirm(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return false
	}
	var request struct {
		Confirm bool `json:"confirm"`
	}
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&request); err != nil && !errors.Is(err, io.EOF) {
		http.Error(w, "invalid request", http.StatusBadRequest)
		return false
	}
	if !request.Confirm {
		http.Error(w, "explicit confirmation is required", http.StatusBadRequest)
		return false
	}
	return true
}

// The app path and publisher come from the same compiled catalog as the
// Windows application registration. The installer verifies its own host
// and process boundary before changing any files.
var verifyCatalogDesktopHost = func(app setup.AppID) error {
	official, ok := setup.OfficialProgramOf(app)
	local, err := layout.LocalAppData()
	if !ok || err != nil {
		return errors.New("The Catalog could not verify the official desktop installation.")
	}
	program := filepath.Join(local, filepath.FromSlash(official.LocalAppData))
	if signer, verified := officialSigner(program); !verified || signer != official.Signer {
		return errors.New("The installed desktop app's publisher could not be verified.")
	}
	return nil
}

var readCatalogDesktopPackage = setup.DesktopIntegration.Action

// readOfficialInstall reads Windows' records of the official installer;
// tests replace it.
var readOfficialInstall = setup.ReadOfficialInstall
var applyCatalogDesktopPackage = setup.DesktopIntegration.Apply

func readDesktopIntegration(catalog setup.Catalog, app setup.AppID) setupDesktopIntegrationFacts {
	var title string
	facts := setupDesktopIntegrationFacts{App: app, Installed: desktopProgramPresent(app)}
	for _, choice := range catalog.Choices {
		if choice.ID == app {
			title = choice.Name()
			facts.Coding = choice.Use == setup.UseCode
			break
		}
	}
	if !facts.Installed {
		if official, ok := setup.OfficialProgramOf(app); ok {
			if reason, detail, blocked := setup.OfficialInstallBlocked(title, official.LocalAppData, readOfficialInstall(app)); blocked {
				log.Printf("setup: %s installer blocked by Windows: %s", title, detail)
				facts.Reason, facts.Detail, facts.Blocked = reason, detail, true
				return facts
			}
		}
		facts.Reason = desktopGetSentence(app, title)
		return facts
	}
	if catalog.DesktopIntegration == nil || catalog.DesktopIntegration.App != app {
		facts.Reason = fmt.Sprintf("This build's %s integration package is unavailable.", title)
		return facts
	}
	if err := verifyCatalogDesktopHost(app); err != nil {
		facts.Reason = err.Error()
		return facts
	}
	// A release the integration does not support leaves OpenCode as it is.
	if sentence := unsupportedDesktopRelease(catalog, app); sentence != "" {
		facts.Reason = sentence
		return facts
	}
	action, err := readCatalogDesktopPackage(*catalog.DesktopIntegration)
	if err != nil {
		log.Printf("setup: %s integration status: %v", title, err)
		facts.Reason = fmt.Sprintf("The %s integration package could not be checked.", title)
		return facts
	}
	facts.Action = action
	return facts
}

// setupApplyDesktopIntegration is the person's Catalog click. A detached Setup step
// owns the Windows Installer result even when the person leaves the page.
func (p *servicePanel) setupApplyDesktopIntegration(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		journal, err := p.setupJournalForPanel()
		if err != nil {
			panelError(w, err)
			return
		}
		pin := journal.Catalog().DesktopIntegration
		view := desktopReconnectView{}
		if pin != nil {
			view.Title = desktopReconnectTitle(pin.Title)
			view.Available = desktopReconnectAvailable(*pin)
			view.Action = p.setupActionsNow()[desktopIntegrationStep].StepID
			view.Outcome = p.setupOutcomesNow()[desktopIntegrationStep]
		}
		panelJSON(w, view)
		return
	}
	if !decodeSetupConfirm(w, r) || !p.setupTryStart(w) {
		return
	}
	started := false
	defer func() {
		if !started {
			p.setupActionMu.Unlock()
		}
	}()
	journal, err := p.setupJournalForPanel()
	if err != nil {
		panelError(w, err)
		return
	}
	catalog := journal.Catalog()
	if catalog.DesktopIntegration == nil {
		http.Error(w, "This build's desktop integration package is unavailable.", http.StatusConflict)
		return
	}
	asset := *catalog.DesktopIntegration
	facts := readDesktopIntegration(catalog, asset.App)
	if facts.Action == "" {
		http.Error(w, facts.Reason, http.StatusConflict)
		return
	}
	if requested := r.URL.Query().Get("action"); requested != "" && requested != facts.Action {
		http.Error(w, "The desktop integration changed. Check the Catalog again.", http.StatusConflict)
		return
	}
	if r.URL.Query().Get("reconnect") == "1" && facts.Action != "install" {
		http.Error(w, "This app's integration is already installed. Check the Catalog again.", http.StatusConflict)
		return
	}
	var transfer setup.Transfer = setup.PublicTransfer{Machine: panelMachine(), LocalOnly: true}
	if p.setupTransfer != nil {
		transfer = *p.setupTransfer
	}
	// Windows Installer owns the transaction once started. Keep its action
	// visible and serialized until the native call returns.
	ctx, cancel := context.WithTimeout(context.Background(), desktopIntegrationTimeout)
	_, end := p.beginSetupStep(desktopIntegrationStep, facts.Action, ctx, nil)
	actions := p.setupActionsNow()
	outcomes := p.setupOutcomesNow()
	step := actions[desktopIntegrationStep]
	started = true
	go func() {
		var failure error
		defer p.setupActionMu.Unlock()
		defer cancel()
		defer end()
		defer func() {
			if recovered := recover(); recovered != nil {
				failure = fmt.Errorf("%s integration stopped unexpectedly: %v", asset.Title, recovered)
			}
			outcome := setupOutcome{Kind: step.Kind, StepID: step.StepID}
			if failure != nil {
				log.Printf("setup: %s integration: %v", asset.Title, failure)
				var installerFailure *setup.DesktopInstallerFailure
				if errors.As(failure, &installerFailure) {
					outcome.Failure = installerFailure.Reason
				} else {
					outcome.Failure, outcome.Detail = stepFailureReason(failure), failure.Error()
				}
			}
			p.recordSetupOutcome(desktopIntegrationStep, outcome)
		}()
		if failure = verifyCatalogDesktopHost(asset.App); failure == nil {
			failure = applyCatalogDesktopPackage(asset, ctx, step.Kind, transfer)
		}
	}()
	panelJSON(w, map[string]any{"actions": actions, "outcomes": outcomes, "step_id": step.StepID})
}

type desktopReconnectView struct {
	Title     string       `json:"title,omitempty"`
	Available bool         `json:"available"`
	Action    string       `json:"action,omitempty"`
	Outcome   setupOutcome `json:"outcome"`
}

func desktopReconnectTitle(title string) string { return "Reconnect " + title + " to OpenAbstractions" }

func desktopReconnectAvailable(pin setup.DesktopIntegration) bool {
	if !desktopProgramPresent(pin.App) {
		return false
	}
	action, err := readCatalogDesktopPackage(pin)
	return err == nil && action == "install" && verifyCatalogDesktopHost(pin.App) == nil
}

// desktopGetSentence says what the Catalog row's two buttons do for a
// desktop app OpenAbstractions connects and does not install: Get opens
// the publisher's download page, and Connect links the installed app
// (agent-ux 2026-11-22: "This build can integrate an existing OpenCode
// Desktop installation" said neither).
func desktopGetSentence(app setup.AppID, title string) string {
	if download, ok := integrationDownloads[setup.AgentParty(app)]; ok {
		if parsed, err := url.Parse(download); err == nil && parsed.Host != "" {
			return "Get opens " + parsed.Host + ", where you download and install " + title + " yourself. Then Connect links it to OpenAbstractions."
		}
	}
	return "Install " + title + " yourself. Then Connect links it to OpenAbstractions."
}
