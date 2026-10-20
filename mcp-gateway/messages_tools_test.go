package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type messageAccessProbe struct {
	calls  int
	intent askswire.PermissionIntent
}

func (p *messageAccessProbe) Request(in askswire.PermissionIntent) (askswire.PermissionObservation, error) {
	p.calls++
	p.intent = in
	return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending, Answer: &askswire.Answer{ID: "fixture-access", Pending: true}}, nil
}
func (p *messageAccessProbe) Observe(string, int64) (askswire.PermissionObservation, error) {
	return askswire.PermissionObservation{}, errors.New("unexpected observe")
}
func (p *messageAccessProbe) ObserveQuestion(string, int64) (askswire.PendingQuestion, error) {
	return askswire.PendingQuestion{}, errors.New("unexpected question observation")
}

func TestMessagesRequestAccessUsesGeneratedCallerBoundPermission(t *testing.T) {
	p := &messageAccessProbe{}
	c := askswire.NewPermissionApplicationClient(&askswire.PermissionApplicationDispatcher{Handler: p})
	b := &messagesBackend{contextName: "retained-native-connection", requestAccess: func(_ context.Context, in askswire.PermissionIntent) (askswire.PermissionObservation, error) {
		return c.Request(in)
	}}
	for _, receiver := range []string{"oa-claude-integration", "app:oa-claude-integration"} {
		out, err := b.MessagesRequestAccess(context.Background(), gateway.MessagesAccessInput{Receiver: receiver, IdempotencyKey: "explicit-access"})
		if err != nil || out.Outcome != askswire.PermissionOutcomePending {
			t.Fatal(out, err)
		}
		if p.intent.Purpose != "" || p.intent.Selection == nil || p.intent.Selection.Permission.Action != grants.ApplicationMessageAction || p.intent.Selection.Permission.ResourceProfile != grants.AgentMessageResourceProfile {
			t.Fatal(p.intent)
		}
		name, err := grants.DecodeNamedApplicationTarget(p.intent.Selection.Selector.Target)
		if err != nil || name != "oa-claude-integration" {
			t.Fatal(name, err)
		}
	}
	if p.calls != 2 {
		t.Fatal(p.calls)
	}
	before := p.calls
	if out, _ := b.MessagesRequestAccess(context.Background(), gateway.MessagesAccessInput{Receiver: "app:foreign"}); out.Outcome != askswire.PermissionOutcomeInvalid || p.calls != before {
		t.Fatal(out, p.calls)
	}
}

