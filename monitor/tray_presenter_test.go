package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

type fakeSession struct {
	unlocked atomic.Bool
	revision atomic.Uint64
}

func (s *fakeSession) Revision() uint64 { return s.revision.Load() }
func (s *fakeSession) Unlocked() bool   { return s.unlocked.Load() }

func unlockedSession() *fakeSession {
	s := &fakeSession{}
	s.unlocked.Store(true)
	return s
}

// fakeSurface answers with option, after running during (which a test uses to
// change the session or the book while the prompt is open).
type fakeSurface struct {
	option    string
	err       error
	during    func()
	block     bool
	dismissed chan struct{}
	shown     []trayQuestionPresentation
	once      sync.Once
}

func (s *fakeSurface) Ask(p trayQuestionPresentation) (string, error) {
	s.shown = append(s.shown, p)
	if s.during != nil {
		s.during()
	}
	if s.block {
		select {
		case <-s.dismissed:
			return "", nil
		case <-time.After(5 * time.Second):
			return "", errors.New("prompt was never dismissed")
		}
	}
	return s.option, s.err
}

func (s *fakeSurface) Dismiss() { s.once.Do(func() { close(s.dismissed) }) }

// fakeOperator is the public operator surface. list returns the current book;
// answers records every AnswerQuestion the presenter sends; publishers holds
// the publisher recorded at admission by question id.
type fakeOperator struct {
	mu           sync.Mutex
	records      []askclient.RecordMetadata
	listErr      error
	answers      []string
	publishers   map[string]askclient.OperatorQuestion
	details      map[string]askclient.QuestionDetails
	detailsErr   error
	afterDetails func()
	// beforeAnswer runs as an answer call arrives, before it is applied: an
	// answer from another path that lands after the presenter's re-read.
	beforeAnswer func()
}

