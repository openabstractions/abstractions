package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// Exercise the actual source admission gate as well as the canonical receiver.
// A replacement child uses the same host conversation and a fresh native source.
func TestClaudeUpgradeWithSupervisedSourceKeepsReplacementReceiving(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "local_upgrade_composition")
	runtime := newChaosRuntime()
	queue := new(chaosQueue)
	start := func(index int) (*chaosHelper, map[string]string) {
		t.Helper()
		h := startChaosHelper(t, runtime, queue, index, "oa-claude-integration")
		// Native source admission supplies sending before the host companion
		// supplies its independent session correlation. Body tools stay unbound.
		sent := h.call(gateway.ToolMessagesSend, map[string]any{
			"to": "app:receiver", "context": "target", "kind": "note", "subject": "source probe", "body": "inert fixture",
			"idempotency_key": "source-probe", "expires_unix_ms": time.Now().Add(time.Minute).UnixMilli(),
		})
		var sendResult struct{ Outcome string }
		if err := json.Unmarshal([]byte(sent.Content[0].(*mcp.TextContent).Text), &sendResult); err != nil || sent.IsError || sendResult.Outcome != "sent" {
			t.Fatal("admitted unbound source could not send", sent.Content[0].(*mcp.TextContent).Text)
		}
		for _, out := range []*mcp.CallToolResult{h.wait(), h.call(gateway.ToolMessagesInboxOpen, map[string]any{})} {
			if !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "unavailable") {
				t.Fatal("unbound source exposed receiving", out.Content)
			}
		}
		out := h.call(claudeSessionStartTool, map[string]any{})
		if out.IsError {
			t.Fatal("host lifecycle failed", out.Content)
		}
		var binding map[string]string
		if err := json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &binding); err != nil || binding["outcome"] != "observed" {
			t.Fatal(binding, err)
		}
		h.recovered(3 * time.Second)
		if out := h.call(gateway.ToolMessagesInboxOpen, map[string]any{}); out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "opened") {
			t.Fatal("replacement explicit open failed", out.Content)
		}
		return h, binding
	}
	old, before := start(90)
	current, after := start(91)
	if before["context"] == "" || before["context"] != after["context"] || before["child_instance"] == after["child_instance"] {
		t.Fatal("replacement lost canonical context or reused child", before, after)
	}
	if out := old.wait(); !out.IsError {
		t.Fatal("retired source retained body access", out.Content)
	}
	// A delayed teardown captured by the old Mod cannot end the new child.
	if out := current.call(claudeSessionEndTool, map[string]any{"child_instance": before["child_instance"], "session_id": before["session_id"], "retire": true}); !out.IsError {
		t.Fatal("old teardown retired replacement", out.Content)
	}
	old.close()
	current.recovered(3 * time.Second)
	if out := current.call(claudeSessionStartTool, map[string]any{}); out.IsError {
		t.Fatal("host observer could not reattach to replacement", out.Content)
	}
	current.recovered(3 * time.Second)
}
