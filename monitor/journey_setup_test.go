package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// J-SETUP (owner, 2026-11-10: "Removing ComfyUI stalled with no progress
// shown"). The journey removes an installed ComfyUI package of more than
// 30,000 small files through the Panel routes the Setup page calls, and reads
// the page's state while it runs: a running remove step whose file count
// moves, a request that fails part way leaves that step shown, and the
// removal ends removed with its folder gone.
//
// Run before an installer:
//
//	OA_JOURNEY=1 go -C monitor test -run '^TestJourneySetupRemoval$' -count=1 -v .
func TestJourneySetupRemoval(t *testing.T) {
	if os.Getenv("OA_JOURNEY") != "1" {
		t.Skip("journey: set OA_JOURNEY=1 to run J-SETUP")
	}
	requirePinnedPlatform(t)
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()

	// Every Setup file lives in this test's own runtime state folder.
	stateDir := t.TempDir()
	if err := setup.UseRuntimeState(stateDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.UseUserState)

	const component = "comfyui-app"
	installed := installJourneyComfy(t, ctx, stateDir, 30000)
	t.Logf("J-SETUP: installed %d files under %s in %s", installed.files, installed.root, time.Since(started).Round(time.Millisecond))

	// No registration names the package: the removal's unregister step
	// observes the applications service and finds nothing to withdraw.
	var observed atomic.Int32
	apps := &nativeApplications{application: string(setup.ComfyUI), transport: func(context.Context) applications.ApplicationsTransport {
		return nativeApplicationExchange(func(b []byte) ([]byte, error) {
			var frame struct{ Method, Service string }
			if err := json.Unmarshal(b, &frame); err != nil {
				return nil, err
			}
			if frame.Method != "Observe" {
				return nil, fmt.Errorf("J-SETUP: unexpected applications call %s", frame.Method)
			}
			observed.Add(1)
			value := applications.EncodePage(&applications.Page{Outcome: applications.OutcomePage})
			return json.Marshal(map[string]any{"version": 1, "service": frame.Service, "method": frame.Method, "ok": true, "payload": map[string]any{"value": json.RawMessage(value)}})
		})
	}}
	panel := &servicePanel{setupJournal: &installed.journal, comfyApps: apps}
	const key = "journey-setup-key"
	var inflight atomic.Int32
	handler := panel.handler(key)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		inflight.Add(1)
		defer inflight.Add(-1)
		handler.ServeHTTP(w, r)
	}))
	defer server.Close()
	// A failed journey cancels the removal, so the server's Close does not
	// wait for a request queued behind it.
	defer panel.cancelSetupStep(component)
	page := &journeyPage{t: t, ctx: ctx, base: server.URL, key: key}

	// 1. The Setup page lists the installed package as removable.
	state := page.state()
	if !state.Approved || state.Plan.ID == "" {
		page.fatalf("Setup state has no approved plan to remove from")
	}
	if phase := state.phase(component); phase != setup.Installed {
		page.fatalf("Setup lists %s as %q, not installed", component, phase)
	}
	if !state.removable(component) {
		page.fatalf("Setup offers no application choice that removes %s", component)
	}
	if len(state.Actions) != 0 {
		page.fatalf("a step runs before Remove: %+v", state.Actions)
	}

	// 2. Remove starts the step and answers at once.
	body := fmt.Sprintf(`{"component_id":%q,"plan_id":%q,"confirm":true}`, component, state.Plan.ID)
	removeAt := time.Now()
	code, raw := page.post("/setup/uninstall", body, 10*time.Second)
	if code != http.StatusOK {
		page.fatalf("Remove: %d %s", code, raw)
	}
	var reply journeyState
	if err := json.Unmarshal(raw, &reply); err != nil || reply.StepID == "" {
		page.fatalf("Remove reply names no step: %v %s", err, raw)
	}
	if a, ok := reply.Actions[component]; !ok || a.Kind != "remove" || a.StepID != reply.StepID {
		page.fatalf("Remove reply shows no running remove step %s: %+v", reply.StepID, reply.Actions)
	}
	t.Logf("J-SETUP: Remove answered in %s with step %s", time.Since(removeAt).Round(time.Millisecond), reply.StepID)

	// 3. While it runs, the page's state read shows the step and its count
	// moving; a request that fails part way leaves the step shown.
	const poll = 200 * time.Millisecond
	var dones []int
	total := 0
	failedRequest := false
	reads, lastLogged, slowest := 0, -1, time.Duration(0)
	var ended journeyState
	for {
		if ctx.Err() != nil {
			page.fatalf("the removal did not end within the journey's time; seen done values %v", dones)
		}
		readAt := time.Now()
		s := page.state()
		reads++
		slowest = max(slowest, time.Since(readAt))
		a, running := s.Actions[component]
		if running && a.StepID != reply.StepID {
			// Another step replaced the Remove reply's step, which ended.
			t.Logf("J-SETUP: +%s step %s ended and step %s runs", time.Since(removeAt).Round(time.Millisecond), reply.StepID, a.StepID)
			break
		}
		if running && a.Done/5000 != lastLogged {
			lastLogged = a.Done / 5000
			t.Logf("J-SETUP: +%s the page reads %d of %d files", time.Since(removeAt).Round(time.Millisecond), a.Done, a.Total)
		}
		if !running {
			ended = s
			break
		}
		page.requireRunning(component, a, reply.StepID)
		if a.Total > 0 {
			total = a.Total
			if a.Total < installed.files {
				page.fatalf("the running step counts %d files of the %d installed", a.Total, installed.files)
			}
			if len(dones) > 0 && a.Done < dones[len(dones)-1] {
				page.fatalf("the file count went back from %d to %d", dones[len(dones)-1], a.Done)
			}
			if len(dones) == 0 || a.Done != dones[len(dones)-1] {
				dones = append(dones, a.Done)
			}
		}
		if !failedRequest && a.Done > 0 && a.Done < a.Total {
			failedRequest = true
			page.failMidStep(component, state.Plan.ID, reply.StepID)
		}
		time.Sleep(poll)
	}
	removal := time.Since(removeAt)
	t.Logf("J-SETUP: removal of %d files took %s; %d state reads, the slowest %s; the page saw %d distinct counts", total, removal.Round(time.Millisecond), reads, slowest.Round(time.Millisecond), len(dones))
	if total < 30000 {
		page.fatalf("the page never saw the step's file count of at least 30000 (saw %d)", total)
	}
	if len(dones) < 2 {
		page.fatalf("the page saw the file count at fewer than two values: %v", dones)
	}
	if !failedRequest {
		page.fatalf("the removal ended before the page could make its failing request; seen %v", dones)
	}

	// Requests and steps queued behind the step finish before the result
	// is read.
	for inflight.Load() > 0 || len(panel.setupActionsNow()) > 0 {
		if ctx.Err() != nil {
			page.fatalf("a Panel request still runs after the removal ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
	ended = page.state()

	// 4. The removal ended removed, as the step the Remove reply named.
	if a, running := ended.Actions[component]; running {
		page.fatalf("a step for %s runs again after the removal ended: %+v", component, a)
	}
	o, ok := ended.Outcomes[component]
	if ok && o.StepID != reply.StepID {
		page.fatalf("the outcome is step %s's, not the Remove reply's %s: the second Remove, abandoned by its client, waited for the removal and then ran another step, which ended as %+v", o.StepID, reply.StepID, o)
	}
	if !ok || o.Kind != "remove" || o.Failure != "" || o.Cancelled {
		page.fatalf("the removal ended as %+v", o)
	}
	if phase := ended.phase(component); phase != setup.Removed {
		page.fatalf("Setup lists %s as %q after the removal, not removed", component, phase)
	}
	for _, folder := range []string{installed.root, filepath.Join(filepath.Dir(installed.root), "."+filepath.Base(installed.root)+".removing")} {
		if _, err := os.Lstat(folder); !errors.Is(err, os.ErrNotExist) {
			page.fatalf("%s is still on disk after the removal: %v", folder, err)
		}
	}
	if observed.Load() == 0 {
		page.fatalf("the removal never observed the application registrations")
	}
	t.Logf("J-SETUP: passed in %s", time.Since(started).Round(time.Millisecond))
}

// journeyState is the part of GET /setup/state the Setup page reads.
type journeyState struct {
	Catalog    setup.Catalog           `json:"catalog"`
	Plan       setup.Plan              `json:"plan"`
	Components []setup.ComponentStatus `json:"components"`
	Approved   bool                    `json:"approved"`
	Actions    map[string]setupAction  `json:"actions"`
	Outcomes   map[string]setupOutcome `json:"outcomes"`
	StepID     string                  `json:"step_id"`
}

func (s journeyState) phase(component string) setup.Phase {
	for _, row := range s.Components {
		if row.ID == component {
			return row.Effect.Phase
		}
	}
	return ""
}

// removable is whether an application choice of the plan removes
// component, the row the page's Remove acts on (setupChoiceComponents).
func (s journeyState) removable(component string) bool {
	for _, choice := range s.Catalog.Choices {
		if !choice.Application {
			continue
		}
		for _, c := range choice.Components {
			if c == component {
				return true
			}
		}
	}
	return false
}

// journeyPage calls the Panel routes as the Setup page does and keeps the
// last state it read for a failure report.
type journeyPage struct {
	t    *testing.T
	ctx  context.Context
	base string
	key  string
	last []byte
}

func (p *journeyPage) fatalf(format string, args ...any) {
	p.t.Helper()
	p.t.Fatalf("J-SETUP: "+format+"\nlast /setup/state:\n%s", append(args, p.last)...)
}

func (p *journeyPage) do(ctx context.Context, method, route, body string) (int, []byte, error) {
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, p.base+route, reader)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-Panel-Key", p.key)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	return resp.StatusCode, raw, err
}

func (p *journeyPage) post(route, body string, limit time.Duration) (int, []byte) {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(p.ctx, limit)
	defer cancel()
	code, raw, err := p.do(ctx, http.MethodPost, route, body)
	if err != nil {
		p.fatalf("POST %s: %v", route, err)
	}
	return code, raw
}

// state reads GET /setup/state, which the page reads every few seconds
// while a step runs; it must answer quickly during the removal.
func (p *journeyPage) state() journeyState {
	p.t.Helper()
	ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
	defer cancel()
	code, raw, err := p.do(ctx, http.MethodGet, "/setup/state", "")
	if err != nil || code != http.StatusOK {
		p.fatalf("GET /setup/state: %d %v %s", code, err, raw)
	}
	p.last = raw
	var s journeyState
	if err := json.Unmarshal(raw, &s); err != nil {
		p.fatalf("GET /setup/state: %v", err)
	}
	return s
}

// requireRunning applies the page's setupComponentRunning to a: a remove
// step before its deadline whose count moved within setupStepStallMs. The
// journey also requires its last report within 30 seconds.
func (p *journeyPage) requireRunning(component string, a setupAction, stepID string) {
	p.t.Helper()
	now := time.Now()
	if a.Kind != "remove" || a.StepID != stepID {
		p.fatalf("the running step of %s is %+v, not remove step %s", component, a, stepID)
	}
	if !a.Cancellable {
		p.fatalf("the running removal offers no Cancel")
	}
	deadline, err := time.Parse(time.RFC3339, a.Deadline)
	if err != nil || !deadline.After(now) {
		p.fatalf("the running removal's deadline %q has passed or is unreadable: %v", a.Deadline, err)
	}
	progressAt, err := time.Parse(time.RFC3339, a.ProgressAt)
	if err != nil {
		p.fatalf("the running removal's progress time %q is unreadable: %v", a.ProgressAt, err)
	}
	const setupStepStallMs = 180000 // setup_page.go
	if age := now.Sub(progressAt); age > setupStepStallMs*time.Millisecond || age > 30*time.Second {
		p.fatalf("the running removal last reported %s ago; the page counts it as stalled", age.Round(time.Second))
	}
}

// failMidStep makes a second Remove request for the running step whose
// client gives up, as WebView2 drops a long request, and then reads the
// state the page reads after a failed request (setupRemove).
func (p *journeyPage) failMidStep(component, planID, stepID string) {
	p.t.Helper()
	body := fmt.Sprintf(`{"component_id":%q,"plan_id":%q,"confirm":true}`, component, planID)
	ctx, cancel := context.WithTimeout(p.ctx, 1500*time.Millisecond)
	defer cancel()
	at := time.Now()
	code, raw, err := p.do(ctx, http.MethodPost, "/setup/uninstall", body)
	switch {
	case err != nil:
		p.t.Logf("J-SETUP: the second Remove request failed after %s: %v", time.Since(at).Round(time.Millisecond), err)
	case code == http.StatusOK:
		p.fatalf("a second Remove during the running step answered 200: %s", raw)
	default:
		p.t.Logf("J-SETUP: the second Remove request was refused after %s: %d %s", time.Since(at).Round(time.Millisecond), code, strings.TrimSpace(string(raw)))
	}
	s := p.state()
	a, running := s.Actions[component]
	if !running {
		p.fatalf("after the failed request the page reads no running step for %s", component)
	}
	p.requireRunning(component, a, stepID)
	if a.Total < 30000 || a.Done <= 0 {
		p.fatalf("after the failed request the step counts %d of %d", a.Done, a.Total)
	}
	if o, ok := s.Outcomes[component]; ok && o.StepID == stepID {
		p.fatalf("the running step already has an outcome: %+v", o)
	}
}
