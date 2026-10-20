package main

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstraction-identity/listen"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

type fakePermissionOperator struct {
	mu           sync.Mutex
	details      askclient.TypedPermissionDetails
	err          error
	answers      []string
	beforeAnswer func()
	list         func() ([]askclient.RecordMetadata, error)
}

func (o *fakePermissionOperator) ListAllPermissionsContext(ctx context.Context) ([]askclient.RecordMetadata, error) {
	if o.list != nil {
		return o.list()
	}
	page, err := o.ListPermissionsContext(ctx, "", 64)
	if err != nil {
		return nil, err
	}
	return page.Records, nil
}

func (o *fakePermissionOperator) ReadPermissionDetailsContext(context.Context, string) (askclient.TypedPermissionDetails, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return askclient.ClonePermissionDetails(o.details), o.err
}

func (o *fakePermissionOperator) ListPermissionsContext(_ context.Context, cursor string, _ int64) (askclient.OperatorPage, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return askclient.OperatorPage{Outcome: askclient.OperatorPageOutcomePage, Records: []askclient.RecordMetadata{*o.details.Question.Question.Record}, Complete: true}, nil
}

func (o *fakePermissionOperator) AnswerPermissionIfPendingContext(_ context.Context, id, option string) (askclient.OperatorDecision, error) {
	if o.beforeAnswer != nil {
		o.beforeAnswer()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.answers = append(o.answers, id+":"+option)
	r := o.details.Question.Question.Record
	if r.Option != "" {
		return askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeConflict}, nil
	}
	record := *r
	record.Option = option
	record.Yes = option == "allow"
	record.Answered = time.Now().UTC().Format(time.RFC3339Nano)
	o.details.Question.Question.Record = &record
	answerer := askclient.AnswererOperator
	o.details.Question.Question.AnsweredBy = &answerer
	return askclient.OperatorDecision{Outcome: askclient.OperatorDecisionOutcomeAnswered, Record: &record}, nil
}

type fakePermissionRules struct {
	applied []string
	undone  []string
	outcome rightsclient.PolicyEditOutcome
	err     error
	// onApply runs as the owner's save attempt, as when serve releases an
	// Allow it could not save.
	onApply func()
}

// release is serve's handling of an Allow it could not save: the question
// is gone, and the app's next request asks afresh.
func (o *fakePermissionOperator) release() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.details = askclient.TypedPermissionDetails{Question: askclient.AttributedQuestion{Question: askclient.OperatorQuestion{Outcome: askclient.OperatorQuestionOutcomeUnknown}}}
}

func (r *fakePermissionRules) UndoPermissionEffectContext(_ context.Context, expected, id string) (rightsclient.SelectorEdit, error) {
	r.undone = append(r.undone, expected+":"+id)
	return rightsclient.SelectorEdit{Outcome: rightsclient.PolicyEditOutcomeApplied, Revision: "undo-revision"}, nil
}

func (*fakePermissionRules) ListSelectorRulesContext(context.Context, string, int64) (rightsclient.SelectorRulePage, error) {
	return rightsclient.SelectorRulePage{Outcome: rightsclient.PolicyPageOutcomePage, Revision: "permission-revision"}, nil
}

func (r *fakePermissionRules) ApplyPermissionAllowContext(_ context.Context, expected, id string) (rightsclient.SelectorEdit, error) {
	r.applied = append(r.applied, expected+":"+id)
	if r.onApply != nil {
		r.onApply()
	}
	if r.err != nil {
		return rightsclient.SelectorEdit{}, r.err
	}
	outcome := r.outcome
	if outcome == 0 {
		outcome = rightsclient.PolicyEditOutcomeApplied
	}
	return rightsclient.SelectorEdit{Outcome: outcome, Revision: "applied-revision"}, nil
}

