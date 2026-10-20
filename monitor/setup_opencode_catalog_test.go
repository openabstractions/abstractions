package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestSetupOpenCodeIntegrationUsesOneCatalogStep(t *testing.T) {
	stubOverviewMachine(t)
	verify, read, apply, present := verifyCatalogDesktopHost, readCatalogDesktopPackage, applyCatalogDesktopPackage, desktopProgramPresent
	t.Cleanup(func() {
		verifyCatalogDesktopHost, readCatalogDesktopPackage, applyCatalogDesktopPackage = verify, read, apply
		desktopProgramPresent = present
	})
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	const product = "{01234567-89AB-CDEF-0123-456789ABCDEF}"
	doc := `{"schema":2,"release_tag":"v0.8.0","apps":[],"extensions":[{"id":"opencode","file":"opencode-0.8.0.msi","sha256":"` + strings.Repeat("a", 64) + `","bytes":100,"version":"0.8.0","product_code":"` + product + `"}]}`
	var err error
	journal, err = journal.WithAdopterAssets([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if journal.Catalog().DesktopIntegration == nil {
		t.Skip("OpenCode integration is a Windows x64 catalog asset")
	}
	p := &servicePanel{setupJournal: &journal}
	const path = "/setup/desktop-integration"
	post := installHandlerFor(p.handler("setup-test-key"))
	verifyCatalogDesktopHost = func(setup.AppID) error { return errors.New("Unverified publisher.") }
	readCatalogDesktopPackage = func(setup.DesktopIntegration) (string, error) { return "update", nil }
	desktopProgramPresent = func(setup.AppID) bool { return true }
	if got := post("POST", path, `{"confirm":true}`); got.Code != http.StatusConflict || !strings.Contains(got.Body.String(), "Unverified publisher.") {
		t.Fatalf("unverified host: %d %s", got.Code, got.Body.String())
	}
	verifyCatalogDesktopHost = func(setup.AppID) error { return nil }
	check := readOverview(t, p).DesktopIntegration
	if check.App != setup.OpenCode || !check.Installed || check.Action != "update" || check.Reason != "" {
		t.Fatalf("catalog facts: %+v", check)
	}
	if got := post("POST", path, `{"confirm":false}`); got.Code != http.StatusBadRequest {
		t.Fatalf("unconfirmed request: %d %s", got.Code, got.Body.String())
	}
	if got := post("POST", path+"?reconnect=1", `{"confirm":true}`); got.Code != http.StatusConflict {
		t.Fatalf("stale Apps reconnect must not update an installed integration: %d %s", got.Code, got.Body.String())
	}
	readCatalogDesktopPackage = func(setup.DesktopIntegration) (string, error) { return "install", nil }
	if got := post("POST", path+"?action=update", `{"confirm":true}`); got.Code != http.StatusConflict {
		t.Fatalf("an update click must not reinstall an integration the owner removed: %d %s", got.Code, got.Body.String())
	}
	readCatalogDesktopPackage = func(setup.DesktopIntegration) (string, error) { return "update", nil }
	var applied bool
	var appliedDeadline time.Time
	finished := make(chan struct{})
	release := func() {
		select {
		case <-finished:
		default:
			close(finished)
		}
	}
	defer release()
	applyCatalogDesktopPackage = func(pin setup.DesktopIntegration, ctx context.Context, action string, transfer setup.Transfer) error {
		public, local := transfer.(setup.PublicTransfer)
		applied = pin.ProductCode == product && action == "update" && local && public.LocalOnly
		appliedDeadline, _ = ctx.Deadline()
		<-finished
		return errors.New("Close OpenCode completely, then try again.")
	}
	got := post("POST", path+"?action=update", `{"confirm":true}`)
	if got.Code != http.StatusOK {
		t.Fatalf("confirmed request: %d %s", got.Code, got.Body.String())
	}
	var reply struct {
		StepID  string                 `json:"step_id"`
		Actions map[string]setupAction `json:"actions"`
	}
	if err := json.Unmarshal(got.Body.Bytes(), &reply); err != nil || reply.StepID == "" {
		t.Fatalf("confirmed step identity: %+v (%v)", reply, err)
	}
	action := reply.Actions[desktopIntegrationStep]
	if action.StepID != reply.StepID || action.Deadline == "" || action.Cancellable {
		t.Fatalf("active native step: %+v", action)
	}
	if duplicate := post("POST", path, `{"confirm":true}`); duplicate.Code != http.StatusConflict || !strings.Contains(duplicate.Body.String(), `"busy":true`) {
		t.Fatalf("duplicate was not excluded: %d %s", duplicate.Code, duplicate.Body.String())
	}
	release()
	p.setupActionMu.Lock()
	p.setupActionMu.Unlock()
	outcome := p.setupOutcomesNow()[desktopIntegrationStep]
	if !applied || appliedDeadline.UTC().Format(time.RFC3339) != action.Deadline || outcome.StepID != reply.StepID || outcome.Kind != "update" || !strings.Contains(outcome.Failure, "Close OpenCode") {
		t.Fatalf("applied=%v, outcome=%+v", applied, outcome)
	}
}

func TestDesktopReconnectOffersOneNoticeAndNoInstallOnRead(t *testing.T) {
	verify, read, present := verifyCatalogDesktopHost, readCatalogDesktopPackage, desktopProgramPresent
	t.Cleanup(func() {
		verifyCatalogDesktopHost, readCatalogDesktopPackage, desktopProgramPresent = verify, read, present
	})
	doc := `{"schema":2,"release_tag":"v0.8.0","apps":[],"extensions":[{"id":"opencode","file":"opencode-0.8.0.msi","sha256":"` + strings.Repeat("a", 64) + `","bytes":100,"version":"0.8.0","product_code":"{01234567-89AB-CDEF-0123-456789ABCDEF}"}]}`
	config := t.TempDir()
	journal, err := (setup.Journal{Path: filepath.Join(config, "plan.json")}).WithAdopterAssets([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	pin := journal.Catalog().DesktopIntegration
	if pin == nil {
		t.Skip("desktop integration requires Windows x64")
	}
	p := &servicePanel{setupJournal: &journal}
	const path = "/setup/desktop-integration"
	view := func() desktopReconnectView {
		t.Helper()
		w := httptest.NewRecorder()
		p.setupApplyDesktopIntegration(w, httptest.NewRequest("GET", path, nil))
		var out desktopReconnectView
		if err := json.Unmarshal(w.Body.Bytes(), &out); w.Code != http.StatusOK || err != nil {
			t.Fatalf("read returned %d: %v", w.Code, err)
		}
		return out
	}
	action, installed, verified := "install", false, false
	desktopProgramPresent = func(setup.AppID) bool { return installed }
	readCatalogDesktopPackage = func(setup.DesktopIntegration) (string, error) { return action, nil }
	verifyCatalogDesktopHost = func(setup.AppID) error {
		if verified {
			return nil
		}
		return errors.New("unverified")
	}
	notices := 0
	notify := func(_ context.Context, notice setup.StepNotice) error {
		notices++
		if notice.Title != "Reconnect OpenCode to OpenAbstractions" || notice.Tag != "desktop-reconnect" {
			t.Fatalf("notice: %+v", notice)
		}
		return nil
	}
	check := func(available bool, count int) {
		t.Helper()
		if got := view(); got.Available != available || got.Action != "" {
			t.Fatalf("available=%t, view=%+v", available, got)
		}
		if err := notifyDesktopReconnect(t.Context(), *pin, config, notify); err != nil || notices != count {
			t.Fatalf("notices=%d want=%d err=%v", notices, count, err)
		}
	}
	check(false, 0)
	installed = true
	check(false, 0)
	verified = true
	check(true, 1)
	check(true, 1)
	action = "repair"
	check(false, 1)
}
