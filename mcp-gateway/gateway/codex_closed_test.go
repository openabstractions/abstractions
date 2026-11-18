package gateway

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// A sent message to a closed Codex chat that OA will not wake says so
// plainly (architect ruling 1, 2026-11-22).
func TestSentToClosedCodexSaysCodexIsClosed(t *testing.T) {
	said := func(out wire.SendResult) bool {
		for _, c := range messageReceiptContent(out).Content {
			if text, ok := c.(*mcp.TextContent); ok && text.Text == "Codex is closed; messages wait until it opens." {
				return true
			}
		}
		return false
	}
	if !said(wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "m1", Reason: "codex_closed"}) {
		t.Fatal("a send to a closed Codex chat did not say Codex is closed")
	}
	if said(wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "m1", ReceiverWatching: true}) {
		t.Fatal("a watched send said Codex is closed")
	}
}