func typedNativeFixture(t *testing.T, all, bundle bool) (askclient.RecordMetadata, askclient.TypedPermissionDetails) {
	t.Helper()
	record := pendingFixture()
	record.Key = asks.TypedPermissionKey
	record.Text = "OA permission request"
	record.Options = []string{"allow", "refuse"}
	selector := rightsclient.ResourceSelector{Kind: rightsclient.ResourceSelectorKindOne, Target: []byte("owning-alpha-document")}
	if all {
		selector = rightsclient.ResourceSelector{Kind: rightsclient.ResourceSelectorKindAll}
	}
	operations := []string{"Use"}
	if bundle {
		operations = []string{"Use", "Read secret value", "Read metadata", "Create", "Rotate", "Revoke"}
	}
	effect := rightsclient.PermissionEffect{Subject: rightsclient.Subject{Account: "S-1-5-21-test", Program: record.About}}
	display := askclient.PermissionDisplay{}
	for index, operation := range operations {
		selected := selector
		selected.Target = slices.Clone(selector.Target)
		effect.Selections = append(effect.Selections, rightsclient.PermissionSelection{Permission: rightsclient.PermissionKey{
			Action: "abstraction.credentials/operation." + string(rune('a'+index)), ResourceProfile: "abstraction.credentials/saved-secret@1"}, Selector: selected})
		disclosure := ""
		if operation == "Use" {
			disclosure = "Uses the secret through its designated service."
		}
		if operation == "Read secret value" {
			disclosure = "Exports the raw secret value to this app. The app can copy it and use it elsewhere."
		}
		display.Selections = append(display.Selections, askclient.PermissionDisplaySelection{Operation: operation,
			Target: "Saved app secret “alpha”", Collection: "saved app secrets", AllEffect: "Includes every current and future saved app secret. Runtime-managed keys are excluded.", Disclosure: disclosure})
	}
	if bundle {
		effect.Bundle = &rightsclient.BundleExpansion{BundleRef: "abstraction.credentials/full-control@1", DefinitionDigest: "sha256:" + strings.Repeat("a", 64), Selector: selector, Selections: slices.Clone(effect.Selections)}
		display.BundleLabel = "Full control"
		display.BundleDisclosure = "Includes these six operations. Later operations require new consent."
	}
	details := askclient.TypedPermissionDetails{Question: askclient.AttributedQuestion{Question: askclient.OperatorQuestion{Outcome: askclient.OperatorQuestionOutcomeFound,
		Record: &record, PublisherTrusted: true, Publisher: "Recorded Publisher", ProgramDescription: "Recorded App"}}, Effect: &effect,
		EffectDigest: asks.PermissionEffectDigest(effect), Origin: "application", Purpose: "App-supplied purpose claims administrator access and a different secret.", Display: &display}
	if !asks.ValidTypedPermission(asks.TypedPermission{Effect: effect, Digest: details.EffectDigest, Origin: details.Origin, Purpose: details.Purpose, Display: display}) {
		t.Fatal("invalid typed native fixture")
	}
	return record, details
}

func nativeTypedPresenter(session presenterSession, surface presenterSurface, operator *fakePermissionOperator, rules *fakePermissionRules) *questionPresenter {
	return &questionPresenter{session: session, surface: surface, account: "S-1-5-21-test",
		permissions:     func(context.Context) (presenterPermissionOperator, error) { return operator, nil },
		permissionRules: func() grants.PermissionRuleOperator { return rules }, budget: time.Second}
}

