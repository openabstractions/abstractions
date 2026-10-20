package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
)

func choiceFixture() askclient.ChoiceRecord {
	now := time.Now()
	return askclient.ChoiceRecord{ID: "choice-id", Asker: "fixture-app", AskedMs: now.Add(-time.Second).UnixMilli(),
		State: askclient.ChoiceStatePending, PublisherTrusted: true, Publisher: "Recorded Publisher",
		ProgramDescription: "Recorded App", ProgramOriginalName: "fixture-app",
		Request: askclient.ChoiceRequest{RequestRef: askclient.RequestRef{Key: "retained-intent", ReplayUntilMs: now.Add(2 * time.Minute).UnixMilli()},
			PendingUntilMs: now.Add(time.Minute).UnixMilli(), Title: "Allow System access?",
			Options: []askclient.ChoiceOption{{ID: "allow", Label: "Replace file"}, {ID: "cancel", Label: "Cancel this delivery"}}}}
}

type fakeChoiceOperator struct {
	mu             sync.Mutex
	record         askclient.ChoiceRecord
	err            error
	reads          int
	decisions      []string
	afterRead      func(int)
	beforeDecision func()
}

func (o *fakeChoiceOperator) ReadChoiceContext(_ context.Context, id string) (askclient.ChoiceRead, error) {
	o.mu.Lock()
	o.reads++
	count, after := o.reads, o.afterRead
	record, err := cloneChoiceRecord(o.record), o.err
	o.mu.Unlock()
	if after != nil {
		after(count)
	}
	if err != nil {
		return askclient.ChoiceRead{}, err
	}
	if id != record.ID {
		return askclient.ChoiceRead{Outcome: askclient.OperatorQuestionOutcomeUnknown}, nil
	}
	return askclient.ChoiceRead{Outcome: askclient.OperatorQuestionOutcomeFound, Record: &record}, nil
}

func (o *fakeChoiceOperator) ListChoicesContext(context.Context, string, int64) (askclient.ChoicePage, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.err != nil {
		return askclient.ChoicePage{}, o.err
	}
	return askclient.ChoicePage{Outcome: askclient.OperatorPageOutcomePage, Records: []askclient.ChoiceRecord{cloneChoiceRecord(o.record)}, Complete: true}, nil
}