func (o *fakeOperator) ReadQuestionAskerContext(_ context.Context, id string) (askclient.QuestionAsker, error) {
	if o.afterDetails != nil {
		defer o.afterDetails()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.detailsErr != nil {
		return askclient.QuestionAsker{}, o.detailsErr
	}
	if details, ok := o.details[id]; ok {
		return askclient.QuestionAsker{Details: details}, nil
	}
	return askclient.QuestionAsker{}, &askclient.ServiceError{Code: askclient.ServiceErrorCodeUnknownMethod}
}

func typedPresenterDetails(t *testing.T, record askclient.RecordMetadata, target askclient.PermissionTarget) askclient.QuestionDetails {
	t.Helper()
	permission, err := asks.NewPermissionRequest(target)
	if err != nil {
		t.Fatal(err)
	}
	return askclient.QuestionDetails{Question: askclient.AttributedQuestion{Question: askclient.OperatorQuestion{
		Outcome: askclient.OperatorQuestionOutcomeFound, Record: &record, PublisherTrusted: true,
		Publisher: "Recorded Signer", ProgramDescription: "Recorded Application"}}, Permission: &permission}
}

func TestPresenterUsesFullStructuredTargetsAndRejectsChanges(t *testing.T) {
	record := pendingFixture()
	// Current service wording carries no parsable permission tuple. Exact targets
	// remain authoritative and long values remain fully available in Details.
	record.Text = "Send this application's results and read them back."
	program := nativePresenterProgram(strings.Repeat("long-app-", 32), "real.exe")
	resource := "output:image"
	target := askclient.PermissionTarget{Account: "S-1-5-21-test", Program: program, Action: asks.PermissionContentWrite, Resource: resource}
	for _, changed := range []string{"", "program", "action", "resource"} {
		t.Run(changed, func(t *testing.T) {
			details := typedPresenterDetails(t, record, target)
			operator := &fakeOperator{records: []askclient.RecordMetadata{record}, details: map[string]askclient.QuestionDetails{record.ID: details}}
			surface := &fakeSurface{option: "refuse"}
			if changed != "" {
				surface.during = func() {
					operator.mu.Lock()
					defer operator.mu.Unlock()
					// Mutate the original slice to catch a presenter retaining a live
					// reference instead of the complete frozen occurrence.
					for index := range details.Permission.Targets {
						target := &details.Permission.Targets[index]
						switch changed {
						case "program":
							target.Program = nativePresenterProgram("replacement.exe")
						case "action":
							target.Action = "abstraction.model/lookup"
						case "resource":
							target.Resource = "output:speech"
						}
					}
					if changed == "action" {
						details.Permission.Targets = details.Permission.Targets[:1]
					}
				}
			}
			result := testPresenter(unlockedSession(), surface, operator, &fakeRules{}).present(context.Background(), record)
			if len(surface.shown) != 1 {
				t.Fatalf("shown=%+v result=%+v", surface.shown, result)
			}
			prompt := surface.shown[0]
			for _, full := range []string{program, target.Account, target.Action, asks.PermissionContentRead, resource} {
				if !strings.Contains(prompt.Details, full) {
					t.Fatalf("missing exact %q in %q", full, prompt.Details)
				}
			}
			if !strings.Contains(prompt.Instruction, "Recorded Application") || !strings.Contains(prompt.Content, "Recorded Signer") || !strings.Contains(prompt.Footer, "reading this application's content back") {
				t.Fatalf("structured publisher/effect missing: %+v", prompt)
			}
			if changed == "" {
				if result.Outcome != presentAnswered || len(operator.sent()) != 1 {
					t.Fatalf("result=%+v sent=%v", result, operator.sent())
				}
			} else if result.Outcome != presentStale || len(operator.sent()) != 0 {
				t.Fatalf("changed targets must send nothing: result=%+v sent=%v", result, operator.sent())
			}
		})
	}
}

func TestPresenterRefusesUnavailableOrMalformedCurrentDetails(t *testing.T) {
	record := pendingFixture()
	for _, cause := range []string{"unavailable", "missing permission", "foreign account"} {
		t.Run(cause, func(t *testing.T) {
			details := typedPresenterDetails(t, record, askclient.PermissionTarget{Account: "S-1-5-21-test", Program: record.About, Action: "abstraction.model/lookup", Resource: "hf"})
			operator := &fakeOperator{records: []askclient.RecordMetadata{record}, details: map[string]askclient.QuestionDetails{record.ID: details}}
			switch cause {
			case "unavailable":
				operator.detailsErr = errors.New("details unavailable")
			case "missing permission":
				details.Permission = nil
				operator.details[record.ID] = details
			case "foreign account":
				details.Permission.Targets[0].Account = "other-account"
			}
			surface := &fakeSurface{option: "allow"}
			result := testPresenter(unlockedSession(), surface, operator, &fakeRules{}).present(context.Background(), record)
			if result.Outcome != presentUnavailable || len(surface.shown) != 0 || len(operator.sent()) != 0 {
				t.Fatalf("result=%+v shown=%v sent=%v", result, surface.shown, operator.sent())
			}
		})
	}
}

func TestPresenterDismissesChangedStructuredTargetsWhileShown(t *testing.T) {
	record := pendingFixture()
	details := typedPresenterDetails(t, record, askclient.PermissionTarget{Account: "S-1-5-21-test", Program: record.About, Action: "abstraction.model/lookup", Resource: "hf"})
	operator := &fakeOperator{records: []askclient.RecordMetadata{record}, details: map[string]askclient.QuestionDetails{record.ID: details}}
	surface := &fakeSurface{block: true, dismissed: make(chan struct{}), during: func() {
		operator.mu.Lock()
		defer operator.mu.Unlock()
		details.Permission.Targets[0].Resource = "replacement-registry"
	}}
	presenter := testPresenter(unlockedSession(), surface, operator, &fakeRules{})
	presenter.watchEvery = 5 * time.Millisecond
	result := presenter.present(context.Background(), record)
	if result.Outcome != presentStale || !result.ClosedElsewhere || len(operator.sent()) != 0 {
		t.Fatalf("changed structured targets must close with no answer: %+v sent=%v", result, operator.sent())
	}
}

func TestPresenterRechecksSessionAfterDetailsFetch(t *testing.T) {
	for _, interruptRead := range []int{1, 2} {
		t.Run(string(rune('0'+interruptRead)), func(t *testing.T) {
			record := pendingFixture()
			session := unlockedSession()
			operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
			reads := 0
			operator.afterDetails = func() {
				reads++
				if reads == interruptRead {
					session.revision.Add(1)
				}
			}
			surface := &fakeSurface{option: "allow"}
			result := testPresenter(session, surface, operator, &fakeRules{}).present(context.Background(), record)
			if result.Outcome != presentDeferred || len(operator.sent()) != 0 || len(surface.shown) != interruptRead-1 {
				t.Fatalf("result=%+v shown=%v sent=%v", result, surface.shown, operator.sent())
			}
		})
	}
}

func (o *fakeOperator) ReadAttributedQuestionContext(_ context.Context, id string) (askclient.AttributedQuestion, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.listErr != nil {
		return askclient.AttributedQuestion{}, o.listErr
	}
	for _, record := range o.records {
		if record.ID == id {
			read := o.publishers[id]
			read.Outcome, read.Record = askclient.OperatorQuestionOutcomeFound, &record
			return askclient.AttributedQuestion{Question: read}, nil
		}
	}
	return askclient.AttributedQuestion{Question: askclient.OperatorQuestion{Outcome: askclient.OperatorQuestionOutcomeUnknown}}, nil
}

func (o *fakeOperator) set(records ...askclient.RecordMetadata) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.records = records
}

