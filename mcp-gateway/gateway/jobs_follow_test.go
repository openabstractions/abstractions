package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

type jobsFollowFixture struct {
	*fixtureBackend
	admitted bool
	calls    atomic.Int32
	input    jobsFollowInput
	identity wire.RequestIdentity
	result   wire.WorkObservation
	block    bool
	entered  chan struct{}
	joined   chan struct{}
}

func (b *jobsFollowFixture) BeginNativeTool(ctx context.Context, _ string) (context.Context, func(), bool) {
	return ctx, func() {}, b.admitted
}
func (b *jobsFollowFixture) JobsFollowWork(ctx context.Context, id wire.RequestIdentity, after string, waitMS int64) (wire.WorkObservation, error) {
	b.calls.Add(1)
	b.input = jobsFollowInput{After: after, WaitMS: waitMS}
	b.identity = id
	if b.block {
		close(b.entered)
		defer close(b.joined)
		<-ctx.Done()
		return wire.WorkObservation{}, ctx.Err()
	}
	return b.result, nil
}

func followArguments(after string, waitMS int64) map[string]any {
	return map[string]any{"identity": map[string]any{"key": "key", "history_epoch": "epoch", "attempt": 2}, "after": after, "wait_ms": waitMS}
}

func TestJobsFollowNativeAdmissionGeneratedResultAndOpaqueCursor(t *testing.T) {
	b := &jobsFollowFixture{fixtureBackend: &fixtureBackend{}, result: wire.WorkObservation{Outcome: wire.WorkObservationOutcomeUnchanged, Next: "opaque:é", Mechanism: wire.WorkObservationMechanismPolled}}
	_, session := connected(t, b, "operator-spoof", "2026-07-28")
	invoke := func() *mcp.CallToolResult {
		t.Helper()
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolJobsFollow, Arguments: followArguments("opaque:é", 30000)})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	if result := invoke(); !result.IsError || b.calls.Load() != 0 {
		t.Fatal("native admission bypass", result, b.calls.Load())
	}
	b.admitted = true
	result := invoke()
	if result.IsError || b.input.After != "opaque:é" || b.input.WaitMS != 30000 || b.identity.Attempt != 2 {
		t.Fatal(result, b.input)
	}
	assertAppModelUntrusted(t, result)
	var decoded wire.WorkObservation
	if err := wire.FromJSON([]byte(result.Content[0].(*mcp.TextContent).Text), &decoded); err != nil || decoded.Next != "opaque:é" || decoded.Snapshot != nil {
		t.Fatal(decoded, err)
	}
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range listed.Tools {
		if tool.Name == ToolJobsFollow {
			found = true
			if !tool.Annotations.ReadOnlyHint || !tool.Annotations.IdempotentHint || tool.Annotations.OpenWorldHint == nil || *tool.Annotations.OpenWorldHint {
				t.Fatal(tool.Annotations)
			}
		}
	}
	if !found {
		t.Fatal("tool missing")
	}
}

func TestJobsFollowClosedOutcomesAndGeneratedNumbers(t *testing.T) {
	b := &jobsFollowFixture{fixtureBackend: &fixtureBackend{}, admitted: true}
	_, session := connected(t, b, "forged", "2025-11-25")
	for _, outcome := range wire.WorkObservationOutcomeValues() {
		b.result = wire.WorkObservation{Outcome: outcome, Mechanism: wire.WorkObservationMechanismPolled}
		if outcome == wire.WorkObservationOutcomeChanged {
			b.result.Next = "next"
			b.result.Snapshot = &wire.OperationSnapshot{Receipt: wire.Receipt{Identity: wire.RequestIdentity{Key: "key", HistoryEpoch: "epoch", Attempt: 2}, LogicalOwner: "owner", OperationID: "operation", AcceptedGuarantees: []string{}, HistoryRetentionMs: 1}, State: wire.WorkStateRunning, Progress: wire.WorkProgress{Done: 9007199254740993, Total: 9007199254740994}}
		} else if outcome == wire.WorkObservationOutcomeUnchanged || outcome == wire.WorkObservationOutcomeGap {
			b.result.Next = "opaque:é"
		}
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolJobsFollow, Arguments: followArguments("opaque:é", 0)})
		wantError := outcome != wire.WorkObservationOutcomeChanged && outcome != wire.WorkObservationOutcomeUnchanged && outcome != wire.WorkObservationOutcomeGap
		if err != nil || result.IsError != wantError {
			t.Fatal(outcome, result, err)
		}
		raw := result.Content[0].(*mcp.TextContent).Text
		if outcome == wire.WorkObservationOutcomeChanged && !strings.Contains(raw, "9007199254740993") {
			t.Fatal("number changed", raw)
		}
		if wantError && strings.Contains(raw, "snapshot") {
			t.Fatal("refusal leaked snapshot", raw)
		}
	}
}

func TestJobsFollowBoundsAndAuthorityShapedArgumentsRefuseBeforeBackend(t *testing.T) {
	b := &jobsFollowFixture{fixtureBackend: &fixtureBackend{}, admitted: true}
	_, session := connected(t, b, "forged-program", "2026-07-28")
	for _, args := range []any{
		followArguments("", -1), followArguments("", 30001), followArguments(strings.Repeat("é", 129), 0), followArguments("cursor\n", 0),
		json.RawMessage(`{"identity":{"key":"key","history_epoch":"epoch","program":"operator"},"after":"","wait_ms":0}`),
		json.RawMessage(`{"identity":{"key":"key","history_epoch":"epoch"},"after":"","wait_ms":0,"scope":"operator"}`),
		json.RawMessage(`{"identity":{"key":"key","history_epoch":"epoch"},"after":"","wait_ms":0,"endpoint":"other"}`),
	} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolJobsFollow, Arguments: args})
		if err == nil && (result == nil || !result.IsError) {
			t.Fatal("invalid arguments accepted", args, result)
		}
	}
	if b.calls.Load() != 0 {
		t.Fatal("invalid arguments dispatched", b.calls.Load())
	}
}

func TestJobsFollowCancellationJoinsBoundedBackend(t *testing.T) {
	b := &jobsFollowFixture{fixtureBackend: &fixtureBackend{}, admitted: true, block: true, entered: make(chan struct{}), joined: make(chan struct{})}
	_, session := connected(t, b, "cancel-client", "2026-07-28")
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := session.CallTool(ctx, &mcp.CallToolParams{Name: ToolJobsFollow, Arguments: followArguments("cursor", 30000)})
		done <- err
	}()
	select {
	case <-b.entered:
	case <-time.After(time.Second):
		t.Fatal("backend not entered")
	}
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("canceled call succeeded")
		}
	case <-time.After(time.Second):
		t.Fatal("client did not cancel")
	}
	select {
	case <-b.joined:
	case <-time.After(time.Second):
		t.Fatal("backend did not join")
	}
}