func TestMessagesRequestAccessRejectsInvalidReceiverBeforeNativeEffects(t *testing.T) {
	calls := 0
	b := &messagesBackend{contextName: "inert-name-boundary", requestAccess: func(context.Context, askswire.PermissionIntent) (askswire.PermissionObservation, error) {
		calls++
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending}, nil
	}}
	for _, receiver := range []string{"", strings.Repeat("a", 65), strings.Repeat("é", 33)} {
		out, err := b.MessagesRequestAccess(context.Background(), gateway.MessagesAccessInput{Receiver: receiver, IdempotencyKey: "bounded-input"})
		if err != nil || out.Outcome != askswire.PermissionOutcomeInvalid || calls != 0 {
			t.Fatal(receiver, out, err, calls)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	server, err := gateway.NewServer(b).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "inert-name-boundary", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	reply, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_request_access", Arguments: map[string]any{"receiver": strings.Repeat("é", 33), "idempotency_key": "bounded-input"}})
	if err != nil || reply.IsError || len(reply.Content) != 1 {
		t.Fatal(reply, err)
	}
	out, err := askswire.DecodePermissionObservation([]byte(reply.Content[0].(*mcp.TextContent).Text))
	if err != nil || out.Outcome != askswire.PermissionOutcomeInvalid || calls != 0 {
		t.Fatal(out, err, calls)
	}
	reply, err = client.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_request_access", Arguments: map[string]any{"receiver": "app:" + strings.Repeat("a", 64), "idempotency_key": "canonical-bounded-input"}})
	if err != nil || reply.IsError || len(reply.Content) != 2 {
		t.Fatal(reply, err)
	}
	out, err = askswire.DecodePermissionObservation([]byte(reply.Content[0].(*mcp.TextContent).Text))
	if err != nil || out.Outcome != askswire.PermissionOutcomePending || calls != 1 {
		t.Fatal("canonical program failed the native name boundary", out, err, calls)
	}
}

type accessInboxProbe struct {
	*messagesProbe
	page wire.InboxPage
}

func (p *accessInboxProbe) InboxesList(wire.InboxQuery) (wire.InboxPage, error) {
	p.calls++
	return p.page, p.err
}

func TestMessagesAccessConfirmationRequiresCurrentRuleAndAuthorizedLiveInbox(t *testing.T) {
	for _, tc := range []struct {
		name     string
		decision rightswire.DecisionOutcome
		page     wire.InboxPage
		want     askswire.PermissionOutcome
		queries  int
	}{
		{"current", rightswire.DecisionOutcomePermitted, wire.InboxPage{Outcome: wire.OutcomePage, Inboxes: []wire.Inbox{{Program: "app:oa-claude-integration", Context: "opaque-live-context", Revision: "r"}}}, askswire.PermissionOutcomePermitted, 1},
		{"no-live-inbox-or-revoked", rightswire.DecisionOutcomePermitted, wire.InboxPage{Outcome: wire.OutcomePage, Inboxes: []wire.Inbox{}}, askswire.PermissionOutcomePending, 1},
		{"needs-permission-is-not-a-permitted-inbox", rightswire.DecisionOutcomePermitted, wire.InboxPage{Outcome: wire.OutcomePage, NeedsPermission: []wire.AgentApplication{{Program: "app:oa-claude-integration", Title: "Claude Code"}}}, askswire.PermissionOutcomePending, 1},
		// A listing that changed between the runtime's reads, as a just-saved
		// rule changes it, reads pending with no revision: list again.
		{"lapsed-snapshot-gap", rightswire.DecisionOutcomePermitted, wire.InboxPage{Outcome: wire.OutcomeGap}, askswire.PermissionOutcomePending, 1},
		{"other-receiver", rightswire.DecisionOutcomePermitted, wire.InboxPage{Outcome: wire.OutcomePage, Inboxes: []wire.Inbox{{Program: "app:other", Context: "opaque", Revision: "r"}}}, askswire.PermissionOutcomePending, 1},
		// Same-app (architect ruling 2026-11-09): the caller's own row has
		// the receiver's program and proves no receiver.
		{"own-row-is-no-receiver", rightswire.DecisionOutcomePermitted, wire.InboxPage{Outcome: wire.OutcomePage, Inboxes: []wire.Inbox{{Program: "app:oa-claude-integration", Context: "chat-own", Revision: "r", Mine: new(true)}}}, askswire.PermissionOutcomePending, 1},
		{"own-row-and-sibling-chat", rightswire.DecisionOutcomePermitted, wire.InboxPage{Outcome: wire.OutcomePage, Inboxes: []wire.Inbox{{Program: "app:oa-claude-integration", Context: "chat-own", Revision: "r", Mine: new(true)}, {Program: "app:oa-claude-integration", Context: "chat-sibling", Revision: "r"}}}, askswire.PermissionOutcomePermitted, 1},
		{"native-owner-unavailable", rightswire.DecisionOutcomePermitted, wire.InboxPage{Outcome: wire.OutcomeUnavailable, Inboxes: []wire.Inbox{}}, askswire.PermissionOutcomeUnavailable, 1},
		{"revoked-rule", rightswire.DecisionOutcomeNotGranted, wire.InboxPage{}, askswire.PermissionOutcomePending, 0},
		{"explicit-denial", rightswire.DecisionOutcomeDenied, wire.InboxPage{}, askswire.PermissionOutcomeDenied, 0},
		{"unknown-rule-state", rightswire.DecisionOutcomeUnavailable, wire.InboxPage{}, askswire.PermissionOutcomeUnavailable, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &accessInboxProbe{messagesProbe: &messagesProbe{}, page: tc.page}
			b := &messagesBackend{native: func(context.Context) (nativeMessages, error) {
				return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
			}}
			got, err := b.confirmMessageAccess(context.Background(), "oa-claude-integration", rightswire.Decision{Outcome: tc.decision, PolicyRevision: "policy-r"})
			if err != nil || got.Outcome != tc.want || p.calls != tc.queries {
				t.Fatal(got, err, p.calls)
			}
			if got.Outcome == askswire.PermissionOutcomePermitted && got.PolicyRevision != "policy-r" {
				t.Fatal(got)
			}
			if tc.page.Outcome == wire.OutcomeGap && got.PolicyRevision != "" {
				t.Fatal("a gap listing witnessed a saved rule", got)
			}
		})
	}
}