func (o *fakeOperator) ListQuestionsContext(context.Context, string, int64) (askclient.OperatorPage, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.listErr != nil {
		return askclient.OperatorPage{}, o.listErr
	}
	return askclient.OperatorPage{Outcome: askclient.OperatorPageOutcomePage, Records: append([]askclient.RecordMetadata(nil), o.records...), Complete: true}, nil
}

// AnswerQuestionContext follows ASK-O3: a same-option answer to an answered
// question replays it, another option conflicts.
func (o *fakeOperator) AnswerQuestionContext(_ context.Context, id, option string) (askclient.OperatorDecision, error) {
	return o.answer(id, option, false)
}

// AnswerQuestionIfPendingContext follows ASK-O8: any answer to an answered
// question conflicts.
func (o *fakeOperator) AnswerQuestionIfPendingContext(_ context.Context, id, option string) (askclient.OperatorDecision, error) {
	return o.answer(id, option, true)
}

func (o *fakeOperator) answer(id, option string, ifPending bool) (askclient.OperatorDecision, error) {
	if o.beforeAnswer != nil {
		o.beforeAnswer()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.answers = append(o.answers, id+"="+option)
	for i, record := range o.records {
		if record.ID != id {
			continue
		}
		if record.Option != "" {
			if ifPending || record.Option != option {
				return askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeConflict}, nil
			}
			return askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeAnswered, Record: &record}, nil
		}
		record.Option, record.Answered = option, "2026-09-30T09:01:00Z"
		o.records[i] = record
		return askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeAnswered, Record: &record}, nil
	}
	return askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeUnknown}, nil
}

func (o *fakeOperator) sent() []string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]string(nil), o.answers...)
}

type fakeRules struct {
	edits     []rightsclient.PolicyRule
	questions *fakeOperator
}

func (r *fakeRules) ApplyFirstUseAllowContext(_ context.Context, expected, id string) (rightsclient.PolicyEdit, error) {
	r.questions.mu.Lock()
	defer r.questions.mu.Unlock()
	for _, record := range r.questions.records {
		if record.ID == id {
			rules := grants.FirstUseRules("S-1-5-21-test", record)
			r.edits = append(r.edits, rules...)
			return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeApplied, Revision: expected + "+1"}, nil
		}
	}
	return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeInvalid}, nil
}

func (r *fakeRules) ListPolicyContext(context.Context, string, int64) (rightsclient.PolicyPage, error) {
	return rightsclient.PolicyPage{Outcome: rightsclient.PolicyPageOutcomePage, Revision: "r1"}, nil
}

func (r *fakeRules) SetRuleForContext(_ context.Context, expected string, rule rightsclient.PolicyRule, _ time.Duration, _ string) (rightsclient.PolicyEdit, error) {
	r.edits = append(r.edits, rule)
	return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeApplied, Revision: expected + "+1"}, nil
}
func (r *fakeRules) SetRulesForContext(_ context.Context, expected string, rules []rightsclient.PolicyRule, _ time.Duration, _ string) (rightsclient.PolicyEdit, error) {
	r.edits = append(r.edits, rules...)
	return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeApplied, Revision: expected + "+1"}, nil
}

