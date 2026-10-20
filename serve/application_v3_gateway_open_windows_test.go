package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// agentOpenFixture is the native gateway fixture under a fresh Rights policy:
// the caller holds no application.activate rule. kept makes the caller a
// person-kept agent program through the real enrollment owner.
func agentOpenFixture(t *testing.T, class appmodel.CommandClass, kept bool) (*applicationV3GatewayFixture, *answererFixture) {
	t.Helper()
	f := newApplicationV3GatewayFixture(t)
	permission := applicationPermissionRuntime(t)
	// The runtime registers application.activate with the application actions
	// (configureApplications); this caller holds no rule on it.
	if err := permission.a.rights.policy.RegisterAction(ActionApplicationActivate); err != nil {
		t.Fatal(err)
	}
	f.g.policy = permission.a.rights
	f.g.asks = permission.a
	f.g.consent = f.g.bundleConsent
	if kept {
		run := &selfRegisteredRun{f: permission, d: f.g.directory, ctx: t.Context()}
		run.k = permission.a.keepSelfRegistered(t.Context(), f.g.directory)
		if got := f.g.directory.askConfirmation(t.Context(), permission.panel, "fixture"); got.Outcome != legacy.ApplicationOutcomeApplied {
			t.Fatal(got)
		}
		answer(t, permission, selfRegisteredQuestion(t, permission, f.g.directory, "fixture").ID, "keep")
		waitDirectory(t, f.g.directory, func() bool {
			registered, unconfirmed := unconfirmedNow(f.g.directory, "fixture")
			return registered && !unconfirmed
		})
		agent1478Attach(t, run)
		f.g.directory.agentImage = nil
		if pending := agent1478Set(t, run, "fixture", "", true); pending.State != applications.AgentEnrollmentStatePending {
			t.Fatal(pending)
		}
		answer(t, permission, agent1478Question(t, run, "fixture").ID, "keep")
		agent1478Wait(t, run, "fixture", "enrolled")
	}
	f.manifest.Verbs = []registrationwire.ActivationVerb{{Name: "open", Class: class, InputSchema: `{"type":"object","properties":{},"additionalProperties":false}`}}
	f.mu.Lock()
	f.answer = func(name string, _ json.RawMessage) []byte {
		if name != "open" {
			return nil
		}
		f.effects.Add(1)
		return applications.EncodeHostActivationResult(&applications.HostActivationResult{Outcome: applications.HostActivationOutcomeApplied})
	}
	f.mu.Unlock()
	return f, permission
}

func pendingApplicationQuestions(t *testing.T, permission *answererFixture) []asks.Record {
	t.Helper()
	rows, _, err := permission.a.book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	pending := []asks.Record{}
	for _, row := range rows {
		if row.Pending() {
			pending = append(pending, row)
		}
	}
	return pending
}

type openReceived struct {
	result applications.ActivationResult
	err    error
}

func waitOpen(t *testing.T, done <-chan openReceived) openReceived {
	t.Helper()
	select {
	case got := <-done:
		return got
	case <-time.After(3 * time.Second):
		t.Fatal("open did not finish")
		return openReceived{}
	}
}

// Coordinator dispositions 2026-11-09: a kept agent without application.activate
// is decided by the declared verb's class. An already-running owned instance
// answers at once: no question, no start and no verb delivery.
func TestApplicationV3GatewayAgentOpenDecidedByVerbClass(t *testing.T) {
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	if d := permission.a.rights.decide(t.Context(), f.subject, ActionApplicationActivate, "app:fixture"); d.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("fixture caller holds an activation rule: %+v", d)
	}
	before, _, err := permission.a.book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	request := applications.ActivateRequest{Application: "fixture", Verb: "open", Payload: `{}`}
	for n := 70; n < 72; n++ {
		running, err := f.c.Activate(receiptKey(n, time.Now()), request)
		if err != nil || running.Outcome != applications.ActivationOutcomeReady || running.Instance != f.instance || running.Started || f.effects.Load() != 0 {
			t.Fatalf("agent open of the running instance: %+v %v effects%d", running, err, f.effects.Load())
		}
	}
	after, _, err := permission.a.book.ApplicationHistory()
	if err != nil || len(after) != len(before) || len(pendingApplicationQuestions(t, permission)) != 0 {
		t.Fatal("an open of a running instance asked the person")
	}
	if err := permission.a.rights.policy.Set(f.subject, ActionApplicationActivate, "app:fixture", false); err != nil {
		t.Fatal(err)
	}
	denied, err := f.c.Activate(receiptKey(72, time.Now()), request)
	if err != nil || denied.Outcome != applications.ActivationOutcomeForbidden || denied.Reason != "blocked" || f.effects.Load() != 0 {
		t.Fatalf("a stored activation deny admitted an agent: %+v %v", denied, err)
	}
}

