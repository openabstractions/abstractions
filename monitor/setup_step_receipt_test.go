package main

import (
	"context"
	"errors"
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func frozenNativeSetupQuestion() setup.StepQuestion {
	f := asks.SetupQuestionFacts{Targets: []asks.SetupTargetFact{{Name: "the-app", Title: "The app", Program: `C:\Apps\app.exe`, Use: "code", Chat: true}}, Code: &asks.SetupCodeFact{Title: "Reviewed model", License: "Apache-2.0", State: "served", Ref: asks.SetupModelFact{Family: "reviewed-model", CatalogVersion: "catalog-v1"}}}
	slots, err := asks.SetupFactSlots(f)
	if err != nil {
		panic(err)
	}
	body, err := asks.SetupQuestionPresentation(slots)
	if err != nil {
		panic(err)
	}
	return setup.StepQuestion{Title: body.Title, Instruction: body.Instruction, Details: body.Details, Facts: slots}
}

func TestSetupQuestionInstalledUnregisteredKeepsIndependentConfirm(t *testing.T) {
	for _, change := range []string{"", "record-created", "record-changed"} {
		t.Run(change, func(t *testing.T) {
			root := t.TempDir()
			manifest := filepath.Join(root, "application.json")
			program := filepath.Join(root, "application.exe")
			targets := stepInstalledApps([]setup.Choice{{ID: setup.AppID("opencode"), Title: "OpenCode", Use: setup.UseCode}}, "", nil,
				func(string) (string, bool) { return manifest, true }, func(string) (fwire.ApplicationDescriptor, error) {
					return fwire.ApplicationDescriptor{Name: "opencode", Program: program}, nil
				}, func(string) string { return root }, func(string) string { return "" })
			if len(targets) != 1 || targets[0].Manifest != manifest || targets[0].Revision != "" {
				t.Fatal(targets)
			}
			q := frozenNativeSetupQuestion()
			facts, err := asks.SetupFactsFromSlots(q.Facts)
			if err != nil {
				t.Fatal(err)
			}
			facts.Targets[0] = asks.SetupTargetFact{Name: targets[0].Name, Title: targets[0].Title, Program: targets[0].Program, Use: targets[0].Use}
			q.Facts, err = asks.SetupFactSlots(facts)
			if err != nil {
				t.Fatal(err)
			}
			body, err := asks.SetupQuestionPresentation(q.Facts)
			if err != nil {
				t.Fatal(err)
			}
			q.Title, q.Instruction, q.Details = body.Title, body.Instruction, body.Details
			record := pendingFixture()
			record.Key = asks.SetupModelsKey
			record.About = strings.Repeat("a", 48)
			record.Text = q.Instruction
			record.Options = []string{"setup", "not_now"}
			retained := maps.Clone(q.Facts)
			retained["occurrence"] = record.About
			details := askclient.QuestionDetails{Question: askclient.AttributedQuestion{Question: askclient.OperatorQuestion{Outcome: askclient.OperatorQuestionOutcomeFound, Record: &record}}}
			op := &fakeOperator{records: []askclient.RecordMetadata{record}, contents: map[string]askclient.QuestionContent{record.ID: {Question: askclient.QuestionAsker{Details: details}, Content: &askclient.CatalogueContent{Slots: retained}}}}
			rules := &fakeRules{}
			presenter := testPresenter(unlockedSession(), &fakeSurface{option: "setup"}, op, rules)
			s := setupQuestionSession{report: func(_ context.Context, r applications.SetupPresenterReceipt) (applications.SetupStepReport, error) {
				if !maps.Equal(r.Facts, q.Facts) {
					t.Fatal(r)
				}
				return applications.SetupStepReport{Outcome: applications.OutcomePage, Question: record.ID}, nil
			}, present: func(ctx context.Context, id string, f map[string]string) (bool, error) {
				return presentSetupQuestion(ctx, presenter, id, f)
			}}
			yes, err := s.ask(context.Background(), q)
			if err != nil || !yes {
				t.Fatal(yes, err)
			}
			calls := 0
			stepConfirm(context.Background(), targets, func(_ context.Context, path string) {
				calls++
				if path != manifest {
					t.Fatal(path)
				}
			})
			if calls != 1 {
				t.Fatal(calls)
			}
			if len(rules.edits) != 0 {
				t.Fatal("Setup answer bypassed permission owner", rules.edits)
			}
		})
	}
}

func TestSetupQuestionHistoryRendersSameRetainedBody(t *testing.T) {
	q := frozenNativeSetupQuestion()
	slots := maps.Clone(q.Facts)
	slots["occurrence"] = strings.Repeat("a", 48)
	event := answerEvent(askclient.AnswerContent{Answer: askclient.AnswerEntry{Key: asks.SetupModelsKey, About: slots["occurrence"], Option: "not_now", AnsweredBy: askclient.AnswererOperator}, Content: &askclient.CatalogueContent{Slots: slots}}, newProgramTitler(nil))
	if event.What != q.Title+"\n"+q.Instruction+"\n"+q.Details {
		t.Fatal(event)
	}
}

func TestSetupQuestionLostStartedReplyRetainsPresenterKey(t *testing.T) {
	var first applications.SetupPresenterReceipt
	calls := 0
	s := &setupQuestionSession{report: func(_ context.Context, receipt applications.SetupPresenterReceipt) (applications.SetupStepReport, error) {
		calls++
		if len(receipt.Key) != 48 {
			t.Fatal("presenter key absent", receipt)
		}
		if calls == 1 {
			first = receipt
			return applications.SetupStepReport{}, errors.New("lost reply")
		}
		if receipt.Key != first.Key || receipt.Request != first.Request || !maps.Equal(receipt.Facts, first.Facts) {
			t.Fatal("explicit reconciliation changed request identity", first, receipt)
		}
		return applications.SetupStepReport{Outcome: applications.OutcomePage, Question: "original-question"}, nil
	}, present: func(context.Context, string, map[string]string) (bool, error) { return true, nil }}
	if _, err := s.ask(context.Background(), frozenNativeSetupQuestion()); err == nil || calls != 1 {
		t.Fatal("lost reply was retried automatically", calls, err)
	}
	if yes, err := s.ask(context.Background(), frozenNativeSetupQuestion()); err != nil || !yes || calls != 2 {
		t.Fatal("explicit same-key reconciliation failed", calls, yes, err)
	}
}

func TestSetupQuestionReceiptReportsOnlyPresenterLifecycle(t *testing.T) {
	for _, answer := range []string{"yes", "not_now", "locked", "begin-failed"} {
		t.Run(answer, func(t *testing.T) {
			var events []string
			s := &setupQuestionSession{after: "request", report: func(_ context.Context, r applications.SetupPresenterReceipt) (applications.SetupStepReport, error) {
				events = append(events, r.Outcome.String())
				if r.Outcome == applications.SetupReceiptOutcomeStarted {
					if r.Request != "request" || r.Occurrence != "" || !maps.Equal(r.Facts, frozenNativeSetupQuestion().Facts) {
						t.Fatal(r)
					}
					if answer == "begin-failed" {
						return applications.SetupStepReport{}, errors.New("unavailable")
					}
					return applications.SetupStepReport{Outcome: applications.OutcomePage, Question: "book-id"}, nil
				}
				if r.Occurrence != "book-id" || len(r.Facts) != 0 || r.Request != "" {
					t.Fatal(r)
				}
				return applications.SetupStepReport{Outcome: applications.OutcomePage}, nil
			}, present: func(_ context.Context, id string, facts map[string]string) (bool, error) {
				events = append(events, "native-question")
				if id != "book-id" || !maps.Equal(facts, frozenNativeSetupQuestion().Facts) {
					t.Fatal(id, facts)
				}
				if answer == "locked" {
					return false, errStepQuestionClosed
				}
				return answer == "yes", nil
			}}
			yes, err := s.ask(context.Background(), frozenNativeSetupQuestion())
			if answer == "yes" || answer == "not_now" {
				if err != nil || yes != (answer == "yes") {
					t.Fatal(yes, err)
				}
				if !reflect.DeepEqual(events, []string{"started", "native-question"}) {
					t.Fatal(events)
				}
				if err := s.completed(context.Background()); err != nil {
					t.Fatal(err)
				}
				want := []string{"started", "native-question"}
				if yes {
					want = append(want, "completed")
				}
				if !reflect.DeepEqual(events, want) {
					t.Fatal(events)
				}
			} else if err == nil {
				t.Fatal("closed/refused question accepted")
			}
		})
	}
}

func TestSetupQuestionPresenterUsesRetainedFactsAndExactOccurrence(t *testing.T) {
	for _, mode := range []string{"setup", "not_now", "locked", "answered-elsewhere", "different-plan", "missing-facts", "mutated-facts"} {
		t.Run(mode, func(t *testing.T) {
			q := frozenNativeSetupQuestion()
			slots := maps.Clone(q.Facts)
			slots["occurrence"] = strings.Repeat("a", 48)
			record := pendingFixture()
			record.Key = asks.SetupModelsKey
			record.About = slots["occurrence"]
			record.Options = []string{"setup", "not_now"}
			record.Text = q.Instruction
			if mode == "different-plan" {
				record.About = strings.Repeat("b", 48)
			}
			content := &askclient.CatalogueContent{Slots: slots}
			if mode == "missing-facts" {
				content = nil
			}
			if mode == "mutated-facts" {
				slots["code_source"] = "changed"
			}
			details := askclient.QuestionDetails{Question: askclient.AttributedQuestion{Question: askclient.OperatorQuestion{Outcome: askclient.OperatorQuestionOutcomeFound, Record: &record}}}
			op := &fakeOperator{records: []askclient.RecordMetadata{record}, contents: map[string]askclient.QuestionContent{record.ID: {Question: askclient.QuestionAsker{Details: details}, Content: content}}}
			surface := &fakeSurface{option: mode}
			session := unlockedSession()
			rules := &fakeRules{}
			if mode == "locked" {
				surface.option = "setup"
				surface.during = func() { session.revision.Add(1); session.unlocked.Store(false) }
			}
			if mode == "answered-elsewhere" {
				surface.option = "setup"
				surface.during = func() { record.Option = "not_now"; op.set(record) }
			}
			p := testPresenter(session, surface, op, rules)
			yes, err := presentSetupQuestion(context.Background(), p, record.ID, q.Facts)
			if mode == "setup" || mode == "not_now" {
				if err != nil || yes != (mode == "setup") || len(op.sent()) != 1 {
					t.Fatal(yes, err, op.sent())
				}
			} else if err == nil || len(op.sent()) != 0 {
				t.Fatal("stale/locked question answered", yes, err, op.sent())
			}
			if mode == "different-plan" || mode == "missing-facts" || mode == "mutated-facts" {
				if len(surface.shown) != 0 {
					t.Fatal("unbound facts shown")
				}
			} else {
				if len(surface.shown) != 1 {
					t.Fatal(surface.shown)
				}
				shown := surface.shown[0]
				if shown.Title != "OpenAbstractions" || shown.Instruction != q.Title || shown.Content != q.Instruction || shown.Details != q.Details {
					t.Fatal("frozen wording changed", shown)
				}
				if shown.Permission == nil || shown.Permission.Instruction != q.Title || shown.Permission.Scope != q.Instruction || shown.Permission.Details != q.Details {
					t.Fatal(shown.Permission)
				}
			}
			if len(rules.edits) != 0 {
				t.Fatal("Setup answer changed permission", rules.edits)
			}
		})
	}
}

// presentSetupQuestion is the Panel's hand-over of Setup's question with the
// presenter program's -present mode run in process on p: the Panel checks the
// frozen facts, the presenter draws the occurrence, and the option it sent is
// the answer.
func presentSetupQuestion(ctx context.Context, p *questionPresenter, id string, planned map[string]string) (bool, error) {
	return handSetupQuestion(ctx, p.operator, p.account, id, planned, func(ctx context.Context, _ presenterOperator, id string) (string, error) {
		result := p.presentOccurrence(ctx, id)
		if occurrenceOutcome(result) != occurrenceAnswered {
			return "", nil
		}
		return result.Answered.Decision.Record.Option, nil
	})
}
