package main

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// Each refusal of the request's own fields, and each read that returns no
// page, says in one sentence what to do next.
func TestMessagesRefusalsNameTheNextStep(t *testing.T) {
	probe := &messagesProbe{}
	b := &messagesBackend{application: "oa-claude-integration", instance: "owned", contextName: "chat-" + strings.Repeat("7", 48), native: func(context.Context) (nativeMessages, error) { return probe, nil }}
	for reason, want := range map[string]string{"lifetime": "Set ttl_ms", "kind": "Set kind", "text": "plain-text body", "receiver": "Name the receiver"} {
		probe.prepareResult = wire.PrepareResult{Outcome: wire.OutcomeInvalid, Reason: reason}
		out, err := b.MessagesPrepare(t.Context(), gateway.MessagesPrepareInput{Receiver: "Codex", Body: "hi", AnswerWithinMs: 1000})
		if err != nil || out.Outcome != "invalid" || out.Reason != reason || !strings.HasPrefix(out.Guidance, "Nothing was sent. ") || !strings.Contains(out.Guidance, want) {
			t.Fatal(reason, out, err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	server, err := gateway.NewServer(b).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "model", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	for outcome, want := range map[wire.Outcome]string{wire.OutcomeGap: "without after", wire.OutcomeUnknown: "same after", wire.OutcomeUnavailable: "oa_messages_inbox_open"} {
		probe.page = wire.Page{Outcome: outcome}
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolMessagesWait, Arguments: map[string]any{"wait_ms": 0}})
		if err != nil || len(out.Content) == 0 || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, want) {
			t.Fatal(outcome, out, err)
		}
	}
}
