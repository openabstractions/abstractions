package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// panelQuestionRuntime hosts a real application Book whose operator policy
// admits this test process only while allow is set.
func panelQuestionRuntime(t *testing.T, allow *atomic.Bool) {
	t.Helper()
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	book, err := asks.LoadApplicationBook(filepath.Join(t.TempDir(), "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	prefix := fixtureName("panel-questions")
	o := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		QuestionBook: book, QuestionEndpoint: listen.Endpoint(prefix + "q"),
		QuestionOperator: func(ctx context.Context, peer *identity.Peer) error {
			process, err := peer.Process.AtLeast(listen.Program.Process)
			if err != nil || process.PID != os.Getpid() || !allow.Load() {
				return asksservice.ErrOperatorForbidden
			}
			return ctx.Err()
		}}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime failed to stop")
		}
	})
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", o.Endpoint)
	trustPanelRuntime(t, o.Endpoint)
}

// panelFirstUseRuntime hosts a real application Book and rights policy whose
// operators admit only this test process, for first-use answers that write
// rules. It returns the Book, the policy and this test program's path.
func panelFirstUseRuntime(t *testing.T, actions ...string) (*asks.Book, *rights.DecisionPolicy, string) {
	t.Helper()
	own(t)
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	dir := t.TempDir()
	book, err := asks.LoadApplicationBook(filepath.Join(dir, "questions.json"))
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "policy.json"), append(actions, asks.AnswerAction))
	if err != nil {
		t.Fatal(err)
	}
	self := func(ctx context.Context, peer *identity.Peer) bool {
		process, err := peer.Process.AtLeast(listen.Program.Process)
		return err == nil && process.PID == os.Getpid() && ctx.Err() == nil
	}
	prefix := fixtureName("panel-first-use")
	o := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"),
		QuestionBook: book, QuestionEndpoint: listen.Endpoint(prefix + "q"),
		QuestionOperator: func(ctx context.Context, peer *identity.Peer) error {
			if !self(ctx, peer) {
				return fmt.Errorf("not this test")
			}
			return nil
		},
		RightsPolicy: policy, RightsEndpoint: listen.Endpoint(prefix + "r"),
		RightsOperator: func(ctx context.Context, peer *identity.Peer) error {
			if !self(ctx, peer) {
				return rightsservice.ErrOperatorForbidden
			}
			return nil
		},
		RightsFirstUseAllow: func(ctx context.Context, by rightsclient.Subject, expected, id string, authorize func() error) (rightsclient.PolicyEdit, error) {
			result := rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeInvalid}
			_, err := book.WithApplicationRecordContext(ctx, id, func(record asks.Record, scope string, retained []string) error {
				if scope != "runtime-scope" || record.Option != "allow" || record.AnsweredBy == asks.AnsweredByResponder {
					return nil
				}
				permission, ok := asks.PermissionForRecord(by.Account, record)
				if !ok {
					return nil
				}
				rules := grants.FirstUsePermissionRules(by.Account, askclient.RecordMetadata{ID: record.ID, Key: record.Key, Option: record.Option}, permission)
				permit := true
				var err error
				result, err = policy.ApplyFirstUseAllow(ctx, expected, rules, rights.RuleEdit{Permit: &permit, By: by, Why: grants.FirstUseWhy, Exact: true}, id, retained, authorize)
				return err
			})
			return result, err
		}}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime failed to stop")
		}
	})
	t.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", o.Endpoint)
	trustPanelRuntime(t, o.Endpoint)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return book, policy, filepath.Clean(program)
}

// TestQuestionsPageAnswerAfterADelegatedAnswerWritesNoRule runs against the
// real Book and rights policy. A delegated answer recorded first makes the
// person's same-option answer on the Questions page conflict (ASK-O8), and no
// permanent rule is written over the delegated answer.
func TestQuestionsPageAnswerAfterADelegatedAnswerWritesNoRule(t *testing.T) {
	const action, resource = "abstraction.model/lookup", "hf"
	book, policy, program := panelFirstUseRuntime(t, action)
	slots := map[string]string{"program": program, "action": action, "resource": resource}
	record, _, err := book.AskApplication("runtime-scope", "runtime", "first-use:panel", asks.FirstUseKey, slots, listen.Seen{Why: "test"})
	if err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := book.AnswerApplication(record.ID, "allow"); err != nil || outcome != "answered" {
		t.Fatalf("delegated answer %q %v", outcome, err)
	}
	panel := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, panel, "/questions", questionAction{Action: "answer", ID: record.ID, Option: "allow"})
	answered := decodePanel[questionAnswerPanelResult](t, r.Code, r.Body.Bytes())
	if answered.Outcome != wire.OperatorDecisionOutcomeConflict || answered.Rule != nil || answered.Edit != nil {
		t.Fatalf("an answer after a delegated answer must conflict with no rule: %+v", answered)
	}
	account := probe.Self().Account
	if d := policy.Decide(rightsclient.Subject{Account: account, Program: program}, action, resource); d.Outcome != rightsclient.DecisionOutcomeNotGranted {
		t.Fatalf("no permanent rule may be written over the delegated answer: %+v", d)
	}
}