// Only a kept agent program reaches the class path; the Panel and other
// programs keep the stored-permit path alone.
func TestApplicationV3GatewayOpenWithoutRuleRefusesNonAgent(t *testing.T) {
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, false)
	result, err := f.c.Activate(receiptKey(76, time.Now()), applications.ActivateRequest{Application: "fixture", Verb: "open", Payload: `{}`})
	if err != nil || result.Outcome != applications.ActivationOutcomeForbidden || result.Reason != "rights" || f.effects.Load() != 0 || len(pendingApplicationQuestions(t, permission)) != 0 {
		t.Fatalf("a program that is no kept agent opened by class: %+v %v", result, err)
	}
}

func TestApplicationV3ComfyStopRequiresTheStarterOfThisInstance(t *testing.T) {
	for _, tc := range []struct {
		name    string
		starter func(*applicationV3GatewayFixture) string
		allowed bool
	}{
		{"person started", func(*applicationV3GatewayFixture) string { return "" }, false},
		{"another agent started", func(*applicationV3GatewayFixture) string { return "another-agent" }, false},
		{"same agent started", func(f *applicationV3GatewayFixture) string { return f.subject.Program }, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := agentOpenFixture(t, appmodel.CommandClassReveal, true)
			f.s.bindMu.Lock()
			f.s.binding.application = "comfyui"
			f.s.starter = tc.starter(f)
			f.s.bindMu.Unlock()
			original := f.g.load
			f.g.load = func(ctx context.Context, caller *identity.Peer, app string) (*applicationV3Kept, error) {
				if app != "comfyui" {
					return nil, errApplicationV3NotFound
				}
				k, err := original(ctx, caller, "fixture")
				if err != nil {
					return nil, err
				}
				manifest := *k.manifest
				manifest.Name = app
				manifest.Offers.Commands = []appmodel.DeclaredCommand{{Name: ownedBackendStop, Class: appmodel.CommandClassCommit}}
				k.manifest = &manifest
				return k, nil
			}
			f.description.Kinds = []appmodel.Kind{{Name: "backend", Schema: `{}`, Fields: []appmodel.KindField{}}}
			f.description.Commands = []appmodel.CommandSpec{{Name: ownedBackendStop, Class: appmodel.CommandClassCommit,
				Title: "Stop", InputSchema: `{"type":"object","required":["reason"],"properties":{"reason":{"enum":["task_done","next_job"]}},"additionalProperties":false}`,
				OutputSchema: `{}`, Targets: []string{}}}
			call := appmodel.GatewayCall{Application: "comfyui", Instance: f.instance, Tool: ownedBackendStop, Arguments: `{"reason":"task_done"}`}
			stale := call
			stale.Instance = strings.Repeat("a", 32)
			staleResult, err := f.c.Call(receiptKey(119, time.Now()), stale)
			if err != nil || staleResult.Answer == nil || staleResult.Answer.Result == nil ||
				staleResult.Answer.Result.Outcome != appmodel.CommandOutcomeStale || f.effects.Load() != 0 {
				t.Fatalf("stale stop reached the connector: %+v %v, delivered %d", staleResult, err, f.effects.Load())
			}
			result, err := f.c.Call(receiptKey(120, time.Now()), call)
			if err != nil || result.Answer == nil || result.Answer.Result == nil {
				t.Fatalf("stop answer: %+v %v", result, err)
			}
			if tc.allowed {
				if result.Answer.Result.Outcome != appmodel.CommandOutcomeApplied || f.effects.Load() != 1 {
					t.Fatalf("starter could not stop: %+v, delivered %d", result.Answer.Result, f.effects.Load())
				}
			} else if result.Answer.Result.Outcome != appmodel.CommandOutcomeNotPermitted || f.effects.Load() != 0 {
				t.Fatalf("non-starter reached the connector: %+v, delivered %d", result.Answer.Result, f.effects.Load())
			}
		})
	}
}

