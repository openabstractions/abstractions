package main

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-identity/fileid"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// J-COMFY-OPEN (journey suite, 2026-11-10): the person asked OpenCode to open
// ComfyUI, and the kept agent's oa_applications_describe listed no
// application, since it held no command.read rule. A kept agent may open an
// application whose verb class it may use (FAC-M32); the unnamed listing names
// such an application by name and title, with no instance until read is
// allowed. Both listing forms return the manifest verb without asking.
func TestApplicationV3GatewayKeptAgentListsOpenableApplication(t *testing.T) {
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	for _, query := range []appmodel.ApplicationsQuery{{}, {Application: "fixture"}} {
		listed, err := f.c.Describe(query)
		if err != nil || listed.Outcome != applications.OutcomePage || listed.Answer == nil ||
			len(listed.Answer.Applications) != 1 || len(listed.Answer.OpenVerbs) != 1 {
			t.Fatalf("describe %+v: %+v %v", query, listed, err)
		}
		entry := listed.Answer.Applications[0]
		if entry.Application != "fixture" || entry.Title != "Fixture" || len(entry.Instances) != 0 {
			t.Fatalf("open-only entry: %+v", entry)
		}
		open := listed.Answer.OpenVerbs[0]
		want := f.manifest.Verbs[0]
		if open.Application != entry.Application || len(open.Verbs) != 1 ||
			open.Verbs[0].Name != want.Name || open.Verbs[0].InputSchema != appmodel.Raw(want.InputSchema) ||
			open.Verbs[0].Description == "" {
			t.Fatalf("open metadata: %+v", open)
		}
	}
	if pending := pendingApplicationQuestions(t, permission); len(pending) != 0 {
		t.Fatalf("describing open verbs asked the person: %+v", pending)
	}
	if err := permission.a.rights.policy.Set(f.subject, "abstraction.facade/command.read", "app:fixture", true); err != nil {
		t.Fatal(err)
	}
	listed, err := f.c.Describe(appmodel.ApplicationsQuery{Application: "fixture"})
	if err != nil || listed.Answer == nil || len(listed.Answer.Applications) != 1 ||
		len(listed.Answer.Applications[0].Instances) != 1 || len(listed.Answer.OpenVerbs) != 1 {
		t.Fatalf("authorized read: %+v %v", listed, err)
	}
}

// The gateway's production listing source (listDeclarations) filtered by
// command.read before describe decided anything, so the kept agent's unnamed
// listing stayed empty in J-COMFY-OPEN although describe named openable
// applications. It now hands describe every kept declaration; the
// applications@3 Observe and command snapshots keep their own rights filter.
func TestApplicationV3DeclarationsLeaveTheReadDecisionToDescribe(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	registration1459Publish(t, d, "native", registration1459Program("native.exe"), "Native", "operator", "")
	d.decide = func(context.Context, rwire.Subject, string, string) (bool, error) { return false, nil }
	subject := rwire.Subject{Account: "account", Program: "agent"}
	views, _, _, out := d.applicationV3Declarations(ctx, subject)
	if out != legacy.ApplicationOutcomePage || len(views) != 1 || views[0].Name != "native" {
		t.Fatalf("the describe source dropped a declaration the caller may not read: %v %+v", out, views)
	}
	for _, action := range []string{ActionApplicationRead, "abstraction.facade/command.read"} {
		if views, _, _, out := d.applicationV3SnapshotForAction(ctx, subject, "", action); out != legacy.ApplicationOutcomePage || len(views) != 0 {
			t.Fatalf("%s snapshot disclosed an unpermitted declaration: %v %+v", action, out, views)
		}
	}
	if _, _, _, out := d.applicationV3Declarations(ctx, rwire.Subject{Account: "other", Program: "agent"}); out != legacy.ApplicationOutcomeForbidden {
		t.Fatalf("another account read the declarations: %v", out)
	}
}

// Only an application the agent could open is named: a program that is no
// kept agent, a stored deny on the verb's class or on application.activate,
// a manifest with no open verb, and a registration that is not enabled keep
// it absent.
func TestApplicationV3GatewayUnnamedListingNamesOnlyOpenableApplications(t *testing.T) {
	for _, variant := range []string{"not-agent", "class-deny", "activate-deny", "no-verb", "pending", "disabled", "removing"} {
		t.Run(variant, func(t *testing.T) {
			f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, variant != "not-agent")
			switch variant {
			case "class-deny":
				if err := permission.a.rights.policy.Set(f.subject, "abstraction.facade/command.reveal", "app:fixture", false); err != nil {
					t.Fatal(err)
				}
			case "activate-deny":
				if err := permission.a.rights.policy.Set(f.subject, ActionApplicationActivate, "app:fixture", false); err != nil {
					t.Fatal(err)
				}
			case "no-verb":
				f.manifest.Verbs = nil
			case "pending", "disabled", "removing":
				f.state = variant
			}
			listed, err := f.c.Describe(appmodel.ApplicationsQuery{})
			if err != nil || listed.Answer == nil || listed.Answer.Outcome != appmodel.ReadingOutcomePage || len(listed.Answer.Applications) != 0 || len(listed.Answer.OpenVerbs) != 0 {
				t.Fatalf("%s: the listing names the application: %+v %v", variant, listed, err)
			}
			if pending := pendingApplicationQuestions(t, permission); len(pending) != 0 {
				t.Fatalf("%s: the unnamed listing asked the person: %+v", variant, pending)
			}
		})
	}
}