type messagesProbe struct {
	calls                                  int
	instance, contextName, after, revision string
	view                                   wire.View
	request                                wire.SendRequest
	page                                   wire.Page
	query                                  wire.InboxQuery
	err                                    error
	prepareRequest                         wire.PrepareRequest
	prepareResult                          wire.PrepareResult
	replyID, replyBody                     string
	replyResult                            wire.SendResult
	turnWakes                              []string
	waitMs                                 int64
}

func (p *messagesProbe) SendChat(string, string, wire.ChatSendRequest) (wire.SendResult, error) {
	return wire.SendResult{Outcome: wire.OutcomeUnavailable}, nil
}

func (p *messagesProbe) ObserveDelivery(wire.MessageRef, string) (wire.ObserveDeliveryResult, error) {
	return wire.ObserveDeliveryResult{Outcome: wire.DeliveryObservationOutcomeForbidden}, nil
}

func (p *messagesProbe) Prepare(i, c string, request wire.PrepareRequest) (wire.PrepareResult, error) {
	p.calls++
	p.instance = i
	p.contextName = c
	p.prepareRequest = request
	return p.prepareResult, p.err
}
func (p *messagesProbe) Reply(i, c, id, body string) (wire.SendResult, error) {
	p.calls++
	p.instance = i
	p.contextName = c
	p.replyID = id
	p.replyBody = body
	if p.replyResult.Outcome == 0 {
		return wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "reply-id", ContextID: "thread"}, p.err
	}
	return p.replyResult, p.err
}

func (p *messagesProbe) ReadForPerson(string) (wire.Page, error) {
	return wire.Page{}, errors.New("person-only read is unavailable to the MCP probe")
}

func (p *messagesProbe) StopForPerson(string) (wire.StopResult, error) {
	return wire.StopResult{}, errors.New("person-only stop is unavailable to the MCP probe")
}

func (p *messagesProbe) TurnWake(i, c, wake string) (wire.TurnWakeResult, error) {
	p.instance = i
	p.contextName = c
	p.turnWakes = append(p.turnWakes, wake)
	return wire.TurnWakeResult{Outcome: wire.OutcomePage}, nil
}

func (p *messagesProbe) InboxOpen(i, c, s string) (wire.InboxChange, error) {
	p.calls++
	p.instance = i
	p.contextName = c
	return wire.InboxChange{Outcome: wire.OutcomeOpened, Revision: "r"}, p.err
}
func (p *messagesProbe) InboxesList(query wire.InboxQuery) (wire.InboxPage, error) {
	p.query = query
	c := query.Cursor
	p.calls++
	p.after = c
	if c == "foreign" {
		return wire.InboxPage{Outcome: wire.OutcomeGap}, p.err
	}
	if c == "next" {
		return wire.InboxPage{Outcome: wire.OutcomePage, Inboxes: []wire.Inbox{{Program: "app:last", Context: "context", Revision: "r"}}}, p.err
	}
	rows := make([]wire.Inbox, 64)
	for i := range rows {
		rows[i] = wire.Inbox{Program: "app:first", Context: "context", Revision: "r"}
	}
	return wire.InboxPage{Outcome: wire.OutcomePage, Cursor: "next", Inboxes: rows}, p.err
}

