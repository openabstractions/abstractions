package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestOpenCodeChatReattachesAfterRestartWithLostInboxOpenReply(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	const id = "ses_restart"
	helper := startChaosHelper(t, runtime, queue, 0, "oa-opencode-integration", id)
	for deadline := time.Now().Add(3 * time.Second); ; {
		if out := helper.call(openCodeChatStartTool, map[string]any{}); !out.IsError {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("chat did not bind before the restart")
		}
		time.Sleep(10 * time.Millisecond)
	}
	canonical := fixtureNativeChatContext(id)
	runtime.mu.Lock()
	original := runtime.holder[canonical]
	runtime.lostChatStatus = "lost reply"
	runtime.mu.Unlock()
	if original == "" {
		t.Fatal("canonical inbox not admitted")
	}
	out := helper.call(gateway.ToolMessagesInboxOpen, map[string]any{"status": "lost reply"})
	if !strings.Contains(out.Content[0].(*mcp.TextContent).Text, `"unknown"`) {
		t.Fatal("did not lose the explicit Open reply", out)
	}
	runtime.restart(false)
	for deadline := time.Now().Add(5 * time.Second); ; {
		runtime.mu.Lock()
		current := runtime.holder[canonical]
		runtime.mu.Unlock()
		if current != "" && current != original {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("idle chat did not reattach to its canonical inbox after runtime restart")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if out := helper.call(gateway.ToolMessagesInboxesList, map[string]any{}); out.IsError {
		t.Fatal("existing MCP session could not list inboxes after restart", out)
	}
}

func TestOpenCodeStartUsesTextOnlyUnavailableUntilChatAllocates(t *testing.T) {
	shortenAgentTiming(t)
	runtime := newChaosRuntime()
	runtime.set(func(r *chaosRuntime) { r.allocationFault, r.allocationFaults = "unavailable", 1 })
	const chat = "ses_mcp_start_pending"
	helper := startChaosHelper(t, runtime, new(chaosQueue), 0, "oa-opencode-integration", chat)
	pending := helper.call(openCodeChatStartTool, map[string]any{})
	if !pending.IsError || pending.StructuredContent != nil || len(pending.Content) != 1 {
		t.Fatalf("pending lifecycle frame: %+v", pending)
	}
	part, ok := pending.Content[0].(*mcp.TextContent)
	var fields map[string]string
	if !ok || json.Unmarshal([]byte(part.Text), &fields) != nil {
		t.Fatalf("pending lifecycle disposition: %+v", pending)
	}
	contextName, present := fields["context"]
	if !present || contextName != "" || len(fields) != 3 || fields["outcome"] != "unavailable" || fields["session_id"] != chat {
		t.Fatalf("pending lifecycle disposition: %+v", pending)
	}

	for deadline := time.Now().Add(3 * time.Second); ; {
		out := helper.call(openCodeChatStartTool, map[string]any{})
		if !out.IsError {
			var observed struct {
				Outcome string
				Context string
				Session string `json:"session_id"`
			}
			if err := json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &observed); err != nil ||
				observed.Outcome != "observed" || observed.Session != chat || observed.Context != fixtureNativeChatContext(chat) {
				t.Fatalf("recovered lifecycle disposition: %+v, %v", observed, err)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the same helper did not register the chat after allocation recovered")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestOpenCodeHelperPerChatMCPBindingAndMissingMetadataRefusal(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	first := startChaosHelper(t, runtime, queue, 0, "oa-opencode-integration", "ses_A opaque host id")
	second := startChaosHelper(t, runtime, queue, 1, "oa-opencode-integration", "ses_B opaque host id")
	missing := startChaosHelper(t, runtime, queue, 2, "oa-opencode-integration", "")
	var contexts []string
	for _, h := range []*chaosHelper{first, second, missing} {
		listed, err := h.session.ListTools(t.Context(), nil)
		if err != nil {
			t.Fatal(err)
		}
		names := map[string]int{}
		for _, tool := range listed.Tools {
			names[tool.Name]++
		}
		for _, name := range append(gateway.MessagesReceiverToolNames(), gateway.AsksToolNames()...) {
			if names[name] != 1 {
				t.Fatal("native helper roster", name, names)
			}
		}
		for _, name := range []string{gateway.ToolMessagesInboxesList, gateway.ToolMessagesInboxOpen} {
			out, err := h.session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
			if err != nil || !out.IsError {
				t.Fatal("unbound child served Messages", name, out, err)
			}
		}
		var observed struct {
			Outcome, Context string
			ID               string `json:"session_id"`
		}
		for until := time.Now().Add(3 * time.Second); time.Now().Before(until); {
			out, err := h.session.CallTool(t.Context(), &mcp.CallToolParams{Name: openCodeChatStartTool, Arguments: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			if len(out.Content) != 1 {
				t.Fatal(out)
			}
			if err := json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &observed); err != nil {
				t.Fatal(err)
			}
			if h == missing || observed.Outcome == "observed" {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
		if h == missing {
			if observed.Outcome != "unavailable" || observed.Context != "" {
				t.Fatal("missing host ID supplied ownership", observed)
			}
			continue
		}
		if observed.Outcome != "observed" || observed.Context == "" || !validChatBindingID(observed.ID) {
			t.Fatal(observed)
		}
		contexts = append(contexts, observed.Context)
		out, err := h.session.CallTool(t.Context(), &mcp.CallToolParams{Name: openCodeChatStartTool, Arguments: map[string]any{"chat_id": "forged-other-chat"}})
		if err == nil && !out.IsError {
			t.Fatal("model input selected the child correlation", out)
		}
		out, err = h.session.CallTool(t.Context(), &mcp.CallToolParams{Name: gateway.ToolMessagesInboxOpen, Arguments: map[string]any{}})
		if err != nil || out.IsError {
			t.Fatal("bound chat unavailable", out, err)
		}
	}
	if len(contexts) != 2 || contexts[0] == contexts[1] {
		t.Fatal("two chat children shared a context", contexts)
	}
	runtime.mu.Lock()
	count := len(runtime.chatBindings)
	runtime.mu.Unlock()
	if count != 2 {
		t.Fatal("unbound child created a chat", count)
	}
	if attempts, submitted := queue.snapshot(); attempts != 0 || submitted != 0 {
		t.Fatal("OpenCode idle wake enabled", attempts, submitted)
	}
}