func TestTypedNativePermissionKeepsOneAllAndFiniteOperationsVisible(t *testing.T) {
	for _, all := range []bool{false, true} {
		record, details := typedNativeFixture(t, all, true)
		prompt := trayTypedPermissionView(record, details, nil)
		if selected, err := selectNativeQuestionPresentation(prompt); err != nil || !selected.consent {
			t.Fatalf("typed producer missed Asks consent policy: %v", err)
		}
		view := prompt.Permission
		if view == nil || prompt.Default != "refuse" || len(prompt.Choices) != 2 || !strings.Contains(view.Requester, "Recorded App") {
			t.Fatalf("typed presentation=%+v", prompt)
		}
		visible := visibleText(prompt) + "\n" + view.Scope + "\n" + view.Effects
		for _, word := range []string{"Full control", "Use", "Read secret value", "Read metadata", "Create", "Rotate", "Revoke", "Exports the raw secret value to this app", "copy it and use it elsewhere", "six operations"} {
			if !strings.Contains(prompt.Details, word) {
				t.Fatalf("missing %q in details: %s", word, prompt.Details)
			}
		}
		if strings.Contains(visible, record.Text) || strings.Contains(visible, "administrator") || strings.Contains(visible, details.Purpose) {
			t.Fatalf("caller prose became trusted permission text: %s", visible)
		}
		if all {
			if prompt.Instruction != "Allow Recorded App to have full control of all saved app secrets?" || !strings.Contains(prompt.Details, "current and future") || !strings.Contains(prompt.Details, "Runtime-managed keys are excluded") || !strings.Contains(prompt.Details, "All: saved app secrets") {
				t.Fatalf("all scope lost: %+v", prompt)
			}
		} else if prompt.Instruction != "Allow Recorded App to have full control of saved app secret “alpha”?" || !strings.Contains(prompt.Details, "One: Saved app secret “alpha”") || strings.Contains(prompt.Details, "All effect:") {
			t.Fatalf("one acquired broad scope: %+v", prompt)
		}
		if prompt.Content != "" || view.Scope != "" || view.Publisher != "" {
			t.Fatalf("inline lines: %+v", prompt)
		}
		for _, exact := range []string{record.About, details.Effect.Subject.Account, "Resource profile:", "Selector:", "Bundle version: " + details.Effect.Bundle.BundleRef, "Purpose supplied by the requester:", "Lifetime: Ongoing, until removed."} {
			if !strings.Contains(prompt.Details, exact) {
				t.Fatalf("exact detail %q lost: %s", exact, prompt.Details)
			}
		}
		if strings.Contains(prompt.Details, details.EffectDigest) || strings.Contains(prompt.Details, details.Effect.Bundle.DefinitionDigest) || strings.Contains(prompt.Details, "base64") {
			t.Fatalf("diagnostic bytes leaked into consent details: %s", prompt.Details)
		}
	}
}

func TestTypedNativeSpecificSecretRawReadIsDistinctFromUse(t *testing.T) {
	record, details := typedNativeFixture(t, false, true)
	details.Effect.Selections = slices.Clone(details.Effect.Selections[1:2])
	details.Effect.Bundle = nil
	details.Display.Selections = slices.Clone(details.Display.Selections[1:2])
	details.Display.BundleLabel, details.Display.BundleDisclosure = "", ""
	details.EffectDigest = asks.PermissionEffectDigest(*details.Effect)
	operator := &fakePermissionOperator{details: details}
	surface := &fakeSurface{option: "refuse"}
	result := nativeTypedPresenter(unlockedSession(), surface, operator, &fakePermissionRules{}).present(context.Background(), record)
	if result.Outcome != presentAnswered || result.Err != nil {
		t.Fatalf("specific Read could not be presented: %+v", result)
	}
	read := trayTypedPermissionView(record, details, nil)
	useRecord, useDetails := typedNativeFixture(t, false, false)
	use := trayTypedPermissionView(useRecord, useDetails, nil)
	if !strings.Contains(read.Details, "One: Saved app secret “alpha”") || !strings.Contains(read.Details, "Read secret value") || !strings.Contains(read.Details, "copy it and use it elsewhere") || strings.Contains(read.Details, "Uses the secret through") {
		t.Fatalf("specific raw Read lost its distinct disclosure: %+v", read)
	}
	if !strings.Contains(use.Details, "Uses the secret through") || strings.Contains(use.Details, "Exports the raw secret") || use.Instruction != "Allow Recorded App to use saved app secret “alpha”?" {
		t.Fatalf("Use acquired raw Read meaning: %+v", use)
	}
}

