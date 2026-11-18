package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// 2026-11-22: a Claude chat's woken turn ended without reading its pointer,
// and every later message waited behind that pointer's 30-minute lease. The
// adapter's turn-end signal releases the held pointer; the next new message
// reserves its own wake at once.
func TestClaudeTurnEndReleasesTheHeldWakePointer(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", codexAttachThread)
	runtime := newChaosRuntime()
	h := startChaosHelper(t, runtime, new(chaosQueue), 181, "oa-claude-integration")
	key := strings.Repeat("5a", 16)
	for until := time.Now().Add(3 * time.Second); ; {
		out := h.call(claudeSessionStartTool, map[string]any{"chat_title": "Coordinator", "adapter_key": key})
		var started map[string]string
		if !out.IsError && json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &started) == nil && started["outcome"] == "observed" && started["adapter"] == "accepted" {
			break
		}
		if time.Now().After(until) {
			t.Fatal("Claude chat did not bind", out.Content[0].(*mcp.TextContent).Text)
		}
		time.Sleep(time.Millisecond)
	}
	watch := func(args map[string]any) gateway.MessagesWatchResult {
		t.Helper()
		args["wait_ms"] = 0
		args["revision"] = ""
		out := h.call("oa_messages_watch", args)
		var value struct {
			gateway.WatchMetadata
			Wake string `json:"wake"`
		}
		if out.IsError || json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &value) != nil {
			t.Fatal(out)
		}
		result := gateway.MessagesWatchResult{WatchMetadata: value.WatchMetadata}
		result.Wake = value.Wake
		return result
	}
	runtime.deliver()
	first := watch(map[string]any{})
	held := watch(map[string]any{"reserve_wake": first.Wake, "adapter_key": key})
	if !validCodexWakeTicket(held.WakeTicket) {
		t.Fatal("first message reserved no pointer", held)
	}
	runtime.deliver()
	second := watch(map[string]any{})
	if busy := watch(map[string]any{"reserve_wake": second.Wake, "adapter_key": key}); busy.WakeTicket != "" || busy.Reason != "receiver:busy" {
		t.Fatal("a second pointer was reserved while the first turn ran", busy)
	}
	// The woken turn ends without a read.
	watch(map[string]any{"turn": "ended", "adapter_key": key})
	next := watch(map[string]any{"reserve_wake": second.Wake, "adapter_key": key})
	if !validCodexWakeTicket(next.WakeTicket) || next.WakeTicket == held.WakeTicket || !validWakePointer(next.WakePointer, next.WakeTicket, true) {
		t.Fatal("the ended turn's pointer still held back the next message", next)
	}
	if again := watch(map[string]any{"reserve_wake": second.Wake, "adapter_key": key}); again.WakeTicket != "" {
		t.Fatal("the same message reserved a second pointer", again)
	}
}
