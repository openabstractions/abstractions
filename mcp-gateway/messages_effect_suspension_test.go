package main

import (
	"context"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// suspendingSendProbe holds a Send at the runtime until the test releases it,
// and records whether the exchange's context ended meanwhile.
type suspendingSendProbe struct {
	*messagesProbe
	started, release chan struct{}
	exchange         *context.Context
	cancelled        bool
}

func (p *suspendingSendProbe) Send(i, c string, r wire.SendRequest) (wire.SendResult, error) {
	close(p.started)
	<-p.release
	p.cancelled = (*p.exchange).Err() != nil
	if p.cancelled {
		// The native transport fails an exchange whose context ended.
		return wire.SendResult{}, context.Cause(*p.exchange)
	}
	return p.messagesProbe.Send(i, c, r)
}

func (p *suspendingSendProbe) Reply(i, c, id, body string) (wire.SendResult, error) {
	close(p.started)
	<-p.release
	p.cancelled = (*p.exchange).Err() != nil
	if p.cancelled {
		return wire.SendResult{}, context.Cause(*p.exchange)
	}
	return wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "r"}, nil
}

// Owner, R20c 2026-11-10: OpenCode's Send answered reply_lost while its
// helper's presence renewal missed its budget. The suspension cancelled the
// dispatched exchange. An admitted Send or Reply now runs to the runtime's
// answer; the next call still waits for admission.
func TestMessagesEffectSurvivesAdmissionSuspension(t *testing.T) {
	for _, method := range []string{"send", "reply"} {
		t.Run(method, func(t *testing.T) {
			var exchange context.Context
			p := &suspendingSendProbe{messagesProbe: &messagesProbe{}, started: make(chan struct{}), release: make(chan struct{}), exchange: &exchange}
			admission := &agentToolAdmission{}
			admission.confirm(time.Now().Add(time.Minute))
			b := &messagesBackend{application: "oa-opencode-integration", instance: "instance", contextName: "native-source", status: &messagesInboxStatus{admission: admission}, native: func(ctx context.Context) (nativeMessages, error) {
				exchange = ctx
				return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
			}}
			call, done, ok := admission.begin(context.Background())
			if !ok {
				t.Fatal("not admitted")
			}
			defer done()
			type result struct {
				out wire.SendResult
				err error
			}
			results := make(chan result, 1)
			go func() {
				var out wire.SendResult
				var err error
				if method == "send" {
					out, err = b.MessagesSend(call, gateway.MessagesSendInput{To: "app:oa-claude-integration", Context: "chat-0123456789abcdef", Kind: "note", Body: "hi", IdempotencyKey: "k", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()})
				} else {
					out, err = b.MessagesReply(call, gateway.MessagesReplyInput{MessageID: "q", Body: "hi"})
				}
				results <- result{out, err}
			}()
			<-p.started
			// The presence owner's renewal missed its budget.
			admission.setReady(false)
			if call.Err() == nil {
				t.Fatal("suspension left the tool call admitted")
			}
			close(p.release)
			got := <-results
			if got.err != nil || got.out.Outcome != wire.OutcomeSent || p.cancelled {
				t.Fatal("admitted effect lost its answer", got.out, got.err, p.cancelled)
			}
		})
	}
}

// The host's own cancellation still ends the exchange.
func TestMessagesEffectFollowsHostCancellation(t *testing.T) {
	var exchange context.Context
	p := &suspendingSendProbe{messagesProbe: &messagesProbe{}, started: make(chan struct{}), release: make(chan struct{}), exchange: &exchange}
	admission := &agentToolAdmission{}
	admission.confirm(time.Now().Add(time.Minute))
	b := &messagesBackend{application: "oa-opencode-integration", instance: "instance", contextName: "native-source", status: &messagesInboxStatus{admission: admission}, native: func(ctx context.Context) (nativeMessages, error) {
		exchange = ctx
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}}
	host, cancel := context.WithCancel(context.Background())
	call, done, ok := admission.begin(host)
	if !ok {
		t.Fatal("not admitted")
	}
	defer done()
	results := make(chan wire.SendResult, 1)
	go func() {
		out, _ := b.MessagesSend(call, gateway.MessagesSendInput{To: "app:oa-claude-integration", Context: "chat-0123456789abcdef", Kind: "note", Body: "hi", IdempotencyKey: "k", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()})
		results <- out
	}()
	<-p.started
	cancel()
	select {
	case <-exchange.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("host cancellation did not reach the exchange")
	}
	close(p.release)
	if out := <-results; out.Outcome != wire.OutcomeUnknown || !p.cancelled {
		t.Fatal("host cancellation ignored", out, p.cancelled)
	}
}

