package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"reflect"
	"strings"
	"testing"
)

func reply1884Fixture() (*messagesBackend, *messagesProbe) {
	p := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Messages: []wire.Message{{Header: wire.Header{MessageID: "question-5", From: "app:sender", FromContext: "source", To: "app:receiver", Context: "owned-context", Kind: wire.KindQuestion, ContextID: "thread", ExpiresUnixMs: 2000}, IdempotencyKey: "question-key", Body: "untrusted data"}}}}
	b := &messagesBackend{application: "receiver", instance: "owned-instance", contextName: "owned-context", native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}}
	return b, p
}

func TestMessagesNativeConvenienceSerializedPrepareSendReply(t *testing.T) {
	b, p := reply1884Fixture()
	prepared := wire.SendRequest{To: "app:oa-opencode-integration", Context: "ses_case", Kind: wire.KindQuestion, Subject: "review", Body: "body", IdempotencyKey: "native-key", ExpiresUnixMs: 987654321000, AnswerByUnixMs: 987654320000}
	p.prepareResult = wire.PrepareResult{Outcome: wire.OutcomePage, SendRequest: &prepared}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := gateway.NewServer(b).Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "ordinary-agent", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	reply, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_messages_prepare", Arguments: map[string]any{"receiver": "OpenCode", "body": "body"}})
	if err != nil || reply.IsError {
		t.Fatal(reply, err)
	}
	raw, err := json.Marshal(reply.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var output gateway.MessagesPrepareOutput
	if json.Unmarshal(raw, &output) != nil || output.Outcome != "prepared" || output.SendArguments == nil {
		t.Fatal(string(raw))
	}
	if p.instance != "owned-instance" || p.contextName != "owned-context" || p.prepareRequest.Receiver != "app:oa-opencode-integration" || p.prepareRequest.Kind != nil || p.prepareRequest.TTLMs != 0 {
		t.Fatal(p.prepareRequest, p.instance, p.contextName)
	}
	args, _ := json.Marshal(output.SendArguments)
	for range 2 {
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_messages_send", Arguments: json.RawMessage(args)})
		if err != nil || result.IsError {
			t.Fatal(result, err)
		}
	}
	if !reflect.DeepEqual(p.request, prepared) {
		t.Fatal("prepared request changed", p.request, prepared)
	}
	before := p.calls
	result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_messages_reply", Arguments: map[string]any{"message_id": "question-5", "body": "reply"}})
	if err != nil || result.IsError || p.calls != before+1 {
		t.Fatal(result, err, p.calls)
	}
	if out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_messages_wait", Arguments: map[string]any{"wait_ms": 0}}); err != nil || out.IsError {
		t.Fatal(out, err)
	}
	for _, body := range []string{"reply", "changed-body-native-decides"} {
		before = p.calls
		out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_messages_reply", Arguments: map[string]any{"message_id": "question-5", "body": body}})
		if err != nil || out.IsError || p.calls != before+1 || p.replyID != "question-5" || p.replyBody != body || p.instance != "owned-instance" || p.contextName != "owned-context" {
			t.Fatal(out, err, p.replyID, p.replyBody)
		}
	}
	for _, tc := range []struct {
		outcome          wire.Outcome
		reason, guidance string
	}{
		{wire.OutcomeConflict, "ambiguous_receiver", "More than one registered application"},
		{wire.OutcomeUnavailable, "receiver_unavailable", "No authorized live inbox"},
		{wire.OutcomeUnavailable, "receiver_lapsed", "stopped renewing without closing"},
		{wire.OutcomeUnavailable, "receiver_withdrawn", "closed normally"},
	} {
		p.prepareResult = wire.PrepareResult{Outcome: tc.outcome, Reason: tc.reason}
		reply, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: "oa_messages_prepare", Arguments: map[string]any{"receiver": "Codex", "body": "body"}})
		if err != nil || reply.IsError {
			t.Fatal(reply, err)
		}
		raw, err := json.Marshal(reply.StructuredContent)
		var output gateway.MessagesPrepareOutput
		if err != nil || json.Unmarshal(raw, &output) != nil {
			t.Fatal(string(raw), err)
		}
		if output.Outcome != tc.reason || !strings.Contains(output.Guidance, tc.guidance) {
			t.Fatal(output)
		}
		if tc.outcome == wire.OutcomeConflict && (len(output.Choices) != 0 || output.SendArguments != nil ||
			!strings.Contains(output.Guidance, "authorized inbox discovery") || strings.Contains(output.Guidance, "rename")) {
			t.Fatal("application ambiguity must request application clarification", output)
		}
	}
}

