package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
)

func TestCompleteSchemaKeepsInitialAndResumeModesExclusive(t *testing.T) {
	schema, err := completeSchema().Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{`{"model":"m","prompt":"p","hosting":"local"}`, `{"model":"m","prompt":"p","hosting":"hosted","credential":"mine","max_output":42}`, `{"operation_id":"op","cursor":0}`} {
		var v any
		json.Unmarshal([]byte(raw), &v)
		if err := schema.Validate(v); err != nil {
			t.Fatalf("valid %s: %v", raw, err)
		}
	}
	for _, raw := range []string{`{"model":"m","prompt":"p"}`, `{"operation_id":"op"}`, `{"operation_id":"op","cursor":0,"model":"m"}`, `{"operation_id":"op","cursor":0,"max_output":0}`, `{"model":"m","prompt":"p","hosting":"local","cursor":0}`} {
		var v any
		json.Unmarshal([]byte(raw), &v)
		if err := schema.Validate(v); err == nil {
			t.Fatalf("invalid %s", raw)
		}
	}
	b, err := json.Marshal(CompleteOutput{State: "working", Outcome: "pending", OperationID: "op", Cursor: 0})
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	json.Unmarshal(b, &out)
	if _, ok := out["cursor"]; !ok {
		t.Fatal("zero resume cursor omitted")
	}
}

func TestCompleteWaitCapResumesExistingOperation(t *testing.T) {
	oa, f := startCompletionFixture(t, true, "wait")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got, err := oa.Complete(ctx, CompleteInput{Model: "chosen/model", Prompt: "prompt", Hosting: "local"})
	if err != nil || got.State != "working" || got.Outcome != "pending" || got.OperationID != "operation" {
		t.Fatalf("%+v %v", got, err)
	}
	f.mu.Lock()
	f.mode = "complete"
	starts, cancels := len(f.requests), f.cancels
	f.mu.Unlock()
	if starts != 1 || cancels != 0 {
		t.Fatalf("starts %d cancels %d", starts, cancels)
	}
	result, err := oa.Complete(context.Background(), CompleteInput{OperationID: got.OperationID, Cursor: &got.Cursor})
	if err != nil || result.Text != "fixture reply" || result.Outcome != "completed" {
		t.Fatalf("%+v %v", result, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 || f.cancels != 0 {
		t.Fatalf("resume started/cancelled: %d %d", len(f.requests), f.cancels)
	}
}

// The tool's wait cap ends its last Observe by that Observe's own wait, with
// the call still connected: the provider then keeps the operation for the
// agent's resume (INF-Q3). No Observe waits past the cap.
func TestCompleteWaitCapEndsTheLastObserveByItsWait(t *testing.T) {
	oa, f := startCompletionFixture(t, true, "wait")
	limit := 300 * time.Millisecond
	started := time.Now()
	got, err := oa.Complete(withCallWait(context.Background(), limit), CompleteInput{Model: "chosen/model", Prompt: "prompt", Hosting: "local"})
	if err != nil || got.State != "working" || got.OperationID != "operation" || time.Since(started) > 3*time.Second {
		t.Fatalf("%+v %v after %s", got, err, time.Since(started))
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.waits) == 0 {
		t.Fatal("no Observe")
	}
	for _, wait := range f.waits {
		if wait > limit.Milliseconds() {
			t.Fatalf("an Observe waited %d ms past the %s cap", wait, limit)
		}
	}
	if f.cancels != 0 {
		t.Fatalf("cancels %d", f.cancels)
	}
}

func TestCompleteWaitReturnsPartialTextAndNextCursor(t *testing.T) {
	oa, f := startCompletionFixture(t, true, "partial_wait")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	got, err := oa.Complete(ctx, CompleteInput{Model: "chosen/model", Prompt: "prompt", Hosting: "local"})
	if err != nil || got.State != "working" || got.Cursor != 1 || got.Text != "first" {
		t.Fatalf("%+v %v", got, err)
	}
	f.mu.Lock()
	f.mode = "partial_resume"
	f.mu.Unlock()
	final, err := oa.Complete(context.Background(), CompleteInput{OperationID: got.OperationID, Cursor: &got.Cursor})
	if err != nil || final.Outcome != "completed" || final.Text != "" || final.Cursor != 2 {
		t.Fatalf("%+v %v", final, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 || f.cancels != 0 {
		t.Fatal("resume submitted or cancelled work")
	}
}

func TestCompleteResumeRejectsMixedAndForeignInput(t *testing.T) {
	oa, f := startCompletionFixture(t, true, "complete")
	zero := int64(0)
	for _, in := range []CompleteInput{{OperationID: "operation", Cursor: &zero, Model: "new"}, {OperationID: "operation"}, {Cursor: &zero}, {Model: "chosen/model", Hosting: "local"}, {Model: "chosen/model", Prompt: "prompt"}} {
		if _, err := oa.Complete(context.Background(), in); err == nil {
			t.Fatalf("accepted %+v", in)
		}
	}
	if _, err := oa.Complete(context.Background(), CompleteInput{OperationID: "foreign", Cursor: &zero}); err == nil {
		t.Fatal("foreign handle accepted")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 0 || f.cancels != 0 {
		t.Fatal("invalid resume executed or cancelled")
	}
}

func TestCompleteInitialRefusalPreservesReplyFields(t *testing.T) {
	oa, f := startCompletionFixture(t, true, "refusal")
	out, err := oa.Complete(context.Background(), CompleteInput{Model: "chosen/model", Prompt: "prompt", Hosting: "local"})
	if err != nil || out.Outcome != "no_host" || out.Reason != "router:no_host" || out.StopReason != "no_stop" || out.Text != "" || out.Host != "" || out.Model != "" || out.OperationID != "" {
		t.Fatalf("%+v %v", out, err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 || f.cancels != 0 {
		t.Fatal("refusal retried or cancelled")
	}
}

func TestCompleteStaleResumeGapDoesNotCancel(t *testing.T) {
	oa, f := startCompletionFixture(t, true, "partial_wait")
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	working, err := oa.Complete(ctx, CompleteInput{Model: "chosen/model", Prompt: "prompt", Hosting: "local"})
	if err != nil || working.State != "working" {
		t.Fatalf("%+v %v", working, err)
	}
	f.mu.Lock()
	f.mode = "gap"
	f.mu.Unlock()
	stale := int64(0)
	_, err = oa.Complete(context.Background(), CompleteInput{OperationID: working.OperationID, Cursor: &stale})
	if !errors.Is(err, inferenceclient.ErrGap) {
		t.Fatalf("stale resume: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 || f.cancels != 0 {
		t.Fatalf("requests=%d cancels=%d", len(f.requests), f.cancels)
	}
}

func TestCompleteObservationRefusalAfterContentDoesNotCancel(t *testing.T) {
	oa, f := startCompletionFixture(t, true, "partial_refusal")
	_, err := oa.Complete(context.Background(), CompleteInput{Model: "chosen/model", Prompt: "prompt", Hosting: "local"})
	var refusal *inferenceclient.PageRefusal
	if !errors.As(err, &refusal) {
		t.Fatalf("refusal: %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) != 1 || f.cancels != 0 {
		t.Fatalf("requests=%d cancels=%d", len(f.requests), f.cancels)
	}
}