func testPresenter(session presenterSession, surface presenterSurface, operator *fakeOperator, rules *fakeRules) *questionPresenter {
	rules.questions = operator
	return &questionPresenter{
		session:  session,
		surface:  surface,
		operator: func(context.Context) (presenterOperator, error) { return operator, nil },
		rules:    func() grants.RuleOperator { return rules },
		account:  "S-1-5-21-test",
		budget:   time.Second,
	}
}

// TestPresenterShowsPublisherRecordedAtAdmission: the prompt names the
// publisher the runtime recorded when it admitted the question (ASK-A6), read
// through the operator (ASK-O7), and a trusted program by its recorded signed
// description; a script host adds the sentence asks gives for it (ASK-A8). An
// untrusted or absent record reads "not verified"; a question gone before the
// prompt opens is never shown.
func TestPresenterShowsPublisherRecordedAtAdmission(t *testing.T) {
	record := pendingFixture()
	const hostSentence = "Windows PowerShell runs scripts; its publisher covers Windows PowerShell and the script it runs is unverified."
	cases := []struct {
		name      string
		read      askclient.OperatorQuestion
		publisher string
		program   string
	}{
		{"trusted", askclient.OperatorQuestion{Publisher: "Recorded Signer", PublisherTrusted: true}, "Publisher: Recorded Signer (signature verified when the application asked).", "opencode (not registered)"},
		{"trusted without a recorded signer name", askclient.OperatorQuestion{PublisherTrusted: true},
			"Publisher: signature verified when the application asked; no publisher name was recorded.", "opencode (not registered)"},
		{"script host", askclient.OperatorQuestion{Publisher: "Microsoft Windows", PublisherTrusted: true,
			ProgramDescription: "Windows PowerShell", ProgramOriginalName: "PowerShell.EXE"},
			"Publisher: Microsoft Windows (signature verified when the application asked).\n\n" + hostSentence, "Windows PowerShell"},
		{"untrusted", askclient.OperatorQuestion{Publisher: "Recorded Signer"}, "Publisher: not verified.", "opencode (not registered)"},
		{"none", askclient.OperatorQuestion{}, "Publisher: not verified.", "opencode (not registered)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			operator := &fakeOperator{records: []askclient.RecordMetadata{record}, publishers: map[string]askclient.OperatorQuestion{record.ID: tc.read}}
			surface := &fakeSurface{}
			testPresenter(unlockedSession(), surface, operator, &fakeRules{}).present(context.Background(), record)
			if len(surface.shown) != 1 || !strings.Contains(surface.shown[0].Content, tc.publisher) ||
				surface.shown[0].Instruction != "Allow "+tc.program+" to look up models?" {
				t.Fatalf("prompt must name %q and show %q: %+v", tc.program, tc.publisher, surface.shown)
			}
		})
	}
	operator := &fakeOperator{}
	surface := &fakeSurface{option: "allow"}
	result := testPresenter(unlockedSession(), surface, operator, &fakeRules{}).present(context.Background(), record)
	if len(surface.shown) != 0 || result.Outcome != presentStale || !result.ClosedElsewhere || len(operator.sent()) != 0 {
		t.Fatalf("a question gone before the prompt must not be shown: %+v shown=%d", result, len(surface.shown))
	}
}

func pendingFixture() askclient.RecordMetadata {
	return firstUseFixture("question-1", nativePresenterProgram("opencode", "opencode.exe"), "abstraction.model/lookup", "hf")
}

func nativePresenterProgram(parts ...string) string {
	path, err := filepath.Abs(filepath.Join(append([]string{"presenter-fixtures"}, parts...)...))
	if err != nil {
		panic(err)
	}
	return path
}

func TestPresenterDefersWhileLocked(t *testing.T) {
	record := pendingFixture()
	operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
	surface := &fakeSurface{option: "allow"}
	p := testPresenter(&fakeSession{}, surface, operator, &fakeRules{})
	result := p.present(context.Background(), record)
	if result.Outcome != presentDeferred || len(surface.shown) != 0 || len(operator.sent()) != 0 {
		t.Fatalf("locked session must defer without showing or answering: %+v shown=%d sent=%v", result, len(surface.shown), operator.sent())
	}
	if !presenterResultReport(result).Requeue {
		t.Fatal("a deferred question must be presented again after unlock")
	}
}