func TestMessagesReplyDelegatesNativeAdmissionAndLostResponseWithoutRetry(t *testing.T) {
	b, p := reply1884Fixture()
	in := gateway.MessagesReplyInput{MessageID: "question-5", Body: "same"}
	for _, outcome := range []wire.Outcome{wire.OutcomeForbidden, wire.OutcomeInvalid, wire.OutcomeConflict, wire.OutcomeUnavailable, wire.OutcomeSent} {
		p.replyResult = wire.SendResult{Outcome: outcome}
		before := p.calls
		out, err := b.MessagesReply(t.Context(), in)
		if err != nil || out.Outcome != outcome || p.calls != before+1 || p.replyID != in.MessageID || p.replyBody != in.Body {
			t.Fatal(out, err, p.calls)
		}
	}
	p.err = errors.New("lost native response")
	for range 2 {
		before := p.calls
		out, _ := b.MessagesReply(t.Context(), in)
		if out.Outcome != wire.OutcomeUnknown || p.calls != before+1 {
			t.Fatal(out, p.calls)
		}
	}
	other, _ := reply1884Fixture()
	out, _ := other.MessagesReply(t.Context(), in)
	if out.Outcome != wire.OutcomeSent {
		t.Fatal(out)
	}
}

func TestMessagesPrepareNativeOutcomeAndRelativeInputs(t *testing.T) {
	b, p := reply1884Fixture()
	choices := []wire.Inbox{
		{Program: "app:oa-claude-integration", Context: "internal-one", ChatTitle: "AI abstractions", Revision: "first-revision", LastSeenUnixMs: 1000, Watching: true, Reach: new(wire.InboxReachOpen)},
		{Program: "app:oa-claude-integration", Context: "internal-two", ChatTitle: "AI abstractions", Revision: "second-revision", LastSeenUnixMs: 2000, Watching: true, Reach: new(wire.InboxReachOpen)},
	}
	p.prepareResult = wire.PrepareResult{Outcome: wire.OutcomeConflict, Reason: "ambiguous_receiver", Choices: choices}
	out, err := b.MessagesPrepare(t.Context(), gateway.MessagesPrepareInput{Receiver: "oa-claude-integration", Kind: "note", Body: "body", TTLMs: 42000, AnswerWithinMs: 7000, Context: "chosen"})
	if err != nil || out.Outcome != "ambiguous_receiver" || !strings.Contains(out.Guidance, "More than one authorized chat") || p.prepareRequest.Receiver != "app:oa-claude-integration" || p.prepareRequest.Kind == nil || *p.prepareRequest.Kind != wire.KindNote || p.prepareRequest.TTLMs != 42000 || p.prepareRequest.AnswerWithinMs != 7000 || p.prepareRequest.Context != "chosen" {
		t.Fatal(out, err, p.prepareRequest)
	}
	if out.SendArguments != nil || !reflect.DeepEqual(out.Choices, choices) ||
		!strings.Contains(out.Guidance, "rename it in its app") ||
		!strings.Contains(out.Guidance, "cannot establish which chat the person used") ||
		!strings.Contains(out.Guidance, "keep the context out of the conversation") ||
		strings.Contains(out.Guidance, "internal-one") || strings.Contains(out.Guidance, "internal-two") {
		t.Fatal("native choices must stay machine-readable and guidance must use connection labels", out)
	}
	p.err = errors.New("lost")
	out, _ = b.MessagesPrepare(t.Context(), gateway.MessagesPrepareInput{Receiver: "oa-codex-integration", Body: "body"})
	if out.Outcome != "unknown" {
		t.Fatal(out)
	}
}

func TestMessagesPrepareDiscoveryChurnHasActionableGuidance(t *testing.T) {
	b, p := reply1884Fixture()
	p.prepareResult = wire.PrepareResult{Outcome: wire.OutcomeUnavailable, Reason: "discovery_incomplete"}
	out, err := b.MessagesPrepare(t.Context(), gateway.MessagesPrepareInput{Receiver: "claude", Body: "body"})
	if err != nil || out.Outcome != "unavailable" || out.Reason != "discovery_incomplete" || !strings.Contains(out.Guidance, "prepare again") || !strings.Contains(out.Guidance, "Nothing was sent") || out.SendArguments != nil || p.prepareRequest.Receiver != "app:oa-claude-integration" {
		t.Fatal(out, err, p.prepareRequest)
	}
}
