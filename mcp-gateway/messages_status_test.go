package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type statusMessagesProbe struct {
	nativeMessages
	mu          sync.Mutex
	values      []string
	outcome     msg.Outcome
	err         error
	entered     chan struct{}
	release     chan struct{}
	chatContext *string
}

func (p *statusMessagesProbe) ObserveDelivery(msg.MessageRef, string) (msg.ObserveDeliveryResult, error) {
	return msg.ObserveDeliveryResult{Outcome: msg.DeliveryObservationOutcomeForbidden}, nil
}

func (p *statusMessagesProbe) ReadForPerson(string) (msg.Page, error) {
	return msg.Page{}, errors.New("person-only read is unavailable to the status probe")
}

func (p *statusMessagesProbe) StopForPerson(string) (msg.StopResult, error) {
	return msg.StopResult{}, errors.New("person-only stop is unavailable to the status probe")
}

func (p *statusMessagesProbe) TurnWake(string, string, string) (msg.TurnWakeResult, error) {
	return msg.TurnWakeResult{Outcome: msg.OutcomePage}, nil
}

func (p *statusMessagesProbe) InboxOpen(_, _, value string) (msg.InboxChange, error) {
	p.mu.Lock()
	p.values = append(p.values, value)
	n := len(p.values)
	out, err := p.outcome, p.err
	p.mu.Unlock()
	if n == 1 && p.entered != nil {
		close(p.entered)
		<-p.release
	}
	if out == 0 {
		out = msg.OutcomeOpened
	}
	return msg.InboxChange{Outcome: out, Revision: "revision"}, err
}
func (p *statusMessagesProbe) client() nativeMessages {
	return msg.NewMessagesClient(&msg.MessagesDispatcher{Handler: p})
}
func (p *statusMessagesProbe) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.values...)
}

func TestMessagesStatusRenewalAndExplicitEmptyClear(t *testing.T) {
	p := &statusMessagesProbe{}
	state := new(messagesInboxStatus)
	b := &messagesBackend{instance: "actual", contextName: "kept", native: func(context.Context) (nativeMessages, error) { return p.client(), nil }, status: state}
	for _, value := range []string{"busy", ""} {
		out, err := b.MessagesInboxOpen(context.Background(), gateway.MessagesOpenInput{Context: "kept", Status: value})
		if err != nil || out.Outcome != msg.OutcomeOpened {
			t.Fatal(out, err)
		}
		if out, err = state.open(p.client(), "actual", "kept", nil); err != nil || out.Outcome != msg.OutcomeOpened {
			t.Fatal(out, err)
		}
	}
	if got := p.snapshot(); !reflect.DeepEqual(got, []string{"busy", "busy", "", ""}) {
		t.Fatal(got)
	}
}

