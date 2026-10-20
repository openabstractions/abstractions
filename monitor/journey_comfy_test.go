package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// J-COMFY.setup (owner, R20c, 2026-11-10): ComfyUI installed through Setup
// left no Start-menu shortcut, the registered program stayed an old
// development copy under the repository's .forks\q, and Open in the Panel
// raised an Allow dialog and then failed with "context deadline exceeded".
//
// The journey installs the stand-in ComfyUI package with a development
// copy's registration already in the applications service, runs the Setup
// page's workspace step (/setup/comfy/status, then /setup/comfy/register),
// and presses Open on the app's page (/app/comfyui/start) while the
// runtime's activation waits past the route's own bound.
//
//	OA_JOURNEY=1 go -C monitor test -run '^TestJourneyComfyInstallRegisters$' -count=1 -v .
func TestJourneyComfyInstallRegisters(t *testing.T) {
	if os.Getenv("OA_JOURNEY") != "1" {
		t.Skip("journey: set OA_JOURNEY=1 to run J-COMFY.setup")
	}
	requirePinnedPlatform(t)
	started := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	stateDir := t.TempDir()
	if err := setup.UseRuntimeState(stateDir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(setup.UseUserState)
	installed := installJourneyComfy(t, ctx, stateDir, 300)

	// 1. A development copy of ComfyUI, still on disk and unsigned, holds
	// the comfyui registration.
	stale := filepath.Join(t.TempDir(), ".forks", "q", "ComfyUI", "launch-helper.exe")
	if err := os.MkdirAll(filepath.Dir(stale), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("developer build of the ComfyUI launch helper"), 0o700); err != nil {
		t.Fatal(err)
	}
	registry := &journeyRegistry{t: t, selfRegistered: installed.selfRegistered,
		entry: &journeyEntry{program: stale, revision: "dev-1", title: "ComfyUI — OA build"}}
	apps := &nativeApplications{application: setup.ComfyApplicationName, transport: registry.transport}
	panel := &servicePanel{setupJournal: &installed.journal, comfyApps: apps, appReadRules: &applicationReadFake{}}
	const key = "journey-comfy-key"
	server := httptest.NewServer(panel.handler(key))
	defer server.Close()
	page := &journeyPage{t: t, ctx: ctx, base: server.URL, key: key}
	call := func(method, route, body string, limit time.Duration) (int, string, time.Duration) {
		t.Helper()
		callCtx, cancel := context.WithTimeout(ctx, limit)
		defer cancel()
		at := time.Now()
		code, raw, err := page.do(callCtx, method, route, body)
		if err != nil {
			t.Fatalf("J-COMFY: %s %s: %v", method, route, err)
		}
		return code, strings.TrimSpace(string(raw)), time.Since(at)
	}
	state := page.state()
	if state.phase("comfyui-app") != setup.Installed || !state.Approved {
		t.Fatalf("J-COMFY: Setup does not list the stand-in ComfyUI installed in an approved plan: %q %v", state.phase("comfyui-app"), state.Approved)
	}
	// The installed package passes the receipt check Register reopens.
	if _, err := setup.OpenInstalledComfy(ctx, installed.journal); err != nil {
		t.Fatalf("J-COMFY: the stand-in installation fails the receipt check Register runs: %v", err)
	}
	t.Logf("J-COMFY: installed %s with %s registered", installed.program, stale)

	// 2. The Setup page's workspace step after the install.
	code, body, took := call(http.MethodGet, "/setup/comfy/status", "", 10*time.Second)
	t.Logf("J-COMFY: GET /setup/comfy/status %d in %s: %s", code, took.Round(time.Millisecond), body)
	var status workspaceApplicationReply
	if code != http.StatusOK || json.Unmarshal([]byte(body), &status) != nil {
		t.Fatalf("J-COMFY: the workspace step's status read failed: %d %s", code, body)
	}
	if !status.Conflict && (!status.Registered || status.Shortcut != nil && !*status.Shortcut) {
		request := fmt.Sprintf(`{"setup_plan_id":%q,"confirm":true}`, state.Plan.ID)
		code, body, took = call(http.MethodPost, "/setup/comfy/register", request, 120*time.Second)
		t.Logf("J-COMFY: POST /setup/comfy/register %d in %s: %s", code, took.Round(time.Millisecond), body)
	}

	// 3. The registration names the installed program; the development
	// copy's entry is gone.
	entry := registry.current()
	switch {
	case entry == nil:
		t.Errorf("J-COMFY A3 FAIL: no comfyui entry is registered after the workspace step")
	case !identity.SameSubjectProgram(entry.program, installed.program):
		t.Errorf("J-COMFY A3 FAIL: the comfyui entry still names %s, not the installed %s. The workspace step's rule: an entry for another program is replaced only when comfyEntryReplaceable accepts it (program gone, inside Setup's install folders, or signed by the Panel's signer); this one is %v, so Setup preserved it (setup_comfy_application.go:228-230) and registered nothing",
			entry.program, installed.program, comfyEntryReplaceable(entryDescriptor(entry)))
	default:
		t.Logf("J-COMFY A3 PASS: the comfyui entry names the installed %s", entry.program)
	}

	// 4. The Start-menu shortcut Setup writes (the isolated runtime's own
	// Start Menu folder) opens the registered ComfyUI through the Panel.
	menu := filepath.Join(stateDir, "setup", "Start Menu", "Programs")
	want, err := officialPicture.retainedShortcut().want()
	if err != nil {
		t.Fatal(err)
	}
	shortcut := ""
	for _, name := range officialPicture.retainedShortcut().names() {
		if have, err := readStartShortcut(filepath.Join(menu, name)); err == nil && sameShortcut(have, want) {
			shortcut = filepath.Join(menu, name)
		}
	}
	switch {
	case shortcut == "":
		t.Errorf("J-COMFY A4 FAIL: no Setup shortcut in %s: the workspace step writes it only after the registration succeeded (completeComfyWorkspace, setup_comfy_application.go:175-184)", menu)
	case entry == nil || !identity.SameSubjectProgram(entry.program, installed.program):
		t.Errorf("J-COMFY A4 FAIL: %s runs %s %s, which opens the registered comfyui entry, and that entry is not the installed program", shortcut, want.Target, want.Arguments)
	default:
		t.Logf("J-COMFY A4 PASS: %s runs %s %s, which opens the installed program", shortcut, want.Target, want.Arguments)
	}

	// 5. Open on the app's page while the runtime's activation waits (its
	// Allow dialog) past the route's own bound.
	registry.blockActivation()
	pageApps, shown, _, err := panel.appPageApplications(ctx, setup.ComfyApplicationName)
	if err != nil || shown == nil || pageApps == nil {
		t.Fatalf("J-COMFY: the app page has no comfyui entry to open: %v", err)
	}
	request := fmt.Sprintf(`{"confirm":true,"shown":%q}`, applicationDigest(&shown.Descriptor))
	code, body, took = call(http.MethodPost, "/app/comfyui/start", request, 90*time.Second)
	t.Logf("J-COMFY: POST /app/comfyui/start %d in %s: %s", code, took.Round(time.Millisecond), body)
	message := body
	var reply appActionReply
	if code == http.StatusOK && json.Unmarshal([]byte(body), &reply) == nil {
		message = reply.Message
	}
	title := appPageTitle(shown.Descriptor)
	if strings.Contains(strings.ToLower(message), "deadline exceeded") || !strings.Contains(message, title) {
		t.Errorf("J-COMFY A5 FAIL: Open answered %d %q after %s; the page shows a sentence naming %q and what to do, not the raw error (appStart, app_page.go:485-488 returns panelError(err))",
			code, message, took.Round(time.Second), title)
	} else {
		t.Logf("J-COMFY A5 PASS: Open answered %q", message)
	}
	t.Logf("J-COMFY: finished in %s", time.Since(started).Round(time.Millisecond))
}

// journeyEntry is the one comfyui registration the journey's applications
// service holds.
type journeyEntry struct {
	program, revision, title string
	self                     bool
}

func entryDescriptor(e *journeyEntry) fwire.ApplicationDescriptor {
	return fwire.ApplicationDescriptor{Name: setup.ComfyApplicationName, Program: e.program, Title: e.title}
}

// journeyRegistry is the runtime's applications@3 service for the journey:
// Observe, Remove with the observed revision, Register, and Activate. A
// program's register-self is the stand-in helper's record of its own path.
type journeyRegistry struct {
	t              *testing.T
	mu             sync.Mutex
	entry          *journeyEntry
	selfRegistered string
	consumed       bool
	revisions      int
	blocking       bool
}

func (r *journeyRegistry) blockActivation() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.blocking = true
}