func TestMessagesPairedArgumentsStayExplicit(t *testing.T) {
	p := &messagesProbe{}
	b := &messagesBackend{instance: "native-instance", contextName: "source", native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}}
	key := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	_, err := b.MessagesInboxesList(context.Background(), gateway.MessagesListInput{PeerKey: key, Cursor: "opaque", Receiver: "app:receiver"})
	if err != nil || p.query.PeerKey != key || p.query.Cursor != "opaque" || p.query.Receiver != "app:receiver" {
		t.Fatal(p.query, err)
	}
	_, err = b.MessagesSend(context.Background(), gateway.MessagesSendInput{PeerKey: key, To: "app:receiver", Context: "target", Kind: "reply", IdempotencyKey: "same", ExpiresUnixMs: 2000, ReplyReference: &gateway.MessagesReferenceInput{OwnerPeerKey: key, MessageID: "question-1"}})
	if err != nil || p.request.PeerKey != key || p.request.Origin != nil || p.request.ReplyReference == nil || p.request.ReplyReference.OwnerPeerKey != key || p.request.ReplyReference.MessageID != "question-1" || p.instance != "native-instance" {
		t.Fatal(p.request, err)
	}
}
func (p *messagesProbe) Send(i, c string, r wire.SendRequest) (wire.SendResult, error) {
	p.calls++
	p.instance = i
	p.contextName = c
	p.request = r
	return wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "m", ContextID: "c"}, p.err
}
func (p *messagesProbe) Wait(i, c string, v wire.View, a, r string, w int64) (wire.Page, error) {
	p.calls++
	p.waitMs = w
	p.instance = i
	p.contextName = c
	p.view = v
	p.after = a
	p.revision = r
	return p.page, p.err
}

func TestMessagesRetainedAssociationAndExplicitReconcile(t *testing.T) {
	p := &messagesProbe{}
	// Use the actual generated frame encoder/dispatcher on both directions.
	d := &wire.MessagesDispatcher{Handler: p}
	b := &messagesBackend{instance: "native-instance", contextName: "kept", native: func(context.Context) (nativeMessages, error) { return wire.NewMessagesClient(d), nil }}
	in := gateway.MessagesSendInput{To: "app:receiver", Context: "target", Kind: "question", Subject: "subject", Body: "body", IdempotencyKey: "key", ExpiresUnixMs: 2000, AnswerByUnixMs: 1500}
	out, err := b.MessagesSend(context.Background(), in)
	if err != nil || out.Outcome != wire.OutcomeSent || p.calls != 1 || p.instance != "native-instance" || p.contextName != "kept" || p.request.IdempotencyKey != "key" {
		t.Fatalf("retained send: %+v %v %+v", out, err, p)
	}
	// A second effect occurs only when the caller explicitly asks again.
	_, _ = b.MessagesSend(context.Background(), in)
	if p.calls != 2 {
		t.Fatal("explicit same-key call missing")
	}
	p.err = errors.New("reply lost")
	out, _ = b.MessagesSend(context.Background(), in)
	if out.Outcome != wire.OutcomeUnknown || p.calls != 3 {
		t.Fatal("uncertain send retried or called unavailable")
	}
	b.native = func(context.Context) (nativeMessages, error) { return nil, errors.New("no resolution") }
	out, _ = b.MessagesSend(context.Background(), in)
	if out.Outcome != wire.OutcomeUnavailable || p.calls != 3 {
		t.Fatal("predispatch resolution classification")
	}
}

func TestMessagesSendCodecRefusalPrecedesNativeSender(t *testing.T) {
	valid := gateway.MessagesSendInput{To: "app:receiver", Context: "target", Kind: "reply", Body: "answer", IdempotencyKey: "same", ExpiresUnixMs: 2000, ReplyTo: "question-1"}
	for _, tc := range []struct {
		name   string
		change func(*gateway.MessagesSendInput)
	}{
		{"unknown-kind", func(in *gateway.MessagesSendInput) { in.Kind = "answer" }},
		{"missing-destination", func(in *gateway.MessagesSendInput) { in.To = "" }},
		{"long-context", func(in *gateway.MessagesSendInput) { in.Context = strings.Repeat("x", 65) }},
		{"long-body", func(in *gateway.MessagesSendInput) { in.Body = strings.Repeat("x", 8193) }},
		{"empty-key", func(in *gateway.MessagesSendInput) { in.IdempotencyKey = "" }},
		{"long-local-reference", func(in *gateway.MessagesSendInput) { in.ReplyTo = strings.Repeat("x", 129) }},
		{"malformed-paired-reference", func(in *gateway.MessagesSendInput) {
			in.ReplyReference = &gateway.MessagesReferenceInput{OwnerPeerKey: "short", MessageID: "question-1"}
		}},
		{"malformed-peer", func(in *gateway.MessagesSendInput) { in.PeerKey = "short" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := valid
			tc.change(&in)
			p := &messagesProbe{}
			resolutions := 0
			b := &messagesBackend{instance: "actual", contextName: "source", native: func(context.Context) (nativeMessages, error) {
				resolutions++
				return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
			}}
			out, err := b.MessagesSend(context.Background(), in)
			if err != nil || out.Outcome != wire.OutcomeInvalid || out.MessageID != "" || resolutions != 0 || p.calls != 0 {
				t.Fatal(out, err, resolutions, p.calls)
			}
		})
	}
}

