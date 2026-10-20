package main

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The answerer chooser offers each registered local application's own
// program first, then every program a rule names; no app is named in the
// Panel's source, and a script host is never offered.
func TestAssistantCandidatesComeFromRegisteredApplications(t *testing.T) {
	dir := t.TempDir()
	registered := filepath.Join(dir, "answerer", "answerer.exe")
	remote := filepath.Join(dir, "remote", "remote.exe")
	host := filepath.Join(dir, "python", "python.exe")
	ruled := filepath.Join(dir, "ruled", "ruled.exe")
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{
		{Descriptor: fwire.ApplicationDescriptor{Name: "answerer", Program: registered}, Scope: fwire.ScopeLocal},
		{Descriptor: fwire.ApplicationDescriptor{Name: "remote", Program: remote}, Scope: fwire.ScopeRemote},
		{Descriptor: fwire.ApplicationDescriptor{Name: "host", Program: host}, Scope: fwire.ScopeLocal},
	}}}
	p := &servicePanel{comfyApps: apps}
	rules := []rightsclient.PolicyRule{{Subject: rightsclient.Subject{Account: "alice", Program: ruled}}, {Subject: rightsclient.Subject{Account: "alice", Program: registered}}}
	if got := p.assistantCandidates(context.Background(), rules); strings.Join(got, "|") != registered+"|"+ruled {
		t.Fatalf("candidates %q", got)
	}
	unreadable := &servicePanel{comfyApps: &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomeForbidden}}}
	if got := unreadable.assistantCandidates(context.Background(), rules); strings.Join(got, "|") != ruled+"|"+registered {
		t.Fatalf("an unreadable directory changed the rule candidates: %q", got)
	}
}

// assistantRuntime is the Panel's real runtime fixture with an enrollable
// program beside it: the Book, the rights policy, the app the question is
// about (this test binary), and the assistant's program.
func assistantRuntime(t *testing.T, action string) (*asks.Book, *rights.DecisionPolicy, string, string) {
	t.Helper()
	book, policy, program := panelFirstUseRuntime(t, action)
	assistant := filepath.Join(t.TempDir(), "opencode.exe")
	if err := os.WriteFile(assistant, []byte("assistant"), 0o755); err != nil {
		t.Fatal(err)
	}
	return book, policy, program, assistant
}

func assistantState(t *testing.T, panel http.Handler) assistantPage {
	t.Helper()
	r := panelRequest(t, panel, "/rights/assistant", nil)
	return decodePanel[assistantPage](t, r.Code, r.Body.Bytes())
}

// answerAs records the assistant's allow as the runtime's responder does:
// the Book answer naming its actor, then the permit under its enrollment
// (asks ASK-R4, rights RIGHTS-R16).
func answerAs(t *testing.T, book *asks.Book, policy *rights.DecisionPolicy, assistant, id, program, action, resource string) {
	t.Helper()
	if _, outcome, err := book.AnswerApplicationAsResponder(id, "allow", assistant, nil); err != nil || outcome != "answered" {
		t.Fatalf("the assistant's answer %s %v", outcome, err)
	}
	account := probe.Self().Account
	edit, err := policy.ChangeRuleUnderAnswer(rightsclient.Subject{Account: account, Program: assistant}, rightsclient.Subject{Account: account, Program: program},
		action, resource, true, "answered for you", id, func() error { return nil })
	if err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("the assistant's permit %+v %v", edit, err)
	}
}