// current is the registration after reading a register-self the helper
// made since the last read.
func (r *journeyRegistry) current() *journeyEntry {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.consumed {
		if raw, err := os.ReadFile(r.selfRegistered); err == nil {
			r.consumed = true
			r.revisions++
			r.entry = &journeyEntry{program: strings.TrimSpace(string(raw)), revision: fmt.Sprintf("self-%d", r.revisions), title: "ComfyUI — OA build", self: true}
		}
	}
	if r.entry == nil {
		return nil
	}
	copied := *r.entry
	return &copied
}

func (r *journeyRegistry) transport(ctx context.Context) applications.ApplicationsTransport {
	return nativeApplicationExchange(func(b []byte) ([]byte, error) { return r.exchange(ctx, b) })
}

func (r *journeyRegistry) exchange(ctx context.Context, b []byte) ([]byte, error) {
	var frame struct {
		Service, Method string
		Arguments       json.RawMessage
	}
	if err := json.Unmarshal(b, &frame); err != nil {
		return nil, err
	}
	var value []byte
	switch frame.Method {
	case "Observe":
		page := applications.Page{Outcome: applications.OutcomePage}
		if e := r.current(); e != nil {
			manifest := registrationwire.Manifest{Version: "applications@3", Name: setup.ComfyApplicationName, Title: e.title,
				Offers: appmodel.CommandDeclaration{Version: "abstraction.facade/appmodel@1"},
				Verbs:  []registrationwire.ActivationVerb{{Name: "open", Class: appmodel.CommandClassReveal, InputSchema: applications.Raw(`{"type":"object","properties":{},"additionalProperties":false}`)}}}
			row := nativeRegistrationFixture(manifest, e.program, e.revision, strings.Repeat("a", 64), nil)
			if e.self {
				row.Source = registrationwire.SourceSelfRegistration
			}
			page.Registrations = []registrationwire.Registration{row}
		}
		value = applications.EncodePage(&page)
	case "Remove":
		var args struct{ Request json.RawMessage }
		if err := json.Unmarshal(frame.Arguments, &args); err != nil {
			return nil, err
		}
		request, err := applications.DecodeChangeRequest(args.Request)
		if err != nil {
			return nil, err
		}
		r.mu.Lock()
		outcome := applications.OutcomeStale
		if r.entry != nil && request.ExpectedRevision == r.entry.revision {
			r.entry, outcome = nil, applications.OutcomeApplied
		}
		r.mu.Unlock()
		value = applications.EncodeChange(&applications.Change{Outcome: outcome})
	case "Register":
		var args struct{ Request applications.RegisterRequest }
		if err := json.Unmarshal(frame.Arguments, &args); err != nil {
			return nil, err
		}
		r.mu.Lock()
		r.revisions++
		r.entry = &journeyEntry{program: args.Request.Program, revision: fmt.Sprintf("register-%d", r.revisions), title: "ComfyUI — OA build"}
		r.mu.Unlock()
		value = applications.EncodeChange(&applications.Change{Outcome: applications.OutcomeApplied})
	case "Activate":
		r.mu.Lock()
		blocking := r.blocking
		r.mu.Unlock()
		if blocking {
			// The runtime waits for the person's Allow.
			<-ctx.Done()
			return nil, ctx.Err()
		}
		value = applications.EncodeActivationResult(&applications.ActivationResult{Outcome: applications.ActivationOutcomeReady, Instance: "journey"})
	default:
		return nil, fmt.Errorf("J-COMFY: unexpected applications call %s", frame.Method)
	}
	return json.Marshal(map[string]any{"version": 1, "service": frame.Service, "method": frame.Method, "ok": true, "payload": map[string]any{"value": json.RawMessage(value)}})
}