// J-COMFY-OPEN: the person opened the app from the Panel, then the agent
// opened it. While it runs the agent reaches the running instance with no
// question and no second start. After the person closed it, the closed
// session stayed in the gateway and every later open read unknown/owned_host
// ("delivery uncertain; session retired") until the runtime restarted. A
// session whose process exited now retires and the agent's open, after the
// person's Open answer, reaches the start; a channel that ended while its
// process may still run still starts nothing.
//
// The fixture has one native subject: it opens as the person's Panel while it
// holds application.activate, and as the kept agent once that rule is revoked.
func TestApplicationV3GatewayAgentOpensAppThePersonOpened(t *testing.T) {
	for _, variant := range []string{"running", "closed", "crashed", "channel-lost"} {
		t.Run(variant, func(t *testing.T) {
			f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
			if err := permission.a.rights.policy.Set(f.subject, ActionApplicationActivate, "app:fixture", true); err != nil {
				t.Fatal(err)
			}
			person, err := f.c.Activate(receiptKey(110, time.Now()), openRequest)
			if err != nil || person.Outcome != applications.ActivationOutcomeReady || person.Instance != f.instance || f.effects.Load() != 1 {
				t.Fatalf("the person's Open: %+v %v effects%d", person, err, f.effects.Load())
			}
			if err := permission.a.rights.policy.Revoke(f.subject, ActionApplicationActivate, "app:fixture"); err != nil {
				t.Fatal(err)
			}
			launches, starts := new(atomic.Int32), new(atomic.Int32)
			f.g.directory.unsignedLaunch = func(string, bool) { launches.Add(1) }
			f.g.start = func(context.Context, ownedApplicationImage, ownedApplicationBinding, []string, func(context.Context, ownedApplicationBinding) error) (*ownedApplicationSession, error) {
				starts.Add(1)
				return nil, errors.New("fixture start")
			}
			if variant == "running" {
				agent, err := f.c.Activate(receiptKey(111, time.Now()), openRequest)
				if err != nil || agent.Outcome != applications.ActivationOutcomeReady || agent.Instance != f.instance || agent.Started || launches.Load() != 0 || starts.Load() != 0 {
					t.Fatalf("the agent's open of the running app: %+v %v launches%d starts%d", agent, err, launches.Load(), starts.Load())
				}
				if pending := pendingApplicationQuestions(t, permission); len(pending) != 0 {
					t.Fatalf("the agent's open of the running app asked the person: %+v", pending)
				}
				return
			}
			// The person closes the app: its presence ends and its channel
			// closes; "closed" also has the launch owner's wait for its exit.
			// "crashed" has the process exit with its presence lease still
			// held; the launch owner's confirmed exit ends that lease.
			if variant != "crashed" {
				f.g.directory.mu.Lock()
				f.g.directory.instances = map[string]applicationLease{}
				f.g.directory.mu.Unlock()
			}
			if variant == "channel-lost" {
				_ = f.s.Close()
			} else {
				_ = f.s.processExited()
			}
			var started atomic.Int32
			f.g.report = func(err error) {
				if strings.Contains(err.Error(), "owned start") && !errors.Is(err, errOwnedApplicationUncertain) {
					started.Add(1)
				}
			}
			done, question := beginOpen(t, f, permission, 112)
			answerOpen(t, permission, question.ID, "once")
			got := waitOpen(t, done)
			if got.err != nil {
				t.Fatal(got.err)
			}
			// The fixture holds no signed image, so a start that passed the
			// retired session ends at the held-image revalidation that
			// precedes g.start, as identity_refused.
			if variant != "channel-lost" && (launches.Load() != 1 || started.Load() != 1 || got.result.Outcome != applications.ActivationOutcomeIdentityRefused) {
				t.Fatalf("the agent's open of the app the person closed: %+v launches%d starts%d", got.result, launches.Load(), starts.Load())
			}
			if variant == "channel-lost" && (launches.Load() != 0 || starts.Load() != 0 || got.result.Outcome != applications.ActivationOutcomeUnknown || got.result.Reason != "owned_host") {
				t.Fatalf("a lost channel whose process may still run started again: %+v launches%d starts%d", got.result, launches.Load(), starts.Load())
			}
		})
	}
}