// ASK-R5: the yes is worded from the one tier table, refuses a script host
// and the Panel itself, needs the person's confirmation, and writes the
// enrollment that lapses after 30 days; Stop ends it.
func TestTheAssistantYesIsWordedFromTheTierTableAndRefusesScriptHosts(t *testing.T) {
	const action = "abstraction.model/lookup"
	_, policy, program, assistant := assistantRuntime(t, action)
	panel := (&servicePanel{}).handler("test-key")
	state := assistantState(t, panel)
	if state.Outcome != "page" || len(state.Assistants) != 0 || state.Revision == "" {
		t.Fatalf("before the yes %+v", state)
	}
	answers, stays := strings.Join(state.Warning.Answers, "|"), strings.Join(state.Warning.StaysWithYou, "|")
	for _, want := range []string{"use a model on this device", "save and show the pictures, speech or video it made"} {
		if !strings.Contains(answers, want) {
			t.Fatalf("the yes does not name %q: %s", want, answers)
		}
	}
	for _, want := range []string{"lend your model files to another app", "use a model on a hosted server, which can cost money", "passwords and keys"} {
		if !strings.Contains(stays, want) {
			t.Fatalf("the yes does not keep %q with the person: %s", want, stays)
		}
	}
	python := filepath.Join(filepath.Dir(assistant), "python.exe")
	for _, edit := range []assistantEdit{
		{Action: "enroll", Program: assistant, Revision: state.Revision},
		{Action: "enroll", Program: python, Revision: state.Revision, Confirm: true},
		{Action: "enroll", Program: program, Revision: state.Revision, Confirm: true},
	} {
		if r := panelRequest(t, panel, "/rights/assistant", edit); r.Code != 400 {
			t.Fatalf("%+v = %d %s", edit, r.Code, r.Body.String())
		}
	}
	account := probe.Self().Account
	for _, p := range []string{assistant, python, program} {
		if d := policy.Decide(rightsclient.Subject{Account: account, Program: p}, asks.AnswerAction, rights.MetaScopeApps); d.Outcome.String() == "permitted" {
			t.Fatalf("a refused yes enrolled %s", p)
		}
	}
	r := panelRequest(t, panel, "/rights/assistant", assistantEdit{Action: "enroll", Program: assistant, Revision: state.Revision, Confirm: true})
	result := decodePanel[assistantResult](t, r.Code, r.Body.Bytes())
	if result.Outcome != "applied" || result.Message != "opencode (not registered) can now answer routine questions for you." {
		t.Fatalf("the yes %+v", result)
	}
	read := policy.ReadRule(rightsclient.Subject{Account: account, Program: assistant}, asks.AnswerAction, rights.MetaScopeApps)
	expires, err := time.Parse(time.RFC3339Nano, read.Record.Expires)
	if read.Record == nil || err != nil || time.Until(expires) < rights.MetaRuleLapse-time.Minute || read.Record.Why != "Let opencode answer OpenAbstractions questions for you" {
		t.Fatalf("the enrollment %+v %v", read.Record, err)
	}
	state = assistantState(t, panel)
	if len(state.Assistants) != 1 || state.Assistants[0].Name != "opencode (not registered)" {
		t.Fatalf("after the yes %+v", state.Assistants)
	}
	r = panelRequest(t, panel, "/rights/assistant", assistantEdit{Action: "stop", Program: assistant, Revision: state.Revision})
	if result := decodePanel[assistantResult](t, r.Code, r.Body.Bytes()); result.Outcome != "applied" || !strings.HasPrefix(result.Message, "opencode (not registered) can no longer answer new questions for you.") {
		t.Fatalf("stop %+v", result)
	}
	// Stop ends answering and keeps the next enrollment question away for
	// the refuse interval (ASK-R10).
	if d := policy.Decide(rightsclient.Subject{Account: account, Program: assistant}, asks.AnswerAction, rights.MetaScopeApps); d.Outcome.String() != "denied" {
		t.Fatalf("after Stop %v", d.Outcome)
	}
	read = policy.ReadRule(rightsclient.Subject{Account: account, Program: assistant}, asks.AnswerAction, rights.MetaScopeApps)
	quiet, err := time.Parse(time.RFC3339Nano, read.Record.Expires)
	if read.Record == nil || read.Record.Rule.Permit || read.Record.Why != grants.EnrollmentStoppedWhy || err != nil ||
		time.Until(quiet) > grants.FirstUseRefuseInterval || time.Until(quiet) < grants.FirstUseRefuseInterval-time.Minute {
		t.Fatalf("plain Stop's quiet period %+v %v", read.Record, err)
	}
	// Stop and don't ask again: the deny stands with no expiry.
	state = assistantState(t, panel)
	if r := panelRequest(t, panel, "/rights/assistant", assistantEdit{Action: "enroll", Program: assistant, Revision: state.Revision, Confirm: true}); r.Code != 200 {
		t.Fatalf("enroll again %d %s", r.Code, r.Body.String())
	}
	state = assistantState(t, panel)
	r = panelRequest(t, panel, "/rights/assistant", assistantEdit{Action: "stop", Program: assistant, Revision: state.Revision, DontAskAgain: true})
	if result := decodePanel[assistantResult](t, r.Code, r.Body.Bytes()); result.Outcome != "applied" || !strings.HasSuffix(result.Message, "It will not ask again.") {
		t.Fatalf("stop and don't ask again %+v", result)
	}
	read = policy.ReadRule(rightsclient.Subject{Account: account, Program: assistant}, asks.AnswerAction, rights.MetaScopeApps)
	if read.Record == nil || read.Record.Rule.Permit || read.Record.Expires != "" || read.Record.Why != grants.EnrollmentNeverAgainWhy {
		t.Fatalf("don't ask again %+v", read.Record)
	}
}

