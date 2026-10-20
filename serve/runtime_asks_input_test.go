package main

import (
	"context"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// appProgram is an absolute program path on this platform. Rights refuse a
// program that is not absolute, and a Windows spelling is relative elsewhere.
func appProgram(name string) string {
	if runtime.GOOS == "windows" {
		return `C:\apps\` + name + ".exe"
	}
	return "/opt/apps/" + name
}

// inputAsks composes a rights policy with the content actions and the first-use
// book the runtime admits paired input questions into.
func inputAsks(t *testing.T) (*runtimeRights, *runtimeAsks, *asks.Book) {
	t.Helper()
	policy, err := rights.LoadDecisionPolicy(filepath.Join(t.TempDir(), "rights.json"), append(append([]string{}, firstUseActions...), contentReadAction, contentWriteAction))
	if err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	r := &runtimeRights{policy: policy, owner: a.rights.owner, firstUse: a.admitFirstUse, pairedInput: a.admitPairedInput}
	a.rights = r
	return r, a, book
}

// policyRules is the grants.RuleOperator a Panel answer writes through, over
// the runtime's own decision policy.
type policyRules struct {
	policy *rights.DecisionPolicy
	by     rwire.Subject
	asks   *runtimeAsks
}

func (p policyRules) ApplyFirstUseAllowContext(ctx context.Context, expected, id string) (rwire.PolicyEdit, error) {
	return p.asks.applyOperatorAllow(ctx, p.by, expected, id, func() error { return nil })
}

func (p policyRules) ListPolicyContext(context.Context, string, int64) (rwire.PolicyPage, error) {
	snapshot, err := p.policy.OperatorSnapshot()
	if err != nil {
		return rwire.PolicyPage{Outcome: rwire.PolicyPageOutcomeUnavailable}, nil
	}
	return rwire.PolicyPage{Outcome: rwire.PolicyPageOutcomePage, Revision: snapshot.Revision}, nil
}

func (p policyRules) SetRuleForContext(_ context.Context, expected string, rule rwire.PolicyRule, ttl time.Duration, why string) (rwire.PolicyEdit, error) {
	permit := rule.Permit
	return p.policy.ChangeRule(expected, rule.Subject, rule.Action, rule.Resource, rights.RuleEdit{Permit: &permit, By: p.by, TTL: ttl, Why: why, Exact: true}, func() error { return nil })
}
func (p policyRules) SetRulesForContext(_ context.Context, expected string, rules []rwire.PolicyRule, ttl time.Duration, why string) (rwire.PolicyEdit, error) {
	permit := rules[0].Permit
	return p.policy.ChangeRules(expected, rules, rights.RuleEdit{Permit: &permit, By: p.by, TTL: ttl, Why: why, Exact: true}, func() error { return nil })
}

// pendingQuestion answers one recorded application question as the Panel does.
type pendingQuestion struct {
	record askclient.RecordMetadata
	book   *asks.Book
}

func (q pendingQuestion) ReadAttributedQuestionContext(ctx context.Context, id string) (askclient.AttributedQuestion, error) {
	read, err := q.ReadQuestionAskerContext(ctx, id)
	return read.Details.Question, err
}

func (q pendingQuestion) ReadQuestionAskerContext(_ context.Context, id string) (askclient.QuestionAsker, error) {
	r, found, err := q.book.ApplicationRecord(id)
	if err != nil || !found {
		return askclient.QuestionAsker{Details: askclient.QuestionDetails{Question: askclient.AttributedQuestion{Question: askclient.OperatorQuestion{Outcome: askclient.OperatorQuestionOutcomeUnknown}}}}, err
	}
	record := askclient.RecordMetadata{ID: r.ID, Key: r.Key, About: r.About, Text: r.Text, Option: r.Option}
	return askclient.QuestionAsker{Details: askclient.QuestionDetails{Question: askclient.AttributedQuestion{Question: askclient.OperatorQuestion{Outcome: askclient.OperatorQuestionOutcomeFound, Record: &record}, Answerer: r.Answerer}, Permission: r.Permission}}, nil
}

func (q pendingQuestion) AnswerQuestionIfPendingContext(_ context.Context, id, option string) (askclient.OperatorDecision, error) {
	if q.book != nil {
		r, outcome, err := q.book.AnswerApplicationIfPendingAuthorized(id, option, nil)
		parsed, _ := askswire.ParseOperatorDecisionOutcome(outcome)
		record := askclient.RecordMetadata{ID: r.ID, Key: r.Key, About: r.About, Text: r.Text, Option: r.Option}
		return askclient.OperatorDecision{Outcome: parsed, Record: &record}, err
	}
	record := q.record
	record.ID, record.Option = id, option
	return askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeAnswered, Record: &record}, nil
}

// INF-V4: a first-use question on an input profile's write is never admitted
// bare: neither the generic first-use path nor a not_granted content decision
// asks it.
func TestBareInputProfileQuestionIsRefused(t *testing.T) {
	r, a, book := inputAsks(t)
	subject := rwire.Subject{Account: r.owner, Program: appProgram("opencode")}
	a.admitFirstUse(subject, nil, contentWriteAction, "input:audio")
	if d := r.decideAsking(context.Background(), subject, contentWriteAction, "input:audio"); d.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("decision %+v", d)
	}
	// An output profile that pairs nothing (live) is not admitted as a
	// paired question either, and the generic path never admits a bare
	// output-profile question.
	a.admitPairedInput(subject, nil, "output:live")
	a.admitFirstUse(subject, nil, contentWriteAction, "output:video")
	if records, _, err := book.ApplicationHistory(); err != nil || len(records) != 0 {
		t.Fatalf("a bare input-profile question was admitted: %+v %v", records, err)
	}
}

// INF-V4: a consuming request admits one paired question for its program,
// and none once the program holds both rights or a denial.
func TestConsumingRequestAdmitsThePairedInputQuestion(t *testing.T) {
	r, _, book := inputAsks(t)
	caller := inference.Subject{Account: r.owner, Program: appProgram("opencode")}
	r.askInput(context.Background(), caller, inference.InputAudio)
	r.askInput(context.Background(), caller, inference.InputAudio)
	records, _, err := book.ApplicationHistory()
	if err != nil || len(records) != 1 {
		t.Fatalf("one paired question expected: %+v %v", records, err)
	}
	program, action, resource, ok := asks.FirstUse(records[0].Key, records[0].About, records[0].Text)
	if !ok || program != caller.Program || action != contentWriteAction || resource != "input:audio" {
		t.Fatalf("question %+v", records[0])
	}
	other := inference.Subject{Account: r.owner, Program: appProgram("editor")}
	bound := rwire.Subject{Account: other.Account, Program: other.Program}
	for _, action := range []string{contentWriteAction, contentReadAction} {
		if err := r.policy.Set(bound, action, "input:image", true); err != nil {
			t.Fatal(err)
		}
	}
	r.askInput(context.Background(), other, inference.InputImage)
	if err := r.policy.Set(bound, contentWriteAction, "input:audio", false); err != nil {
		t.Fatal(err)
	}
	r.askInput(context.Background(), other, inference.InputAudio)
	if records, _, err := book.ApplicationHistory(); err != nil || len(records) != 1 {
		t.Fatalf("a granted or denied profile asked again: %+v %v", records, err)
	}
}

// INF-V4: allowing the paired question writes content.write and content.read
// on that input profile for the asking program only; another program, and
// another resource, gain nothing.
func TestPairedInputAllowWritesBothRulesForThatProgramOnly(t *testing.T) {
	r, a, book := inputAsks(t)
	editor := inference.Subject{Account: r.owner, Program: appProgram("editor")}
	r.askInput(context.Background(), editor, inference.InputImage)
	records, _, err := book.ApplicationHistory()
	if err != nil || len(records) != 1 {
		t.Fatalf("paired question %+v %v", records, err)
	}
	question := askclient.RecordMetadata{Key: records[0].Key, About: records[0].About, Text: records[0].Text}
	// An earlier read block is deliberately overridden by this human Allow.
	// The paired write and read must land in one owner edit.
	if err := r.policy.Set(rwire.Subject{Account: r.owner, Program: editor.Program}, contentReadAction, "input:image", false); err != nil {
		t.Fatal(err)
	}
	operator := policyRules{policy: r.policy, by: rwire.Subject{Account: r.owner, Program: appProgram("Abstraction Panel")}, asks: a}
	answered, err := grants.AnswerQuestion(context.Background(), pendingQuestion{record: question, book: book}, operator, r.owner, records[0].ID, "allow")
	if err != nil || answered.Edit == nil || answered.Edit.Outcome != rwire.PolicyEditOutcomeApplied || answered.PairedEdit == nil || answered.PairedEdit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("answer %+v %v", answered, err)
	}
	decide := func(program, action, resource string) rwire.DecisionOutcome {
		return r.decide(context.Background(), rwire.Subject{Account: r.owner, Program: program}, action, resource).Outcome
	}
	for _, action := range []string{contentWriteAction, contentReadAction} {
		if got := decide(editor.Program, action, "input:image"); got != rwire.DecisionOutcomePermitted {
			t.Fatalf("editor %s on input:image = %v", action, got)
		}
		if got := decide(appProgram("other"), action, "input:image"); got != rwire.DecisionOutcomeNotGranted {
			t.Fatalf("another program %s on input:image = %v", action, got)
		}
		for _, resource := range []string{"input:audio", "output:image"} {
			if got := decide(editor.Program, action, resource); got != rwire.DecisionOutcomeNotGranted {
				t.Fatalf("editor %s on %s = %v", action, resource, got)
			}
		}
	}
}
