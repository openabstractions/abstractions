//go:build windows

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Architect ruling 2026-11-11 (APPLICATIONS.md "Connect is the Keep"): the
// Panel's Connect click, recorded through ExpectConnect, keeps the app's own
// registration of exactly the pinned image at the pinned path, once, within
// ten minutes. These tests register real unsigned PE files through the
// production applications@3 register path.

type connectRun struct {
	*unsignedRun
	clock time.Time
}

func newConnectRun(t *testing.T) *connectRun {
	t.Helper()
	run := &connectRun{unsignedRun: newUnsignedRun(t, "ComfyUI"), clock: time.Now()}
	panel := run.f.panel.Program
	run.d.connectPanel = func(program string) bool { return identity.SameSubjectProgram(program, panel) }
	run.d.operator = func(program string) bool { return identity.SameSubjectProgram(program, panel) }
	run.d.now = func() time.Time { return run.clock }
	return run
}

func (run *connectRun) pin(t *testing.T) string {
	t.Helper()
	image, _ := imagePin(t, run.program)
	return image
}

func (run *connectRun) expect(subject rwire.Subject, name, program, sha256 string) wire.ApplicationChange {
	return run.d.expectConnect(run.ctx, subject, applications.ConnectExpectation{Application: name, Program: program, SHA256: sha256})
}

// registerSelf is the connector's own register-self through applications@3.
func (run *connectRun) registerSelf(t *testing.T) applicationV3Registration {
	t.Helper()
	if out := run.d.registerApplicationV3(run.ctx, run.subject, fileid.Self, run.program, ""); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("register-self: %+v", out)
	}
	e, _, ok := run.entry()
	if !ok {
		t.Fatal("register-self left no entry")
	}
	return e
}

func (run *connectRun) pendingKeeps(t *testing.T) []asks.Record {
	t.Helper()
	history, _, err := run.f.a.book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	var out []asks.Record
	for _, r := range history {
		if r.Pending() && r.Key == asks.SelfRegisteredKey {
			out = append(out, r)
		}
	}
	return out
}

// askedOnce is the Panel's fallback after a Connect that matched nothing:
// AskConfirmation raises exactly one Keep question and the entry waits.
func (run *connectRun) askedOnce(t *testing.T, why string) {
	t.Helper()
	if e, _, _ := run.entry(); e.State != "pending" {
		t.Fatalf("%s: the entry is %q, want it waiting", why, e.State)
	}
	if r := run.d.askConfirmation(run.ctx, run.f.panel, "comfyui"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("%s: AskConfirmation %+v", why, r)
	}
	if pending := run.pendingKeeps(t); len(pending) != 1 {
		t.Fatalf("%s: want one Keep question, got %+v", why, pending)
	}
}

func connectKeptRecord(t *testing.T, run *connectRun) map[string]connectKept {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(run.d.dir, connectKeptFile))
	if err != nil {
		t.Fatal(err)
	}
	kept := map[string]connectKept{}
	if err := json.Unmarshal(raw, &kept); err != nil {
		t.Fatal(err)
	}
	return kept
}

// A Connect click names the connector and its pinned SHA-256; the
// connector's own registration of exactly that image is kept with decision
// source panel_connect, and nobody is asked.
func TestConnectExpectationKeepsTheMatchingRegistration(t *testing.T) {
	run := newConnectRun(t)
	if r := run.expect(run.f.panel, "comfyui", run.program, run.pin(t)); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("ExpectConnect: %+v", r)
	}
	if e := run.registerSelf(t); e.State != "enabled" || e.Question != "" {
		t.Fatalf("matched register-self: %+v", e)
	}
	if pending := run.pendingKeeps(t); len(pending) != 0 {
		t.Fatalf("a matched Connect asked: %+v", pending)
	}
	kept := connectKeptRecord(t, run)["comfyui"]
	if kept.Decision != "panel_connect" || kept.SHA256 != run.pin(t) || !identity.SameSubjectProgram(kept.Program, run.program) {
		t.Fatalf("Keep record %+v", kept)
	}
}