// Answered for you (PROPOSAL §15 part 5): the record names the assistant;
// Undo removes its current permit. Meeting point: an Undo
// after the person changed that permit leaves the person's rule.
func TestAnsweredForYouUndoesOnlyTheAssistantsOwnPermit(t *testing.T) {
	const action = "abstraction.model/lookup"
	book, policy, program, assistant := assistantRuntime(t, action)
	panel := (&servicePanel{}).handler("test-key")
	state := assistantState(t, panel)
	if r := panelRequest(t, panel, "/rights/assistant", assistantEdit{Action: "enroll", Program: assistant, Revision: state.Revision, Confirm: true}); r.Code != 200 {
		t.Fatalf("enroll %d %s", r.Code, r.Body.String())
	}
	ask := func(resource string) string {
		slots := map[string]string{"program": program, "action": action, "resource": resource}
		record, _, err := book.AskApplication("runtime-scope", "runtime", "first-use:"+resource, asks.FirstUseKey, slots, listen.Seen{Why: "test"})
		if err != nil {
			t.Fatal(err)
		}
		return record.ID
	}
	undone, changed := ask("hf"), ask("ollama")
	answerAs(t, book, policy, assistant, undone, program, action, "hf")
	answerAs(t, book, policy, assistant, changed, program, action, "ollama")
	list := func() answeredPage {
		r := panelRequest(t, panel, "/rights/answered", nil)
		return decodePanel[answeredPage](t, r.Code, r.Body.Bytes())
	}
	page := list()
	if page.Outcome != "page" || len(page.Answers) != 2 {
		t.Fatalf("answered for you %+v", page)
	}
	for _, a := range page.Answers {
		if a.State != "in_force" || a.AnswererName != "opencode (not registered)" || a.Option != "allow" || a.Program != program || !strings.Contains(a.Sentence, "wants to") {
			t.Fatalf("an answer %+v", a)
		}
	}
	account := probe.Self().Account
	target := rightsclient.Subject{Account: account, Program: program}
	// The person changes one permit themselves after the assistant answered.
	snap, err := policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	permit := true
	if _, err := policy.ChangeRule(snap.Revision, target, action, "ollama", rights.RuleEdit{Permit: &permit, By: rightsclient.Subject{Account: account, Program: program}, Why: "the person's own edit", Exact: true}, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	r := panelRequest(t, panel, "/rights/answered", map[string]string{"id": changed})
	if result := decodePanel[assistantResult](t, r.Code, r.Body.Bytes()); result.Outcome != "conflict" {
		t.Fatalf("undo over the person's change %+v", result)
	}
	if d := policy.Decide(target, action, "ollama"); d.Outcome.String() != "permitted" {
		t.Fatalf("undo replaced the person's permit: %v", d.Outcome)
	}
	r = panelRequest(t, panel, "/rights/answered", map[string]string{"id": undone})
	if result := decodePanel[assistantResult](t, r.Code, r.Body.Bytes()); result.Outcome != "applied" || !strings.HasPrefix(result.Message, "Undone:") {
		t.Fatalf("undo %+v", result)
	}
	if d := policy.Decide(target, action, "hf"); d.Outcome.String() != "not_granted" {
		t.Fatalf("after undo %v", d.Outcome)
	}
	states := map[string]string{}
	for _, a := range list().Answers {
		states[a.ID] = a.State
	}
	if states[undone] != "ended" || states[changed] != "changed" {
		t.Fatalf("states after undo %+v", states)
	}
	// Optional cleanup ends the assistant's permits; their answers read ended.
	third := ask("listed")
	answerAs(t, book, policy, assistant, third, program, action, "listed")
	state = assistantState(t, panel)
	panelRequest(t, panel, "/rights/assistant", assistantEdit{Action: "stop", Program: assistant, Revision: state.Revision, AlsoRevokeGrants: true})
	for _, a := range list().Answers {
		if a.ID == third && a.State != "ended" {
			t.Fatalf("an answer after Stop %+v", a)
		}
	}
}

// Meeting point: the person changes the permit between Undo's read and its
// write. The write is conditional on the revision it read and conflicts.
func TestUndoRacingThePersonsEditConflicts(t *testing.T) {
	const action = "abstraction.model/lookup"
	book, policy, program, assistant := assistantRuntime(t, action)
	panel := (&servicePanel{}).handler("test-key")
	state := assistantState(t, panel)
	panelRequest(t, panel, "/rights/assistant", assistantEdit{Action: "enroll", Program: assistant, Revision: state.Revision, Confirm: true})
	slots := map[string]string{"program": program, "action": action, "resource": "hf"}
	record, _, err := book.AskApplication("runtime-scope", "runtime", "first-use:race", asks.FirstUseKey, slots, listen.Seen{Why: "test"})
	if err != nil {
		t.Fatal(err)
	}
	answerAs(t, book, policy, assistant, record.ID, program, action, "hf")
	real := assistantRuleOperator
	t.Cleanup(func() { assistantRuleOperator = real })
	target := rightsclient.Subject{Account: probe.Self().Account, Program: program}
	assistantRuleOperator = func() assistantRules { return racingRules{real(), policy, target, action} }
	r := panelRequest(t, panel, "/rights/answered", map[string]string{"id": record.ID})
	if result := decodePanel[assistantResult](t, r.Code, r.Body.Bytes()); result.Outcome != "conflict" {
		t.Fatalf("undo racing the person %+v", result)
	}
	if read := policy.ReadRule(target, action, "hf"); read.Record == nil || !read.Record.Rule.Permit || read.Record.Why != "the person's own edit" {
		t.Fatalf("the person's edit was replaced %+v", read.Record)
	}
}

func TestPairedUndoRemainsAvailableAfterPersonChangesOnlyRead(t *testing.T) {
	book, policy, program, assistant := assistantRuntime(t, grants.ContentWriteAction)
	if err := policy.RegisterAction(grants.ContentReadAction); err != nil {
		t.Fatal(err)
	}
	panel := (&servicePanel{}).handler("test-key")
	state := assistantState(t, panel)
	panelRequest(t, panel, "/rights/assistant", assistantEdit{Action: "enroll", Program: assistant, Revision: state.Revision, Confirm: true})
	record, _, err := book.AskApplication("runtime-scope", "runtime", "paired-one", asks.FirstUseKey,
		map[string]string{"program": program, "action": grants.ContentWriteAction, "resource": "input:image"}, listen.Seen{Why: "test"})
	if err != nil {
		t.Fatal(err)
	}
	answerAs(t, book, policy, assistant, record.ID, program, grants.ContentWriteAction, "input:image")
	account := probe.Self().Account
	target := rightsclient.Subject{Account: account, Program: program}
	holder := rightsclient.Subject{Account: account, Program: assistant}
	if edit, err := policy.ChangeRuleUnderAnswer(holder, target, grants.ContentReadAction, "input:image", true, "answered for you", record.ID, func() error { return nil }); err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("paired read %+v %v", edit, err)
	}
	permit := true
	if edit, err := policy.ChangeRule(revisionOf(t, policy), target, grants.ContentReadAction, "input:image", rights.RuleEdit{Permit: &permit, By: target, Why: "person changed read", Exact: true}, func() error { return nil }); err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("person edit %+v %v", edit, err)
	}
	r := panelRequest(t, panel, "/rights/answered", nil)
	page := decodePanel[answeredPage](t, r.Code, r.Body.Bytes())
	if len(page.Answers) != 1 || page.Answers[0].State != "in_force" {
		t.Fatalf("Undo hidden while write survives %+v", page)
	}
	r = panelRequest(t, panel, "/rights/answered", map[string]string{"id": record.ID})
	if result := decodePanel[assistantResult](t, r.Code, r.Body.Bytes()); result.Outcome != "applied" {
		t.Fatalf("partial paired Undo %+v", result)
	}
	if policy.Decide(target, grants.ContentWriteAction, "input:image").Outcome.String() != "not_granted" || policy.Decide(target, grants.ContentReadAction, "input:image").Outcome.String() != "permitted" {
		t.Fatal("Undo removed person's read or left answerer's write")
	}
}

