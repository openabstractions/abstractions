package main

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// Prepare's send_arguments carry a question's deadlines through the MCP
// object unchanged, so Send decodes the exact prepared request.
func TestMessagesPreparedQuestionArgumentsRoundTripToSend(t *testing.T) {
	for _, to := range []string{"app:oa-claude-integration", "app:oa-codex-integration"} {
		prepared := wire.SendRequest{To: to, Context: "chat-ab33", Kind: wire.KindQuestion, Subject: "Branch", Body: "Which branch holds the fix?", IdempotencyKey: "key-1", ExpiresUnixMs: 1794163000000, AnswerByUnixMs: 1794162900000}
		var args gateway.MessagesSendInput
		if err := json.Unmarshal(wire.EncodeSendRequest(&prepared), &args); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(args)
		if err != nil {
			t.Fatal(err)
		}
		sent, err := wire.DecodeSendRequest(raw)
		if err != nil || !reflect.DeepEqual(*sent, prepared) {
			t.Fatal(to, sent, err)
		}
	}
}

func TestMessagesSendShapeRefusalNamesTheField(t *testing.T) {
	good := gateway.MessagesSendInput{To: "app:oa-claude-integration", Context: "chat-ab33", Kind: "question", Body: "b", IdempotencyKey: "k", ExpiresUnixMs: 1794163000000, AnswerByUnixMs: 1794162900000}
	cases := []struct {
		field  string
		change func(*gateway.MessagesSendInput)
	}{
		{"idempotency_key", func(in *gateway.MessagesSendInput) { in.IdempotencyKey = "" }},
		{"kind", func(in *gateway.MessagesSendInput) { in.Kind = "" }},
		{"kind", func(in *gateway.MessagesSendInput) { in.Kind = "answer" }},
		{"context", func(in *gateway.MessagesSendInput) { in.Context = "" }},
		{"to", func(in *gateway.MessagesSendInput) { in.To = "" }},
		{"subject", func(in *gateway.MessagesSendInput) { in.Subject = strings.Repeat("s", 321) }},
		{"body", func(in *gateway.MessagesSendInput) { in.Body = strings.Repeat("b", 8193) }},
	}
	b := &messagesBackend{}
	for _, c := range cases {
		in := good
		c.change(&in)
		out, err := b.MessagesSend(t.Context(), in)
		if err != nil || out.Outcome != wire.OutcomeInvalid || !strings.Contains(out.Reason, c.field) || len(out.Reason) > 64 {
			t.Fatal(c.field, out, err)
		}
		if _, err := wire.DecodeSendResult(wire.EncodeSendResult(&out)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMessagesPrepareAmbiguityListsChoiceTitles(t *testing.T) {
	guidance := messagePrepareChoiceGuidance([]wire.Inbox{{ChatTitle: "AI abstractions (ab33)"}, {ChatTitle: "AI abstractions (cd44)"}, {}})
	for _, want := range []string{"More than one", `"AI abstractions (ab33)"`, `"AI abstractions (cd44)"`, "an unnamed chat", "chosen choice's context as context", "ask the person which chat", "keep the context out of the conversation", "rename it in its app", "cannot establish which chat the person used"} {
		if !strings.Contains(guidance, want) {
			t.Fatal(want, guidance)
		}
	}
	single := messagePrepareChoiceGuidance([]wire.Inbox{{ChatTitle: "Untitled chat · last active 14:05"}})
	if strings.Contains(single, "More than one") || !strings.Contains(single, `"Untitled chat · last active 14:05"`) || !strings.Contains(single, "chosen choice's context as context") {
		t.Fatal(single)
	}
}
