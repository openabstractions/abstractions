package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

type deliveryMCPFixture struct {
	messageFixture
	page   wire.Page
	notify *bool
}

func (b *deliveryMCPFixture) MessagesWait(context.Context, MessagesWaitInput) (wire.Page, error) {
	return b.page, nil
}
func (b *deliveryMCPFixture) MessagesSend(_ context.Context, in MessagesSendInput) (wire.SendResult, error) {
	b.notify = in.NotifyDelivered
	return wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "accepted", ContextID: "thread"}, nil
}

func TestMessagesDeliveryMCPUsesNativeEvidenceAndOptionalSuccess(t *testing.T) {
	b := &deliveryMCPFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}, page: wire.Page{Outcome: wire.OutcomePage, Messages: []wire.Message{{Header: wire.Header{MessageID: "accepted", From: "app:receiver", FromContext: "target", To: "app:sender", Context: "source", Kind: wire.KindNote, ContextID: "thread", ExpiresUnixMs: 1234, Delivery: new(wire.DeliveryUndelivered), Undelivered: new(wire.UndeliveredReasonExpired), Count: new(int64(1))}, IdempotencyKey: "delivery-event"}}, Headers: []wire.Header{}}}
	_, session := connected(t, b, "arbitrary-client", "2025-11-25")
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == ToolMessagesSend {
			raw, _ := json.Marshal(tool.InputSchema)
			if !strings.Contains(string(raw), "notify_delivered") {
				t.Fatal("send schema omitted success opt-in", string(raw))
			}
		}
		if tool.Name == ToolMessagesWait {
			raw, _ := json.Marshal(tool.OutputSchema)
			for _, field := range []string{"delivery", "undelivered", "count"} {
				if !strings.Contains(string(raw), `"`+field+`"`) {
					t.Fatal("wait schema omitted delivery evidence", field, string(raw))
				}
			}
		}
	}
	sent, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolMessagesSend, Arguments: map[string]any{"to": "app:receiver", "context": "target", "kind": "note", "subject": "", "body": "note", "idempotency_key": "key", "expires_unix_ms": 1234, "notify_delivered": true}})
	if err != nil || sent.IsError || b.notify == nil || !*b.notify {
		t.Fatal(sent, err, b.notify)
	}
	page, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolMessagesWait, Arguments: map[string]any{"wait_ms": 0}})
	if err != nil || page.IsError {
		t.Fatal(page, err)
	}
	raw, err := json.Marshal(page.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	native, err := wire.DecodePage(raw)
	if err != nil || len(native.Messages) != 1 || native.Messages[0].Header.Delivery == nil || *native.Messages[0].Header.Delivery != wire.DeliveryUndelivered || native.Messages[0].Header.Undelivered == nil || *native.Messages[0].Header.Undelivered != wire.UndeliveredReasonExpired {
		t.Fatal(string(raw), err)
	}
}