type unappliedRules struct {
	grants.RuleOperator
	err error
}

func (r unappliedRules) ApplyFirstUseAllowContext(context.Context, string, string) (rightsclient.PolicyEdit, error) {
	return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeConflict}, r.err
}

func TestPresenterCompletesPermissionWithoutSuccessModal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		option  string
		failure bool
		err     error
	}{
		{name: "allow", option: "allow"},
		{name: "refuse", option: "refuse"},
		{name: "rule conflict", option: "allow", failure: true},
		{name: "rule unavailable", option: "allow", failure: true, err: errors.New("rights unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record := pendingFixture()
			operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
			rules := &fakeRules{}
			surface := &fakeSurface{option: tc.option}
			p := testPresenter(unlockedSession(), surface, operator, rules)
			if tc.failure {
				p.rules = func() grants.RuleOperator { return unappliedRules{RuleOperator: rules, err: tc.err} }
			}
			var result presentResult
			var reports []presenterReport
			opened := 0
			ok := presentFresh(context.Background(), []askclient.RecordMetadata{record},
				func(ctx context.Context, current askclient.RecordMetadata) presentResult {
					result = p.present(ctx, current)
					return result
				}, newTrayAnnouncer(), func() { opened++ }, func(report presenterReport) bool {
					reports = append(reports, report)
					return true
				})
			if !ok || len(surface.shown) != 1 || surface.shown[0].Instruction != "Allow opencode (not registered) to look up models?" {
				t.Fatalf("ok=%v prompt shown = %+v", ok, surface.shown)
			}
			if result.Outcome != presentAnswered || result.Answered.Decision.Outcome != askclient.OperatorDecisionOutcomeAnswered {
				t.Fatalf("result = %+v", result)
			}
			if sent := operator.sent(); len(sent) != 1 || sent[0] != "question-1="+tc.option || operator.records[0].Option != tc.option {
				t.Fatalf("answers sent = %v record = %+v", sent, operator.records[0])
			}
			wantEdits := 0
			if tc.option == "allow" && !tc.failure {
				wantEdits = 1
				want := rightsclient.PolicyRule{Subject: rightsclient.Subject{Account: "S-1-5-21-test", Program: record.About}, Action: "abstraction.model/lookup", Resource: "hf", Permit: true}
				if len(rules.edits) != 1 || rules.edits[0] != want {
					t.Fatalf("first-use rule edits = %+v", rules.edits)
				}
			}
			if len(rules.edits) != wantEdits {
				t.Fatalf("rule edits = %+v, want %d", rules.edits, wantEdits)
			}
			if tc.failure {
				if opened != 1 || len(reports) != 1 || !strings.Contains(reports[0].Content, "permission rule was not written") {
					t.Fatalf("failed effect must report and open Questions: opened=%d reports=%+v", opened, reports)
				}
			} else if opened != 0 || len(reports) != 0 {
				t.Fatalf("successful answer produced a second dialog: opened=%d reports=%+v", opened, reports)
			}
		})
	}
}

func TestPresenterLockWhileShownDefersWithoutAnswer(t *testing.T) {
	record := pendingFixture()
	operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
	session := unlockedSession()
	// The lock dismisses the prompt; a quick unlock must not let the
	// person's earlier view be treated as current.
	surface := &fakeSurface{option: "allow", during: func() { session.revision.Add(1) }}
	result := testPresenter(session, surface, operator, &fakeRules{}).present(context.Background(), record)
	if result.Outcome != presentDeferred || len(operator.sent()) != 0 {
		t.Fatalf("interrupted prompt must defer without answering: %+v sent=%v", result, operator.sent())
	}
}

// TestPresenterDefersPromptNotShown: a surface that could not show the prompt
// (secure desktop, screen saver, disconnected session) defers the question
// for the next unlocked poll; it is never treated as closed.
func TestPresenterDefersPromptNotShown(t *testing.T) {
	record := pendingFixture()
	operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
	result := testPresenter(unlockedSession(), &fakeSurface{err: errPromptNotShown}, operator, &fakeRules{}).present(context.Background(), record)
	if result.Outcome != presentDeferred || !presenterResultReport(result).Requeue || len(operator.sent()) != 0 {
		t.Fatalf("an unshown prompt must be deferred and requeued with nothing sent: %+v sent=%v", result, operator.sent())
	}
}

