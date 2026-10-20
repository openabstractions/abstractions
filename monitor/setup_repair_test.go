package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// repairFixture stands in the fix-up check and the repair: status is what
// the check reads, repair what one Repair does.
type repairFixture struct {
	mu      sync.Mutex
	status  setup.FixupStatus
	repairs int
	repair  func(context.Context) (setup.FixupStatus, error)
}

func stockOpenCode() setup.FixupStatus {
	return setup.FixupStatus{App: setup.OpenCode, Name: "OpenCode", Component: setup.OpenCodeOfficialComponent, State: setup.FixupNeedsRepair, Repair: true,
		Sentence: "Needs its OpenAbstractions fix again", Detail: "An update to OpenCode removed it.", Close: "Close OpenCode, then press Repair.", Versions: []string{"1.18.34", "1.19.5"}}
}

func newRepairFixture(t *testing.T, status setup.FixupStatus) (*servicePanel, *repairFixture) {
	t.Helper()
	f := &repairFixture{status: status}
	oldCheck, oldRepair, oldIsolated := setupFixupCheck, setupFixupRepair, setupRepairIsolated
	t.Cleanup(func() { setupFixupCheck, setupFixupRepair, setupRepairIsolated = oldCheck, oldRepair, oldIsolated })
	setupFixupCheck = func(context.Context, setup.Catalog) ([]setup.FixupStatus, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return []setup.FixupStatus{f.status}, nil
	}
	setupFixupRepair = func(ctx context.Context, _ setup.Catalog, component string) (setup.FixupStatus, error) {
		f.mu.Lock()
		f.repairs++
		repair := f.repair
		f.mu.Unlock()
		if component != setup.OpenCodeOfficialComponent {
			t.Errorf("repair of %q", component)
		}
		if repair != nil {
			return repair(ctx)
		}
		return setup.FixupStatus{State: setup.FixupCurrent}, nil
	}
	setupRepairIsolated = func() bool { return false }
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	return &servicePanel{setupJournal: &journal}, f
}

func postRepair(p *servicePanel, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	p.setupRepair(w, httptest.NewRequest(http.MethodPost, "/setup/repair", strings.NewReader(body)))
	return w
}

const repairBody = `{"component_id":"opencode-official","confirm":true}`

// waitStepEnd waits until the Repair step released the Setup action lock.
func waitStepEnd(t *testing.T, p *servicePanel) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !p.setupActionMu.TryLock() {
		if time.Now().After(deadline) {
			t.Fatal("the Repair step kept the lock")
		}
		time.Sleep(5 * time.Millisecond)
	}
	p.setupActionMu.Unlock()
}

func TestSetupOverviewCarriesFixups(t *testing.T) {
	p, _ := newRepairFixture(t, stockOpenCode())
	journal, _ := p.setupJournalForPanel()
	overview := p.readSetupOverview(context.Background(), journal)
	if len(overview.Fixes) != 1 || overview.Fixes[0].State != setup.FixupNeedsRepair || !overview.Fixes[0].Repair {
		t.Fatalf("fixes %+v", overview.Fixes)
	}
	encoded, _ := json.Marshal(overview)
	if !strings.Contains(string(encoded), `"fixes":[{"app":"opencode","name":"OpenCode","component":"opencode-official","state":"needs_repair"`) {
		t.Fatalf("overview JSON %s", encoded)
	}
}