// The Panel snapshots Q1 rights before re-reading its asks record. Between
// that read and its grouped revoke, Q1 is released and Q2 grants the same
// target with the same setter and service millisecond. Q1 Undo must not
// delete Q2, even if all public rule fields would otherwise be identical.
func TestUndoCannotRemoveSameMillisecondReplacementAfterQuestionRelease(t *testing.T) {
	const action, resource = "abstraction.model/lookup", "hf"
	book, policy, program, assistant := assistantRuntime(t, action)
	fixed := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	policy.Clock = func() time.Time { return fixed }
	panel := (&servicePanel{}).handler("test-key")
	state := assistantState(t, panel)
	panelRequest(t, panel, "/rights/assistant", assistantEdit{Action: "enroll", Program: assistant, Revision: state.Revision, Confirm: true})
	slots := map[string]string{"program": program, "action": action, "resource": resource}
	q1, _, err := book.AskApplication("runtime-scope", "runtime", "first-use:aba", asks.FirstUseKey, slots, listen.Seen{Why: "test"})
	if err != nil {
		t.Fatal(err)
	}
	answerAs(t, book, policy, assistant, q1.ID, program, action, resource)
	real := assistantRuleOperator
	t.Cleanup(func() { assistantRuleOperator = real })
	interleave := &replacementRules{assistantRules: real(), t: t, book: book, policy: policy, program: program, assistant: assistant, action: action, resource: resource, slots: slots}
	assistantRuleOperator = func() assistantRules { return interleave }
	r := panelRequest(t, panel, "/rights/answered", map[string]string{"id": q1.ID})
	if result := decodePanel[assistantResult](t, r.Code, r.Body.Bytes()); result.Outcome != "conflict" {
		t.Fatalf("old Undo %+v", result)
	}
	if interleave.q2 == "" || interleave.q2 == q1.ID {
		t.Fatal("replacement question not admitted")
	}
	target := rightsclient.Subject{Account: probe.Self().Account, Program: program}
	if read := policy.ReadRule(target, action, resource); read.Record == nil || !read.Record.Rule.Permit || read.Record.SetBy.Program != assistant {
		t.Fatalf("replacement was removed %+v", read)
	}
}

