package main

import (
	"context"
	"errors"
	"fmt"
	"testing"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

type assistantAuditPages struct {
	pages   map[string]askclient.AnswerEntryPage
	cursors []string
	err     error
}

func (p *assistantAuditPages) ListAnswersContext(_ context.Context, cursor string, limit int64) (askclient.AnswerEntryPage, error) {
	p.cursors = append(p.cursors, cursor)
	if limit != 64 {
		return askclient.AnswerEntryPage{}, fmt.Errorf("unexpected limit %d", limit)
	}
	return p.pages[cursor], p.err
}

func assistantChoice(id string, by askclient.Answerer) askclient.AnswerEntry {
	return askclient.AnswerEntry{ID: id, Kind: askclient.AnswerKindChoice, Asker: "app", Title: "Which?", Option: "keep", Label: "Keep", AnsweredBy: by, Answerer: "assistant", AnsweredMs: 100}
}

func TestAssistantAnswerFilterReadsAllPagesAndPreservesWatermark(t *testing.T) {
	p := &assistantAuditPages{pages: map[string]askclient.AnswerEntryPage{
		"": {Outcome: askclient.OperatorPageOutcomePage, Next: "next", CompleteSinceMs: 42,
			Answers: []askclient.AnswerEntry{{Kind: askclient.AnswerKindQuestion, AnsweredBy: askclient.AnswererResponder}, assistantChoice("operator", askclient.AnswererOperator)}},
		"next": {Outcome: askclient.OperatorPageOutcomePage, Next: "last", CompleteSinceMs: 42, Answers: []askclient.AnswerEntry{assistantChoice("one", askclient.AnswererResponder)}},
		"last": {Outcome: askclient.OperatorPageOutcomePage, Complete: true, CompleteSinceMs: 42, Answers: []askclient.AnswerEntry{assistantChoice("two", askclient.AnswererResponder)}},
	}}
	got, since, outcome, err := choiceAnswers(context.Background(), p, nil)
	if err != nil || outcome != "page" || since != 42 || len(got) != 2 || got[0].ID != "one" || got[1].ID != "two" || len(p.cursors) != 3 {
		t.Fatalf("filtered complete audit: %+v since=%d outcome=%s err=%v cursors=%v", got, since, outcome, err, p.cursors)
	}
}

func TestAssistantAnswerFilterStillObservesGapAfterDisplayLimit(t *testing.T) {
	first := askclient.AnswerEntryPage{Outcome: askclient.OperatorPageOutcomePage, Next: "gap", CompleteSinceMs: 42}
	for i := range answeredShown + 1 {
		first.Answers = append(first.Answers, assistantChoice(fmt.Sprint(i), askclient.AnswererResponder))
	}
	p := &assistantAuditPages{pages: map[string]askclient.AnswerEntryPage{
		"": first, "gap": {Outcome: askclient.OperatorPageOutcomeGap},
	}}
	got, since, outcome, err := choiceAnswers(context.Background(), p, nil)
	if err != nil || outcome != "gap" || got != nil || since != 0 || len(p.cursors) != 2 {
		t.Fatalf("a truncated display must still expose gap: %+v %d %s %v %v", got, since, outcome, err, p.cursors)
	}
	p.pages["gap"] = askclient.AnswerEntryPage{Outcome: askclient.OperatorPageOutcomePage, Complete: true, CompleteSinceMs: 42}
	p.cursors = nil
	got, since, outcome, err = choiceAnswers(context.Background(), p, nil)
	if err != nil || outcome != "page" || since != 42 || len(got) != answeredShown || len(p.cursors) != 2 {
		t.Fatalf("complete display remains bounded: %+v %d %s %v %v", got, since, outcome, err, p.cursors)
	}
}

func TestAssistantAnswerFilterRefusesTransportAndNonadvancingCursor(t *testing.T) {
	want := errors.New("transport failed")
	p := &assistantAuditPages{err: want}
	if _, _, _, err := choiceAnswers(context.Background(), p, nil); !errors.Is(err, want) {
		t.Fatal(err)
	}
	p = &assistantAuditPages{pages: map[string]askclient.AnswerEntryPage{
		"":     {Outcome: askclient.OperatorPageOutcomePage, Next: "loop"},
		"loop": {Outcome: askclient.OperatorPageOutcomePage, Next: "loop"},
	}}
	if got, _, _, err := choiceAnswers(context.Background(), p, nil); err == nil || got != nil {
		t.Fatal("a stalled continuation must not claim complete history", got, err)
	}
}
