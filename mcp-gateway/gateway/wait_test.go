package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

// slowChat3 reports one progress event, then holds the stream open as a
// provider's Observe does: until its wait ends or the caller leaves. cut
// counts Observes the caller left mid-wait.
type slowChat3 struct {
	chat3CompleteFixture
	cut int
}

func (f *slowChat3) Observe(ctx context.Context, _ string, cursor, _, _, waitMS int64) (wire.ChatPage, error) {
	if cursor == 0 {
		done, total := int64(5), int64(9)
		return wire.ChatPage{Outcome: wire.PageOutcomePage, Next: 1, Events: []wire.ChatEvent{
			{Sequence: 0, Kind: wire.ChatEventKindProgress, Progress: &wire.ChatProgress{Phase: "decode", Done: &done, Total: &total, Unit: "tokens"}}}}, nil
	}
	select {
	case <-time.After(time.Duration(waitMS) * time.Millisecond):
		return wire.ChatPage{Outcome: wire.PageOutcomePage, Next: cursor, Events: []wire.ChatEvent{}}, nil
	case <-ctx.Done():
		f.cut++
		return wire.ChatPage{}, ctx.Err()
	}
}

func TestCompleteWaitAnswersWorkingWithProgress(t *testing.T) {
	var mu sync.Mutex
	var seen []string
	ctx := context.WithValue(context.Background(), progressKey{}, progressSink(func(message string, done, total float64) {
		mu.Lock()
		seen = append(seen, message)
		mu.Unlock()
	}))
	ctx = withCallWait(ctx, 300*time.Millisecond)
	started := time.Now()
	chat := &slowChat3{}
	out, err := waitCompletionChat3(ctx, chat, "operation", 0)
	if err != nil || out.State != "working" || out.OperationID != "operation" || out.Cursor != 1 || time.Since(started) > 3*time.Second {
		t.Fatalf("%+v %v after %s", out, err, time.Since(started))
	}
	// The wait cap ends the last Observe by its own wait, with the call still
	// connected: the provider keeps the operation for the agent's resume.
	if chat.cut != 0 {
		t.Fatalf("%d Observe calls were cut off mid-wait", chat.cut)
	}
	if !strings.HasPrefix(out.Message, "Working: 5 of 9 tokens, ") {
		t.Fatalf("message %q", out.Message)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 || !strings.HasPrefix(seen[len(seen)-1], "Working: 5 of 9 tokens") {
		t.Fatalf("progress %q", seen)
	}
}

func TestWaitingBudgetsFollowWaitMS(t *testing.T) {
	for _, c := range []struct {
		tool, args string
		want       time.Duration
	}{
		{ToolImageGenerate, `{"prompt":"a fox"}`, defaultCallWait + callStartRoom + callFinishRoom},
		{ToolImageGenerate, `{"prompt":"a fox","wait_ms":600000}`, 10*time.Minute + callStartRoom + callFinishRoom},
		{ToolComplete, `{"operation_id":"o","cursor":0,"wait_ms":1800000}`, 30*time.Minute + callStartRoom + callFinishRoom},
		{ToolApplicationsCall, `{"tool":"workflow.run"}`, appCommandWait + callFinishRoom},
		{ToolApplicationsCall, `{"tool":"workflow.run","wait_ms":900000}`, 15*time.Minute + callFinishRoom},
	} {
		if got, ok := waitingBudget(c.tool, json.RawMessage(c.args)); !ok || got != c.want {
			t.Errorf("%s %s: %s, want %s", c.tool, c.args, got, c.want)
		}
	}
	if _, ok := waitingBudget(ToolModels, nil); ok {
		t.Error("a tool without wait_ms got a waiting budget")
	}
}

func TestAppCommandWaitLeavesTheCallRecordUnchanged(t *testing.T) {
	rest, wait, err := appCommandWaitArgument(json.RawMessage(`{"tool":"workflow.run","arguments":{},"wait_ms":900000}`))
	if err != nil || wait != 15*time.Minute || strings.Contains(string(rest), "wait_ms") {
		t.Fatalf("%s %s %v", rest, wait, err)
	}
	raw := json.RawMessage(`{"tool":"workflow.run","arguments":{}}`)
	if rest, wait, err := appCommandWaitArgument(raw); err != nil || wait != appCommandWait || string(rest) != string(raw) {
		t.Fatalf("%s %s %v", rest, wait, err)
	}
	if _, _, err := appCommandWaitArgument(json.RawMessage(`{"wait_ms":-1}`)); err == nil {
		t.Fatal("negative wait accepted")
	}
}

func TestAppCommandHeartbeatReportsWhileRunning(t *testing.T) {
	every := appCommandHeartbeatEvery
	appCommandHeartbeatEvery = 20 * time.Millisecond
	t.Cleanup(func() { appCommandHeartbeatEvery = every })
	var mu sync.Mutex
	var seen []string
	ctx := context.WithValue(context.Background(), progressKey{}, progressSink(func(message string, _, _ float64) {
		mu.Lock()
		seen = append(seen, message)
		mu.Unlock()
	}))
	stop := appCommandHeartbeat(ctx)
	time.Sleep(150 * time.Millisecond)
	stop()
	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 || !strings.HasPrefix(seen[0], "Running the command: ") {
		t.Fatalf("heartbeat %q", seen)
	}
}