func TestTypedNativeApplicationShowsEveryGrantedOperation(t *testing.T) {
	for _, class := range []string{"read", "edit"} {
		record, details := typedNativeFixture(t, false, false)
		p, err := grants.ApplicationPermission(details.Effect.Subject, "app:comfyui", "ComfyUI", class)
		if err != nil {
			t.Fatal(err)
		}
		details.Effect, details.Display, details.EffectDigest, details.Origin, details.Purpose = &p.Effect, &p.Display, p.Digest, p.Origin, p.Purpose
		view := trayTypedPermissionView(record, details, nil)
		if view.Instruction != "Allow Recorded App to use ComfyUI?" {
			t.Fatal(view.Instruction)
		}
		for _, words := range p.Display.Selections {
			if !strings.Contains(view.Details, words.Operation) || !strings.Contains(view.Details, words.Disclosure) {
				t.Fatal("hidden granted operation", words, view)
			}
		}
		if class == "edit" && (!strings.Contains(view.Details, "Spend money within the application's limit") || !strings.Contains(view.Details, "Only the person")) {
			t.Fatal(view)
		}
		operator := &fakePermissionOperator{details: details}
		rules := &fakePermissionRules{}
		result := nativeTypedPresenter(unlockedSession(), &fakeSurface{option: "allow"}, operator, rules).present(context.Background(), record)
		if result.Outcome != presentAnswered || result.Err != nil || len(rules.applied) != 1 {
			t.Fatal(result, rules.applied)
		}
	}
}

func TestTypedApplicationAnswerHistoryAndByIDUndo(t *testing.T) {
	record, details := typedNativeFixture(t, false, false)
	p, err := grants.ApplicationPermission(details.Effect.Subject, "app:comfyui", "ComfyUI", "read")
	if err != nil {
		t.Fatal(err)
	}
	details.Effect, details.Display, details.EffectDigest, details.Origin, details.Purpose = &p.Effect, &p.Display, p.Digest, p.Origin, p.Purpose
	record.Option, record.Yes, record.Answered = "allow", true, time.Now().UTC().Format(time.RFC3339Nano)
	details.Question.Question.Record = &record
	actor := askclient.AnswererResponder
	details.Question.Question.AnsweredBy = &actor
	details.Question.Answerer = p.Effect.Subject.Program + ".assistant.exe"
	entry, targets, ok := answeredApplicationRecord(details, nil)
	if !ok || len(targets) != 3 || entry.Answerer != details.Question.Answerer {
		t.Fatal(entry, targets, ok)
	}
	for _, word := range p.Display.Selections {
		if !strings.Contains(entry.Sentence, word.Operation) || !strings.Contains(entry.Sentence, word.Target) {
			t.Fatal(entry)
		}
	}
	rules := &fakePermissionRules{}
	questions := &fakePermissionOperator{details: details}
	edit, err := grants.UndoPermissionQuestion(t.Context(), questions, rules, p.Effect.Subject.Account, record.ID)
	if err != nil || edit.Outcome != rightsclient.PolicyEditOutcomeApplied || len(rules.undone) != 1 || rules.undone[0] != "permission-revision:"+record.ID {
		t.Fatal(edit, err, rules.undone)
	}
	standing, err := grants.ApplicationPermission(p.Effect.Subject, "app:comfyui", "ComfyUI", "edit")
	if err != nil {
		t.Fatal(err)
	}
	details.Effect, details.Display, details.EffectDigest = &standing.Effect, &standing.Display, standing.Digest
	if _, _, ok := answeredApplicationRecord(details, nil); ok {
		t.Fatal("standing assistant answer accepted")
	}
}

func TestTypedNativeAnswerUsesOnlyTypedRouteAndOccurrenceID(t *testing.T) {
	for _, option := range []string{"allow", "refuse"} {
		record, details := typedNativeFixture(t, true, true)
		operator := &fakePermissionOperator{details: details}
		rules := &fakePermissionRules{}
		presenter := nativeTypedPresenter(unlockedSession(), &fakeSurface{option: option}, operator, rules)
		presenter.operator = func(context.Context) (presenterOperator, error) {
			t.Fatal("typed request used legacy operator")
			return nil, errors.New("legacy")
		}
		result := presenter.present(context.Background(), record)
		if result.Outcome != presentAnswered || result.TypedAnswered == nil || result.TypedAnswered.Decision.Outcome != askclient.OperatorDecisionOutcomeAnswered || len(operator.answers) != 1 {
			t.Fatalf("typed answer=%+v calls=%v", result, operator.answers)
		}
		if option == "allow" && (len(rules.applied) != 1 || rules.applied[0] != "permission-revision:"+record.ID) || option == "refuse" && len(rules.applied) != 0 {
			t.Fatalf("effect calls=%v", rules.applied)
		}
		if report := presenterResultReport(result); report.OpenQuestions {
			t.Fatalf("successful typed answer produced second popup: %+v", report)
		}
	}
}