// The owner's machine on R30: the entry has waited since an earlier Connect
// and was never asked about, or its question is open. Connect records the
// expectation and the connector registers again, unchanged; that
// re-registration goes through the same match and keeps the entry. An open
// Keep question is retired.
func TestConnectExpectationKeepsAnEntryAlreadyWaiting(t *testing.T) {
	for _, asked := range []bool{false, true} {
		name := "never asked"
		if asked {
			name = "question open"
		}
		t.Run(name, func(t *testing.T) {
			run := newConnectRun(t)
			first := run.registerSelf(t)
			if first.State != "pending" {
				t.Fatalf("register-self without a Connect click: %+v", first)
			}
			if asked {
				run.askedOnce(t, "before the second Connect")
			}
			if r := run.expect(run.f.panel, "comfyui", run.program, run.pin(t)); r.Outcome != wire.ApplicationOutcomeApplied {
				t.Fatalf("ExpectConnect: %+v", r)
			}
			if e := run.registerSelf(t); e.State != "enabled" {
				t.Fatalf("re-registration of the waiting entry was not kept: %+v", e)
			}
			if pending := run.pendingKeeps(t); len(pending) != 0 {
				t.Fatalf("the open Keep question stayed: %+v", pending)
			}
		})
	}
}

// Checklist: only the Panel's person-driven path records an expectation.
// The app itself, the command line, the MCP gateway, another account and a
// Panel without application.manage are refused, and nothing they sent keeps
// a registration.
func TestConnectExpectationOnlyFromThePanel(t *testing.T) {
	run := newConnectRun(t)
	pin := run.pin(t)
	gateway := rwire.Subject{Account: run.f.answerer.Account, Program: identity.NormalizeSubjectProgram(filepath.Join(t.TempDir(), "openabstractions-mcp.exe"))}
	if err := run.f.a.rights.policy.Set(gateway, ActionApplicationManage, "account", true); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name    string
		subject rwire.Subject
	}{
		{"the app itself", run.subject},
		{"the command line", run.f.cli},
		{"the MCP gateway", gateway},
		{"another account", rwire.Subject{Account: "S-1-5-21-0-0-0-1", Program: run.f.panel.Program}},
	} {
		if r := run.expect(c.subject, "comfyui", run.program, pin); r.Outcome != wire.ApplicationOutcomeForbidden {
			t.Errorf("%s recorded an expectation: %+v", c.name, r)
		}
	}
	// The Panel program without application.manage.
	if err := run.f.a.rights.policy.Set(run.f.panel, ActionApplicationManage, "account", false); err != nil {
		t.Fatal(err)
	}
	if r := run.expect(run.f.panel, "comfyui", run.program, pin); r.Outcome == wire.ApplicationOutcomeApplied {
		t.Errorf("a Panel without application.manage recorded an expectation: %+v", r)
	}
	if err := run.f.a.rights.policy.Set(run.f.panel, ActionApplicationManage, "account", true); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []applications.ConnectExpectation{
		{Application: "comfyui", Program: "comfyui-connector.exe", SHA256: pin},
		{Application: "comfyui", Program: run.program, SHA256: strings.ToUpper(pin)},
		{Application: "comfyui", Program: run.program, SHA256: pin[:63]},
		{Application: "Comfy UI", Program: run.program, SHA256: pin},
	} {
		if r := run.d.expectConnect(run.ctx, run.f.panel, bad); r.Outcome != wire.ApplicationOutcomeInvalid {
			t.Errorf("invalid expectation %+v: %+v", bad, r)
		}
	}
	run.registerSelf(t)
	run.askedOnce(t, "after refused expectations")
}