// TestQuestionsPageRetryRuleRefusesADelegatedAnswer runs against the real
// Book and rights policy. A responder's recorded allow is listed as delegated,
// is not offered Retry rule, and a rule retry writes nothing (ASK-O7,
// ASK-R4). An operator's recorded allow is still retried into its rule.
func TestQuestionsPageRetryRuleRefusesADelegatedAnswer(t *testing.T) {
	const action = "abstraction.model/lookup"
	book, policy, program := panelFirstUseRuntime(t, action)
	admit := func(key, resource string) string {
		slots := map[string]string{"program": program, "action": action, "resource": resource}
		record, _, err := book.AskApplication("runtime-scope", "runtime", key, asks.FirstUseKey, slots, listen.Seen{Why: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return record.ID
	}
	delegated, operated := admit("first-use:delegated", "hf"), admit("first-use:operated", "ollama")
	responder := filepath.Join(filepath.Dir(program), "oa-delegated-responder.exe")
	if _, outcome, err := book.AnswerApplicationAsResponder(delegated, "allow", responder, nil); err != nil || outcome != "answered" {
		t.Fatalf("responder answer %q %v", outcome, err)
	}
	if _, outcome, err := book.AnswerApplication(operated, "allow"); err != nil || outcome != "answered" {
		t.Fatalf("operator answer %q %v", outcome, err)
	}
	panel := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, panel, "/questions?cursor=", nil)
	page := decodePanel[questionPage](t, r.Code, r.Body.Bytes())
	if page.RuleStates[delegated] != grants.RuleDelegated || page.RuleStates[operated] != grants.RuleNotWritten {
		t.Fatalf("rule states %+v: the delegated answer must not read not_written, which offers Retry rule", page.RuleStates)
	}
	account := probe.Self().Account
	r = panelRequest(t, panel, "/questions", questionAction{Action: "rule", ID: delegated})
	refused := decodePanel[questionAnswerPanelResult](t, r.Code, r.Body.Bytes())
	if refused.RuleOutcome != grants.RuleDelegated || refused.Rule != nil || refused.Edit != nil {
		t.Fatalf("a responder's allow must not be retried into a rule: %+v", refused)
	}
	if d := policy.Decide(rightsclient.Subject{Account: account, Program: program}, action, "hf"); d.Outcome != rightsclient.DecisionOutcomeNotGranted {
		t.Fatalf("no permanent rule may follow a delegated answer: %+v", d)
	}
	r = panelRequest(t, panel, "/questions", questionAction{Action: "rule", ID: operated})
	retried := decodePanel[questionAnswerPanelResult](t, r.Code, r.Body.Bytes())
	if retried.Edit == nil || retried.Edit.Outcome != rightsclient.PolicyEditOutcomeApplied {
		t.Fatalf("an operator's allow must still retry into its rule: %+v", retried)
	}
	if d := policy.Decide(rightsclient.Subject{Account: account, Program: program}, action, "ollama"); d.Outcome != rightsclient.DecisionOutcomePermitted {
		t.Fatalf("the operator's retried rule must permit: %+v", d)
	}
}

// TestQuestionsPageNamesASignedProgramByItsDescription runs against the real
// Book. A first-use question admitted with a trusted publisher and a signed
// description lists that program by its description, with its publisher and
// the script-host sentence asks gives (ASK-A6, ASK-A8). A trusted program
// with no description is listed by its file name with its publisher, as the
// Tray shows it. A question with no trusted signature recorded lists no
// program entry, and the page names it by its file name.
func TestQuestionsPageNamesASignedProgramByItsDescription(t *testing.T) {
	// The asking program here is the test binary, which stands in for the
	// Panel; without the own-program titles it reads as any other program.
	withoutOwnPrograms(t)
	const action = "abstraction.model/lookup"
	book, _, program := panelFirstUseRuntime(t, action)
	admit := func(key, resource string, publisher asks.Publisher) string {
		slots := map[string]string{"program": program, "action": action, "resource": resource}
		record, _, err := book.AskApplicationFrom("runtime-scope", "runtime", key, asks.FirstUseKey, slots, listen.Seen{Why: "test"}, publisher)
		if err != nil {
			t.Fatal(err)
		}
		return record.ID
	}
	signed := admit("first-use:signed", "hf", asks.Publisher{Subject: "Microsoft Windows", Trusted: true,
		Description: "Windows PowerShell", OriginalName: "PowerShell.EXE"})
	unsigned := admit("first-use:unsigned", "ollama", asks.Publisher{Description: "Windows PowerShell", OriginalName: "PowerShell.EXE"})
	unnamed := admit("first-use:unnamed", "lmstudio", asks.Publisher{Subject: "Example Publisher", Trusted: true})
	panel := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, panel, "/questions?cursor=", nil)
	page := decodePanel[questionPage](t, r.Code, r.Body.Bytes())
	want := questionProgram{Name: "Windows PowerShell", Description: "Windows PowerShell", Publisher: "Microsoft Windows", OriginalName: "PowerShell.EXE",
		ScriptHost: "Windows PowerShell runs scripts; its publisher covers Windows PowerShell and the script it runs is unverified."}
	if got, ok := page.Programs[signed]; !ok || got != want {
		t.Fatalf("a signed script host must be listed by its description: %+v", page.Programs)
	}
	if got, ok := page.Programs[unnamed]; !ok || got != (questionProgram{Name: newProgramTitler(nil).name(program), Publisher: "Example Publisher"}) {
		t.Fatalf("a trusted program without a description must list its publisher under its file name: %+v", page.Programs)
	}
	if _, ok := page.Programs[unsigned]; ok {
		t.Fatalf("a program without a trusted signature must list no program entry: %+v", page.Programs)
	}
	// The sentence names each program as the native prompt does: by its
	// signed description only when the signature was trusted (D5).
	for id, want := range map[string]string{
		signed:   "Windows PowerShell wants to look up models in the model registry “hf”.",
		unsigned: newProgramTitler(nil).name(program) + " wants to look up models in the model registry “ollama”.",
		unnamed:  newProgramTitler(nil).name(program) + " wants to look up models in the model registry “lmstudio”.",
	} {
		if got := page.Sentences[id]; got != want {
			t.Fatalf("sentence %q, want %q", got, want)
		}
	}
}