func TestTypedNativeReportsIncompleteEffectAfterAllow(t *testing.T) {
	record, details := typedNativeFixture(t, true, true)
	operator := &fakePermissionOperator{details: details}
	rules := &fakePermissionRules{outcome: rightsclient.PolicyEditOutcomeConflict}
	result := nativeTypedPresenter(unlockedSession(), &fakeSurface{option: "allow"}, operator, rules).present(context.Background(), record)
	if result.TypedAnswered == nil || result.TypedAnswered.Decision.Outcome != askclient.OperatorDecisionOutcomeAnswered || len(operator.answers) != 1 || len(rules.applied) != 1 {
		t.Fatalf("incomplete recorded Allow=%+v", result)
	}
	report := presenterResultReport(result)
	if report.OpenQuestions || report.Notice == nil || report.Notice.Title != "Couldn't save that permission" {
		t.Fatalf("incomplete effect claimed success: %+v", report)
	}
}

func TestTypedNativeLogsOnlyIncompleteAllowEffects(t *testing.T) {
	own(t)
	panelLoggingRuntime(t, nil)
	p := newTestPanel(t)
	pending := make(chan map[string]string, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.log.writePermissionCompletions(t.Context(), pending)
	}()
	var expected []map[string]string
	for i, c := range []struct {
		option, outcome, notice string
		edit                    rightsclient.PolicyEditOutcome
		err                     error
	}{
		{option: "allow", outcome: "conflict", notice: "permission rule was not written", edit: rightsclient.PolicyEditOutcomeConflict},
		{option: "allow", outcome: "unavailable", notice: "permission change could not be confirmed", edit: rightsclient.PolicyEditOutcomeUnavailable},
		{option: "allow", outcome: "unconfirmed", notice: "permission change could not be confirmed", err: errors.New("private fixture transport detail")},
		{option: "allow"},
		{option: "refuse"},
		{option: ""},
	} {
		record, details := typedNativeFixture(t, false, false)
		record.ID = strings.Repeat(string(rune('a'+i)), 32)
		details.Question.Question.Record = &record
		operator := &fakePermissionOperator{details: details}
		rules := &fakePermissionRules{outcome: c.edit, err: c.err}
		presenter := nativeTypedPresenter(unlockedSession(), &fakeSurface{option: c.option}, operator, rules)
		presenter.permissionFailures = pending
		result := presenter.present(t.Context(), record)
		report := presenterResultReport(result)
		if c.outcome == "" {
			if report.OpenQuestions {
				t.Fatal("ordinary completion opened a second surface", report)
			}
			continue
		}
		if report.OpenQuestions || (c.outcome == "conflict") != (report.Notice != nil) {
			t.Fatal("incomplete Allow report", c.notice, report)
		}
		attrs := map[string]string{"permission.stage": "effect", "permission.outcome": c.outcome, "question.id": record.ID}
		if c.err != nil {
			// The class only: the fixture's private text never reaches Logging.
			attrs["permission.error"] = "other"
		}
		expected = append(expected, attrs)
	}
	close(pending)
	<-done
	r := panelRequest(t, p.handler("test-key"), "/logging?cursor=", nil)
	page := decodePanel[logView](t, r.Code, r.Body.Bytes())
	if page.Outcome != "page" || len(page.Records) != len(expected) {
		t.Fatal("unexpected permission records", page)
	}
	for i, record := range page.Records {
		if record.Message != "native permission completion" || !maps.Equal(record.Attrs, expected[i]) {
			t.Fatal("permission completion logged unexpected attributes", record)
		}
	}
}

