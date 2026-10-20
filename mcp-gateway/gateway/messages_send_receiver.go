package gateway

import (
	"context"
	"encoding/json"
	"strconv"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Owner, R20c 2026-11-10: OpenCode called oa_messages_prepare twice and never
// sent; told to continue, it copied send_arguments without answer_by_unix_ms.
// The one-call form runs the same native Prepare and passes its exact request
// to Send, on this connection and under this tool call's admission. Prepare's
// refusals, permission steps and choices come back unchanged with nothing sent.

// messagesSendPending keeps, for this connection only, the exact prepared
// request of each one-call Send whose outcome was uncertain. The same one-call
// Send again reconciles with those identical arguments, and the runtime's
// idempotency key answers with the original outcome; it never prepares a
// second request with a new key.
type messagesSendPending struct {
	mu   sync.Mutex
	rows map[string]MessagesSendInput
}

func newMessagesSendPending() *messagesSendPending {
	return &messagesSendPending{rows: map[string]MessagesSendInput{}}
}

func (p *messagesSendPending) key(in MessagesSendInput) string {
	key, _ := json.Marshal([]string{MessageReceiverName(in.Receiver), in.Context, in.Kind, in.Subject, in.Body})
	return string(key)
}

func (p *messagesSendPending) take(in MessagesSendInput, now time.Time) (MessagesSendInput, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := p.key(in)
	args, ok := p.rows[key]
	if ok && args.ExpiresUnixMs <= now.UnixMilli() {
		delete(p.rows, key)
		return MessagesSendInput{}, false
	}
	return args, ok
}

func (p *messagesSendPending) settle(in, args MessagesSendInput, out wire.SendResult) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if messageSendUncertain(out) {
		p.rows[p.key(in)] = args
		return
	}
	delete(p.rows, p.key(in))
}

// messageSendUncertain names a Send outcome that does not prove absence of
// acceptance.
func messageSendUncertain(out wire.SendResult) bool {
	return out.Outcome == wire.OutcomeUnknown || out.Outcome == wire.OutcomeUnavailable
}

func sendByReceiver(ctx context.Context, b MessagesBackend, in MessagesSendInput, pending *messagesSendPending) (*mcp.CallToolResult, error) {
	if in.To != "" || in.IdempotencyKey != "" || in.ExpiresUnixMs != 0 || in.ReplyTo != "" || in.Withdraw != "" || in.AnswerByUnixMs != 0 || in.PeerKey != "" || in.ReplyReference != nil || in.WithdrawReference != nil || in.Kind == "reply" {
		out := wire.SendResult{Outcome: wire.OutcomeInvalid, Reason: "receiver_form"}
		r := messageEncodedContent(wire.EncodeSendResult(&out))
		r.Content = append(r.Content, assistantText("Nothing was sent. receiver sends a new question or note with body and optional context, kind and subject. Pass prepared send_arguments without receiver."))
		return r, nil
	}
	args, reconcile := pending.take(in, time.Now())
	var prepared MessagesPrepareOutput
	if !reconcile {
		native, ok := b.(MessagesPrepareBackend)
		if !ok {
			out := wire.SendResult{Outcome: wire.OutcomeUnavailable, Reason: "prepare_unavailable"}
			return messageEncodedContent(wire.EncodeSendResult(&out)), nil
		}
		var err error
		prepared, err = native.MessagesPrepare(ctx, MessagesPrepareInput{Receiver: in.Receiver, Context: in.Context, Kind: in.Kind, Subject: in.Subject, Body: in.Body})
		if err != nil {
			return nil, err
		}
		if prepared.Outcome != "prepared" || prepared.SendArguments == nil {
			prepared.Next = "Nothing was sent. Follow guidance; when it is resolved, call oa_messages_send again with the same receiver and body, adding the chosen context when guidance lists choices."
			raw, err := json.Marshal(prepared)
			if err != nil {
				return nil, err
			}
			return messageEncodedContent(raw), nil
		}
		args = *prepared.SendArguments
		args.NotifyDelivered = in.NotifyDelivered
	}
	out, err := b.MessagesSend(ctx, args)
	if err != nil {
		return nil, err
	}
	pending.settle(in, args, out)
	r := sendResultContent(out, args)
	if out.Outcome == wire.OutcomeSent && prepared.Tell != "" {
		r.Content = append(r.Content, assistantText(prepared.Tell))
	}
	return r, nil
}

// SendUncertainLead opens the step sendResultContent adds to exactly the Send
// results kept for reconciliation. The agent handover recognises uncertain
// Sends by it; a Prepare refusal says unavailable too but sent nothing.
const SendUncertainLead = "The send may or may not have been accepted"

// sendResultContent is the generated Send result, with one plain next step
// when its outcome is uncertain: the same arguments again on this connection,
// unless OpenAbstractions reconnected or the connection closed since.
func sendResultContent(out wire.SendResult, args MessagesSendInput) *mcp.CallToolResult {
	r := messageReceiptContent(out)
	if !messageSendUncertain(out) {
		return r
	}
	next := SendUncertainLead + "; this is not a failure to report yet. Call oa_messages_send again on this connection with the same arguments"
	if args.Receiver == "" {
		if exact, err := json.Marshal(args); err == nil {
			next += " exactly: " + string(exact)
		}
	}
	next += ". On this same connection OA answers with the original outcome and delivers the message at most once. If OpenAbstractions reconnected or this connection closed since, do not send again; tell the person the outcome is not known. Do not prepare again. If it stays uncertain, tell the person the outcome is not known yet."
	r.Content = append(r.Content, assistantText(next))
	return r
}

func messageReceiptContent(out wire.SendResult) *mcp.CallToolResult {
	r := messageEncodedContent(wire.EncodeSendResult(&out))
	if out.Outcome == wire.OutcomeSent && out.ChatTitle != "" {
		r.Content = append(r.Content, assistantText("Tell the person the chat title the result names: "+strconv.Quote(out.ChatTitle)+"."))
	}
	return r
}

func assistantText(text string) *mcp.TextContent {
	return &mcp.TextContent{Text: text, Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}}}
}