func TestSetupRepairRunsAsAStepThenRecordsItsOutcome(t *testing.T) {
	p, f := newRepairFixture(t, stockOpenCode())
	release := make(chan struct{})
	f.repair = func(context.Context) (setup.FixupStatus, error) {
		<-release
		return setup.FixupStatus{State: setup.FixupCurrent}, nil
	}
	w := postRepair(p, repairBody)
	var reply struct {
		StepID  string                 `json:"step_id"`
		Actions map[string]setupAction `json:"actions"`
	}
	if w.Code != http.StatusOK || json.Unmarshal(w.Body.Bytes(), &reply) != nil || reply.StepID == "" {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if a := reply.Actions[setup.OpenCodeOfficialComponent]; a.Kind != "repair" || a.StepID != reply.StepID {
		t.Fatalf("running steps %+v", reply.Actions)
	}
	// A second step-starting request while the Repair runs is refused at once.
	busy := postRepair(p, repairBody)
	var refusal setupBusyReply
	if busy.Code != http.StatusConflict || json.Unmarshal(busy.Body.Bytes(), &refusal) != nil || !refusal.Busy || refusal.Actions[setup.OpenCodeOfficialComponent].Kind != "repair" {
		t.Fatalf("busy: %d %s", busy.Code, busy.Body)
	}
	close(release)
	waitStepEnd(t, p)
	if f.repairs != 1 {
		t.Fatalf("%d repairs ran", f.repairs)
	}
	if o := p.setupOutcomesNow()[setup.OpenCodeOfficialComponent]; o.Kind != "repair" || o.StepID != reply.StepID || o.Failure != "" {
		t.Fatalf("outcome %+v", o)
	}
	if len(p.setupActionsNow()) != 0 {
		t.Fatal("the step is still listed")
	}
}

func TestSetupRepairRefusesWhileTheAppRuns(t *testing.T) {
	status := stockOpenCode()
	status.Running = true
	p, f := newRepairFixture(t, status)
	w := postRepair(p, repairBody)
	if w.Code != http.StatusConflict || strings.TrimSpace(w.Body.String()) != "Close OpenCode, then press Repair." || f.repairs != 0 {
		t.Fatalf("%d %q, %d repairs", w.Code, w.Body, f.repairs)
	}
	// The app closed between the check and the repair: the step's outcome
	// says the same.
	f.status.Running = false
	f.repair = func(context.Context) (setup.FixupStatus, error) {
		return setup.FixupStatus{State: setup.FixupNeedsRepair, Running: true}, fmt.Errorf("%w: Close OpenCode, then press Repair.", setup.ErrFixupAppRunning)
	}
	if w := postRepair(p, repairBody); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	waitStepEnd(t, p)
	if o := p.setupOutcomesNow()[setup.OpenCodeOfficialComponent]; o.Failure != "Close OpenCode, then press Repair." {
		t.Fatalf("outcome %+v", o)
	}
}

func TestSetupRepairRefusesWithoutARepairableFix(t *testing.T) {
	for name, status := range map[string]setup.FixupStatus{
		"unknown": {App: setup.OpenCode, Name: "OpenCode", Component: setup.OpenCodeOfficialComponent, State: setup.FixupUnknown, Sentence: "Not a version this release supports"},
		"current": {App: setup.OpenCode, Name: "OpenCode", Component: setup.OpenCodeOfficialComponent, State: setup.FixupCurrent},
	} {
		p, f := newRepairFixture(t, status)
		w := postRepair(p, repairBody)
		if w.Code != http.StatusConflict || f.repairs != 0 {
			t.Fatalf("%s: %d %s", name, w.Code, w.Body)
		}
		if name == "unknown" && strings.TrimSpace(w.Body.String()) != "Not a version this release supports." {
			t.Fatalf("%s: %q", name, w.Body)
		}
	}
	p, f := newRepairFixture(t, stockOpenCode())
	for _, body := range []string{`{"component_id":"opencode-official"}`, `{"confirm":true}`, `{"component_id":"opencode-official","confirm":true,"plan_id":"x"}`} {
		if w := postRepair(p, body); w.Code != http.StatusBadRequest {
			t.Fatalf("%s: %d", body, w.Code)
		}
	}
	if w := postRepair(p, `{"component_id":"comfyui-app","confirm":true}`); w.Code != http.StatusConflict || f.repairs != 0 {
		t.Fatalf("another component: %d", w.Code)
	}
	setupRepairIsolated = func() bool { return true }
	if w := postRepair(p, repairBody); w.Code != http.StatusConflict || f.repairs != 0 {
		t.Fatalf("isolated: %d", w.Code)
	}
}

func TestSetupRepairFailureSentence(t *testing.T) {
	p, f := newRepairFixture(t, stockOpenCode())
	f.repair = func(context.Context) (setup.FixupStatus, error) {
		return setup.FixupStatus{State: setup.FixupNeedsRepair}, errors.New("Windows Installer did not repair it (code 1603)")
	}
	if w := postRepair(p, repairBody); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	waitStepEnd(t, p)
	if o := p.setupOutcomesNow()[setup.OpenCodeOfficialComponent]; o.Failure != "Repair did not finish." || !strings.Contains(o.Detail, "code 1603") {
		t.Fatalf("outcome %+v", o)
	}
	// Windows Installer busy with another installation has its own sentence.
	f.repair = func(context.Context) (setup.FixupStatus, error) {
		return setup.FixupStatus{State: setup.FixupNeedsRepair}, fmt.Errorf("%w (Windows Installer code 1618)", setup.ErrFixupInstallerBusy)
	}
	if w := postRepair(p, repairBody); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	waitStepEnd(t, p)
	if o := p.setupOutcomesNow()[setup.OpenCodeOfficialComponent]; o.Failure != "Another installation is running. Press Repair when it finishes." || !strings.Contains(o.Detail, "1618") {
		t.Fatalf("outcome %+v", o)
	}
	// A refusal for a running app that the repair no longer sees says the
	// repair did not finish, never Close OpenCode.
	f.repair = func(context.Context) (setup.FixupStatus, error) {
		return setup.FixupStatus{State: setup.FixupNeedsRepair}, fmt.Errorf("%w: Close OpenCode, then press Repair.", setup.ErrFixupAppRunning)
	}
	if w := postRepair(p, repairBody); w.Code != http.StatusOK {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	waitStepEnd(t, p)
	if o := p.setupOutcomesNow()[setup.OpenCodeOfficialComponent]; o.Failure != "Repair did not finish." {
		t.Fatalf("outcome %+v", o)
	}
}

func TestSetupRepairNeedsThePanelKey(t *testing.T) {
	p, f := newRepairFixture(t, stockOpenCode())
	handler := p.handler("setup-test-key")
	for _, key := range []string{"", "wrong-key"} {
		request := httptest.NewRequest(http.MethodPost, "/setup/repair", strings.NewReader(repairBody))
		if key != "" {
			request.Header.Set("X-Panel-Key", key)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, request)
		if w.Code != http.StatusForbidden || f.repairs != 0 {
			t.Fatalf("key %q: %d, %d repairs", key, w.Code, f.repairs)
		}
	}
}

// N3: a Panel serving an isolated runtime shows the fix-up and offers no
// Repair.
func TestSetupOverviewOffersNoRepairWhileIsolated(t *testing.T) {
	p, _ := newRepairFixture(t, stockOpenCode())
	setupRepairIsolated = func() bool { return true }
	journal, _ := p.setupJournalForPanel()
	overview := p.readSetupOverview(context.Background(), journal)
	if len(overview.Fixes) != 1 || overview.Fixes[0].State != setup.FixupNeedsRepair || overview.Fixes[0].Repair {
		t.Fatalf("fixes %+v", overview.Fixes)
	}
}