func TestTypedNativeReportsBeforeBlockedLogging(t *testing.T) {
	own(t)
	panelLoggingRuntime(t, nil)
	var sink panelLog
	sink.mu.Lock() // Hold the actual lazy bind before it can resolve Logging.
	ctx, cancel := context.WithCancel(t.Context())
	pending := make(chan map[string]string, 1)
	pending <- map[string]string{"permission.stage": "effect", "permission.outcome": "unavailable"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		sink.writePermissionCompletions(ctx, pending)
	}()
	defer func() {
		cancel()
		sink.mu.Unlock()
		<-done
	}()
	deadline := time.NewTimer(time.Second)
	defer deadline.Stop()
	ticks := time.NewTicker(time.Millisecond)
	defer ticks.Stop()
	for len(pending) != 0 {
		select {
		case <-ticks.C:
		case <-deadline.C:
			t.Fatal("Logging worker did not reach the blocked bind")
		}
	}
	pending <- map[string]string{"permission.stage": "effect", "permission.outcome": "unconfirmed"}

	record, details := typedNativeFixture(t, false, false)
	presenter := nativeTypedPresenter(unlockedSession(), &fakeSurface{option: "allow"},
		&fakePermissionOperator{details: details}, &fakePermissionRules{outcome: rightsclient.PolicyEditOutcomeConflict})
	presenter.permissionFailures = pending
	notices := make(chan presenterNotice, 1)
	saved := postPresenterNotice
	postPresenterNotice = func(_ context.Context, n presenterNotice) error { notices <- n; return nil }
	t.Cleanup(func() { postPresenterNotice = saved })
	go presentFresh(ctx, []askclient.RecordMetadata{record}, presenter.present, newTrayAnnouncer(),
		func() {})
	select {
	case notice := <-notices:
		if notice.Title != "Couldn't save that permission" {
			t.Fatal("blocked Logging changed the notice", notice)
		}
	case <-time.After(time.Second):
		t.Fatal("full diagnostic queue blocked the existing failure notice")
	}
}

func TestTypedNativeUnavailableCompletionDoesNotClaimNoWrite(t *testing.T) {
	record, details := typedNativeFixture(t, false, false)
	operator := &fakePermissionOperator{details: details}
	rules := &fakePermissionRules{outcome: rightsclient.PolicyEditOutcomeUnavailable}
	result := nativeTypedPresenter(unlockedSession(), &fakeSurface{option: "allow"}, operator, rules).present(t.Context(), record)
	if result.TypedAnswered == nil || len(rules.applied) != 1 {
		t.Fatal(result, rules.applied)
	}
	for _, lostReply := range []bool{false, true} {
		if lostReply {
			result.Err = context.DeadlineExceeded
			result.TypedAnswered.Edit = nil
		}
		// An unconfirmed change claims nothing: no "not saved" notice.
		report := presenterResultReport(result)
		if report.OpenQuestions || report.Notice != nil {
			t.Fatal("uncertain completion claimed no durable write", report)
		}
	}
	legacy := presenterResultReport(presentResult{Outcome: presentAnswered, Answered: grants.Answered{Decision: result.TypedAnswered.Decision, Rule: &rightsclient.PolicyRule{}, Edit: &rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeUnavailable}}})
	if legacy.Notice != nil {
		t.Fatal("shared report claimed an unavailable completion was not written", legacy)
	}
}

func TestTypedNativePrivilegeWarningUsesOnlyTrustedDisplay(t *testing.T) {
	record, details := typedNativeFixture(t, false, false)
	details.Question.Question.PublisherTrusted = false
	details.Question.Question.Publisher = ""
	if prompt := trayTypedPermissionView(record, details, nil); strings.Contains(prompt.Content, "Windows asks next.") {
		t.Fatalf("unknown publisher implied privilege: %+v", prompt)
	}
	details.Display.RequiresOSAuthorization = true
	prompt := trayTypedPermissionView(record, details, nil)
	if prompt.Content != "Publisher not verified.\nWindows asks next." || strings.Contains(prompt.Content, "current and future") {
		t.Fatalf("trusted OS authorization mixed with breadth: %+v", prompt)
	}
}

