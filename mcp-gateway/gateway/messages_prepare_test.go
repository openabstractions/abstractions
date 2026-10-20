package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"reflect"
	"testing"
)

type prepareFixture struct {
	messageFixture
	input  MessagesPrepareInput
	calls  int
	result *MessagesPrepareOutput
}

// Ports the public behavior in OpenCode messages.test.mjs: native preparation
// supplies the exact Send arguments, and uncertain effects never cause another
// Prepare, Send, Reply or body acknowledgment inside the tool adapter.
type messageCorpusFixture struct {
	messageFixture
	prepared MessagesSendInput
	prepares int
	sends    []MessagesSendInput
	replies  []MessagesReplyInput
	reads    []MessagesWaitInput
	result   wire.SendResult
	failure  error
}

func (b *messageCorpusFixture) MessagesPrepare(_ context.Context, _ MessagesPrepareInput) (MessagesPrepareOutput, error) {
	b.prepares++
	return MessagesPrepareOutput{Outcome: "prepared", SendArguments: &b.prepared}, nil
}
func (b *messageCorpusFixture) MessagesSend(_ context.Context, in MessagesSendInput) (wire.SendResult, error) {
	b.sends = append(b.sends, in)
	return b.result, b.failure
}
func (b *messageCorpusFixture) MessagesReply(_ context.Context, in MessagesReplyInput) (wire.SendResult, error) {
	b.replies = append(b.replies, in)
	return b.result, b.failure
}
func (b *messageCorpusFixture) MessagesWait(ctx context.Context, in MessagesWaitInput) (wire.Page, error) {
	b.reads = append(b.reads, in)
	return b.messageFixture.MessagesWait(ctx, in)
}

func TestMessagesCorpusPreparedArgumentsSurviveUnknownSend(t *testing.T) {
	prepared := MessagesSendInput{To: "app:receiver", Context: "native-destination", Kind: "question", Subject: "review", Body: "untrusted body", IdempotencyKey: "native-prepared-key", ExpiresUnixMs: 1900000000000, AnswerByUnixMs: 1899999999000}
	b := &messageCorpusFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}, prepared: prepared, result: wire.SendResult{Outcome: wire.OutcomeUnknown}}
	_, session := connected(t, b, "native-caller", "2025-11-25")
	r, out := call[MessagesPrepareOutput](t, session, ToolMessagesPrepare, MessagesPrepareInput{Receiver: "OpenCode", Body: "review"})
	if r.IsError || out.Outcome != "prepared" || !reflect.DeepEqual(out.SendArguments, &prepared) || b.prepares != 1 || len(b.sends) != 0 {
		t.Fatal(r, out, b.prepares, b.sends)
	}
	for i := 1; i <= 2; i++ { // The second call is explicit identical reconciliation.
		r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesSend, Arguments: out.SendArguments})
		if err != nil || r.IsError || len(b.sends) != i || b.prepares != 1 || !reflect.DeepEqual(b.sends[i-1], prepared) {
			t.Fatal(r, err, b.sends, b.prepares)
		}
		value, err := wire.DecodeSendResult([]byte(r.Content[0].(*mcp.TextContent).Text))
		if err != nil || value.Outcome != wire.OutcomeUnknown || len(b.reads) != 0 || len(b.replies) != 0 {
			t.Fatal(value, err, b.reads, b.replies)
		}
	}
}

func TestMessagesCorpusUncertainReplyNeverFallsBackOrAcknowledges(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "unknown", true: "lost-reply"}[failed], func(t *testing.T) {
			b := &messageCorpusFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}, result: wire.SendResult{Outcome: wire.OutcomeUnknown}}
			if failed {
				b.failure = errors.New("native reply lost")
			}
			_, session := connected(t, b, "native-caller", "2025-11-25")
			in := MessagesReplyInput{MessageID: "question-read-here", Body: "exact reply"}
			r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesReply, Arguments: in})
			if err != nil || r.IsError != failed || len(b.replies) != 1 || b.replies[0] != in || len(b.sends) != 0 || b.prepares != 0 || len(b.reads) != 0 {
				t.Fatal(r, err, b.replies, b.sends, b.prepares, b.reads)
			}
			for _, field := range []string{"to", "context", "instance", "idempotency_key", "expires_unix_ms"} {
				r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesReply, Arguments: map[string]any{"message_id": in.MessageID, "body": in.Body, field: "forged"}})
				if err == nil && !r.IsError {
					t.Fatalf("reply accepted caller routing: %s", field)
				}
			}
			if len(b.replies) != 1 {
				t.Fatal("invalid reply dispatched", b.replies)
			}
		})
	}
}

