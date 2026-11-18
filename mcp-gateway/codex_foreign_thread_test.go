package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// callAs makes one tool call on h's connection with the given host metadata,
// as Codex does for another thread that shares the chat's helper.
func callAs(t *testing.T, h *chaosHelper, meta mcp.Meta, name string, arguments map[string]any) *mcp.CallToolResult {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := h.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments, Meta: meta})
	if err != nil {
		t.Fatalf("tool %s: the MCP connection failed: %v", name, err)
	}
	return out
}

func toolAnswer(t *testing.T, out *mcp.CallToolResult) (string, string) {
	t.Helper()
	if len(out.Content) == 0 {
		t.Fatal("tool answer without content")
	}
	text, _ := out.Content[0].(*mcp.TextContent)
	if text == nil {
		t.Fatalf("untyped tool answer: %+v", out.Content[0])
	}
	var answer struct{ Outcome, Reason string }
	_ = json.Unmarshal([]byte(text.Text), &answer)
	return answer.Outcome, answer.Reason
}

// Incident 2026-11-21 and architect ruling 2026-11-22 (backlog/AB-1p.md): one
// Codex helper serves the main chat and its worker threads. A worker thread's
// ordinary tool runs; its chat-bound messaging is refused per call with
// not_permitted; the main chat's receiving, Send, inbox open and wait keep
// working with no restart. A call with no thread metadata is handled the same.
func TestChaosCodexForeignThreadKeepsTheChatReceiving(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	main := mcp.Meta{"threadId": h.thread}
	worker := mcp.Meta{"threadId": "99999999-9999-4999-8999-999999999999"}
	if out := callAs(t, h, main, gateway.ToolMessagesInboxOpen, map[string]any{}); out.IsError {
		t.Fatal("main chat inbox open", out.Content[0].(*mcp.TextContent).Text)
	}

	listed, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == gateway.ToolApplicationsOpen {
			// The incident's first call. Its arguments are refused by the
			// handler itself; the call must reach it.
			if outcome, reason := toolAnswer(t, callAs(t, h, worker, gateway.ToolApplicationsOpen, map[string]any{"handle": false})); outcome == "not_permitted" {
				t.Fatal("worker thread's applications call refused as chat-bound", reason)
			}
		}
	}
	models := h.backend.models.Load()
	if out := callAs(t, h, worker, gateway.ToolModels, map[string]any{}); out.IsError {
		t.Fatal("worker thread's ordinary tool refused", out.Content[0].(*mcp.TextContent).Text)
	}
	if h.backend.models.Load() != models+1 {
		t.Fatal("worker thread's ordinary tool did not run")
	}

	sends := chaosChatSends.Load()
	for _, meta := range []mcp.Meta{worker, nil, {"threadId": "not-a-thread"}} {
		for _, call := range []struct {
			name string
			args map[string]any
		}{
			{gateway.ToolMessagesSend, map[string]any{"receiver": "Claude", "body": "from a worker thread"}},
			{gateway.ToolMessagesInboxOpen, map[string]any{}},
			{gateway.ToolMessagesWait, map[string]any{"wait_ms": 0}},
			{"oa_messages_watch", map[string]any{"wait_ms": 0}},
			{gateway.ToolMessagesPrepare, map[string]any{"receiver": "Claude", "body": "from a worker thread"}},
			{gateway.ToolMessagesReply, map[string]any{"message_id": "m1", "body": "from a worker thread"}},
		} {
			out := callAs(t, h, meta, call.name, call.args)
			outcome, reason := toolAnswer(t, out)
			if !out.IsError || outcome != "not_permitted" || reason != codexForeignThreadReason {
				t.Fatalf("%v %s: want not_permitted with the ruling's reason, got %s %q", meta, call.name, outcome, reason)
			}
		}
	}
	if chaosChatSends.Load() != sends {
		t.Fatal("a foreign thread's send reached the chat route")
	}

	if out := callAs(t, h, main, gateway.ToolMessagesInboxOpen, map[string]any{}); out.IsError {
		t.Fatal("main chat inbox open after worker calls", out.Content[0].(*mcp.TextContent).Text)
	}
	h.recovered(5 * time.Second)
	out := callAs(t, h, main, gateway.ToolMessagesSend, map[string]any{
		"to": "app:oa-claude-integration", "context": "target", "kind": "note", "body": "from the main chat",
		"idempotency_key": "foreign-thread-main-send", "expires_unix_ms": time.Now().Add(time.Hour).UnixMilli()})
	if text := out.Content[0].(*mcp.TextContent).Text; out.IsError || !strings.Contains(text, `"sent"`) {
		t.Fatal("main chat Send after worker calls", text)
	}
	if chaosChatSends.Load() != sends+1 {
		t.Fatal("main chat Send did not use the chat route")
	}
	// The receiver kept running: a new message wakes the main thread.
	runtime.deliver()
	woken(t, queue, 1, 5*time.Second)
	queue.mu.Lock()
	woke := queue.threads[h.thread]
	queue.mu.Unlock()
	if woke != 1 {
		t.Fatal("the wake did not reach the bound thread", queue.threads)
	}
	h.consumePointer(queue)
	if record := h.record(); record.Receiving != nil && record.Receiving.Reason == "thread_changed" {
		t.Fatalf("a foreign thread stopped receiving: %+v", record.Receiving)
	}
}