func exactQuestionTwoMalformedReply() gateway.MessagesSendInput {
	return gateway.MessagesSendInput{To: "app:oa-codex-integration", Context: "native-c3990d73e7ffd310d92476c2d0534232", Kind: "note", Subject: "Re: OA connection check", Body: "Received from Codex through OA.", IdempotencyKey: "claude-reply-oa-codex-integration-2-v1", ExpiresUnixMs: 1793229317183, ReplyTo: "oa-codex-integration-2", ReplyReference: &gateway.MessagesReferenceInput{OwnerPeerKey: "5EoORHd0g-qJ3-IgrTBm3jiMo_bYQPVx", MessageID: "oa-codex-integration-2"}}
}

type replyCodecTransport struct {
	calls    int
	response []byte
}

type messageBoundaryTransport struct {
	dispatcher *wire.MessagesDispatcher
	frames     int
	loseReply  bool
}

func (p *messageBoundaryTransport) ExchangeFrame(frame []byte) ([]byte, error) {
	p.frames++
	reply, err := p.dispatcher.ExchangeFrame(frame)
	if err == nil && p.loseReply {
		return []byte(`{}`), nil
	}
	return reply, err
}

func TestMessagesSerializedMCPToGeneratedNativeBoundary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p := &messagesProbe{}
	physical := &messageBoundaryTransport{dispatcher: &wire.MessagesDispatcher{Handler: p}}
	resolutions := 0
	b := &messagesBackend{instance: "original-instance", contextName: "original-native-context", native: func(context.Context) (nativeMessages, error) {
		resolutions++
		return wire.NewMessagesClient(physical), nil
	}}
	st, ct := mcp.NewInMemoryTransports()
	server, err := gateway.NewServer(b).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "serialized-messages-boundary", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	call := func(in gateway.MessagesSendInput) wire.SendResult {
		t.Helper()
		reply, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_send", Arguments: in})
		// An uncertain outcome adds one assistant line naming reconciliation.
		if err != nil || reply.IsError || len(reply.Content) == 0 || len(reply.Content) > 2 {
			t.Fatal(reply, err)
		}
		out, err := wire.DecodeSendResult([]byte(reply.Content[0].(*mcp.TextContent).Text))
		if err != nil {
			t.Fatal(err)
		}
		return *out
	}
	for _, kind := range []string{"note", "question", "reply"} {
		in := gateway.MessagesSendInput{To: "app:peer", Context: "peer-native-context", Kind: kind, Body: "untrusted text", IdempotencyKey: "serialized-" + kind, ExpiresUnixMs: 2000}
		if kind == "question" {
			in.AnswerByUnixMs = 1500
		}
		if kind == "reply" {
			in.ReplyTo = "received-question"
		}
		before := physical.frames
		out := call(in)
		if out.Outcome != wire.OutcomeSent || physical.frames != before+1 || p.instance != b.instance || p.contextName != b.contextName || p.request.Context != in.Context || p.request.IdempotencyKey != in.IdempotencyKey || p.request.ReplyTo != in.ReplyTo || p.request.ReplyReference != nil {
			t.Fatal("serialized local call changed route or retried", out, p, physical.frames)
		}
	}
	beforeFrames, beforeResolution := physical.frames, resolutions
	if out := call(exactQuestionTwoMalformedReply()); out.Outcome != wire.OutcomeInvalid || physical.frames != beforeFrames || resolutions != beforeResolution {
		t.Fatal("malformed serialized reply reached native owner", out, physical.frames, resolutions)
	}
	physical.loseReply = true
	in := gateway.MessagesSendInput{To: "app:peer", Context: "target", Kind: "reply", ReplyTo: "received-question", Body: "answer", IdempotencyKey: "lost-reply", ExpiresUnixMs: 2000}
	beforeAccepted := p.calls
	if out := call(in); out.Outcome != wire.OutcomeUnknown || physical.frames != beforeFrames+1 || p.calls != beforeAccepted+1 {
		t.Fatal("accepted reply loss was retried or claimed absent", out, physical.frames, p.calls)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_send", Arguments: in}); err == nil || physical.frames != beforeFrames+1 || p.calls != beforeAccepted+1 {
		t.Fatal("closed MCP client dispatched or retried", err, physical.frames, p.calls)
	}
}