func TestTypedNativeRefusesChangedWholeOccurrence(t *testing.T) {
	for _, changed := range []string{"target", "profile", "bundle", "display", "breadth", "publisher", "purpose", "digest", "options"} {
		t.Run(changed, func(t *testing.T) {
			record, details := typedNativeFixture(t, false, true)
			operator := &fakePermissionOperator{details: details}
			rules := &fakePermissionRules{}
			surface := &fakeSurface{option: "allow", during: func() {
				operator.mu.Lock()
				defer operator.mu.Unlock()
				d := &operator.details
				switch changed {
				case "target":
					d.Effect.Selections[0].Selector.Target[0] = 'X'
				case "profile":
					d.Effect.Selections[0].Permission.ResourceProfile = "another/profile@1"
					d.Effect.Bundle.Selections[0] = d.Effect.Selections[0]
				case "bundle":
					d.Effect.Bundle.DefinitionDigest = "sha256:" + strings.Repeat("b", 64)
				case "display":
					d.Display.Selections[0].Operation = "Different operation"
				case "breadth":
					for index := range d.Effect.Selections {
						d.Effect.Selections[index].Selector = rightsclient.ResourceSelector{Kind: rightsclient.ResourceSelectorKindAll}
						d.Effect.Bundle.Selections[index] = d.Effect.Selections[index]
					}
					d.Effect.Bundle.Selector = rightsclient.ResourceSelector{Kind: rightsclient.ResourceSelectorKindAll}
				case "publisher":
					d.Question.Question.Publisher = "Different Publisher"
				case "purpose":
					d.Purpose = "Different requester purpose"
				case "options":
					d.Question.Question.Record.Options[0] = "different"
				}
				if changed == "digest" {
					d.EffectDigest = strings.Repeat("0", 64)
				} else {
					d.EffectDigest = asks.PermissionEffectDigest(*d.Effect)
				}
			}}
			result := nativeTypedPresenter(unlockedSession(), surface, operator, rules).present(context.Background(), record)
			if result.Outcome != presentStale && result.Outcome != presentUnavailable || len(operator.answers) != 0 || len(rules.applied) != 0 {
				t.Fatalf("changed occurrence answered: %+v calls=%v", result, operator.answers)
			}
		})
	}
}

func TestTypedNativeDismissesChangedDisplayAndDefersAcrossLock(t *testing.T) {
	for _, mode := range []string{"display", "lock"} {
		record, details := typedNativeFixture(t, true, true)
		operator := &fakePermissionOperator{details: details}
		session := unlockedSession()
		surface := &fakeSurface{option: "allow", dismissed: make(chan struct{}), block: mode == "display", during: func() {
			if mode == "lock" {
				session.revision.Add(1)
				return
			}
			operator.mu.Lock()
			defer operator.mu.Unlock()
			operator.details.Display.Selections[0].AllEffect = "A changed collection effect."
		}}
		presenter := nativeTypedPresenter(session, surface, operator, &fakePermissionRules{})
		presenter.watchEvery = time.Millisecond
		result := presenter.present(context.Background(), record)
		want := presentStale
		if mode == "lock" {
			want = presentDeferred
		}
		if result.Outcome != want || len(operator.answers) != 0 {
			t.Fatalf("%s interruption=%+v", mode, result)
		}
	}
}

func TestTypedNativeRechecksSessionAfterAnswerResolution(t *testing.T) {
	record, details := typedNativeFixture(t, true, true)
	operator := &fakePermissionOperator{details: details}
	session := unlockedSession()
	presenter := nativeTypedPresenter(session, &fakeSurface{option: "allow"}, operator, &fakePermissionRules{})
	calls := 0
	presenter.permissions = func(context.Context) (presenterPermissionOperator, error) {
		calls++
		if calls == 3 {
			session.revision.Add(1)
		}
		return operator, nil
	}
	result := presenter.present(context.Background(), record)
	if result.Outcome != presentDeferred || len(operator.answers) != 0 {
		t.Fatalf("session changed during final operator resolution: %+v calls=%v", result, operator.answers)
	}
}