// The production listing source reads each registration's state and parses
// each kept declaration. A disabled registration is not openable, so the kept
// agent's listing leaves it out, and a declaration that no longer parses is
// skipped while the others stay listed.
func TestApplicationV3GatewayDirectoryListingNamesOnlyEnabledParsedApplications(t *testing.T) {
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	ctx := t.Context()
	d := f.g.directory
	// The directory keeps "broken" as a display descriptor the gateway's
	// manifest parser refuses.
	decode := func(raw []byte, program string, held *heldApplicationManifest) (legacy.ApplicationDescriptor, string, error) {
		if v, digest, err := f.g.decoder(raw, program, held); err == nil {
			return v, digest, nil
		}
		return registration1459Decoder(raw, program, held)
	}
	if err := d.attachApplicationV3(ctx, decode); err != nil {
		t.Fatal(err)
	}
	manifest := *f.manifest
	manifest.Name, manifest.Title = "listed", "Listed"
	program := filepath.Join(t.TempDir(), "listed.exe")
	if err := os.WriteFile(program, manifestPE(registrationwire.EncodeManifest(&manifest), false), 0o600); err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: f.subject.Account, Program: program}
	if out := d.registerApplicationV3(ctx, subject, fileid.Self, program, ""); out.Outcome != legacy.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	answer(t, permission, selfRegisteredQuestion(t, permission, d, "listed").ID, "keep")
	waitDirectory(t, d, func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.v3.entries["listed"].State == "enabled"
	})
	f.g.load = f.g.loadDeclaration
	mixed, err := f.c.Describe(appmodel.ApplicationsQuery{Application: "LiStEd"})
	if err != nil || mixed.Answer == nil || mixed.Answer.Outcome != appmodel.ReadingOutcomePage ||
		len(mixed.Answer.Applications) != 1 || mixed.Answer.Applications[0].Application != "listed" {
		t.Fatalf("mixed-case canonical Describe: %+v, %v", mixed, err)
	}
	view, found, err := d.applicationV3Declaration(ctx, "L\u0131sted")
	if err == nil || found || view.Name != "" {
		t.Fatalf("non-ASCII lookalike resolved: %+v, %v, %v", view, found, err)
	}
	registration1459Publish(t, d, "broken", f.subject.Program, "Broken", "operator", "")
	var reports atomic.Int32
	f.g.report = func(error) { reports.Add(1) }
	f.g.list = f.g.listDeclarations
	names := func() map[string]bool {
		t.Helper()
		listed, err := f.c.Describe(appmodel.ApplicationsQuery{})
		if err != nil || listed.Answer == nil || listed.Answer.Outcome != appmodel.ReadingOutcomePage {
			t.Fatalf("the directory-backed listing: %+v %v", listed, err)
		}
		got := map[string]bool{}
		for _, entry := range listed.Answer.Applications {
			got[entry.Application] = true
		}
		return got
	}
	if got := names(); !got["listed"] || got["broken"] || reports.Load() == 0 {
		t.Fatalf("the enabled declaration is missing or the unparseable one is listed or unreported: %v reports%d", got, reports.Load())
	}
	if !d.lock(ctx) {
		t.Fatal("directory lock")
	}
	next := maps.Clone(d.v3.entries)
	entry := next["listed"]
	if entry.State != "enabled" {
		d.unlock()
		t.Fatalf("the published registration is %q", entry.State)
	}
	entry.State = "disabled"
	next["listed"] = entry
	err = d.v3.persistLocked(ctx, next)
	d.unlock()
	if err != nil {
		t.Fatal(err)
	}
	if got := names(); got["listed"] {
		t.Fatalf("the listing names a disabled registration: %v", got)
	}
}

// Native Activate resolves omitted verbs against the currently kept declaration
// before any question or launch. An invalid payload distinguishes a resolved
// single verb from an ambiguous or blocked choice without starting an app.
func TestApplicationV3ActivateResolvesOmittedVerb(t *testing.T) {
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	request := applications.ActivateRequest{Application: "fixture", Payload: "[]"}
	check := func(n int, want string) {
		t.Helper()
		got, err := f.c.Activate(receiptKey(n, time.Now()), request)
		if err != nil || got.Outcome != applications.ActivationOutcomeInvalid || got.Reason != want {
			t.Fatalf("activate %+v: %+v %v, want invalid/%s", request, got, err, want)
		}
	}
	check(119, "missing_verb")
	explicitEmpty, err := applications.DecodeActivateRequest([]byte(`{"application":"fixture","verb":"","payload":[]}`))
	if err != nil {
		t.Fatal(err)
	}
	request = *explicitEmpty
	check(118, "missing_verb")
	yes := true
	request.DefaultVerb = &yes
	check(121, "payload")
	second := f.manifest.Verbs[0]
	second.Name = "other"
	f.manifest.Verbs = append(f.manifest.Verbs, second)
	check(122, "missing_verb")
	request.Verb = second.Name
	check(120, "verb")
	request.DefaultVerb = nil
	check(123, "payload")
	request.DefaultVerb = &yes
	request.Verb = ""
	f.manifest.Verbs = f.manifest.Verbs[1:]
	check(124, "payload")
	if err := permission.a.rights.policy.Set(f.subject, "abstraction.facade/command.reveal", "app:fixture", false); err != nil {
		t.Fatal(err)
	}
	check(125, "missing_verb")
	if pending := pendingApplicationQuestions(t, permission); len(pending) != 0 {
		t.Fatalf("invalid activation asked the person: %+v", pending)
	}
}