func TestApplicationV3OtherAppsKeepOrdinaryBackendStopRights(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	f.manifest.Offers.Commands = []appmodel.DeclaredCommand{{Name: ownedBackendStop, Class: appmodel.CommandClassRead}}
	f.description.Kinds = []appmodel.Kind{{Name: "backend", Schema: `{}`, Fields: []appmodel.KindField{}}}
	f.description.Commands = []appmodel.CommandSpec{{Name: ownedBackendStop, Class: appmodel.CommandClassRead,
		Title: "Stop", InputSchema: `{}`, OutputSchema: `{}`, Targets: []string{}}}
	listed, err := f.c.Commands(appmodel.CommandsQuery{Application: "fixture", Instance: f.instance})
	if err != nil || listed.Answer == nil || len(listed.Answer.Commands) != 1 || listed.Answer.Commands[0].Name != ownedBackendStop {
		t.Fatalf("unrelated app's command was filtered: %+v %v", listed, err)
	}
	call := appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: ownedBackendStop, Arguments: `{}`}
	got, err := f.c.Call(receiptKey(121, time.Now()), call)
	if err != nil || got.Answer == nil || got.Answer.Result == nil ||
		got.Answer.Result.Outcome != appmodel.CommandOutcomeApplied || f.effects.Load() != 1 {
		t.Fatalf("ordinary command was refused: %+v %v, delivered %d", got, err, f.effects.Load())
	}
}

func TestApplicationV3GatewayAgentOpenRefusesDeniedClassAndPersonOnlyVerb(t *testing.T) {
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	if err := permission.a.rights.policy.Set(f.subject, "abstraction.facade/command.reveal", "app:fixture", false); err != nil {
		t.Fatal(err)
	}
	request := applications.ActivateRequest{Application: "fixture", Verb: "open", Payload: `{}`}
	result, err := f.c.Activate(receiptKey(73, time.Now()), request)
	if err != nil || result.Outcome != applications.ActivationOutcomeForbidden || f.effects.Load() != 0 || len(pendingApplicationQuestions(t, permission)) != 0 {
		t.Fatalf("denied reveal class opened: %+v %v", result, err)
	}
	f, permission = agentOpenFixture(t, appmodel.CommandClassDestroy, true)
	result, err = f.c.Activate(receiptKey(74, time.Now()), request)
	if err != nil || result.Outcome != applications.ActivationOutcomeForbidden || result.Reason != "rights" || f.effects.Load() != 0 || len(pendingApplicationQuestions(t, permission)) != 0 {
		t.Fatalf("an agent started a verb whose question follows the start: %+v %v", result, err)
	}
}

// stopFixture leaves the fixture kept but stopped, and counts how far an open
// reaches the start path.
func stopFixture(f *applicationV3GatewayFixture) (launches, starts *atomic.Int32) {
	launches, starts = new(atomic.Int32), new(atomic.Int32)
	f.g.mu.Lock()
	f.g.owned = map[string]*ownedApplicationSession{}
	f.g.mu.Unlock()
	f.g.directory.mu.Lock()
	f.g.directory.instances = map[string]applicationLease{}
	f.g.directory.mu.Unlock()
	f.g.directory.unsignedLaunch = func(string, bool) { launches.Add(1) }
	f.g.start = func(context.Context, ownedApplicationImage, ownedApplicationBinding, []string, func(context.Context, ownedApplicationBinding) error) (*ownedApplicationSession, error) {
		starts.Add(1)
		return nil, errors.New("fixture start")
	}
	return launches, starts
}

var openRequest = applications.ActivateRequest{Application: "fixture", Verb: "open", Payload: `{}`}

// openAdmission is the once-occurrence the runtime admitted for one exact
// open request key, or an empty record before it is admitted.
func openAdmission(f *applicationV3GatewayFixture, permission *answererFixture, key string) asks.Record {
	requestKey, _ := applicationV3OnceRequestKey(applicationV3ConsentRequest{caller: applicationV3Caller{subject: f.subject}, application: "fixture", revision: "manifest1", tool: "open", class: appmodel.CommandClassReveal, method: "Activate", key: key, bodyDigest: applicationV3BodyDigest(applications.EncodeActivateRequest(&openRequest))})
	record, _, err := permission.a.book.ObserveApplication(permission.a.scope, requestKey)
	if err != nil {
		return asks.Record{}
	}
	return record
}