func (p *replyCodecTransport) ExchangeFrame([]byte) ([]byte, error) {
	p.calls++
	if p.response != nil {
		return p.response, nil
	}
	return nil, errors.New("transport reached")
}

func TestMessagesSendPostDispatchCodecRefusalRemainsUnknown(t *testing.T) {
	transport := &replyCodecTransport{response: []byte(`{}`)}
	b := &messagesBackend{instance: "actual", contextName: "source", native: func(context.Context) (nativeMessages, error) { return wire.NewMessagesClient(transport), nil }}
	in := gateway.MessagesSendInput{To: "app:peer", Context: "target", Kind: "reply", Body: "answer", IdempotencyKey: "stable", ExpiresUnixMs: 2000, ReplyTo: "received-question"}
	out, err := b.MessagesSend(context.Background(), in)
	if err != nil || out.Outcome != wire.OutcomeUnknown || transport.calls != 1 {
		t.Fatal(out, err, transport.calls)
	}
}

type messagesServiceRefusalProbe struct{ messagesProbe }

func (p *messagesServiceRefusalProbe) Send(i, c string, r wire.SendRequest) (wire.SendResult, error) {
	p.calls++
	p.request = r
	return wire.SendResult{Outcome: wire.OutcomeInvalid}, nil
}
func TestMessagesSendServiceOwnsReplyCrossFieldRules(t *testing.T) {
	p := &messagesServiceRefusalProbe{}
	b := &messagesBackend{instance: "actual", contextName: "source", native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}}
	// note is a valid wire word. The native service retains the decision
	// about this codec-valid combination's reply semantics.
	in := gateway.MessagesSendInput{To: "app:peer", Context: "target", Kind: "note", Body: "answer", IdempotencyKey: "stable", ExpiresUnixMs: 2000, ReplyTo: "received-question"}
	out, err := b.MessagesSend(context.Background(), in)
	if err != nil || out.Outcome != wire.OutcomeInvalid || p.calls != 1 || p.request.ReplyTo != in.ReplyTo || p.request.Kind != wire.KindNote {
		t.Fatal(out, err, p)
	}
}

func TestMessagesExactQuestionTwoGeneratedCodecNeverExchangesFrame(t *testing.T) {
	in := exactQuestionTwoMalformedReply()
	transport := &replyCodecTransport{}
	// Reconstruct the original adapter's request to establish where the
	// historical native client refused this exact supplied call.
	request := wire.SendRequest{To: in.To, Context: in.Context, Kind: wire.KindNote, Subject: in.Subject, Body: in.Body, IdempotencyKey: in.IdempotencyKey, ExpiresUnixMs: in.ExpiresUnixMs, ReplyTo: in.ReplyTo, ReplyReference: &wire.MessageRef{OwnerPeerKey: in.ReplyReference.OwnerPeerKey, MessageID: in.ReplyReference.MessageID}}
	out, err := wire.NewMessagesClient(transport).Send("original-instance", "original-native-context", request)
	var refusal *wire.Refusal
	if !errors.As(err, &refusal) || refusal.Word != "bad_value" || transport.calls != 0 || out.MessageID != "" {
		t.Fatal(out, err, transport.calls)
	}
}

func TestMessagesExactQuestionTwoMalformedReplyIsInvalidBeforeResolution(t *testing.T) {
	resolutions := 0
	transport := &replyCodecTransport{}
	b := &messagesBackend{instance: "original-instance", contextName: "original-native-context", native: func(context.Context) (nativeMessages, error) {
		resolutions++
		return wire.NewMessagesClient(transport), nil
	}}
	out, err := b.MessagesSend(context.Background(), exactQuestionTwoMalformedReply())
	if err != nil || out.Outcome != wire.OutcomeInvalid || out.MessageID != "" || resolutions != 0 || transport.calls != 0 {
		t.Fatal(out, err, resolutions, transport.calls)
	}
}