func (o *fakeChoiceOperator) AnswerChoiceContext(_ context.Context, id, option string) (askclient.ChoiceDecision, error) {
	return o.decide(id, option)
}
func (o *fakeChoiceOperator) DismissChoiceContext(_ context.Context, id string) (askclient.ChoiceDecision, error) {
	return o.decide(id, "")
}
func (o *fakeChoiceOperator) decide(id, option string) (askclient.ChoiceDecision, error) {
	if o.beforeDecision != nil {
		o.beforeDecision()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.decisions = append(o.decisions, option)
	if id != o.record.ID || o.record.State != askclient.ChoiceStatePending {
		return askclient.ChoiceDecision{Outcome: askclient.ChoiceDecisionOutcomeConflict}, nil
	}
	o.record.State = askclient.ChoiceStateAnswered
	outcome := askclient.ChoiceDecisionOutcomeAnswered
	if option == "" {
		o.record.State = askclient.ChoiceStateDismissed
		outcome = askclient.ChoiceDecisionOutcomeDismissed
	}
	o.record.Answer = &askclient.ChoiceAnswer{ID: id, Option: option, AnsweredMs: time.Now().UnixMilli(), AnsweredBy: "recorded-operator"}
	record := cloneChoiceRecord(o.record)
	return askclient.ChoiceDecision{Outcome: outcome, Record: &record}, nil
}

func choiceTestPresenter(t *testing.T, operator *fakeChoiceOperator, surface *fakeSurface) *questionPresenter {
	t.Helper()
	return &questionPresenter{session: unlockedSession(), surface: surface,
		choices: func(context.Context) (presenterChoiceOperator, error) { return operator, nil },
		rules:   func() grants.RuleOperator { t.Fatal("ordinary choice called Rights"); return nil }, watchEvery: 10 * time.Millisecond}
}

func TestChoicePresenterKeepsAppOptionsSeparateFromCloseAndPermissionChrome(t *testing.T) {
	for _, count := range []int{1, 2, 3} {
		for _, option := range []string{"cancel", ""} {
			t.Run(fmt.Sprintf("%d/%s", count, option), func(t *testing.T) {
				record := choiceFixture()
				record.Request.Options = []askclient.ChoiceOption{{ID: "cancel", Label: "Cancel this delivery"}}
				for i := 1; i < count; i++ {
					record.Request.Options = append(record.Request.Options, askclient.ChoiceOption{ID: fmt.Sprint(i), Label: "Allow system access"})
				}
				operator := &fakeChoiceOperator{record: record}
				surface := &fakeSurface{option: option}
				result := choiceTestPresenter(t, operator, surface).presentChoice(context.Background(), record)
				if result.Err != nil || result.Outcome != presentAnswered || len(operator.decisions) != 1 || operator.decisions[0] != option {
					t.Fatalf("decision %+v calls %v", result, operator.decisions)
				}
				want := askclient.ChoiceDecisionOutcomeAnswered
				if option == "" {
					want = askclient.ChoiceDecisionOutcomeDismissed
				}
				if result.Decision.Outcome != want {
					t.Fatalf("decision %+v", result)
				}
				prompt := surface.shown[0]
				if !prompt.AppChoice || prompt.Default != "" || !strings.Contains(prompt.Title, "App choice") || prompt.Instruction != record.Request.Title || len(prompt.Choices) != count || prompt.Choices[0].Option != "cancel" || prompt.Choices[0].Label != "Cancel this delivery" {
					t.Fatalf("ordinary projection %+v", prompt)
				}
			})
		}
	}
}

func TestChoicePresenterRejectsEveryChangedRequestAndAttributionField(t *testing.T) {
	changes := map[string]func(*askclient.ChoiceRecord){
		"asker":   func(r *askclient.ChoiceRecord) { r.Asker = "other" },
		"key":     func(r *askclient.ChoiceRecord) { r.Request.RequestRef.Key = "other" },
		"replay":  func(r *askclient.ChoiceRecord) { r.Request.RequestRef.ReplayUntilMs++ },
		"pending": func(r *askclient.ChoiceRecord) { r.Request.PendingUntilMs++ },
		"title":   func(r *askclient.ChoiceRecord) { r.Request.Title = "Changed title" },
		"label":   func(r *askclient.ChoiceRecord) { r.Request.Options[0].Label = "Changed label" },
		"option":  func(r *askclient.ChoiceRecord) { r.Request.Options[0].ID = "other" },
		"order": func(r *askclient.ChoiceRecord) {
			r.Request.Options[0], r.Request.Options[1] = r.Request.Options[1], r.Request.Options[0]
		},
		"asked":     func(r *askclient.ChoiceRecord) { r.AskedMs-- },
		"publisher": func(r *askclient.ChoiceRecord) { r.Publisher = "Other publisher" },
		"trust": func(r *askclient.ChoiceRecord) {
			r.PublisherTrusted = false
			r.Publisher = ""
			r.ProgramDescription = ""
			r.ProgramOriginalName = ""
		},
		"description": func(r *askclient.ChoiceRecord) { r.ProgramDescription = "Other app" },
		"original":    func(r *askclient.ChoiceRecord) { r.ProgramOriginalName = "other" },
	}
	for name, change := range changes {
		t.Run(name, func(t *testing.T) {
			record := choiceFixture()
			operator := &fakeChoiceOperator{record: cloneChoiceRecord(record)}
			surface := &fakeSurface{option: "allow", during: func() { operator.mu.Lock(); defer operator.mu.Unlock(); change(&operator.record) }}
			result := choiceTestPresenter(t, operator, surface).presentChoice(context.Background(), record)
			if result.Outcome != presentStale || len(operator.decisions) != 0 {
				t.Fatalf("changed %s accepted: %+v calls %v", name, result, operator.decisions)
			}
		})
	}
}

func TestChoicePresenterDefersAcrossSessionRevisionAndAfterFetch(t *testing.T) {
	for _, when := range []string{"before", "after fetch", "during"} {
		t.Run(when, func(t *testing.T) {
			record := choiceFixture()
			operator := &fakeChoiceOperator{record: record}
			surface := &fakeSurface{option: "allow"}
			p := choiceTestPresenter(t, operator, surface)
			session := p.session.(*fakeSession)
			lockUnlock := func() { session.revision.Add(1); session.unlocked.Store(true) }
			switch when {
			case "before":
				session.unlocked.Store(false)
			case "after fetch":
				operator.afterRead = func(int) { lockUnlock() }
			case "during":
				surface.during = lockUnlock
			}
			result := p.presentChoice(context.Background(), record)
			if result.Outcome != presentDeferred || len(operator.decisions) != 0 {
				t.Fatalf("interrupted %+v calls %v", result, operator.decisions)
			}
		})
	}
}

func TestChoicePresenterClosesStaleAndExpiredDialogsWithoutDismissal(t *testing.T) {
	for _, expired := range []bool{false, true} {
		t.Run(fmt.Sprint(expired), func(t *testing.T) {
			record := choiceFixture()
			if expired {
				record.Request.PendingUntilMs = time.Now().Add(80 * time.Millisecond).UnixMilli()
			}
			operator := &fakeChoiceOperator{record: record}
			surface := &fakeSurface{block: true, dismissed: make(chan struct{})}
			if !expired {
				surface.during = func() {
					operator.mu.Lock()
					defer operator.mu.Unlock()
					operator.record.State = askclient.ChoiceStateExpired
				}
			}
			result := choiceTestPresenter(t, operator, surface).presentChoice(context.Background(), record)
			if result.Outcome != presentStale || !result.ClosedElsewhere || len(operator.decisions) != 0 {
				t.Fatalf("stale/expiry %+v calls %v", result, operator.decisions)
			}
		})
	}
}

func TestChoicePresenterRetainsConditionalRaceAndExplicitUnsupported(t *testing.T) {
	record := choiceFixture()
	operator := &fakeChoiceOperator{record: record}
	operator.beforeDecision = func() { operator.record.State = askclient.ChoiceStateExpired }
	result := choiceTestPresenter(t, operator, &fakeSurface{option: "allow"}).presentChoice(context.Background(), record)
	if result.Decision.Outcome != askclient.ChoiceDecisionOutcomeConflict {
		t.Fatalf("terminal race %+v", result)
	}
	for _, err := range []error{errors.New("offline"), &askclient.ServiceError{Code: askclient.ServiceErrorCodeUnknownMethod}} {
		o := &fakeChoiceOperator{record: record, err: err}
		s := &fakeSurface{option: "allow"}
		p := choiceTestPresenter(t, o, s)
		r := p.presentChoice(context.Background(), record)
		if r.Outcome != presentUnavailable || r.Unsupported != ordinaryUnsupported(err) || len(s.shown) != 0 {
			t.Fatalf("read failure %+v shown %v", r, s.shown)
		}
		if _, e := pollChoices(context.Background(), p, newTrayAnnouncer()); e == nil || ordinaryUnsupported(e) != ordinaryUnsupported(err) {
			t.Fatalf("poll failure %v", e)
		}
	}
}

func TestChoiceNativeBridgeKeepsOpaqueAnswerAndIgnoresLateInterruptedReply(t *testing.T) {
	record := choiceFixture()
	operator := &fakeChoiceOperator{record: record}
	bridge, frames := nativeBridgeFixture(t)
	bridge.session(true)
	p := &questionPresenter{session: bridge, surface: bridge, choices: func(context.Context) (presenterChoiceOperator, error) { return operator, nil }}
	done := make(chan choicePresentResult, 1)
	go func() { done <- p.presentChoice(context.Background(), record) }()
	shown := nextNativeFrame(t, frames, "question")
	if shown.Prompt == nil || !shown.Prompt.AppChoice || shown.Prompt.Default != "" {
		t.Fatalf("frame %+v", shown)
	}
	bridge.session(false)
	_ = nextNativeFrame(t, frames, "dismiss")
	bridge.session(true)
	if err := bridge.accept(nativeAsksReply{Kind: "answer", ID: shown.ID, Option: "cancel"}, nil); err != nil {
		t.Fatal(err)
	}
	if result := <-done; result.Outcome != presentDeferred || len(operator.decisions) != 0 {
		t.Fatalf("late reply %+v", result)
	}
	go func() { done <- p.presentChoice(context.Background(), record) }()
	again := nextNativeFrame(t, frames, "question")
	if err := bridge.accept(nativeAsksReply{Kind: "answer", ID: again.ID, Option: "cancel"}, nil); err != nil {
		t.Fatal(err)
	}
	if result := <-done; result.Decision.Outcome != askclient.ChoiceDecisionOutcomeAnswered || result.Decision.Record.Answer.Option != "cancel" {
		t.Fatalf("opaque answer %+v", result)
	}
}
