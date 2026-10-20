//go:build windows

package main

import (
	"context"
	"errors"
	"maps"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"golang.org/x/sys/windows"
)

// setupOccurrenceFixture is Setup's pending Book question with its retained
// plan facts.
func setupOccurrenceFixture(t *testing.T) (map[string]string, askclient.RecordMetadata, *fakeOperator) {
	t.Helper()
	q := frozenNativeSetupQuestion()
	slots := maps.Clone(q.Facts)
	slots["occurrence"] = strings.Repeat("a", 48)
	record := pendingFixture()
	record.ID = "setup-1"
	record.Key = asks.SetupModelsKey
	record.About = slots["occurrence"]
	record.Options = []string{"setup", "not_now"}
	record.Text = q.Instruction
	details := askclient.QuestionDetails{Question: askclient.AttributedQuestion{Question: askclient.OperatorQuestion{Outcome: askclient.OperatorQuestionOutcomeFound, Record: &record}}}
	op := &fakeOperator{records: []askclient.RecordMetadata{record},
		contents: map[string]askclient.QuestionContent{record.ID: {Question: askclient.QuestionAsker{Details: details}, Content: &askclient.CatalogueContent{Slots: slots}}}}
	return q.Facts, record, op
}

// A Panel operation that needs the person's own consent (Setup's question)
// starts the presenter program once with the occurrence's Book id, reads the
// answer back from the Book, and the running background presenter never
// shows the same occurrence a second time.
func TestPanelConsentShowsExactlyOnePresenterQuestion(t *testing.T) {
	planned, record, op := setupOccurrenceFixture(t)
	surface := &fakeSurface{option: "setup"}
	presenter := testPresenter(unlockedSession(), surface, op, &fakeRules{})
	var handed []string
	previous := presentThroughPresenter
	t.Cleanup(func() { presentThroughPresenter = previous })
	presentThroughPresenter = func(ctx context.Context, id string) (string, error) {
		handed = append(handed, id)
		// The presenter program's -present mode, run in process; its exit
		// code carries the outcome back.
		return occurrenceFromExitCode(occurrenceExitCode(occurrenceOutcome(presenter.presentOccurrence(ctx, id)))), nil
	}
	operatorFor := func(context.Context) (presenterOperator, error) { return op, nil }
	yes, err := handSetupQuestion(t.Context(), operatorFor, "", record.ID, planned, showSetupThroughPresenter)
	if err != nil || !yes {
		t.Fatal(yes, err)
	}
	if len(handed) != 1 || handed[0] != record.ID || len(surface.occurrences) != 1 || surface.occurrences[0] != record.ID {
		t.Fatal("one presenter question expected", handed, surface.occurrences)
	}
	if sent := op.sent(); len(sent) != 1 || sent[0] != record.ID+"=setup" {
		t.Fatal(sent)
	}
	// The background presenter's poll over the same Book announces
	// nothing: Setup's question was handed over, and is answered now.
	fresh := newTrayAnnouncer().update([]askclient.RecordMetadata{record})
	if len(fresh) != 0 {
		t.Fatal("the background presenter would show Setup's question too", fresh)
	}
}

// A closed presenter question is no answer: the step reads it as closed.
func TestPanelConsentClosedAtPresenterIsNoAnswer(t *testing.T) {
	planned, record, op := setupOccurrenceFixture(t)
	presenter := testPresenter(unlockedSession(), &fakeSurface{option: ""}, op, &fakeRules{})
	previous := presentThroughPresenter
	t.Cleanup(func() { presentThroughPresenter = previous })
	presentThroughPresenter = func(ctx context.Context, id string) (string, error) {
		return occurrenceOutcome(presenter.presentOccurrence(ctx, id)), nil
	}
	operatorFor := func(context.Context) (presenterOperator, error) { return op, nil }
	if _, err := handSetupQuestion(t.Context(), operatorFor, "", record.ID, planned, showSetupThroughPresenter); !errors.Is(err, errStepQuestionClosed) {
		t.Fatal(err)
	}
	if len(op.sent()) != 0 {
		t.Fatal(op.sent())
	}
}

// The presenter's -present mode draws only an occurrence the Book holds of
// the two handed kinds. An unknown id, and an ordinary pending question the
// background presenter owns, are refused without drawing.
func TestPresenterPresentModeRefusesOtherOccurrences(t *testing.T) {
	_, setupRecord, op := setupOccurrenceFixture(t)
	firstUse := pendingFixture()
	op.set(setupRecord, firstUse)
	surface := &fakeSurface{option: "allow"}
	presenter := testPresenter(unlockedSession(), surface, op, &fakeRules{})
	for _, id := range []string{"no-such-occurrence", firstUse.ID} {
		result := presenter.presentOccurrence(t.Context(), id)
		if !errors.Is(result.Err, errHandedOccurrence) || occurrenceOutcome(result) != occurrenceUnavailable {
			t.Fatal(id, result)
		}
	}
	if len(surface.shown) != 0 || len(op.sent()) != 0 {
		t.Fatal("refused occurrence drawn or answered", surface.shown, op.sent())
	}
}

func TestOccurrenceExitCodesRoundTrip(t *testing.T) {
	for outcome := range occurrenceExitCodes {
		if got := occurrenceFromExitCode(occurrenceExitCode(outcome)); got != outcome {
			t.Fatal(outcome, got)
		}
	}
	if occurrenceFromExitCode(1) != occurrenceUnavailable || occurrenceFromExitCode(2) != occurrenceUnavailable {
		t.Fatal("a crash or a usage error is unavailable")
	}
}

// The runtime's supervisor finds a running installed presenter by this claim
// name (serve/presenter_supervise_windows.go installedPresenterClaim).
func TestInstalledPresenterClaimNameIsTheSupervisorsOne(t *testing.T) {
	release, acquired, err := claimQuestionPresenter("")
	if err != nil {
		t.Fatal(err)
	}
	if !acquired {
		t.Skip("an installed presenter runs in this session")
	}
	defer release()
	name, _ := windows.UTF16PtrFromString(`Local\OpenAbstractions.QuestionPresenter.e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855`)
	handle, err := windows.OpenMutex(windows.SYNCHRONIZE, false, name)
	if err != nil {
		t.Fatal("the supervisor would not see this presenter:", err)
	}
	windows.CloseHandle(handle)
}

func TestPanelConsentReconcilesExactOperatorAnswerWhileQueued(t *testing.T) {
	for _, answerer := range []askclient.Answerer{askclient.AnswererOperator, askclient.AnswererResponder} {
		planned, record, op := setupOccurrenceFixture(t)
		operatorFor := func(context.Context) (presenterOperator, error) { return op, nil }
		yes, err := handSetupQuestion(t.Context(), operatorFor, "", record.ID, planned,
			func(context.Context, presenterOperator, string) (string, error) {
				answered := record
				answered.Option = "setup"
				current := op.contents[record.ID]
				current.Question.Details.Question.Question.Record = &answered
				current.Question.Details.Question.Question.AnsweredBy = &answerer
				op.contents[record.ID] = current
				return "", nil
			})
		if answerer == askclient.AnswererOperator {
			if !yes || err != nil {
				t.Fatal(yes, err)
			}
		} else if yes || err == nil {
			t.Fatal("non-operator answer accepted", yes, err)
		}
	}
}