// beginOpen runs one agent Activate of the stopped fixture and returns its
// answer channel once the runtime admitted its open question.
func beginOpen(t *testing.T, f *applicationV3GatewayFixture, permission *answererFixture, n int) (<-chan openReceived, asks.Record) {
	t.Helper()
	key := receiptKey(n, time.Now())
	done := make(chan openReceived, 1)
	go func() {
		result, err := f.c.Activate(key, openRequest)
		done <- openReceived{result, err}
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if record := openAdmission(f, permission, key); record.ID != "" {
			return done, record
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("open %d admitted no question", n)
	return nil, asks.Record{}
}

func answerOpen(t *testing.T, permission *answererFixture, id, option string) {
	t.Helper()
	if _, state, err := permission.a.book.AnswerApplicationScopedAuthorized(id, option, func(r asks.Record, scope string) error {
		if scope != permission.a.scope || r.ID != id {
			return errors.New("operator fixture scope")
		}
		return nil
	}); err != nil || state != "answered" {
		t.Fatalf("person's %s: %s %v", option, state, err)
	}
}

// Coordinator scope decision, 2026-11-09: an agent's start of a stopped
// application is the person's own once-answer "Open <app>?". A second open
// joins the same question; no answering program can answer it; Not now
// refuses with consent and Open reaches the start path.
func TestApplicationV3GatewayAgentOpenStartsNothingBeforeAllow(t *testing.T) {
	for _, option := range []string{"refuse", "once"} {
		t.Run(option, func(t *testing.T) {
			f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
			launches, starts := stopFixture(f)
			done, question := beginOpen(t, f, permission, 77)
			if question.Key != asks.ApplicationCommandOnceKey || question.TypedPermission != nil || !strings.HasPrefix(question.Text, "Open Fixture? ") {
				t.Fatalf("the open question is no person-only once question: %s %q", question.Key, question.Text)
			}
			// The second open's own admission maps to the same question
			// before the person answers.
			joined, second := beginOpen(t, f, permission, 78)
			if second.ID != question.ID {
				t.Fatalf("joined: the second open asked its own question %s, not %s", second.ID, question.ID)
			}
			if pending := pendingApplicationQuestions(t, permission); len(pending) != 1 || launches.Load() != 0 || starts.Load() != 0 {
				t.Fatalf("joined: %d pending questions, launches%d starts%d", len(pending), launches.Load(), starts.Load())
			}
			// Neither the asking agent nor an enrolled nonparty assistant
			// lists or answers it; only the person's answer counts.
			permission.enroll(t, permission.answerer)
			if d := permission.decide(permission.answerer, asks.AnswerAction, rights.MetaScopeApps); d != rwire.DecisionOutcomePermitted {
				t.Fatalf("the assistant holds no apps enrollment rule: %v", d)
			}
			for _, who := range []answerer{{subject: f.subject}, permission.me()} {
				for _, choice := range append([]string{"allow"}, question.Options...) {
					if _, err := permission.a.authorizeAnswerer(t.Context(), who, question, permission.a.scope, choice); !errors.Is(err, asksservice.ErrResponderForbidden) {
						t.Fatalf("%s may list or answer the open question with %s: %v", who.subject.Program, choice, err)
					}
				}
			}
			if outcome, _ := permission.answer(permission.me(), question.ID, "once"); outcome != "forbidden" {
				t.Fatalf("the enrolled assistant answered the open question: %s", outcome)
			}
			deadline := time.Now().Add(3 * time.Second)
			for permission.a.book.LiveQuestionWaiters(question.ID) < 2 && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if got := permission.a.book.LiveQuestionWaiters(question.ID); got != 2 {
				t.Fatalf("joined live Open waiters = %d", got)
			}
			answerOpen(t, permission, question.ID, option)
			got, again := waitOpen(t, done), waitOpen(t, joined)
			if count := permission.a.book.LiveQuestionWaiters(question.ID); count != 0 {
				t.Fatalf("answered Open left %d live waiters", count)
			}
			if got.err != nil || again.err != nil || got.result.Outcome == applications.ActivationOutcomeReady || f.effects.Load() != 0 {
				t.Fatalf("open of a stopped fixture: %+v %v joined %+v", got.result, got.err, again.result)
			}
			if option == "refuse" && (got.result.Outcome != applications.ActivationOutcomeForbidden || got.result.Reason != "consent" || again.result.Reason != "consent" || launches.Load() != 0 || starts.Load() != 0) {
				t.Fatalf("a refused open reached the start path: %+v joined %+v launches%d starts%d", got.result, again.result, launches.Load(), starts.Load())
			}
			if option == "once" && (launches.Load() < 1 || launches.Load() > 2) {
				t.Fatalf("Open did not reach the start path: %+v launches%d", got.result, launches.Load())
			}
		})
	}
}

// A routine permit written for the agent, as an answering assistant's Allow
// of the Describe/Commands question writes it, starts nothing; the open
// still asks the person.
func TestApplicationV3GatewayRoutinePermitStartsNoStoppedApp(t *testing.T) {
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	for _, class := range []string{"read", "reveal", "propose"} {
		if err := permission.a.rights.policy.Set(f.subject, "abstraction.facade/command."+class, "app:fixture", true); err != nil {
			t.Fatal(err)
		}
	}
	launches, starts := stopFixture(f)
	done, question := beginOpen(t, f, permission, 79)
	time.Sleep(50 * time.Millisecond) // the person's question must hold the start path back
	if question.Key != asks.ApplicationCommandOnceKey || launches.Load() != 0 || starts.Load() != 0 {
		t.Fatalf("a routine permit started a stopped app: %s launches%d starts%d", question.Key, launches.Load(), starts.Load())
	}
	answerOpen(t, permission, question.ID, "refuse")
	if got := waitOpen(t, done); got.result.Outcome != applications.ActivationOutcomeForbidden || launches.Load() != 0 || starts.Load() != 0 {
		t.Fatalf("after Not now: %+v launches%d starts%d", got.result, launches.Load(), starts.Load())
	}
}

// An agent's context name is looked up only after authorization. A running
// app answers a named context, existing or not, with the same routine
// question and never ready before it.
func TestApplicationV3GatewayAgentOpenContextNameRevealsNothing(t *testing.T) {
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	for n, name := range []string{"view", "no-such-context"} {
		request := openRequest
		request.Context = name
		done := make(chan openReceived, 1)
		go func() {
			result, err := f.c.Activate(receiptKey(80+n, time.Now()), request)
			done <- openReceived{result, err}
		}()
		var question asks.Record
		for deadline := time.Now().Add(3 * time.Second); question.ID == "" && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			if pending := pendingApplicationQuestions(t, permission); len(pending) > 0 {
				question = pending[0]
			}
		}
		if question.ID == "" || question.TypedPermission == nil {
			t.Fatalf("context %q: no routine question before the lookup: %+v", name, question)
		}
		if _, _, err := permission.a.book.AnswerTypedPermissionIfPendingAuthorized(question.ID, "refuse", func(asks.Record, string) error { return nil }); err != nil {
			t.Fatal(err)
		}
		if got := waitOpen(t, done); got.err != nil || got.result.Outcome == applications.ActivationOutcomeReady || f.effects.Load() != 0 {
			t.Fatalf("context %q answered before authorization: %+v %v", name, got.result, got.err)
		}
	}
}

// Operator programs keep their stored rule: no question, the verb is delivered.
func TestApplicationV3GatewayOperatorOpenKeepsStoredRule(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	f.manifest.Verbs = []registrationwire.ActivationVerb{{Name: "open", Class: appmodel.CommandClassReveal, InputSchema: `{"type":"object","properties":{},"additionalProperties":false}`}}
	f.mu.Lock()
	f.answer = func(name string, _ json.RawMessage) []byte {
		if name != "open" {
			return nil
		}
		f.effects.Add(1)
		return applications.EncodeHostActivationResult(&applications.HostActivationResult{Outcome: applications.HostActivationOutcomeApplied})
	}
	f.mu.Unlock()
	result, err := f.c.Activate(receiptKey(75, time.Now()), applications.ActivateRequest{Application: "fixture", Verb: "open", Payload: `{}`})
	if err != nil || result.Outcome != applications.ActivationOutcomeReady || result.Instance != f.instance || f.effects.Load() != 1 {
		t.Fatalf("operator open: %+v %v", result, err)
	}
}

// Asks reviewer, 2026-11-09: an enrolled agent's edit-class consent ran the
// kept-agent check, which reads the agent's enrollment answer from the Book,
// inside the Book's admission lock, and every question and popup hung. The
// reentry check turns a repeat into a panic; the timeout catches a hang.
func TestApplicationV3GatewayAgentEditConsentRunsNoBookReadUnderItsLock(t *testing.T) {
	t.Cleanup(asks.CheckBookLockReentry())
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	f.manifest.Offers.Commands[0].Class = appmodel.CommandClassEdit
	f.description.Commands[0].Class = appmodel.CommandClassEdit
	call := appmodel.GatewayCall{Application: "fixture", Instance: f.instance, Tool: "note.read", Arguments: `{}`, ExpectedRevision: "r1", IdempotencyKey: "agent-edit"}
	type called struct {
		result applications.CallResult
		err    error
	}
	done := make(chan called, 1)
	go func() {
		result, err := f.c.Call(receiptKey(91, time.Now()), call)
		done <- called{result, err}
	}()
	var question asks.Record
	for deadline := time.Now().Add(3 * time.Second); question.ID == "" && time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if pending := pendingApplicationQuestions(t, permission); len(pending) > 0 {
			question = pending[0]
		}
	}
	if question.ID == "" || question.TypedPermission == nil {
		t.Fatal("the enrolled agent's edit consent admitted no question: the Book lock is held")
	}
	if _, _, err := permission.a.book.AnswerTypedPermissionIfPendingAuthorized(question.ID, "refuse", func(asks.Record, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-done:
		if got.err != nil || f.effects.Load() != 0 {
			t.Fatalf("refused edit: %+v %v effects%d", got.result, got.err, f.effects.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the refused edit call did not finish")
	}
}

// Two first opens at the same moment show the person one question: the
// pending read and the admission run as one step.
func TestApplicationV3GatewayConcurrentFirstOpensAskOnce(t *testing.T) {
	for round := 0; round < 3; round++ {
		f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
		stopFixture(f)
		keys := []string{receiptKey(92+2*round, time.Now()), receiptKey(93+2*round, time.Now())}
		done := make(chan openReceived, len(keys))
		start := make(chan struct{})
		for _, key := range keys {
			go func() {
				<-start
				result, err := f.c.Activate(key, openRequest)
				done <- openReceived{result, err}
			}()
		}
		close(start)
		ids := map[string]bool{}
		for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
			first, second := openAdmission(f, permission, keys[0]), openAdmission(f, permission, keys[1])
			if first.ID != "" && second.ID != "" {
				ids[first.ID], ids[second.ID] = true, true
				break
			}
		}
		if len(ids) != 1 || len(pendingApplicationQuestions(t, permission)) != 1 {
			t.Fatalf("round %d: two first opens asked %d questions", round, len(ids))
		}
		for id := range ids {
			answerOpen(t, permission, id, "refuse")
		}
		for range keys {
			if got := waitOpen(t, done); got.result.Outcome != applications.ActivationOutcomeForbidden {
				t.Fatalf("round %d: %+v", round, got.result)
			}
		}
	}
}

// Wait policy (owner, 2026-11-10): an open waits for the person's answer for
// applicationV3AnswerWait and then answers not_ready/consent, whatever budget
// the caller gave. Before, the wait lasted the caller's whole deadline and
// the Panel's Open ended in a bare "context deadline exceeded".
func TestApplicationV3GatewayOpenAnswerWaitIsBounded(t *testing.T) {
	saved := applicationV3AnswerWait
	applicationV3AnswerWait = time.Second
	t.Cleanup(func() { applicationV3AnswerWait = saved })
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	launches, starts := stopFixture(f)
	began := time.Now()
	done, question := beginOpen(t, f, permission, 93)
	got := waitOpen(t, done)
	if count := permission.a.book.LiveQuestionWaiters(question.ID); count != 0 {
		t.Fatalf("expired Open left %d live waiters", count)
	}
	if got.err != nil || got.result.Outcome != applications.ActivationOutcomeNotReady || got.result.Reason != "consent" {
		t.Fatalf("an unanswered open: %+v %v", got.result, got.err)
	}
	if waited := time.Since(began); waited < applicationV3AnswerWait || waited > applicationV3AnswerWait+2*time.Second {
		t.Fatalf("the open waited %v for an answer bounded at %v", waited, applicationV3AnswerWait)
	}
	if launches.Load() != 0 || starts.Load() != 0 || f.effects.Load() != 0 {
		t.Fatalf("an unanswered open reached the start path: launches%d starts%d effects%d", launches.Load(), starts.Load(), f.effects.Load())
	}
}

// backlog/APP-LAUNCH-JOB.md ruling 2: the Panel holds application.activate
// and no command rule. Its Open asks nothing: a running app answers ready, a
// stopped one reaches the start. A stored deny on the verb's class refuses.
// R20c showed the Panel's own "Access to ComfyUI" Allow on every first Open.
func TestApplicationV3GatewayOperatorOpenAsksNothing(t *testing.T) {
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, false)
	if err := permission.a.rights.policy.Set(f.subject, ActionApplicationActivate, "app:fixture", true); err != nil {
		t.Fatal(err)
	}
	for _, class := range []string{"read", "reveal", "propose"} {
		if d := permission.a.rights.decide(t.Context(), f.subject, "abstraction.facade/command."+class, "app:fixture"); d.Outcome != rwire.DecisionOutcomeNotGranted {
			t.Fatalf("the operator holds a %s rule: %+v", class, d)
		}
	}
	running, err := f.c.Activate(receiptKey(94, time.Now()), openRequest)
	if err != nil || running.Outcome != applications.ActivationOutcomeReady || running.Instance != f.instance || f.effects.Load() != 1 {
		t.Fatalf("operator open of the running app: %+v %v effects%d", running, err, f.effects.Load())
	}
	launches, starts := stopFixture(f)
	stopped, err := f.c.Activate(receiptKey(95, time.Now()), openRequest)
	if err != nil || launches.Load() != 1 {
		t.Fatalf("operator open of the stopped app did not reach the start: %+v %v launches%d starts%d", stopped, err, launches.Load(), starts.Load())
	}
	if pending := pendingApplicationQuestions(t, permission); len(pending) != 0 {
		t.Fatalf("the operator's open asked the person: %+v", pending)
	}
	if err := permission.a.rights.policy.Set(f.subject, "abstraction.facade/command.reveal", "app:fixture", false); err != nil {
		t.Fatal(err)
	}
	denied, err := f.c.Activate(receiptKey(96, time.Now()), openRequest)
	if err != nil || denied.Outcome != applications.ActivationOutcomeForbidden || denied.Reason != "blocked" || launches.Load() != 1 || len(pendingApplicationQuestions(t, permission)) != 0 {
		t.Fatalf("a stored reveal deny: %+v %v launches%d", denied, err, launches.Load())
	}
}

// A receipt replay (the same key again, token == nil) by the operator is
// decided by the same rule as its first Open: it replays the earlier ready
// answer without asking or starting anything, and a stored deny on the verb's
// class refuses the replay.
func TestApplicationV3GatewayOperatorOpenReceiptReplay(t *testing.T) {
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, false)
	if err := permission.a.rights.policy.Set(f.subject, ActionApplicationActivate, "app:fixture", true); err != nil {
		t.Fatal(err)
	}
	key := receiptKey(97, time.Now())
	first, err := f.c.Activate(key, openRequest)
	if err != nil || first.Outcome != applications.ActivationOutcomeReady || first.Instance != f.instance {
		t.Fatalf("operator open: %+v %v", first, err)
	}
	effects := f.effects.Load()
	replay, err := f.c.Activate(key, openRequest)
	if err != nil || replay.Outcome != applications.ActivationOutcomeReady || replay.Instance != f.instance || f.effects.Load() != effects {
		t.Fatalf("operator replay: %+v %v effects %d→%d", replay, err, effects, f.effects.Load())
	}
	if pending := pendingApplicationQuestions(t, permission); len(pending) != 0 {
		t.Fatalf("the operator's replay asked the person: %+v", pending)
	}
	if err := permission.a.rights.policy.Set(f.subject, "abstraction.facade/command.reveal", "app:fixture", false); err != nil {
		t.Fatal(err)
	}
	denied, err := f.c.Activate(key, openRequest)
	if err != nil || denied.Outcome != applications.ActivationOutcomeForbidden || denied.Reason != "blocked" || len(pendingApplicationQuestions(t, permission)) != 0 {
		t.Fatalf("operator replay under a stored reveal deny: %+v %v", denied, err)
	}
}