// TestPresentFreshAppliesEachReport runs the tray loop's per-question step
// over a fake presenter and checks its side effects: requeue of the deferred
// question and every later one, the Questions page, and result dialogs.
func TestPresentFreshAppliesEachReport(t *testing.T) {
	record := pendingFixture()
	answered := grants.Answered{Decision: askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeAnswered, Record: &record}}
	ids := []string{"q1", "q2", "q3"}
	cases := []struct {
		name      string
		results   []presentResult
		presented int
		requeued  []string
		opened    int
		shown     int
	}{
		{"all answered", []presentResult{{Outcome: presentAnswered, Answered: answered}, {Outcome: presentAnswered, Answered: answered}, {Outcome: presentAnswered, Answered: answered}}, 3, nil, 0, 0},
		{"second deferred", []presentResult{{Outcome: presentAnswered, Answered: answered}, {Outcome: presentDeferred}}, 2, []string{"q2", "q3"}, 0, 0},
		{"first deferred", []presentResult{{Outcome: presentDeferred}}, 1, []string{"q1", "q2", "q3"}, 0, 0},
		{"failed opens Questions", []presentResult{{Outcome: presentFailed, Err: errors.New("no dialog")}, {Outcome: presentClosed}, {Outcome: presentClosed}}, 3, nil, 1, 0},
		{"stale shows and opens", []presentResult{{Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict}, {Outcome: presentStale, Stale: askclient.OperatorDecisionOutcomeConflict, ClosedElsewhere: true}, {Outcome: presentUnavailable}}, 3, nil, 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			announcer := newTrayAnnouncer()
			var fresh []askclient.RecordMetadata
			for _, id := range ids {
				fresh = append(fresh, askclient.RecordMetadata{ID: id})
			}
			if got := announcer.update(fresh); len(got) != len(ids) {
				t.Fatalf("setup announced %d", len(got))
			}
			presented, opened, shown := 0, 0, 0
			present := func(context.Context, askclient.RecordMetadata) presentResult {
				result := tc.results[presented]
				presented++
				return result
			}
			ok := presentFresh(context.Background(), fresh, present, announcer, func() { opened++ }, func(presenterReport) bool { shown++; return true })
			if !ok || presented != tc.presented || opened != tc.opened || shown != tc.shown {
				t.Fatalf("ok=%v presented=%d opened=%d shown=%d", ok, presented, opened, shown)
			}
			var requeued []string
			for _, record := range announcer.update(fresh) {
				requeued = append(requeued, record.ID)
			}
			if len(requeued) != len(tc.requeued) {
				t.Fatalf("requeued %v, want %v", requeued, tc.requeued)
			}
			for i := range requeued {
				if requeued[i] != tc.requeued[i] {
					t.Fatalf("requeued %v, want %v", requeued, tc.requeued)
				}
			}
		})
	}
}

func TestPresenterRefusesStaleAnswer(t *testing.T) {
	record := pendingFixture()
	answered := record
	answered.Option, answered.Answered = "refuse", "2026-09-30T09:00:30Z"
	changed := record
	changed.Text = record.About + " wants to abstraction.model/lookup on civitai"
	cases := []struct {
		name    string
		current []askclient.RecordMetadata
		want    askclient.OperatorDecisionOutcome
	}{
		{"answered elsewhere", []askclient.RecordMetadata{answered}, askclient.OperatorDecisionOutcomeConflict},
		{"dismissed", nil, askclient.OperatorDecisionOutcomeUnknown},
		{"content changed", []askclient.RecordMetadata{changed}, askclient.OperatorDecisionOutcomeConflict},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
			surface := &fakeSurface{option: "allow", during: func() { operator.set(tc.current...) }}
			result := testPresenter(unlockedSession(), surface, operator, &fakeRules{}).present(context.Background(), record)
			if result.Outcome != presentStale || result.Stale != tc.want || len(operator.sent()) != 0 {
				t.Fatalf("stale answer must be refused with %s and never sent: %+v sent=%v", tc.want, result, operator.sent())
			}
			report := presenterResultReport(result)
			if !report.Show || !report.OpenQuestions || report.Requeue {
				t.Fatalf("report = %+v", report)
			}
		})
	}
}

