package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
)

type asksWatchFixture struct {
	asksFixture
	questions asks.OperatorPage
	choices   asks.AnswerableChoicePage
	reads     int
}

func (f *asksWatchFixture) ListAnswerableContext(context.Context, string, int64) (asks.OperatorPage, error) {
	f.reads++
	return f.questions, nil
}
func (f *asksWatchFixture) ListChoicesContext(context.Context, string, int64) (asks.AnswerableChoicePage, error) {
	f.reads++
	return f.choices, nil
}
func TestAsksWatchVisibleStampAndNoProtectedGlobalTiming(t *testing.T) {
	f := &asksWatchFixture{questions: asks.OperatorPage{Outcome: asks.OperatorPageOutcomePage, Complete: true, Records: []asks.RecordMetadata{{ID: "routine", Text: "PRIVATE QUESTION", Options: []string{"allow"}}}}, choices: asks.AnswerableChoicePage{Outcome: asks.OperatorPageOutcomePage, Complete: true, Choices: []asks.AnswerableChoice{{ID: "choice", Title: "PRIVATE CHOICE", Options: []asks.ChoiceOption{{ID: "keep", Label: "PRIVATE LABEL"}}}}}}
	access := "permitted"
	own := func(context.Context) (string, error) { return access, nil }
	first, err := ObserveAsks(context.Background(), AsksWatchInput{}, f, own)
	if err != nil || first.Outcome != "page" || len(first.Items) != 2 || f.reads != 4 {
		t.Fatal(first, err, f.reads)
	}
	// Hidden/global churn changes neither visible content nor the observation.
	second, err := ObserveAsks(context.Background(), AsksWatchInput{Revision: first.Revision}, f, own)
	if err != nil || second.Revision != first.Revision {
		t.Fatal("hidden timing changed", second, err)
	}
	raw, _ := json.Marshal(first)
	if strings.Contains(string(raw), "PRIVATE") || strings.Contains(string(raw), "allow") {
		t.Fatal("content leaked", string(raw))
	}
	f.questions.Records[0].Options = []string{"refuse"}
	changed, _ := ObserveAsks(context.Background(), AsksWatchInput{}, f, own)
	if changed.Revision == first.Revision {
		t.Fatal("visible allowed option did not change")
	}
	access = "denied"
	revoked, _ := ObserveAsks(context.Background(), AsksWatchInput{}, f, own)
	if revoked.Revision == changed.Revision {
		t.Fatal("own access did not change")
	}
}
func TestAsksWatchIncompleteAndInvalidNeverClaimEmpty(t *testing.T) {
	f := &asksWatchFixture{questions: asks.OperatorPage{Outcome: asks.OperatorPageOutcomePage, Next: "GLOBAL-REVISION", Records: []asks.RecordMetadata{{ID: "visible"}}}, choices: asks.AnswerableChoicePage{Outcome: asks.OperatorPageOutcomePage, Complete: true}}
	own := func(context.Context) (string, error) { return "not_granted", nil }
	for _, in := range []AsksWatchInput{{WaitMs: 1}, {WaitMs: -1}, {Revision: strings.Repeat("x", 129)}} {
		out, _ := ObserveAsks(context.Background(), in, f, own)
		if out.Outcome != "invalid" || f.reads != 0 {
			t.Fatal(out, f.reads)
		}
	}
	out, _ := ObserveAsks(context.Background(), AsksWatchInput{}, f, own)
	if out.Outcome != "incomplete" || out.Revision != "" || len(out.Items) != 0 {
		t.Fatal(out)
	}
	raw, _ := json.Marshal(out)
	if strings.Contains(string(raw), "GLOBAL") {
		t.Fatal("global cursor leaked")
	}
}