type replacementRules struct {
	assistantRules
	t                                    *testing.T
	book                                 *asks.Book
	policy                               *rights.DecisionPolicy
	program, assistant, action, resource string
	slots                                map[string]string
	q2                                   string
}

func (r *replacementRules) RevokeAnswerPermitsContext(ctx context.Context, expected string, answerer, target rightsclient.Subject, actions []string, resource, questionID string) (rightsclient.PolicyEdit, error) {
	r.t.Helper()
	if err := r.policy.Revoke(target, r.action, r.resource); err != nil {
		r.t.Fatal(err)
	}
	if removed, err := r.book.ReleaseAnsweredApplication("runtime-scope", "first-use:aba", "allow"); err != nil || !removed {
		r.t.Fatalf("release Q1 %v %v", removed, err)
	}
	q2, _, err := r.book.AskApplication("runtime-scope", "runtime", "first-use:aba", asks.FirstUseKey, r.slots, listen.Seen{Why: "test"})
	if err != nil {
		r.t.Fatal(err)
	}
	r.q2 = q2.ID
	answerAs(r.t, r.book, r.policy, r.assistant, r.q2, r.program, r.action, r.resource)
	return r.assistantRules.RevokeAnswerPermitsContext(ctx, expected, answerer, target, actions, resource, questionID)
}