func decodePanel[T any](t *testing.T, code int, body []byte) T {
	t.Helper()
	var v T
	if code != 200 || json.Unmarshal(body, &v) != nil {
		t.Fatalf("panel reply %d %s", code, body)
	}
	return v
}

func TestServicePanelQuestionOperator(t *testing.T) {
	own(t)
	var allow atomic.Bool
	panelQuestionRuntime(t, &allow)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	app, err := panelMachine().ResolveAsks(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	pending := askclient.ApplicationQuestion{RequestKey: "panel-pending", Key: "download.reach", Slots: map[string]string{"host": "pending.example"}}
	answered := askclient.ApplicationQuestion{RequestKey: "panel-answered", Key: "download.reach", Slots: map[string]string{"host": "answered.example"}}
	ids := map[string]string{}
	for _, q := range []askclient.ApplicationQuestion{pending, answered} {
		admitted, err := app.AskContext(ctx, q)
		if err != nil || admitted.Answer == nil {
			t.Fatalf("admission %+v %v", admitted, err)
		}
		ids[q.RequestKey] = admitted.Answer.ID
	}
	h := (&servicePanel{}).handler("test-key")
	list := func() wire.OperatorPage {
		r := panelRequest(t, h, "/questions?cursor=", nil)
		return decodePanel[wire.OperatorPage](t, r.Code, r.Body.Bytes())
	}
	act := func(action questionAction) (int, []byte) {
		r := panelRequest(t, h, "/questions", action)
		return r.Code, r.Body.Bytes()
	}

	if page := list(); page.Outcome != wire.OperatorPageOutcomeForbidden || len(page.Records) != 0 {
		t.Fatalf("unauthorized list %+v", page)
	}
	code, body := act(questionAction{Action: "answer", ID: ids[answered.RequestKey], Option: "once"})
	if d := decodePanel[wire.OperatorDecision](t, code, body); d.Outcome != wire.OperatorDecisionOutcomeForbidden || d.Record != nil {
		t.Fatalf("unauthorized answer %+v", d)
	}
	code, body = act(questionAction{Action: "retire", ID: ids[pending.RequestKey]})
	if d := decodePanel[wire.OperatorRetirement](t, code, body); d.Outcome != wire.OperatorRetirementOutcomeForbidden || d.Record != nil {
		t.Fatalf("unauthorized retirement %+v", d)
	}
	if o, err := app.ObserveContext(ctx, pending.RequestKey, 0); err != nil || o.Outcome != askclient.ObservationOutcomePending {
		t.Fatalf("refused panel actions changed the question %+v %v", o, err)
	}

	allow.Store(true)
	if page := list(); page.Outcome != wire.OperatorPageOutcomePage || len(page.Records) != 2 || !page.Complete {
		t.Fatalf("authorized list %+v", page)
	}
	// Each application question names the bound program that asked, with the
	// display name derived from it (asks ASK-A2, ASK-O11).
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	r := panelRequest(t, h, "/questions?cursor=", nil)
	named := decodePanel[questionPage](t, r.Code, r.Body.Bytes())
	for _, id := range ids {
		asker, ok := named.Askers[id]
		if !ok || !identity.SameSubjectProgram(asker.Program, filepath.Clean(self)) || asker.Name != newProgramTitler(nil).name(asker.Program) {
			t.Fatalf("question %s must name its bound program: %+v", id, named.Askers)
		}
	}
	code, body = act(questionAction{Action: "answer", ID: ids[answered.RequestKey], Option: "once"})
	if d := decodePanel[wire.OperatorDecision](t, code, body); d.Outcome != wire.OperatorDecisionOutcomeAnswered || d.Record == nil || d.Record.Option != "once" {
		t.Fatalf("panel answer %+v", d)
	}
	code, body = act(questionAction{Action: "answer", ID: ids[answered.RequestKey], Option: "refuse"})
	if d := decodePanel[wire.OperatorDecision](t, code, body); d.Outcome != wire.OperatorDecisionOutcomeConflict {
		t.Fatalf("conflicting panel answer %+v", d)
	}
	code, body = act(questionAction{Action: "retire", ID: ids[pending.RequestKey]})
	if d := decodePanel[wire.OperatorRetirement](t, code, body); d.Outcome != wire.OperatorRetirementOutcomeRetired || d.Record == nil || d.Record.ID != ids[pending.RequestKey] {
		t.Fatalf("panel retirement %+v", d)
	}
	code, body = act(questionAction{Action: "retire", ID: ids[pending.RequestKey]})
	if d := decodePanel[wire.OperatorRetirement](t, code, body); d.Outcome != wire.OperatorRetirementOutcomeRetired || d.Record != nil {
		t.Fatalf("panel retirement replay %+v", d)
	}
	code, body = act(questionAction{Action: "retire", ID: "never-admitted"})
	if d := decodePanel[wire.OperatorRetirement](t, code, body); d.Outcome != wire.OperatorRetirementOutcomeUnknown {
		t.Fatalf("unknown panel retirement %+v", d)
	}
	if o, err := app.ObserveContext(ctx, pending.RequestKey, 0); err != nil || o.Outcome != askclient.ObservationOutcomeGone {
		t.Fatalf("application after panel retirement %+v %v", o, err)
	}
	if o, err := app.ObserveContext(ctx, answered.RequestKey, 0); err != nil || o.Outcome != askclient.ObservationOutcomeAnswered {
		t.Fatalf("application after panel answer %+v %v", o, err)
	}
	if page := list(); page.Outcome != wire.OperatorPageOutcomePage || len(page.Records) != 1 || page.Records[0].ID != ids[answered.RequestKey] {
		t.Fatalf("list after retirement %+v", page)
	}
	for _, bad := range []questionAction{{Action: "pause", ID: "x"}, {Action: "answer", ID: "bad\nid", Option: "once"}, {Action: "retire", ID: "x", Option: "once"}, {Action: "answer", ID: "x"}} {
		if code, body := act(bad); code != 400 {
			t.Fatalf("invalid panel action %+v reached the service: %d %s", bad, code, body)
		}
	}
}