func TestMessagesSendGeneratedKindsAndReplyRoutesRetainArguments(t *testing.T) {
	for _, kind := range wire.KindValues() {
		word, ok := kind.WireName()
		if !ok {
			t.Fatal("generated kind lacks wire word")
		}
		t.Run(word, func(t *testing.T) {
			p := &messagesProbe{}
			b := &messagesBackend{instance: "actual", contextName: "source", native: func(context.Context) (nativeMessages, error) {
				return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
			}}
			in := gateway.MessagesSendInput{To: "app:original-sender", Context: "original-source", Kind: word, Subject: "subject", Body: "body", IdempotencyKey: "stable", ExpiresUnixMs: 2000}
			if kind == wire.KindQuestion {
				in.AnswerByUnixMs = 1500
			}
			if kind == wire.KindReply {
				in.ReplyTo = "received-question"
			}
			out, err := b.MessagesSend(context.Background(), in)
			if err != nil || out.Outcome != wire.OutcomeSent || p.calls != 1 || p.request.Kind != kind || p.request.ReplyTo != in.ReplyTo || p.request.To != in.To || p.request.Context != in.Context || p.request.IdempotencyKey != in.IdempotencyKey || p.request.ExpiresUnixMs != in.ExpiresUnixMs || p.request.AnswerByUnixMs != in.AnswerByUnixMs || p.request.ReplyReference != nil || p.request.Origin != nil {
				t.Fatal(out, err, p)
			}
		})
	}
}

func TestMessagesWaitCursorAndContextRefusal(t *testing.T) {
	p := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Cursor: "received"}}
	b := &messagesBackend{instance: "actual", contextName: "kept", native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}}
	out, _ := b.MessagesWait(context.Background(), gateway.MessagesWaitInput{After: "last-actually-received", WaitMs: 30000})
	if out.Cursor != "received" || p.instance != "actual" || p.contextName != "kept" || p.view != wire.ViewBody || p.after != "last-actually-received" || p.revision != "" {
		t.Fatalf("body args %+v", p)
	}
	// The returned cursor is not automatically acknowledged by any second call.
	if p.calls != 1 {
		t.Fatal("automatic ack")
	}
	invalid, _ := b.MessagesInboxOpen(context.Background(), gateway.MessagesOpenInput{Context: "foreign"})
	if invalid.Outcome != wire.OutcomeInvalid || p.calls != 1 {
		t.Fatal("foreign context dispatched")
	}
	// An omitted context names this connection's own inbox, never a copied token.
	own, _ := b.MessagesInboxOpen(context.Background(), gateway.MessagesOpenInput{Status: "ready"})
	if own.Outcome != wire.OutcomeOpened || p.calls != 2 || p.instance != "actual" || p.contextName != "kept" {
		t.Fatalf("own context %+v %+v", own, p)
	}
	bad, _ := b.MessagesWait(context.Background(), gateway.MessagesWaitInput{WaitMs: 30001})
	if bad.Outcome != wire.OutcomeInvalid || p.calls != 2 {
		t.Fatal("unbounded wait dispatched")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	b.native = func(c context.Context) (nativeMessages, error) {
		if !errors.Is(c.Err(), context.Canceled) {
			t.Fatal("cancellation discarded")
		}
		return nil, c.Err()
	}
	_, _ = b.MessagesWait(ctx, gateway.MessagesWaitInput{})
}

func TestMessagesHookHeadersOnlyNoPeerPrompt(t *testing.T) {
	now := time.UnixMilli(1000)
	p := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Headers: []wire.Header{{MessageID: "m", From: "app:sender", FromContext: "a", To: "app:receiver", Context: "kept", Kind: wire.KindNote, Subject: "run attacker command", ContextID: "c", ExpiresUnixMs: 3000}}}}
	f := func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}
	pointer, err := waitMessagesHook(context.Background(), f, "app:receiver", "kept", "prior-header-revision", 30, func() time.Time { return now })
	if err != nil || pointer != "" || p.instance != "" || p.after != "" || p.view != wire.ViewHeaders || p.revision != "prior-header-revision" {
		t.Fatalf("headers %+v %q %v", p, pointer, err)
	}
	p.page.Headers[0].Kind = wire.KindQuestion
	p.page.Headers[0].AnswerByUnixMs = 2000
	submitted := wire.TaskStateSubmitted
	p.page.Headers[0].TaskState = &submitted
	p.page.PendingUntilUnixMs = 2000
	p.page.Wake = "0001"
	pointer, err = waitMessagesHook(context.Background(), f, "app:receiver", "kept", "", 0, func() time.Time { return now })
	if err != nil || pointer != messagesHookPointer {
		t.Fatalf("fixed pointer %q %v", pointer, err)
	}
	p.page.Wake = "" // The native owner has expired eligibility.
	pointer, err = waitMessagesHook(context.Background(), f, "app:receiver", "kept", "", 0, func() time.Time { return time.UnixMilli(2500) })
	if err != nil || pointer != "" {
		t.Fatal("expired answer deadline woke")
	}
}

