package main

import (
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// The OpenCode chat child reserves the same fixed, ticketed pointer as the
// Claude and Codex children. One outstanding pointer covers later messages
// until the woken model reads its inbox.
func TestOpenCodeChatReservesOneWakePointerUntilTheModelReads(t *testing.T) {
	shortenAgentTiming(t)
	_, ctx, _ := diagnosticFixture(t)
	runtime := newChaosRuntime()
	session := openCodeChatSession(t, ctx, runtime, "ses_wake", "Wake chat", 1)
	type publicWatchResult struct {
		gateway.WatchMetadata
		Outcome string `json:"outcome"`
		Wake    string `json:"wake"`
	}
	call := func(name string, args map[string]any) publicWatchResult {
		t.Helper()
		out, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || out.IsError || len(out.Content) != 1 {
			t.Fatal(out, err)
		}
		var value publicWatchResult
		if err := json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	watch := func(args map[string]any) publicWatchResult {
		t.Helper()
		args["wait_ms"] = 0
		return call("oa_messages_watch", args)
	}
	if idle := watch(map[string]any{}); idle.Outcome != "page" || idle.Wake != "" {
		t.Fatal("empty inbox offered a wake", idle)
	}
	runtime.deliver()
	first := watch(map[string]any{})
	if first.Wake == "" {
		t.Fatal("pending message produced no wake", first)
	}
	reserved := watch(map[string]any{"reserve_wake": first.Wake})
	if !validCodexWakeTicket(reserved.WakeTicket) || len(reserved.WakeTicket) != 8 || reserved.WakePointer == "" || !validWakePointer(reserved.WakePointer, reserved.WakeTicket, false) {
		t.Fatal("OpenCode chat reserved no fixed pointer", reserved)
	}
	if again := watch(map[string]any{"reserve_wake": first.Wake}); again.WakeTicket != "" {
		t.Fatal("the same wake reserved twice", again)
	}
	runtime.deliver()
	second := watch(map[string]any{})
	if busy := watch(map[string]any{"reserve_wake": second.Wake}); busy.WakeTicket != "" || busy.Reason != "receiver:busy" {
		t.Fatal("a second message woke before the first pointer was read", busy)
	}
	if refreshed := watch(map[string]any{"reserve_wake": second.Wake, "wake_ticket": reserved.WakeTicket}); refreshed.WakeTicket != reserved.WakeTicket {
		t.Fatal("the held pointer did not refresh", refreshed)
	}
	out, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: gateway.ToolMessagesWait, Arguments: map[string]any{"wait_ms": 0, "wake_ticket": reserved.WakeTicket}})
	if err != nil || out.IsError {
		t.Fatal(out, err)
	}
	runtime.deliver()
	third := watch(map[string]any{})
	next := watch(map[string]any{"reserve_wake": third.Wake})
	if !validCodexWakeTicket(next.WakeTicket) || next.WakeTicket == reserved.WakeTicket || !validWakePointer(next.WakePointer, next.WakeTicket, true) {
		t.Fatal("a read pointer did not release the next wake", next)
	}
}
