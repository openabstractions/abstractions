package main

import (
	"context"
	"errors"
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
	pages        func(string) (askclient.OperatorPage, error)
}

func (o *fakePermissionOperator) ReadPermissionDetailsContext(context.Context, string) (askclient.TypedPermissionDetails, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return askclient.ClonePermissionDetails(o.details), o.err
}

func (o *fakePermissionOperator) ListPermissionsContext(_ context.Context, cursor string, _ int64) (askclient.OperatorPage, error) {
	if o.pages != nil {
		return o.pages(cursor)
	}
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
	outcome rightsclient.PolicyEditOutcome
}

func (*fakePermissionRules) ListSelectorRulesContext(context.Context, string, int64) (rightsclient.SelectorRulePage, error) {
	return rightsclient.SelectorRulePage{Outcome: rightsclient.PolicyPageOutcomePage, Revision: "permission-revision"}, nil
}

func (r *fakePermissionRules) ApplyPermissionAllowContext(_ context.Context, expected, id string) (rightsclient.SelectorEdit, error) {
	r.applied = append(r.applied, expected+":"+id)
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
		view := prompt.Permission
		if view == nil || prompt.Default != "refuse" || len(prompt.Choices) != 2 || !strings.Contains(view.Requester, "Recorded App") {
			t.Fatalf("typed presentation=%+v", prompt)
		}
		visible := visibleText(prompt) + "\n" + view.Scope + "\n" + view.Effects
		for _, word := range []string{"Full control", "Use", "Read secret value", "Read metadata", "Create", "Rotate", "Revoke", "Exports the raw secret value to this app", "copy it and use it elsewhere", "six operations"} {
			if !strings.Contains(visible, word) {
				t.Fatalf("missing visible %q: %s", word, visible)
			}
		}
		if strings.Contains(visible, record.Text) || strings.Contains(visible, "administrator") || strings.Contains(visible, details.Purpose) {
			t.Fatalf("caller prose became trusted permission text: %s", visible)
		}
		if all {
			if prompt.Warning == "" || view.Warning == nil || !strings.Contains(*view.Warning, "current and future") || !strings.Contains(*view.Warning, "Runtime-managed keys are excluded") || !strings.Contains(view.Scope, "All: saved app secrets") {
				t.Fatalf("all scope warning lost: %+v", prompt)
			}
		} else if prompt.Warning != "" || !strings.Contains(view.Scope, "One: Saved app secret “alpha”") || strings.Contains(prompt.Details, "All effect:") {
			t.Fatalf("one acquired broad scope: %+v", prompt)
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
	if !strings.Contains(read.Permission.Scope, "One: Saved app secret “alpha”") || !strings.Contains(read.Content, "Read secret value") || !strings.Contains(read.Content, "copy it and use it elsewhere") || strings.Contains(read.Content, "Uses the secret through") {
		t.Fatalf("specific raw Read lost its distinct disclosure: %+v", read)
	}
	if !strings.Contains(use.Content, "Uses the secret through") || strings.Contains(use.Content, "Exports the raw secret") {
		t.Fatalf("Use acquired raw Read meaning: %+v", use)
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
		if report := presenterResultReport(result); report.Show || report.OpenQuestions {
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
	if !report.Show || !report.OpenQuestions || !strings.Contains(report.Content, "permission rule was not written") {
		t.Fatalf("incomplete effect claimed success: %+v", report)
	}
}

func TestTypedNativePrivilegeWarningUsesOnlyTrustedDisplay(t *testing.T) {
	record, details := typedNativeFixture(t, false, false)
	details.Question.Question.PublisherTrusted = false
	details.Question.Question.Publisher = ""
	if prompt := trayTypedPermissionView(record, details, nil); prompt.Warning != "" {
		t.Fatalf("unknown publisher implied privilege: %+v", prompt)
	}
	details.Display.RequiresOSAuthorization = true
	prompt := trayTypedPermissionView(record, details, nil)
	if !strings.Contains(prompt.Warning, "operating system authorization") || strings.Contains(prompt.Warning, "current and future") {
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

func TestTypedPermissionListingRestartsGapAndRejectsPartialPages(t *testing.T) {
	record, _ := typedNativeFixture(t, true, false)
	calls := 0
	operator := &fakePermissionOperator{pages: func(string) (askclient.OperatorPage, error) {
		calls++
		if calls == 1 {
			return askclient.OperatorPage{Outcome: askclient.OperatorPageOutcomeGap}, nil
		}
		return askclient.OperatorPage{Outcome: askclient.OperatorPageOutcomePage, Records: []askclient.RecordMetadata{record}, Complete: true}, nil
	}}
	listed, err := listPermissionQuestions(context.Background(), operator)
	if err != nil || len(listed) != 1 || calls != 2 {
		t.Fatalf("gap listing=%v %v calls=%d", listed, err, calls)
	}
	operator.pages = func(string) (askclient.OperatorPage, error) {
		return askclient.OperatorPage{Outcome: askclient.OperatorPageOutcomePage, Records: []askclient.RecordMetadata{record}}, nil
	}
	if listed, err = listPermissionQuestions(context.Background(), operator); err == nil || listed != nil {
		t.Fatalf("partial page announced=%v %v", listed, err)
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
	if frame.Prompt == nil || frame.Prompt.Warning == "" || !strings.Contains(frame.Prompt.Content, "Exports the raw secret value to this app") || !strings.Contains(frame.Prompt.Content, "Full control") {
		t.Fatalf("typed native frame=%+v", frame)
	}
	if err := bridge.accept(nativeAsksReply{Kind: "answer", ID: frame.ID, Option: "refuse"}, nil); err != nil {
		t.Fatal(err)
	}
	result := <-done
	if result.Outcome != presentAnswered || result.TypedAnswered == nil || result.TypedAnswered.Decision.Outcome != askclient.OperatorDecisionOutcomeAnswered {
		t.Fatalf("public typed answer=%+v", result)
	}
	if report := presenterResultReport(result); report.Show {
		t.Fatalf("typed success displayed second popup: %+v", report)
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