func revisionOf(t *testing.T, policy *rights.DecisionPolicy) string {
	t.Helper()
	snapshot, err := policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Revision
}

// racingRules lands the person's own edit just before Undo's write.
type racingRules struct {
	assistantRules
	policy *rights.DecisionPolicy
	target rightsclient.Subject
	action string
}

func (r racingRules) RevokeAnswerPermitsContext(ctx context.Context, expected string, answerer, target rightsclient.Subject, actions []string, resource, questionID string) (rightsclient.PolicyEdit, error) {
	snap, err := r.policy.OperatorSnapshot()
	if err != nil {
		return rightsclient.PolicyEdit{}, err
	}
	permit := true
	if _, err := r.policy.ChangeRule(snap.Revision, r.target, r.action, "hf", rights.RuleEdit{Permit: &permit, By: r.target, Why: "the person's own edit", Exact: true}, func() error { return nil }); err != nil {
		return rightsclient.PolicyEdit{}, err
	}
	return r.assistantRules.RevokeAnswerPermitsContext(ctx, expected, answerer, target, actions, resource, questionID)
}

// The page renders the yes, the assistant and Answered for you from the real
// handler's replies, every app-supplied string as text (VISION 2026-10-03;
// untrusted input). The page script sets no markup from data.
func TestTheQuestionsPageShowsAssistantsAndAnsweredForYou(t *testing.T) {
	if strings.Contains(rightsPage, "innerHTML") || strings.Contains(rightsPage, "insertAdjacentHTML") {
		t.Fatal("the Questions page sets markup from data")
	}
	const action = "abstraction.model/lookup"
	book, policy, program, assistant := assistantRuntime(t, action)
	panel := (&servicePanel{}).handler("test-key")
	state := assistantState(t, panel)
	panelRequest(t, panel, "/rights/assistant", assistantEdit{Action: "enroll", Program: assistant, Revision: state.Revision, Confirm: true})
	const hostile = "<img src=x onerror=alert(1)>"
	slots := map[string]string{"program": program, "action": action, "resource": hostile}
	record, _, err := book.AskApplication("runtime-scope", "runtime", "first-use:hostile", asks.FirstUseKey, slots, listen.Seen{Why: "test"})
	if err != nil {
		t.Fatal(err)
	}
	answerAs(t, book, policy, assistant, record.ID, program, action, hostile)
	report := runRightsPage(t)
	if len(report.Assistants) != 1 || len(report.Assistants[0]) != 4 || !strings.HasPrefix(report.Assistants[0][0], "opencode (not registered) answers routine questions for you") ||
		report.Assistants[0][2] != "Revoke answering access" || report.Assistants[0][3] != "Stop and don't ask again" {
		t.Fatalf("assistants %+v (%s)", report.Assistants, report.AssistantStatus)
	}
	warning := strings.Join(report.AssistantWarning, "\n")
	for _, want := range []string{"use a model on this device", "lend your model files to another app", "Warning: ", "30 days without an answer"} {
		if !strings.Contains(warning, want) {
			t.Fatalf("the warning lacks %q:\n%s", want, warning)
		}
	}
	// The app's resource name is no plain name, so the sentence leaves it out;
	// nothing of it reaches the page as markup or as text.
	if len(report.Answered) != 1 || !strings.Contains(report.Answered[0].Lines[0], "wants to look up models") || strings.Contains(strings.Join(report.Answered[0].Lines, "\n"), "<img") ||
		!strings.Contains(strings.Join(report.Answered[0].Lines, "\n"), "opencode (not registered) answered Allow") || len(report.Answered[0].Buttons) != 1 || report.Answered[0].Buttons[0] != "Undo" {
		t.Fatalf("answered for you %+v (%s)", report.Answered, report.AnsweredStatus)
	}
}
