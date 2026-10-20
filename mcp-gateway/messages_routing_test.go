package main

import (
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// Architect ruling 2026-11-12 (messages CONTRACT, "Program-only routing and
// chat disclosure"): the gateway words the runtime's program-only outcomes and
// names the chat a prepared request reaches.
func TestMessagesPrepareProgramOnlyOutcomes(t *testing.T) {
	b, p := reply1884Fixture()

	p.prepareResult = wire.PrepareResult{Outcome: wire.OutcomeUnavailable, Reason: "no_reachable_chat"}
	out, err := b.MessagesPrepare(t.Context(), gateway.MessagesPrepareInput{Receiver: "Claude", Body: "body"})
	if err != nil || out.Outcome != "no_reachable_chat" || !strings.Contains(out.Guidance, "No Claude chat") || !strings.Contains(out.Guidance, "nothing was sent") || len(out.Choices) != 0 || out.SendArguments != nil {
		t.Fatal("no_reachable_chat", out, err)
	}

	choices := []wire.Inbox{
		{Program: "app:oa-claude-integration", Context: "internal-one", ChatTitle: "Pictures", Revision: "r1", LastSeenUnixMs: 2000, Reach: new(wire.InboxReachOpen)},
		{Program: "app:oa-claude-integration", Context: "internal-two", ChatTitle: "Reports", Revision: "r2", LastSeenUnixMs: 1000, Reach: new(wire.InboxReachOpen)},
	}
	p.prepareResult = wire.PrepareResult{Outcome: wire.OutcomeConflict, Reason: "ambiguous_receiver", Choices: choices}
	out, err = b.MessagesPrepare(t.Context(), gateway.MessagesPrepareInput{Receiver: "Claude", Body: "body"})
	if err != nil || out.Outcome != "ambiguous_receiver" || len(out.Choices) != 2 ||
		!strings.HasPrefix(out.Guidance, "Several Claude chats are open. If the person's words name one of these, resend with its context; otherwise ask the person in your own chat which one, showing these titles:") ||
		!strings.Contains(out.Guidance, `"Pictures"`) || !strings.Contains(out.Guidance, `"Reports"`) || strings.Contains(out.Guidance, "internal-one") {
		t.Fatal("ambiguous program-only send", out, err)
	}

	prepared := wire.SendRequest{To: "app:oa-claude-integration", Context: "internal-one", Kind: wire.KindQuestion, Body: "body", IdempotencyKey: "native-key", ExpiresUnixMs: 987654321000, AnswerByUnixMs: 987654320000}
	p.prepareResult = wire.PrepareResult{Outcome: wire.OutcomePage, SendRequest: &prepared, ChatTitle: "Pictures"}
	out, err = b.MessagesPrepare(t.Context(), gateway.MessagesPrepareInput{Receiver: "Claude", Body: "body"})
	if err != nil || out.Outcome != "prepared" || out.ChatTitle != "Pictures" || out.Context != "internal-one" || out.SendArguments == nil || out.SendArguments.Context != "internal-one" {
		t.Fatal("prepared result does not name its chat", out, err)
	}
}
