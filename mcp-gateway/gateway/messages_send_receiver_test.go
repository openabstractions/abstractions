package gateway

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

type receiverSendFixture struct {
	messageCorpusFixture
	prepareInputs []MessagesPrepareInput
	refusal       *MessagesPrepareOutput
}

func (b *receiverSendFixture) MessagesPrepare(ctx context.Context, in MessagesPrepareInput) (MessagesPrepareOutput, error) {
	b.prepareInputs = append(b.prepareInputs, in)
	if b.refusal != nil {
		b.prepares++
		return *b.refusal, nil
	}
	return b.messageCorpusFixture.MessagesPrepare(ctx, in)
}

func sendResultText(t *testing.T, r *mcp.CallToolResult) (wire.SendResult, string) {
	t.Helper()
	value, err := wire.DecodeSendResult([]byte(r.Content[0].(*mcp.TextContent).Text))
	if err != nil {
		t.Fatal(err, r.Content)
	}
	rest := ""
	for _, c := range r.Content[1:] {
		rest += c.(*mcp.TextContent).Text
	}
	return *value, rest
}

// Owner, R20c 2026-11-10: OpenCode prepared "hi" for Claude and Codex and
// never sent. One oa_messages_send with receiver and body prepares through
// the native service and sends that exact request.
func TestMessagesSendByReceiverPreparesAndSendsExactRequest(t *testing.T) {
	prepared := MessagesSendInput{To: "app:oa-claude-integration", Context: "chat-ab33", Kind: "question", Body: "hi", IdempotencyKey: "native-key", ExpiresUnixMs: 1900000000000, AnswerByUnixMs: 1899999999000}
	b := &receiverSendFixture{messageCorpusFixture: messageCorpusFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}, prepared: prepared, result: wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "m"}}}
	_, session := connected(t, b, "native-caller", "2025-11-25")
	r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesSend, Arguments: map[string]any{"receiver": "Claude", "body": "hi"}})
	if err != nil || r.IsError {
		t.Fatal(r, err)
	}
	if out, _ := sendResultText(t, r); out.Outcome != wire.OutcomeSent || len(b.sends) != 1 || !reflect.DeepEqual(b.sends[0], prepared) || b.prepares != 1 {
		t.Fatal(out, b.sends, b.prepares)
	}
	if got := b.prepareInputs[0]; got.Receiver != "Claude" || got.Body != "hi" {
		t.Fatal(got)
	}
}

// A refusal, a permission step or a choice comes back as Prepare's own
// outcome with nothing sent.
func TestMessagesSendByReceiverReturnsPrepareRefusalUnsent(t *testing.T) {
	refusal := MessagesPrepareOutput{Outcome: "forbidden", Reason: "access:oa-claude-integration", Guidance: "Use oa_messages_request_access."}
	b := &receiverSendFixture{messageCorpusFixture: messageCorpusFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}, refusal: &refusal}
	_, session := connected(t, b, "native-caller", "2025-11-25")
	r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesSend, Arguments: map[string]any{"receiver": "Claude", "body": "hi"}})
	if err != nil || r.IsError || len(b.sends) != 0 {
		t.Fatal(r, err, b.sends)
	}
	var out MessagesPrepareOutput
	if err := json.Unmarshal([]byte(r.Content[0].(*mcp.TextContent).Text), &out); err != nil || out.Reason != refusal.Reason || !strings.HasPrefix(out.Next, "Nothing was sent.") {
		t.Fatal(out, err)
	}
}

