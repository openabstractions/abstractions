package main

import (
	"bytes"
	"context"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go/client"
)

// The output choice is shared with an enrolled assistant, which may choose
// Keep or Cancel; Replace stays with the person (asks ASK-A12).
func TestOutputChoiceSharesKeepAndCancelOnly(t *testing.T) {
	c, op := choicesRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	where := filepath.Join(t.TempDir(), "weights.bin")
	q, err := outputChoice(ctx, where)
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	done := make(chan asks.ChoiceAnswer, 1)
	failed := make(chan error, 1)
	go func() {
		answer, err := waitOutputChoice(ctx, c, q, where, &out, nil)
		if err != nil {
			failed <- err
			return
		}
		done <- answer
	}()
	var id string
	for id == "" {
		page, err := op.ListChoicesContext(ctx, "", 16)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page.Records {
			if r.Request.RequestRef == q.RequestRef {
				id = r.ID
			}
		}
		if id == "" {
			select {
			case <-ctx.Done():
				t.Fatal("the shared choice never arrived")
			case <-time.After(20 * time.Millisecond):
			}
		}
	}
	details, err := op.ReadChoiceDetailsContext(ctx, id)
	if err != nil || !slices.Equal(details.AssistantOptions, []string{"keep", "cancel"}) {
		t.Fatalf("shared options %+v %v", details, err)
	}
	if decision, err := op.AnswerChoiceContext(ctx, id, "keep"); err != nil || decision.Outcome != asks.ChoiceDecisionOutcomeAnswered {
		t.Fatal(decision, err)
	}
	select {
	case answer := <-done:
		if answer.Option != "keep" {
			t.Fatal(answer)
		}
	case err := <-failed:
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "may choose Keep or Cancel; Replace stays with you") {
		t.Fatalf("the person is not told what an assistant may choose:\n%s", out.String())
	}
}

// olderRuntime refuses ShareChoice as a runtime without it does.
type olderRuntime struct {
	choiceService
	asked int
}

func (o *olderRuntime) ShareContext(context.Context, asks.ChoiceRequest, []string) (asks.ChoiceObservation, error) {
	return asks.ChoiceObservation{}, &asks.ServiceError{Code: asks.ServiceErrorCodeUnknownMethod}
}
func (o *olderRuntime) AskContext(ctx context.Context, q asks.ChoiceRequest) (asks.ChoiceObservation, error) {
	o.asked++
	return o.choiceService.AskContext(ctx, q)
}

func TestOutputChoiceAsksUnsharedOnAnOlderRuntime(t *testing.T) {
	c, _ := choicesRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	q, err := outputChoice(ctx, filepath.Join(t.TempDir(), "weights.bin"))
	if err != nil {
		t.Fatal(err)
	}
	older := &olderRuntime{choiceService: c}
	var unshared bool
	var out bytes.Buffer
	observation, err := admitOutputChoice(ctx, older, q, &unshared, &out)
	if err != nil || observation.Outcome != asks.ChoiceOutcomePending || !unshared || older.asked != 1 || !strings.Contains(out.String(), "waits for you") {
		t.Fatal(observation, err, unshared, older.asked, out.String())
	}
	if _, err := admitOutputChoice(ctx, older, q, &unshared, &out); err != nil || older.asked != 2 {
		t.Fatal("a retry must ask unshared again", err, older.asked)
	}
}