func TestMessagesStatusSerializesNativeCallAndConfirmation(t *testing.T) {
	p := &statusMessagesProbe{entered: make(chan struct{}), release: make(chan struct{})}
	state := new(messagesInboxStatus)
	done := make(chan error, 2)
	busy := "busy"
	go func() { _, err := state.open(p.client(), "actual", "kept", &busy); done <- err }()
	<-p.entered
	started := make(chan struct{})
	go func() { close(started); _, err := state.open(p.client(), "actual", "kept", nil); done <- err }()
	<-started
	select {
	case err := <-done:
		t.Fatal("renewal overlapped unconfirmed native Open", err)
	case <-time.After(20 * time.Millisecond):
	}
	if got := p.snapshot(); len(got) != 1 {
		t.Fatal("concurrent native status calls", got)
	}
	close(p.release)
	for range 2 {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if got := p.snapshot(); !reflect.DeepEqual(got, []string{"busy", "busy"}) {
		t.Fatal(got)
	}
}

func TestMessagesStatusLockWaitRespectsRenewalContext(t *testing.T) {
	state := new(messagesInboxStatus)
	state.mu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	p := &statusMessagesProbe{}
	_, err := state.openContext(ctx, p, "original", "ctx", nil)
	state.mu.Unlock()
	if !errors.Is(err, context.Canceled) || len(p.snapshot()) != 0 {
		t.Fatal(err, p.snapshot())
	}
}

func TestMessagesStatusRenewalUnknownRetriesOnlySameConfirmedStatus(t *testing.T) {
	p := &statusMessagesProbe{}
	state := new(messagesInboxStatus)
	busy := "busy"
	if _, err := state.open(p, "original", "ctx", &busy); err != nil {
		t.Fatal(err)
	}
	p.err = context.DeadlineExceeded
	if _, err := state.open(p, "original", "ctx", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	p.err = nil
	if out, err := state.open(p, "original", "ctx", nil); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	if got := p.snapshot(); !reflect.DeepEqual(got, []string{"busy", "busy", "busy"}) {
		t.Fatal(got)
	}
}

func TestMessagesStatusUnknownRefusesAutomaticRenewal(t *testing.T) {
	for _, mode := range []string{"transport", "outcome"} {
		t.Run(mode, func(t *testing.T) {
			p := &statusMessagesProbe{}
			state := new(messagesInboxStatus)
			busy := "busy"
			if _, err := state.open(p.client(), "actual", "kept", &busy); err != nil {
				t.Fatal(err)
			}
			if mode == "transport" {
				p.err = errors.New("reply lost")
			} else {
				p.outcome = msg.OutcomeUnknown
			}
			changed := "changed"
			_, _ = state.open(p.client(), "actual", "kept", &changed)
			p.err = nil
			p.outcome = msg.OutcomeOpened
			if _, err := state.open(p.client(), "actual", "kept", nil); !errors.Is(err, errMessagesInboxStatusUnknown) {
				t.Fatal(err)
			}
			if len(p.snapshot()) != 2 {
				t.Fatal("unknown status automatically overwritten")
			}
			// Only an explicit successful Open resolves the uncertainty.
			if _, err := state.open(p.client(), "actual", "kept", &changed); err != nil {
				t.Fatal(err)
			}
			if _, err := state.open(p.client(), "actual", "kept", nil); err != nil {
				t.Fatal(err)
			}
			if got := p.snapshot(); !reflect.DeepEqual(got, []string{"busy", "changed", "changed", "changed"}) {
				t.Fatal(got)
			}
		})
	}
}

func TestMessagesStatusLostReplyDoesNotBlockFreshLease(t *testing.T) {
	probe := &statusMessagesProbe{}
	state := new(messagesInboxStatus)
	confirmed := "ready"
	if _, err := state.open(probe, "original", "chat", &confirmed); err != nil {
		t.Fatal(err)
	}
	probe.err = context.DeadlineExceeded
	changed := "unconfirmed"
	if _, err := state.open(probe, "original", "chat", &changed); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	probe.err = nil
	if _, err := state.open(probe, "original", "chat", nil); !errors.Is(err, errMessagesInboxStatusUnknown) {
		t.Fatal("original lease should require explicit reconciliation", err)
	}
	if out, err := state.open(probe, "fresh", "chat", nil); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal("fresh lease should reopen with confirmed status", out, err)
	}
	if got := probe.snapshot(); !reflect.DeepEqual(got, []string{"ready", "unconfirmed", "ready"}) {
		t.Fatal(got)
	}
}

func TestMessagesStatusUnknownStopsPresenceBeforeWithdrawal(t *testing.T) {
	p := &statusMessagesProbe{}
	state := new(messagesInboxStatus)
	f := &agentPresenceFixture{announce: func(int) (facade.ApplicationChange, error) {
		return facade.ApplicationChange{Outcome: facade.ApplicationOutcomeApplied, Instance: "actual"}, nil
	}}
	ticks := make(chan time.Time, 1)
	ticks <- time.Time{}
	stopped := false
	err := maintainAgentPresence(context.Background(), f, func(context.Context, string, string) error {
		out, err := state.open(p.client(), "actual", "kept", nil)
		if err == nil && out.Outcome != msg.OutcomeOpened {
			return errors.New("inbox refused")
		}
		return err
	}, "agent", "kept", func(string) {
		p.err = errors.New("explicit reply lost")
		busy := "busy"
		_, _ = state.open(p.client(), "actual", "kept", &busy)
		p.err = nil
	}, ticks, func() {
		if len(f.withdrawals) != 0 {
			t.Fatal("withdrawn before server stop")
		}
		stopped = true
	})
	if !errors.Is(err, errMessagesInboxStatusUnknown) || !stopped || !reflect.DeepEqual(f.withdrawals, []string{"agent:actual"}) || len(p.snapshot()) != 2 {
		t.Fatal(err, stopped, f.withdrawals, p.snapshot())
	}
}

func (p *statusMessagesProbe) InboxOpenChat(i, c, s string, claim msg.ChatBinding) (msg.ChatInboxChange, error) {
	out, err := p.InboxOpen(i, c, s)
	contextName := ""
	if claim.ChatID != "" && (out.Outcome == msg.OutcomeOpened || out.Outcome == msg.OutcomePage) {
		contextName = fixtureNativeChatContext(claim.ChatID)
	}
	if p.chatContext != nil {
		contextName = *p.chatContext
	}
	return msg.ChatInboxChange{Outcome: out.Outcome, Context: contextName, Revision: out.Revision, Holder: out.Holder, OpenedUnixMs: out.OpenedUnixMs}, err
}

func TestMessagesTypedOpenProjectionOutcomeMatrix(t *testing.T) {
	for _, bound := range []bool{false, true} {
		for outcome := msg.OutcomeSent; outcome <= msg.OutcomeEnded; outcome++ {
			for _, returned := range []string{"", fixtureNativeChatContext(codexAttachThread), "wrong-context"} {
				t.Run(fmt.Sprintf("bound=%t/%s/context=%s", bound, outcome, returned), func(t *testing.T) {
					p := &statusMessagesProbe{outcome: outcome, chatContext: &returned}
					s := &messagesInboxStatus{admission: new(agentToolAdmission), chatSource: true, chatReceiver: bound, chatSourceContext: "source", chatSourceInstance: func() string { return "original-source" }}
					expected := ""
					if bound {
						s.chatID = codexAttachThread
						expected = fixtureNativeChatContext(s.chatID)
					}
					out, err := s.openContext(context.Background(), p, "receiver", fixtureNativeChatContext(codexAttachThread), nil)
					success := outcome == msg.OutcomeOpened || outcome == msg.OutcomePage
					if success && returned != expected {
						if err == nil || out.Outcome != msg.OutcomeUnavailable {
							t.Fatal("malformed success accepted", out, err)
						}
					} else if err != nil || out.Outcome != outcome {
						t.Fatal("authoritative outcome changed", out, err)
					}
					if s.admission.final != (outcome == msg.OutcomeEnded || success && returned != expected) {
						t.Fatal("explicit ended finality lost or transient outcome retired", outcome, s.admission.final)
					}
				})
			}
		}
	}
}

func TestMessagesTypedOpenSourceToBoundTransitionAndTransportError(t *testing.T) {
	p := new(statusMessagesProbe)
	s := &messagesInboxStatus{chatSource: true}
	if out, err := s.openContext(t.Context(), p, "source-instance", "source", nil); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	s.chatReceiver, s.chatID, s.chatSourceContext = true, codexAttachThread, "source"
	s.chatSourceInstance = func() string { return "source-instance" }
	if out, err := s.openContext(t.Context(), p, "receiver", fixtureNativeChatContext(s.chatID), nil); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	p.err = errors.New("transport lost")
	if _, err := s.openContext(t.Context(), p, "receiver", fixtureNativeChatContext(s.chatID), nil); !errors.Is(err, p.err) {
		t.Fatal("transport error coerced", err)
	}
}

func TestMessagesTypedReceiverChecksOriginalSourceBeforeNativeClaim(t *testing.T) {
	for _, outcome := range []msg.Outcome{msg.OutcomeUnavailable, msg.OutcomeEnded} {
		p := new(statusMessagesProbe)
		finished := false
		s := &messagesInboxStatus{chatReceiver: true, chatSource: true, chatSourceProof: func(context.Context) (nativeMessages, func(), msg.Outcome, error) {
			return nil, func() { finished = true }, outcome, nil
		}}
		out, err := s.openContext(t.Context(), p, "receiver", "canonical", nil)
		if err != nil || out.Outcome != outcome || len(p.snapshot()) != 0 || !finished {
			t.Fatal("source absence dispatched a native claim", out, err, p.snapshot(), finished)
		}
	}
}

type sourceScopedClaimProbe struct {
	*statusMessagesProbe
	ctx     context.Context
	entered chan struct{}
	claims  *atomic.Int32
}

func (p *sourceScopedClaimProbe) InboxOpenChat(string, string, string, msg.ChatBinding) (msg.ChatInboxChange, error) {
	p.claims.Add(1)
	close(p.entered)
	<-p.ctx.Done()
	return msg.ChatInboxChange{Outcome: msg.OutcomeUnknown}, p.ctx.Err()
}
func TestMessagesTypedClaimRetainsOriginalSourceCancellationAndDeadline(t *testing.T) {
	for _, expiry := range []bool{false, true} {
		t.Run(fmt.Sprint(expiry), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			a := new(agentToolAdmission)
			until := time.Now().Add(time.Minute)
			if expiry {
				until = time.Now().Add(75 * time.Millisecond)
			}
			a.confirm(until)
			entered := make(chan struct{})
			var claims atomic.Int32
			base := new(statusMessagesProbe)
			b := &messagesBackend{instance: "original-source", contextName: "original-context", status: &messagesInboxStatus{admission: a}, native: func(call context.Context) (nativeMessages, error) {
				return &sourceScopedClaimProbe{statusMessagesProbe: base, ctx: call, entered: entered, claims: &claims}, nil
			}}
			s := &messagesInboxStatus{chatReceiver: true, chatSource: true, chatID: codexAttachThread, chatSourceProof: b.chatSourceProof}
			done := make(chan error, 1)
			go func() {
				_, err := s.openContext(ctx, base, "receiver", fixtureNativeChatContext(codexAttachThread), nil)
				done <- err
			}()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("claim did not start", ctx.Err())
			}
			if !expiry {
				a.setReady(false)
			}
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					t.Fatal(err)
				}
			case <-ctx.Done():
				t.Fatal("claim outlived original source admission")
			}
			if ctx.Err() != nil {
				t.Fatal("source loss canceled unrelated caller")
			}
			out, _ := s.openContext(ctx, base, "receiver", fixtureNativeChatContext(codexAttachThread), nil)
			if out.Outcome != msg.OutcomeUnavailable || claims.Load() != 1 {
				t.Fatal("lost source dispatched another claim", out, claims.Load())
			}
		})
	}
}