// An uncertain one-call Send names the next step, and the same call again
// reconciles with the identical prepared request instead of preparing anew.
func TestMessagesSendByReceiverReconcilesUncertainSend(t *testing.T) {
	prepared := MessagesSendInput{To: "app:oa-claude-integration", Context: "chat-ab33", Kind: "note", Body: "hi", IdempotencyKey: "native-key", ExpiresUnixMs: 4102444800000}
	b := &receiverSendFixture{messageCorpusFixture: messageCorpusFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}, prepared: prepared, result: wire.SendResult{Outcome: wire.OutcomeUnknown, Reason: "reply_lost"}}}
	_, session := connected(t, b, "native-caller", "2025-11-25")
	args := map[string]any{"receiver": "Claude", "body": "hi"}
	r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesSend, Arguments: args})
	if err != nil || r.IsError {
		t.Fatal(r, err)
	}
	out, next := sendResultText(t, r)
	if out.Outcome != wire.OutcomeUnknown || !strings.Contains(next, "Call oa_messages_send again on this connection") || !strings.Contains(next, `"idempotency_key":"native-key"`) ||
		!strings.Contains(next, "If OpenAbstractions reconnected or this connection closed since, do not send again; tell the person the outcome is not known.") || !strings.Contains(next, "On this same connection OA answers with the original outcome and delivers the message at most once") {
		t.Fatal(out, next)
	}
	b.result = wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "m"}
	r, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesSend, Arguments: args})
	if err != nil || r.IsError || b.prepares != 1 || len(b.sends) != 2 || !reflect.DeepEqual(b.sends[1], prepared) {
		t.Fatal(r, err, b.prepares, b.sends)
	}
	if out, next := sendResultText(t, r); out.Outcome != wire.OutcomeSent || next != "" {
		t.Fatal(out, next)
	}
	// Settled: a later identical message is a new request.
	if _, err = session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesSend, Arguments: args}); err != nil || b.prepares != 2 {
		t.Fatal(err, b.prepares)
	}
}

// The prepared form keeps its exact grammar; mixing the two forms sends nothing.
func TestMessagesSendByReceiverRefusesMixedForms(t *testing.T) {
	b := &receiverSendFixture{messageCorpusFixture: messageCorpusFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}}
	_, session := connected(t, b, "native-caller", "2025-11-25")
	r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesSend, Arguments: map[string]any{"receiver": "Claude", "body": "hi", "to": "app:oa-claude-integration", "idempotency_key": "k"}})
	if err != nil || r.IsError || len(b.sends) != 0 || b.prepares != 0 {
		t.Fatal(r, err, b.sends, b.prepares)
	}
	if out, _ := sendResultText(t, r); out.Outcome != wire.OutcomeInvalid {
		t.Fatal(out)
	}
}

// Prepare's prepared outcome states the next call in one line.
func TestMessagesPrepareToolNamesSendAsNextStep(t *testing.T) {
	b := &receiverSendFixture{messageCorpusFixture: messageCorpusFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}}
	_, session := connected(t, b, "native-caller", "2025-11-25")
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range listed.Tools {
		if tool.Name == ToolMessagesPrepare && !strings.Contains(tool.Description, "Prepare alone sends nothing") {
			t.Fatal(tool.Description)
		}
		if tool.Name == ToolMessagesSend {
			schema, _ := json.Marshal(tool.InputSchema)
			var parsed struct {
				Required   []string                   `json:"required"`
				Properties map[string]json.RawMessage `json:"properties"`
			}
			if err := json.Unmarshal(schema, &parsed); err != nil || !reflect.DeepEqual(parsed.Required, []string{"body"}) || parsed.Properties["receiver"] == nil {
				t.Fatal(string(schema), err)
			}
		}
	}
}

// A one-call Send tells the person the sentence Prepare set for its reach.
func TestMessagesSendByReceiverTellsReachSentence(t *testing.T) {
	for _, c := range []struct{ tell, want, refuse string }{
		{"Codex did not confirm the last wake of that chat. Tell the person: queued; Codex did not confirm the wake.", "did not confirm the wake", "closed"},
		{"That conversation is closed. Tell the person plainly that the chat is closed and the message is waiting there.", "the chat is closed", "did not confirm"},
	} {
		args := MessagesSendInput{To: "app:oa-codex-integration", Context: "chat-ab33", Kind: "note", Body: "hi", IdempotencyKey: "k", ExpiresUnixMs: 4102444800000}
		b := &receiverSendFixture{messageCorpusFixture: messageCorpusFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}, result: wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "m"}}}
		b.refusal = &MessagesPrepareOutput{Outcome: "prepared", Reach: "queued", SendArguments: &args, Tell: c.tell}
		_, session := connected(t, b, "native-caller", "2025-11-25")
		r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesSend, Arguments: map[string]any{"receiver": "Codex", "body": "hi"}})
		if err != nil || r.IsError {
			t.Fatal(r, err)
		}
		out, next := sendResultText(t, r)
		if out.Outcome != wire.OutcomeSent || !strings.Contains(next, c.want) || strings.Contains(next, c.refuse) {
			t.Fatal(out, next)
		}
	}
}