// The runtime's Panel predicate admits the Panel among the operator
// programs, never the runtime's own images, the presenter or a program that
// is not an operator.
func TestConnectExpectationPanelIsTheOnlyOperatorAdmitted(t *testing.T) {
	dir := t.TempDir()
	at := func(name string) string { return identity.NormalizeSubjectProgram(filepath.Join(dir, name)) }
	for _, name := range oaProgramFiles(oaProgramStems) {
		if err := os.WriteFile(at(name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	twin := filepath.Join(t.TempDir(), "development-panel.exe")
	if err := os.Link(at("openabstractionsw.exe"), twin); err != nil {
		t.Fatal(err)
	}
	panelAlias := filepath.Join(t.TempDir(), "panel-alias.exe")
	if err := os.Link(at("Abstraction Panel.exe"), panelAlias); err != nil {
		t.Fatal(err)
	}
	operators, panels := operatorSiblings(at("openabstractions.exe"), twin)
	defer panels.close()
	r := &runtimeRights{operators: operators, panels: panels}
	other := filepath.Join(t.TempDir(), "Abstraction Panel.exe")
	if err := os.WriteFile(other, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for program, want := range map[string]bool{
		at("Abstraction Panel.exe"):          true,
		panelAlias:                           true,
		at("openabstractions.exe"):           false,
		at("openabstractionsw.exe"):          false,
		at("OpenAbstractions Presenter.exe"): false,
		at("openabstractions-mcp.exe"):       false,
		at("comfyui-connector.exe"):          false,
		twin:                                 false,
		other:                                false,
	} {
		if got := r.isPanel(program); got != want {
			t.Errorf("isPanel(%s) = %t, want %t", filepath.Base(program), got, want)
		}
	}
}

func TestPanelRoleSurvivesAliasAndRefusesReplacementAndNewReservedImage(t *testing.T) {
	dir := t.TempDir()
	exe := filepath.Join(dir, "openabstractions.exe")
	dev := filepath.Join(t.TempDir(), "dev-panel.exe")
	for _, path := range []string{exe, dev} {
		if err := os.WriteFile(path, nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	operators, panels := operatorSiblings(exe, dev)
	defer panels.close()
	r := &runtimeRights{operators: operators, panels: panels}
	if !r.isPanel(dev) {
		t.Fatal("approved development Panel refused")
	}
	original := filepath.Join(filepath.Dir(dev), "original.exe")
	if err := os.Rename(dev, original); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dev, []byte("replacement image"), 0600); err != nil {
		t.Fatal(err)
	}
	if r.isPanel(dev) || !r.isPanel(original) {
		t.Fatal("a replacement file acquired the Panel role by name")
	}
	if err := os.Remove(dev); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(exe, dev); err != nil {
		t.Fatal(err)
	}
	if r.isPanel(dev) || r.isPanel(exe) || !r.isPanel(original) {
		t.Fatal("Panel trust followed a replaced name instead of the original file")
	}
	presenter := filepath.Join(dir, "OpenAbstractions Presenter.exe")
	if err := os.Link(original, presenter); err != nil {
		t.Fatal(err)
	}
	if r.isPanel(presenter) || r.isPanel(original) {
		t.Fatal("a newly installed reserved image acquired the Panel role")
	}
}

func TestExpectConnectChecksTheLivePanelImage(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	principal, err := identity.Self()
	if err != nil {
		t.Fatal(err)
	}
	account, err := principal.Account()
	if err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(t.TempDir(), "other-panel.exe")
	if err := os.WriteFile(other, []byte("different native file"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, panel string
		want        wire.ApplicationOutcome
	}{
		{"selected_peer", exe, wire.ApplicationOutcomeApplied},
		{"different_peer", other, wire.ApplicationOutcomeForbidden},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			panel, err := openPanelImage(ctx, fileid.Self, test.panel, fileid.RefuseLinks)
			if err != nil {
				t.Fatal(err)
			}
			policy := &runtimeRights{owner: account, panels: panelRoster{images: []fileid.Handle{panel}}}
			defer policy.close()
			d, err := openApplications(t.TempDir(), account, func(context.Context, rwire.Subject, string, string) (bool, error) {
				return true, nil
			})
			if err != nil {
				t.Fatal(err)
			}
			// Force the directory's synthetic subject check to permit both
			// requests. Only the production peer gate can refuse the second.
			d.connectPanel = func(string) bool { return true }
			d.connectPeer = policy.isPanelPeer
			d.operator = func(string) bool { return true }
			host := &applicationsHost{owner: account, directory: d, v3: &applicationV3Gateway{}}
			id, err := applicationToken()
			if err != nil {
				t.Fatal(err)
			}
			endpoint := mustProviderEndpoint(t, "oa-test-connect-panel-"+id[:16])
			listener, err := listen.ListenFramed(endpoint, listen.Program)
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan error, 1)
			serve := func() error {
				conn, err := listener.Accept()
				if err != nil {
					return err
				}
				defer conn.Close()
				call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 4096)
				if err != nil {
					return err
				}
				defer call.Close()
				reply, err := host.exchangeApplicationFrame(ctx, call)
				if err != nil {
					return err
				}
				return call.Reply(reply)
			}
			go func() {
				for range 2 {
					if err := serve(); err != nil {
						done <- err
						return
					}
				}
				done <- nil
			}()
			server := listen.ServerExpectation{Principal: principal, Program: exe}
			transport := listen.FrameClient{Endpoint: endpoint, Server: &server, Timeout: 5 * time.Second, MaxFrame: 4096}.WithContext(ctx)
			client := applications.NewApplicationsOperatorClient(transport)
			got, err := client.ExpectConnect(applications.ConnectExpectation{Application: "comfyui", Program: exe, SHA256: strings.Repeat("a", 64)})
			if err != nil || got.Outcome != test.want {
				t.Fatalf("ExpectConnect = %+v, %v", got, err)
			}
			bind := true
			enrollment, err := applications.NewApplicationsClient(transport).SetAgentEnrollment(applications.AgentEnrollmentRequest{
				Application: catalogueAgentApplication, Enabled: true, BindCatalogueHost: &bind,
			})
			// The selected Panel reaches the pre-existing native image lookup.
			// The fixture has no catalogue connector to enrol.
			want, reason := applications.OutcomeUnavailable, "native_identity"
			if test.want == wire.ApplicationOutcomeForbidden {
				want, reason = applications.OutcomeForbidden, "catalogue_host"
			}
			if err != nil || enrollment.Outcome != want || enrollment.Reason != reason {
				t.Fatalf("SetAgentEnrollment BindCatalogueHost = %+v, %v", enrollment, err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

// Checklist: the match covers name, path and hash, and the window. Each
// mismatch leaves the entry waiting, and the Panel's fallback asks.
func TestConnectExpectationMismatchFallsBackToTheQuestion(t *testing.T) {
	other := strings.Repeat("0", 64)
	for _, c := range []struct {
		name   string
		expect func(run *connectRun) wire.ApplicationChange
		before func(run *connectRun)
	}{
		{"hash", func(run *connectRun) wire.ApplicationChange {
			return run.expect(run.f.panel, "comfyui", run.program, other)
		}, nil},
		{"path", func(run *connectRun) wire.ApplicationChange {
			return run.expect(run.f.panel, "comfyui", filepath.Join(filepath.Dir(run.program), "other", "comfy-helper.exe"), run.pin(t))
		}, nil},
		{"name", func(run *connectRun) wire.ApplicationChange {
			return run.expect(run.f.panel, "comfyui2", run.program, run.pin(t))
		}, nil},
		{"expired", func(run *connectRun) wire.ApplicationChange {
			return run.expect(run.f.panel, "comfyui", run.program, run.pin(t))
		}, func(run *connectRun) { run.clock = run.clock.Add(connectExpectationWindow) }},
	} {
		t.Run(c.name, func(t *testing.T) {
			run := newConnectRun(t)
			if r := c.expect(run); r.Outcome != wire.ApplicationOutcomeApplied {
				t.Fatalf("ExpectConnect: %+v", r)
			}
			if c.before != nil {
				c.before(run)
			}
			run.registerSelf(t)
			run.askedOnce(t, c.name+" mismatch")
		})
	}
}

// Checklist: one expectation keeps one registration. After its Keep the
// entry is removed and the connector registers again: nothing is kept
// without a new click, and the Panel's fallback asks.
func TestConnectExpectationIsSingleUse(t *testing.T) {
	run := newConnectRun(t)
	if r := run.expect(run.f.panel, "comfyui", run.program, run.pin(t)); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("ExpectConnect: %+v", r)
	}
	kept := run.registerSelf(t)
	if kept.State != "enabled" {
		t.Fatalf("first register-self: %+v", kept)
	}
	if r := run.d.changeApplicationV3(run.ctx, run.f.panel, "comfyui", kept.Revision, "remove"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("remove: %+v", r)
	}
	run.registerSelf(t)
	run.askedOnce(t, "an expectation already used")
}

// removeWaiting removes the waiting comfyui entry at revision.
func (run *connectRun) removeWaiting(t *testing.T, revision string) {
	t.Helper()
	if r := run.d.changeApplicationV3(run.ctx, run.f.panel, "comfyui", revision, "remove"); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("remove: %+v", r)
	}
}

// Review R31 F1: the Keep binds to the registration whose image matched.
// The connector's image is replaced and the replacement registers under the
// same name before the matched registration's deferred Connect runs; that
// Connect keeps nothing, records nothing, and the Panel's fallback asks.
func TestConnectExpectationKeepsOnlyTheCheckedRegistration(t *testing.T) {
	run := newConnectRun(t)
	if r := run.expect(run.f.panel, "comfyui", run.program, run.pin(t)); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("ExpectConnect: %+v", r)
	}
	checked, connect := run.d.registerApplicationV3Held(run.ctx, run.subject, fileid.Self, run.program, "")
	if checked.Outcome != wire.ApplicationOutcomeApplied || connect == nil {
		t.Fatalf("register-self of the pinned image: %+v", checked)
	}
	// The same path holding another image conflicts while the checked entry
	// exists; the replacement registers once that entry is gone.
	run.removeWaiting(t, checked.Revision)
	writeUnsignedImage(t, run.program, "ComfyUI", true)
	replaced, _ := run.d.registerApplicationV3Held(run.ctx, run.subject, fileid.Self, run.program, "")
	if replaced.Outcome != wire.ApplicationOutcomeApplied || replaced.Revision == checked.Revision {
		t.Fatalf("register-self of the replaced image: %+v (checked %s)", replaced, checked.Revision)
	}
	connect()
	if e, _, _ := run.entry(); e.State != "pending" || e.Revision != replaced.Revision {
		t.Fatalf("the Connect of the checked image kept the replacement: %+v", e)
	}
	if _, err := os.Stat(filepath.Join(run.d.dir, connectKeptFile)); !os.IsNotExist(err) {
		t.Fatalf("a Keep was recorded for the replacement: %v", err)
	}
	run.askedOnce(t, "a replaced image")
}

// Review R31 N1: the first own registration for the name uses the click up
// whether or not it matches. A later registration of the pinned image waits
// for the person's Keep question.
func TestConnectExpectationMismatchUsesItUp(t *testing.T) {
	run := newConnectRun(t)
	pin := run.pin(t)
	if r := run.expect(run.f.panel, "comfyui", run.program, pin); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatalf("ExpectConnect: %+v", r)
	}
	writeUnsignedImage(t, run.program, "ComfyUI", true)
	mismatched := run.registerSelf(t)
	if mismatched.State != "pending" {
		t.Fatalf("a mismatching image was kept: %+v", mismatched)
	}
	run.removeWaiting(t, mismatched.Revision)
	writeUnsignedImage(t, run.program, "ComfyUI", false)
	if run.pin(t) != pin {
		t.Fatal("the pinned image was not restored")
	}
	if e := run.registerSelf(t); e.State != "pending" {
		t.Fatalf("the pinned image was kept after a mismatch used the click: %+v", e)
	}
	run.askedOnce(t, "a click already used by a mismatch")
}

func TestConnectExpectationReplayPreservesLiveRecord(t *testing.T) {
	run := newConnectRun(t)
	pin := run.pin(t)
	const name = "comfyui"
	expect := func(program, sha256 string) connectExpectation {
		t.Helper()
		if out := run.expect(run.f.panel, name, program, sha256); out.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatalf("ExpectConnect: %+v", out)
		}
		return run.d.connects.pending[name]
	}
	first := expect(run.program, pin)
	run.clock = first.expires.Add(-time.Nanosecond)
	if replay := expect(filepath.Dir(run.program)+string(filepath.Separator)+"."+string(filepath.Separator)+filepath.Base(run.program), pin); replay != first {
		t.Fatalf("live replay changed record: %+v -> %+v", first, replay)
	}
	run.clock = first.expires
	second := expect(run.program, pin)
	if second.id == first.id || !second.expires.Equal(run.clock.Add(connectExpectationWindow)) {
		t.Fatalf("fresh click after expiry: %+v", second)
	}
	if entry := run.registerSelf(t); entry.State != "enabled" {
		t.Fatalf("fresh click after expiry did not keep the registration: %+v", entry)
	}
	third := expect(run.program, pin)
	if third.id == second.id {
		t.Fatal("fresh click after consumption reused its receipt")
	}
	if _, ok := run.d.takeConnectExpected(name, run.program, pin, second.id); ok || run.d.connects.pending[name] != third {
		t.Fatal("consumed receipt affected a new expectation")
	}

	otherPath := filepath.Join(filepath.Dir(run.program), "other", filepath.Base(run.program))
	pathReplacement := expect(otherPath, pin)
	if pathReplacement.id == third.id || !identity.SameSubjectProgram(pathReplacement.program, otherPath) {
		t.Fatalf("same pin, different path did not replace expectation: %+v", pathReplacement)
	}
	otherPin := strings.Repeat("a", 64)
	if otherPin == pin {
		otherPin = strings.Repeat("b", 64)
	}
	pinReplacement := expect(otherPath, otherPin)
	if pinReplacement.id == pathReplacement.id || pinReplacement.sha256 != otherPin {
		t.Fatalf("different pin did not replace expectation: %+v", pinReplacement)
	}
}
