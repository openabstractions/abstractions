package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// newerRuntime serves the actual generated dispatcher and adds one member to
// every record named by field in its replies, as a runtime one additive
// messages@1 field ahead of this integration image does. The 2026-11-01
// helpers met the runtime's new Inbox.reach this way.
type newerRuntime struct {
	dispatcher *wire.MessagesDispatcher
	field      string
}

func (n newerRuntime) ExchangeFrame(frame []byte) ([]byte, error) {
	reply, err := n.dispatcher.ExchangeFrame(frame)
	if err != nil || n.field == "" {
		return reply, err
	}
	return bytes.ReplaceAll(reply, []byte(`"`+n.field+`":`), []byte(`"added_by_newer_runtime": true, "`+n.field+`":`)), nil
}

func newerRuntimeBackend(p *messagesProbe, field string) *messagesBackend {
	return &messagesBackend{application: "oa-codex-integration", instance: "native-instance", contextName: "kept", native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(newerRuntime{dispatcher: &wire.MessagesDispatcher{Handler: p}, field: field}), nil
	}}
}

func captureMessagesCause(t *testing.T) *strings.Builder {
	t.Helper()
	log := &strings.Builder{}
	previous := messagesCauseLog
	messagesCauseLog = log
	t.Cleanup(func() { messagesCauseLog = previous })
	return log
}

// Observation metadata accepts additive fields while preserving known rows.
func TestMessagesNewerRuntimeListingAcceptsObservationMetadata(t *testing.T) {
	log := captureMessagesCause(t)
	same, err := newerRuntimeBackend(&messagesProbe{}, "").MessagesInboxesList(context.Background(), gateway.MessagesListInput{})
	if err != nil || same.Outcome != wire.OutcomePage || len(same.Inboxes) != 64 || same.Reason != "" {
		t.Fatal("same-version listing", same.Outcome, same.Reason, err)
	}
	page, err := newerRuntimeBackend(&messagesProbe{}, "program").MessagesInboxesList(context.Background(), gateway.MessagesListInput{})
	if err != nil || page.Outcome != wire.OutcomePage || page.Reason != "" || len(page.Inboxes) != 64 {
		t.Fatal("newer runtime listing", page.Outcome, page.Reason, err)
	}
	if log.Len() != 0 {
		t.Fatal(log.String())
	}
	if _, err := wire.DecodeInboxPage(wire.EncodeInboxPage(&page)); err != nil {
		t.Fatal("observation round trip", err)
	}
}

func TestMessagesNewerRuntimeSendAcceptsOutputMetadataAndPreparedInputRefuses(t *testing.T) {
	captureMessagesCause(t)
	ctx := context.Background()
	b := newerRuntimeBackend(&messagesProbe{}, "message_id")
	sent, err := b.MessagesSend(ctx, gateway.MessagesSendInput{To: "app:receiver", Context: "target", Kind: "note", Body: "body", IdempotencyKey: "key", ExpiresUnixMs: 2000})
	if err != nil || sent.Outcome != wire.OutcomeSent {
		t.Fatal("Send output accepts additive metadata", sent, err)
	}
	p := &messagesProbe{prepareResult: wire.PrepareResult{Outcome: wire.OutcomePage, SendRequest: &wire.SendRequest{To: "app:receiver", Context: "target", Kind: wire.KindNote, Body: "body", IdempotencyKey: "key", ExpiresUnixMs: 2000}}}
	prepared, err := newerRuntimeBackend(p, "idempotency_key").MessagesPrepare(ctx, gateway.MessagesPrepareInput{Receiver: "receiver", Body: "body"})
	if err != nil || prepared.Outcome != "unknown" || prepared.Reason != "reply_refused_unknown_field" || !strings.Contains(prepared.Guidance, "update the agent connection") {
		t.Fatal("Prepare names the refusal", prepared, err)
	}
}

func TestMessagesNewerRuntimeWatchAcceptsObservationFields(t *testing.T) {
	p := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Revision: "r", Wake: "0001", PendingUntilUnixMs: 2000}}
	b := newerRuntimeBackend(p, "revision")
	out, err := b.MessagesWatch(context.Background(), gateway.MessagesWatchInput{})
	if err != nil || out.Outcome.String() != "page" || out.Revision != "r" || out.Wake != "0001" || out.PendingUntilUnixMs != 2000 {
		t.Fatal("additive native Page did not reach observer", out, err)
	}
}

func TestMessagesPrepareCarriesQueuedReach(t *testing.T) {
	p := &messagesProbe{prepareResult: wire.PrepareResult{Outcome: wire.OutcomePage, Reason: "queued", SendRequest: &wire.SendRequest{To: "app:oa-codex-integration", Context: "chat", Kind: wire.KindQuestion, Body: "body", IdempotencyKey: "key", ExpiresUnixMs: 2000, AnswerByUnixMs: 2000}}}
	prepared, err := newerRuntimeBackend(p, "").MessagesPrepare(context.Background(), gateway.MessagesPrepareInput{Receiver: "oa-codex-integration", Body: "body"})
	if err != nil || prepared.Outcome != "prepared" || prepared.Reach != "queued" || prepared.SendArguments == nil || !strings.Contains(prepared.Guidance, "After oa_messages_send answers sent, tell the person plainly that the chat is closed") || !strings.Contains(prepared.Tell, "the chat is closed") {
		t.Fatal(prepared, err)
	}
}

func TestMessagesPrepareEmptyDiscoveryKeepsNativeNextStep(t *testing.T) {
	for _, tc := range []struct {
		name, receiver, reason, outcome string
		native                          wire.Outcome
	}{
		{"permitted-without-chat", "Claude", "receiver_unavailable", "receiver_unavailable", wire.OutcomeUnavailable},
		{"denied", "app:denied-agent", "access:denied-agent", "forbidden", wire.OutcomeForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			probe := &messagesProbe{prepareResult: wire.PrepareResult{Outcome: tc.native, Reason: tc.reason}}
			out, err := newerRuntimeBackend(probe, "").MessagesPrepare(t.Context(), gateway.MessagesPrepareInput{Receiver: tc.receiver, Body: "hello"})
			if err != nil || out.Outcome != tc.outcome || out.SendArguments != nil {
				t.Fatal(out, err)
			}
			if tc.name == "permitted-without-chat" && !strings.Contains(out.Guidance, "Tell the person to open a chat in that app with OpenAbstractions connected, then try again.") {
				t.Fatal(out.Guidance)
			}
			if tc.name == "denied" {
				for _, guidance := range []string{"continue in the same chat", "It will check access again before sending.", "same receiver and idempotency_key", "only after permission is confirmed"} {
					if out.Reason != tc.reason || !strings.Contains(out.Guidance, guidance) {
						t.Fatal(out)
					}
				}
			}
			if probe.prepareRequest.Body != "hello" || probe.request.To != "" {
				t.Fatal("read-only fallback changed the message or sent it", probe)
			}
		})
	}
}