// TestPresenterAnswerCannotReplayADelegatedAnswer: an HP9 responder answers
// the same option after the presenter's re-read and before its answer
// arrives. The presenter's answer is conditional on the question still being
// pending (ASK-O8): it is conflict, and no permanent rule is written over the
// responder's bounded permit.
func TestPresenterAnswerCannotReplayADelegatedAnswer(t *testing.T) {
	record := pendingFixture()
	operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
	operator.beforeAnswer = func() {
		operator.mu.Lock()
		defer operator.mu.Unlock()
		operator.records[0].Option, operator.records[0].Answered = "allow", "2026-09-30T09:00:45Z"
		operator.beforeAnswer = nil
	}
	rules := &fakeRules{}
	result := testPresenter(unlockedSession(), &fakeSurface{option: "allow"}, operator, rules).present(context.Background(), record)
	if result.Outcome != presentAnswered || result.Answered.Decision.Outcome != askclient.OperatorDecisionOutcomeConflict || len(rules.edits) != 0 {
		t.Fatalf("a delegated answer that landed first must be conflict with no rule written: %+v edits=%+v", result, rules.edits)
	}
	if report := presenterResultReport(result); !report.OpenQuestions || report.Content != trayQuestionOutcomeMessage("conflict") {
		t.Fatalf("report = %+v", report)
	}
}

func TestPresenterUnavailableRereadSendsNothing(t *testing.T) {
	record := pendingFixture()
	operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
	surface := &fakeSurface{option: "refuse", during: func() {
		operator.mu.Lock()
		operator.listErr = errors.New("pipe closed")
		operator.mu.Unlock()
	}}
	result := testPresenter(unlockedSession(), surface, operator, &fakeRules{}).present(context.Background(), record)
	if result.Outcome != presentUnavailable || len(operator.sent()) != 0 {
		t.Fatalf("an unreadable question must not be answered: %+v sent=%v", result, operator.sent())
	}
}

func TestPresenterClosesPromptAnsweredElsewhere(t *testing.T) {
	record := pendingFixture()
	operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
	answered := record
	answered.Option = "allow"
	surface := &fakeSurface{block: true, dismissed: make(chan struct{}), during: func() { operator.set(answered) }}
	p := testPresenter(unlockedSession(), surface, operator, &fakeRules{})
	p.watchEvery = 10 * time.Millisecond
	result := p.present(context.Background(), record)
	if result.Outcome != presentStale || !result.ClosedElsewhere || len(operator.sent()) != 0 {
		t.Fatalf("prompt for a question answered elsewhere must close without an answer: %+v sent=%v", result, operator.sent())
	}
	if report := presenterResultReport(result); report.Show || report.Requeue {
		t.Fatalf("closing for an answer elsewhere needs no second dialog: %+v", report)
	}
}

func TestPresenterClosedPromptSendsNothing(t *testing.T) {
	record := pendingFixture()
	operator := &fakeOperator{records: []askclient.RecordMetadata{record}}
	result := testPresenter(unlockedSession(), &fakeSurface{}, operator, &fakeRules{}).present(context.Background(), record)
	if result.Outcome != presentClosed || len(operator.sent()) != 0 {
		t.Fatalf("closing the prompt is not an answer: %+v sent=%v", result, operator.sent())
	}
}

func TestPresenterReportKeepsRecordedAnswerDistinctFromRule(t *testing.T) {
	rule := rightsclient.PolicyRule{Action: "a", Resource: "r", Permit: true}
	record := pendingFixture()
	answered := grants.Answered{Decision: askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeAnswered, Record: &record}, Rule: &rule,
		Edit: &rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeConflict}}
	report := presenterResultReport(presentResult{Outcome: presentAnswered, Answered: answered})
	if !report.Show || !report.OpenQuestions || report.Content != "Your answer was recorded, but the related permission rule was not written. Review the Questions page before retrying." {
		t.Fatalf("incomplete rule report = %+v", report)
	}
	refused := presenterResultReport(presentResult{Outcome: presentAnswered, Answered: grants.Answered{Decision: askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeForbidden}}})
	if !refused.OpenQuestions || refused.Content != trayQuestionOutcomeMessage("forbidden") {
		t.Fatalf("typed refusal report = %+v", refused)
	}
}

