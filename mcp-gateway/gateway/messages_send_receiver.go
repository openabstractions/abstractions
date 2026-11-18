package gateway

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
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

// The one plain next step of each receiver_form refusal.
const (
	receiverFormReply    = "Nothing was sent. Answer a question with oa_messages_reply, passing its message_id and body."
	receiverFormWithdraw = "Nothing was sent. Add the context the question was sent to, then call oa_messages_send again with receiver, context and withdraw."
	receiverFormPaired   = "Nothing was sent. Call oa_messages_prepare for the paired machine's receiver and pass its send_arguments without receiver."
	receiverFormTo       = "Nothing was sent. Leave out to, or make it name the same agent as receiver, and call oa_messages_send again."
)

// receiverFormRefusal answers a receiver send whose other fields ask for a
// reply, a paired peer, a withdrawal without its chat or a different
// receiver. step says what the caller does next.
func receiverFormRefusal(step string) *mcp.CallToolResult {
	out := wire.SendResult{Outcome: wire.OutcomeInvalid, Reason: "receiver_form"}
	r := messageEncodedContent(wire.EncodeSendResult(&out))
	r.Content = append(r.Content, assistantText(step))
	return r
}

// receiverWithdraw builds the withdrawal of an earlier question by receiver
// and context (2026-11-22: withdraw with receiver app:oa-codex-integration and
// context answered receiver_form). The native service checks that this chat
// sent that question to that receiver and chat. A retry with the same
// arguments reuses the same idempotency key.
func receiverWithdraw(in MessagesSendInput, now time.Time) MessagesSendInput {
	key := in.IdempotencyKey
	if key == "" || len(key) > 128 {
		key = "withdraw:" + in.Withdraw
		if len(key) > 128 {
			key = key[:128]
		}
	}
	expires := in.ExpiresUnixMs
	if expires <= now.UnixMilli() {
		expires = now.Add(time.Hour).UnixMilli()
	}
	return MessagesSendInput{To: "app:" + strings.TrimPrefix(MessageReceiverName(in.Receiver), "app:"), Context: in.Context, Kind: "note", Subject: in.Subject, Body: in.Body, IdempotencyKey: key, ExpiresUnixMs: expires, Withdraw: in.Withdraw, NotifyDelivered: in.NotifyDelivered}
}

// receiverNamesSame reports a to field that names the receiver the
// one-call form prepared: its receiver name or the prepared app:<name>.
func receiverNamesSame(to, receiver, prepared string) bool {
	name := func(label string) string { return strings.TrimPrefix(MessageReceiverName(label), "app:") }
	return to == "" || strings.EqualFold(name(to), name(receiver)) || strings.EqualFold(name(to), name(prepared))
}

// adoptCallerFields keeps the prepared-form fields a model fills in beside
// receiver (2026-11-19: "Codex" and "app:oa-codex-integration" were refused
// with receiver_form). The caller's idempotency key makes a retry of the
// same message idempotent; an earlier expiry or answer-by within the prepared
// bounds narrows the request. Anything else keeps the prepared value.
func adoptCallerFields(args *MessagesSendInput, in MessagesSendInput, now time.Time) {
	if in.IdempotencyKey != "" && len(in.IdempotencyKey) <= 128 {
		args.IdempotencyKey = in.IdempotencyKey
	}
	if in.ExpiresUnixMs > now.UnixMilli() && in.ExpiresUnixMs < args.ExpiresUnixMs {
		args.ExpiresUnixMs = in.ExpiresUnixMs
	}
	if args.Kind == "question" && in.AnswerByUnixMs > now.UnixMilli() && in.AnswerByUnixMs < args.ExpiresUnixMs && (args.AnswerByUnixMs == 0 || in.AnswerByUnixMs < args.AnswerByUnixMs) {
		args.AnswerByUnixMs = in.AnswerByUnixMs
	}
}

func sendByReceiver(ctx context.Context, b MessagesBackend, in MessagesSendInput, pending *messagesSendPending) (*mcp.CallToolResult, error) {
	switch {
	case in.ReplyTo != "" || in.ReplyReference != nil || in.Kind == "reply":
		return receiverFormRefusal(receiverFormReply), nil
	case in.PeerKey != "" || in.WithdrawReference != nil:
		return receiverFormRefusal(receiverFormPaired), nil
	case in.Withdraw != "" && in.Context == "":
		return receiverFormRefusal(receiverFormWithdraw), nil
	case in.Withdraw != "" && !receiverNamesSame(in.To, in.Receiver, ""):
		return receiverFormRefusal(receiverFormTo), nil
	case in.Withdraw != "":
		args := receiverWithdraw(in, time.Now())
		out, err := b.MessagesSend(ctx, args)
		if err != nil {
			return nil, err
		}
		return sendResultContent(out, args), nil
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
			prepared.Next = "Nothing was sent. Follow guidance; when it is resolved, call oa_messages_send again with the same receiver, body and idempotency_key, adding the chosen choice's context when guidance lists choices."
			raw, err := json.Marshal(prepared)
			if err != nil {
				return nil, err
			}
			return messageEncodedContent(raw), nil
		}
		args = *prepared.SendArguments
		args.NotifyDelivered = in.NotifyDelivered
		if !receiverNamesSame(in.To, in.Receiver, args.To) {
			return receiverFormRefusal(receiverFormTo), nil
		}
		adoptCallerFields(&args, in, time.Now())
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
	if out.Outcome == wire.OutcomeInvalid && out.Reason != "" {
		r.Content = append(r.Content, assistantText("Nothing was sent. Correct the field the reason names and call oa_messages_send again, or call it with receiver and body alone."))
	}
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
	if out.Outcome == wire.OutcomeSent && out.Reason == CodexClosedReason {
		r.Content = append(r.Content, assistantText(CodexClosedText))
	}
	if out.Outcome == wire.OutcomeSent && out.ChatTitle != "" {
		r.Content = append(r.Content, assistantText("Tell the person the chat title the result names: "+strconv.Quote(out.ChatTitle)+"."))
	}
	return r
}

// A sent message to a closed Codex chat that OA will not wake carries
// CodexClosedReason; the result says CodexClosedText (architect ruling 1,
// 2026-11-22).
const (
	CodexClosedReason = "codex_closed"
	CodexClosedText   = "Codex is closed; messages wait until it opens."
)

func assistantText(text string) *mcp.TextContent {
	return &mcp.TextContent{Text: text, Annotations: &mcp.Annotations{Audience: []mcp.Role{"assistant"}}}
}