// readmittingProbe models the runtime: a refused stale instance records
// nothing, and one record exists per idempotency key.
type readmittingProbe struct {
	*messagesProbe
	status  *messagesInboxStatus
	records map[string]int
	seen    []string
}

func (p *readmittingProbe) Send(i, c string, r wire.SendRequest) (wire.SendResult, error) {
	p.seen = append(p.seen, i)
	if i != "new" {
		// The process re-admitted its context while this call was in flight.
		p.status.setInstance("new")
		return wire.SendResult{Outcome: wire.OutcomeForbidden}, nil
	}
	p.records[r.IdempotencyKey]++
	return wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "m"}, nil
}

func (p *readmittingProbe) Reply(i, c, id, body string) (wire.SendResult, error) {
	p.seen = append(p.seen, i)
	if i != "new" {
		p.status.setInstance("new")
		return wire.SendResult{Outcome: wire.OutcomeForbidden}, nil
	}
	p.records[id]++
	return wire.SendResult{Outcome: wire.OutcomeSent, MessageID: "r"}, nil
}

// A re-admission between dispatch and answer resends once under the new
// instance with the same key.
func TestMessagesEffectResendsOnceAfterReadmission(t *testing.T) {
	for _, method := range []string{"send", "reply"} {
		t.Run(method, func(t *testing.T) {
			status := &messagesInboxStatus{}
			p := &readmittingProbe{messagesProbe: &messagesProbe{}, status: status, records: map[string]int{}}
			b := &messagesBackend{application: "oa-opencode-integration", instance: "old", contextName: "native-source", status: status, native: func(context.Context) (nativeMessages, error) {
				return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
			}}
			var out wire.SendResult
			var err error
			if method == "send" {
				out, err = b.MessagesSend(context.Background(), gateway.MessagesSendInput{To: "app:oa-claude-integration", Context: "chat-0123456789abcdef", Kind: "note", Body: "hi", IdempotencyKey: "k", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()})
			} else {
				out, err = b.MessagesReply(context.Background(), gateway.MessagesReplyInput{MessageID: "q", Body: "hi"})
			}
			total := 0
			for _, n := range p.records {
				total += n
			}
			if err != nil || out.Outcome != wire.OutcomeSent || total != 1 || len(p.seen) != 2 || p.seen[0] != "old" || p.seen[1] != "new" {
				t.Fatal(out, err, p.records, p.seen)
			}
		})
	}
}

// A call whose context ended before dispatch answers unavailable and sends
// nothing.
func TestMessagesEffectRefusesUndispatchedCancelledCall(t *testing.T) {
	p := &messagesProbe{}
	dialed := false
	b := &messagesBackend{application: "oa-opencode-integration", instance: "instance", contextName: "native-source", status: &messagesInboxStatus{}, native: func(context.Context) (nativeMessages, error) {
		dialed = true
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	out, err := b.MessagesSend(ctx, gateway.MessagesSendInput{To: "app:oa-claude-integration", Context: "chat-0123456789abcdef", Kind: "note", Body: "hi", IdempotencyKey: "k", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()})
	if err != nil || out.Outcome != wire.OutcomeUnavailable || dialed || p.calls != 0 {
		t.Fatal(out, err, dialed, p.calls)
	}
	reply, err := b.MessagesReply(ctx, gateway.MessagesReplyInput{MessageID: "q", Body: "hi"})
	if err != nil || reply.Outcome != wire.OutcomeUnavailable || dialed {
		t.Fatal(reply, err, dialed)
	}
}