// The client's ListAll owns the ASK-O2 paging cases (paging_test.go); the
// presenter turns its refusal into its own and refuses a foreign record.
func TestTypedPermissionListingRefusesStallAndForeignRecords(t *testing.T) {
	record, _ := typedNativeFixture(t, true, false)
	operator := &fakePermissionOperator{list: func() ([]askclient.RecordMetadata, error) {
		return []askclient.RecordMetadata{record}, nil
	}}
	if listed, err := listPermissionQuestions(context.Background(), operator); err != nil || len(listed) != 1 {
		t.Fatalf("listing=%v %v", listed, err)
	}
	operator.list = func() ([]askclient.RecordMetadata, error) {
		return nil, &askclient.PageRefusal{Outcome: "stalled"}
	}
	var refusal *presenterRefusal
	if listed, err := listPermissionQuestions(context.Background(), operator); listed != nil || !errors.As(err, &refusal) || refusal.outcome != "stalled" {
		t.Fatalf("repeated cursor announced=%v %v", listed, err)
	}
	foreign := record
	foreign.Key = "another-key"
	operator.list = func() ([]askclient.RecordMetadata, error) {
		return []askclient.RecordMetadata{foreign}, nil
	}
	if listed, err := listPermissionQuestions(context.Background(), operator); listed != nil || !errors.As(err, &refusal) || refusal.outcome != "foreign permission record" {
		t.Fatalf("foreign record announced=%v %v", listed, err)
	}
}

func TestTypedNativePollAnswersActualBookWhilePanelClosed(t *testing.T) {
	book, legacy := trayTestBook(t)
	_, details := typedNativeFixture(t, true, true)
	details.Effect.Subject.Account = probe.Self().Account
	permission := asks.TypedPermission{Effect: *details.Effect, Digest: asks.PermissionEffectDigest(*details.Effect),
		Origin: details.Origin, Purpose: details.Purpose, Display: *details.Display}
	record, outcome, err := book.AskTypedPermissionFrom("native-typed-fixture", "fixture-service", "one-frozen-full-control",
		permission, listen.Seen{Why: "native typed fixture"}, asks.Publisher{Subject: "Recorded Publisher", Trusted: true, Description: "Recorded App"})
	if err != nil || outcome != "pending" {
		t.Fatalf("typed admission=%s %v", outcome, err)
	}
	oldPage, err := legacy.ListQuestionsContext(context.Background(), "", 64)
	if err != nil || len(oldPage.Records) != 0 {
		t.Fatalf("typed entry reached old operator: %+v %v", oldPage, err)
	}
	if _, err := defaultPresenterPermissions(context.Background()); err != nil {
		t.Fatalf("typed native operator resolution: %v", err)
	}
	announcer := newTrayAnnouncer()
	fresh, _, unavailable := pollTray(context.Background(), panelMachine(), announcer)
	if unavailable || len(fresh) != 1 || fresh[0].ID != record.ID {
		t.Fatalf("native closed-Panel discovery: %+v unavailable=%t", fresh, unavailable)
	}
	bridge, frames := nativeBridgeFixture(t)
	bridge.session(true)
	presenter := &questionPresenter{session: bridge, surface: bridge, account: probe.Self().Account, budget: time.Second}
	done := make(chan presentResult, 1)
	go func() { done <- presenter.present(context.Background(), fresh[0]) }()
	frame := nextNativeFrame(t, frames, "question")
	if frame.Prompt == nil || !strings.Contains(frame.Prompt.Details, "Exports the raw secret value to this app") || !strings.Contains(frame.Prompt.Instruction, "full control") {
		t.Fatalf("typed native frame=%+v", frame)
	}
	if err := bridge.accept(nativeAsksReply{Kind: "answer", ID: frame.ID, Option: "refuse"}, nil); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Outcome != presentAnswered || result.TypedAnswered == nil || result.TypedAnswered.Decision.Outcome != askclient.OperatorDecisionOutcomeAnswered {
		t.Fatalf("public typed answer=%+v", result)
	}
	if report := presenterResultReport(result); report.OpenQuestions || report.Notice != nil {
		t.Fatalf("typed success displayed a second report: %+v", report)
	}
	read, err := defaultPresenterPermissions(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	current, err := read.ReadPermissionDetailsContext(context.Background(), record.ID)
	if err != nil || current.Question.Question.Record == nil || current.Question.Question.Record.Option != "refuse" {
		t.Fatalf("native typed decision absent: %+v %v", current, err)
	}
	if fresh, _, unavailable = pollTray(context.Background(), panelMachine(), announcer); unavailable || len(fresh) != 0 {
		t.Fatalf("typed decision announced twice: %+v unavailable=%t", fresh, unavailable)
	}
}