func TestMessagesCorpusLostSendHasNoImplicitReconciliation(t *testing.T) {
	b := &messageCorpusFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}, failure: errors.New("native send reply lost")}
	_, session := connected(t, b, "native-caller", "2025-11-25")
	in := MessagesSendInput{To: "app:receiver", Context: "target", Kind: "note", Subject: "note", Body: "exact body", IdempotencyKey: "retained-key", ExpiresUnixMs: 1900000000000}
	r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesSend, Arguments: in})
	if err != nil || !r.IsError || len(b.sends) != 1 || !reflect.DeepEqual(b.sends[0], in) || b.prepares != 0 || len(b.reads) != 0 || len(b.replies) != 0 {
		t.Fatal(r, err, b.sends, b.prepares, b.reads, b.replies)
	}
}

func (b *prepareFixture) MessagesPrepare(_ context.Context, in MessagesPrepareInput) (MessagesPrepareOutput, error) {
	b.calls++
	b.input = in
	if b.result != nil {
		return *b.result, nil
	}
	return MessagesPrepareOutput{Outcome: "conflict", Guidance: "ambiguous_receiver"}, nil
}

func TestMessagesPrepareChoicesMatchOutputSchema(t *testing.T) {
	for _, reach := range wire.InboxReachValues() {
		t.Run(reach.String(), func(t *testing.T) {
			out := MessagesPrepareOutput{Outcome: "conflict", Guidance: "ambiguous_receiver", Choices: []wire.Inbox{{Program: "app:receiver", Context: "chat", Revision: "r", Reach: &reach}}}
			b := &prepareFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}, result: &out}
			_, session := connected(t, b, "native-caller", "2025-11-25")
			r, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesPrepare, Arguments: MessagesPrepareInput{Receiver: "Claude", Body: "inert"}})
			if err != nil || r.IsError {
				t.Fatalf("authorized choice rejected: %v, %+v", err, r)
			}
			tools, err := session.ListTools(t.Context(), nil)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, tool := range tools.Tools {
				if tool.Name != ToolMessagesPrepare {
					continue
				}
				found = true
				raw, _ := json.Marshal(tool.OutputSchema)
				var schema jsonschema.Schema
				if err := json.Unmarshal(raw, &schema); err != nil {
					t.Fatal(err)
				}
				resolved, err := schema.Resolve(nil)
				if err != nil {
					t.Fatal(err)
				}
				var value map[string]any
				raw, _ = json.Marshal(out)
				if err := json.Unmarshal(raw, &value); err != nil {
					t.Fatal(err)
				}
				if err := resolved.Validate(value); err != nil {
					t.Fatal(err)
				}
				choice := value["choices"].([]any)[0].(map[string]any)
				for _, bad := range []any{"wrong_reach", 1} {
					choice["Reach"] = bad
					if err := resolved.Validate(value); err == nil {
						t.Fatalf("invalid reach admitted: %v", bad)
					}
				}
				choice["Reach"] = nil
				if err := resolved.Validate(value); err != nil {
					t.Fatalf("absent optional reach rejected: %v", err)
				}
			}
			if !found {
				t.Fatal("prepare tool missing")
			}
		})
	}
}

func TestMessagesPreparePublicInputProjectsToNativeBackend(t *testing.T) {
	b := &prepareFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}
	_, session := connected(t, b, "native-caller", "2025-11-25")
	in := MessagesPrepareInput{Receiver: "OpenCode", Context: "chosen", Kind: "question", Subject: "subject", Body: "body", TTLMs: 900000, AnswerWithinMs: 60000}
	_, out := call[MessagesPrepareOutput](t, session, ToolMessagesPrepare, in)
	if out.Outcome != "conflict" || out.Guidance != "ambiguous_receiver" || !reflect.DeepEqual(b.input, in) {
		t.Fatal(out, b.input)
	}
	for _, field := range []string{"to", "idempotency_key", "expires_unix_ms", "answer_by_unix_ms", "instance", "source_context", "peer_key"} {
		result, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesPrepare, Arguments: map[string]any{"receiver": "Codex", "body": "body", field: "forged"}})
		if err == nil && !result.IsError {
			t.Fatal("authority/routing field accepted", field)
		}
	}
	if b.calls != 1 {
		t.Fatal(b.calls)
	}
}

func TestMessagesReceiverAliasesAreDisplayProjection(t *testing.T) {
	for label, want := range map[string]string{"Claude": "app:oa-claude-integration", "Claude Code": "app:oa-claude-integration", "Codex": "app:oa-codex-integration", " OpenCode ": "app:oa-opencode-integration", "app:custom": "app:custom", "custom": "app:custom"} {
		if got := MessageReceiverResource(label); got != want {
			t.Fatal(label, got, want)
		}
	}
}
