package main

import (
	"context"
	"encoding/json"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// chaosStallBudget is the caller's budget for one presence or preflight call
// to a suspended runtime: every such call has two seconds.
const chaosStallBudget = 2 * time.Second

// stall holds a call while the runtime is suspended. A suspended process
// answers nothing: a presence or preflight call ends with its caller's
// budget, and an effect dispatched meanwhile is answered when the process
// resumes.
func (r *chaosRuntime) stall(effect bool) error {
	r.mu.Lock()
	stalled := r.stalled
	r.mu.Unlock()
	if stalled == nil {
		return nil
	}
	if effect {
		<-stalled
		return nil
	}
	select {
	case <-stalled:
		return nil
	case <-time.After(chaosStallBudget):
		return context.DeadlineExceeded
	}
}

// suspend stops the runtime answering for d, as NtSuspendProcess does in
// the J-MSG journey.
func (r *chaosRuntime) suspend(d time.Duration) {
	stalled := make(chan struct{})
	r.set(func(r *chaosRuntime) { r.stalled = stalled })
	time.AfterFunc(d, func() {
		r.set(func(r *chaosRuntime) { r.stalled = nil })
		close(stalled)
	})
}

var chaosChatSends atomic.Int64

func (r *chaosRuntime) SendChat(_, _ string, _ msg.ChatSendRequest) (msg.SendResult, error) {
	r.actions.Add(1)
	_ = r.stall(true)
	if err := r.fault(); err != nil {
		return msg.SendResult{}, err
	}
	chaosChatSends.Add(1)
	return msg.SendResult{Outcome: msg.OutcomeSent, MessageID: "chaos-chat-message"}, nil
}

// Journey J-MSG, 2026-11-10: OpenCode's Send, issued while the runtime was
// suspended for 14 s, answered "reconnecting" after 10 s; the next Sends
// answered chat_owner_unavailable at once. The first Send now waits for the
// connection and its chat to be admitted again and is delivered once.
func TestMessagesSendDuringRuntimeStallSucceedsOnFirstCall(t *testing.T) {
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := startChaosHelper(t, runtime, queue, 0, "oa-opencode-integration", "ses_stall opaque host id")
	bound := false
	for until := time.Now().Add(10 * time.Second); !bound && time.Now().Before(until); {
		out := h.call(openCodeChatStartTool, map[string]any{})
		var observed struct{ Outcome string }
		_ = json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &observed)
		bound = observed.Outcome == "observed"
		if !bound {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !bound {
		t.Fatal("chat not bound")
	}
	before := chaosChatSends.Load()
	const stall = 14 * time.Second
	started := time.Now()
	runtime.suspend(stall)
	time.Sleep(200 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	out, err := h.session.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolMessagesSend, Arguments: map[string]any{
		"to": "app:oa-claude-integration", "context": "target", "kind": "note", "body": "hello during a stall",
		"idempotency_key": "stall-send", "expires_unix_ms": time.Now().Add(time.Hour).UnixMilli()}})
	if err != nil {
		t.Fatal("the MCP connection failed", err)
	}
	text := out.Content[0].(*mcp.TextContent).Text
	if out.IsError || !strings.Contains(text, `"outcome": "sent"`) && !strings.Contains(text, `"outcome":"sent"`) {
		t.Fatalf("the Send in flight during a %s stall did not complete after %s: %s", stall, time.Since(started).Round(time.Millisecond), text)
	}
	if sent := chaosChatSends.Load() - before; sent != 1 {
		t.Fatal("deliveries", sent)
	}
	if elapsed := time.Since(started); elapsed < stall {
		t.Fatal("the Send completed while the runtime was suspended", elapsed)
	}
}