// TestPresenterStopsPresentingAnExpiredQuestion: a question that expires
// (ASK-A7) while its prompt is open closes the prompt with nothing sent, and
// the next poll no longer lists it to present.
func TestPresenterStopsPresentingAnExpiredQuestion(t *testing.T) {
	book, operator := trayTestBook(t)
	if err := book.SetPendingLifetime(time.Second); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id := ask(t, book, "expiring", `C:\apps\one.exe`, "abstraction.model/lookup", "hf")
	records, err := listQuestions(ctx, operator)
	if err != nil || len(records) != 1 || records[0].ID != id {
		t.Fatalf("question before expiry %+v %v", records, err)
	}
	rules := &fakeRules{}
	surface := &fakeSurface{option: "allow", block: true, dismissed: make(chan struct{})}
	presenter := &questionPresenter{
		session:    unlockedSession(),
		surface:    surface,
		operator:   defaultPresenterOperator,
		rules:      func() grants.RuleOperator { return rules },
		account:    probe.Self().Account,
		watchEvery: 50 * time.Millisecond,
	}
	result := presenter.present(ctx, records[0])
	if len(surface.shown) != 1 || result.Outcome != presentStale || !result.ClosedElsewhere || len(rules.edits) != 0 {
		t.Fatalf("an expiring prompt must close with nothing sent: %+v shown=%d edits=%+v", result, len(surface.shown), rules.edits)
	}
	if after, err := listQuestions(ctx, operator); err != nil || len(after) != 0 {
		t.Fatalf("an expired question must not be listed for presenting: %+v %v", after, err)
	}
	if _, found, err := book.ApplicationRecord(id); err != nil || found {
		t.Fatalf("the expired question must not be retained: %v %v", found, err)
	}
}

// TestPresenterAnswersThroughRuntimeOperator runs the presenter against an
// isolated runtime whose asks operator admits only this test process by its
// bound peer identity (trayTestBook). The answer reaches the Book through the
// public operator API; a question answered elsewhere while shown keeps the
// other answer and the presenter sends nothing (ASK-O6).
func TestPresenterAnswersThroughRuntimeOperator(t *testing.T) {
	book, operator := trayTestBook(t)
	ctx := context.Background()
	first := ask(t, book, "live-1", `C:\apps\one.exe`, "abstraction.model/lookup", "hf")
	second := ask(t, book, "live-2", `C:\apps\two.exe`, "abstraction.model/lookup", "hf")
	records, err := listQuestions(ctx, operator)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]askclient.RecordMetadata{}
	for _, record := range records {
		byID[record.ID] = record
	}
	rules := &fakeRules{}
	presenter := &questionPresenter{
		session:  unlockedSession(),
		surface:  &fakeSurface{option: "refuse"},
		operator: defaultPresenterOperator,
		rules:    func() grants.RuleOperator { return rules },
		account:  probe.Self().Account,
	}
	result := presenter.present(ctx, byID[first])
	if result.Outcome != presentAnswered || result.Answered.Decision.Outcome != askclient.OperatorDecisionOutcomeAnswered || len(rules.edits) != 0 {
		t.Fatalf("refuse through the runtime operator = %+v edits=%+v", result, rules.edits)
	}
	presenter.surface = &fakeSurface{option: "refuse", during: func() {
		if decision, err := operator.AnswerQuestionContext(ctx, second, "allow"); err != nil || decision.Outcome != askclient.OperatorDecisionOutcomeAnswered {
			t.Errorf("answer elsewhere: %+v %v", decision, err)
		}
	}}
	stale := presenter.present(ctx, byID[second])
	if stale.Outcome != presentStale || stale.Stale != askclient.OperatorDecisionOutcomeConflict {
		t.Fatalf("stale prompt = %+v", stale)
	}
	after, err := listQuestions(ctx, operator)
	if err != nil {
		t.Fatal(err)
	}
	for _, record := range after {
		switch record.ID {
		case first:
			if record.Option != "refuse" {
				t.Fatalf("first answer = %q", record.Option)
			}
		case second:
			if record.Option != "allow" {
				t.Fatalf("the answer given elsewhere must stand, got %q", record.Option)
			}
		}
	}
}
