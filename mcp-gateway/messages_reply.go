package main

import (
	"context"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// messagesEffectBudget bounds one admitted Send or Reply exchange, as the
// native transport's own default does.
var messagesEffectBudget = 35 * time.Second

// messagesEffectContext lets an admitted Send or Reply run to the runtime's
// answer. A presence renewal that misses its budget suspends this connection's
// admission and cancels its calls; cancelling a dispatched effect only turns
// its outcome into reply_lost (owner, R20c 2026-11-10: "Claude: Reply lost").
// The runtime rechecks the sender's inbox and authority for the request, so
// the effect gains nothing from the local suspension. The host's own
// cancellation and deadline end it as before, within this budget. A call
// whose context already ended was never dispatched; ok is false and the
// caller answers unavailable.
func messagesEffectContext(ctx context.Context) (_ context.Context, _ context.CancelFunc, ok bool) {
	if ctx.Err() != nil {
		return ctx, func() {}, false
	}
	caller := agentAdmissionCaller(ctx)
	effect, cancel := context.WithTimeout(context.WithoutCancel(ctx), messagesEffectBudget)
	stop := context.AfterFunc(caller, cancel)
	return effect, func() { stop(); cancel() }, true
}

// messagesReadmitted repeats one effect once under the connection's new
// presence instance when the runtime refused the instance the call was
// dispatched under. The same idempotency key keeps the runtime record single.
func (b *messagesBackend) messagesReadmitted(effect func(instance string) (wire.SendResult, error)) (wire.SendResult, error) {
	instance := b.inboxInstance()
	out, err := effect(instance)
	if err == nil && out.Outcome == wire.OutcomeForbidden {
		if current := b.inboxInstance(); current != instance {
			return effect(current)
		}
	}
	return out, err
}

// The native owner derives routing and enforces the current canonical chat,
// open question, deadline and same-body reconciliation. No adapter read gate.
func (b *messagesBackend) MessagesReply(ctx context.Context, in gateway.MessagesReplyInput) (wire.SendResult, error) {
	if receiver, ok := b.chatBodyBackend(ctx); !ok {
		return wire.SendResult{Outcome: wire.OutcomeUnavailable, Reason: "chat_owner_unavailable"}, nil
	} else if receiver != b {
		return receiver.MessagesReply(ctx, in)
	}
	ctx, cancel, ok := messagesEffectContext(ctx)
	defer cancel()
	if !ok {
		return wire.SendResult{Outcome: wire.OutcomeUnavailable}, nil
	}
	c, err := b.native(ctx)
	if err != nil {
		return wire.SendResult{Outcome: wire.OutcomeUnavailable}, nil
	}
	out, err := b.messagesReadmitted(func(instance string) (wire.SendResult, error) {
		return c.Reply(instance, b.contextName, in.MessageID, in.Body)
	})
	if err != nil {
		return wire.SendResult{Outcome: wire.OutcomeUnknown, Reason: messageReplyCause("Reply", err)}, nil
	}
	return out, nil
}