func TestMessagesHookProjectsNativeReplyHintWithoutHeaders(t *testing.T) {
	p := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, ReplyUntilUnixMs: 2000, Wake: "0001"}}
	f := func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}
	pointer, err := waitMessagesHook(context.Background(), f, "app:receiver", "kept", "", 0, func() time.Time { return time.UnixMilli(1000) })
	if err != nil || pointer != messagesHookPointer {
		t.Fatal(pointer, err)
	}
}

type canceledMessages struct {
	nativeMessages
	ctx     context.Context
	entered chan struct{}
}

func (c canceledMessages) Wait(string, string, wire.View, string, string, int64) (wire.Page, error) {
	close(c.entered)
	<-c.ctx.Done()
	return wire.Page{}, c.ctx.Err()
}

func TestMessagesActiveWaitCancellationDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	entered := make(chan struct{})
	done := make(chan wire.Page, 1)
	calls := 0
	b := &messagesBackend{instance: "actual", contextName: "kept", native: func(c context.Context) (nativeMessages, error) {
		calls++
		return canceledMessages{ctx: c, entered: entered}, nil
	}}
	go func() { out, _ := b.MessagesWait(ctx, gateway.MessagesWaitInput{WaitMs: 30000}); done <- out }()
	<-entered
	cancel()
	select {
	case out := <-done:
		if out.Outcome != wire.OutcomeUnknown || calls != 1 {
			t.Fatal("cancellation retried or claimed absence")
		}
	case <-time.After(time.Second):
		t.Fatal("wait ignored cancellation")
	}
}

func TestMessagesInboxPaginationAndForeignGap(t *testing.T) {
	p := &messagesProbe{}
	b := &messagesBackend{native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}}
	first, err := b.MessagesInboxesList(context.Background(), gateway.MessagesListInput{})
	if err != nil || len(first.Inboxes) != 64 || first.Cursor != "next" || p.after != "" {
		t.Fatalf("first %+v %v", first, err)
	}
	second, err := b.MessagesInboxesList(context.Background(), gateway.MessagesListInput{Cursor: first.Cursor})
	if err != nil || len(second.Inboxes) != 1 || second.Inboxes[0].Program != "app:last" || p.after != "next" || p.calls != 2 {
		t.Fatalf("second %+v %v", second, err)
	}
	gap, err := b.MessagesInboxesList(context.Background(), gateway.MessagesListInput{Cursor: "foreign"})
	if err != nil || gap.Outcome != wire.OutcomeGap || len(gap.Inboxes) != 0 || p.calls != 3 {
		t.Fatalf("foreign %+v %v", gap, err)
	}
}

func (p *messagesProbe) InboxOpenChat(i, c, s string, claim wire.ChatBinding) (wire.ChatInboxChange, error) {
	out, err := p.InboxOpen(i, c, s)
	contextName := ""
	if claim.ChatID != "" {
		contextName = fixtureNativeChatContext(claim.ChatID)
		if i == claim.SourceInstance && out.Outcome == wire.OutcomeOpened {
			out.Outcome = wire.OutcomePage
		}
	}
	return wire.ChatInboxChange{Outcome: out.Outcome, Context: contextName, Revision: out.Revision, Holder: out.Holder, OpenedUnixMs: out.OpenedUnixMs}, err
}

func (p *messagesProbe) InboxesForPerson(string) (wire.InboxPage, error) {
	return wire.InboxPage{Outcome: wire.OutcomeForbidden}, nil
}
