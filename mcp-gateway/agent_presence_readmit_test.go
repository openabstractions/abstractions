package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
)

func readmitImmediately(time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	ch <- time.Time{}
	return ch
}

func TestChatSourceTerminalClassesDispatchNoDependentPresence(t *testing.T) {
	for _, cause := range []error{&messages.Refusal{Word: "bad_string"}, messages.DispatchError("mismatched_response"), &client.ResolutionError{Status: client.ResolutionErrorStatus("forbidden")}, listen.ErrServerUntrusted} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		a := new(agentToolAdmission)
		a.confirm(time.Now().Add(time.Minute))
		p := &statusMessagesProbe{err: cause}
		b := &messagesBackend{instance: "original-source", contextName: "original-context", status: &messagesInboxStatus{admission: a, chatSource: true}, native: func(context.Context) (nativeMessages, error) { return p, nil }}
		status := &messagesInboxStatus{admission: new(agentToolAdmission), chatReceiver: true, chatSource: true, chatID: codexAttachThread, chatSourceProof: b.chatSourceProof}
		directory := new(agentPresenceFixture)
		done := make(chan struct{})
		go func() {
			defer close(done)
			superviseAgentPresence(ctx, func(ctx context.Context, ready func(string), readmit bool, _ func()) error {
				return agentPresenceAttempt(ctx, directory, func(context.Context) nativeMessages { return p }, "oa-codex-integration", fixtureNativeChatContext(codexAttachThread), ready, status, readmit)
			}, status, readmitImmediately, time.Now)
		}()
		for {
			status.admission.mu.Lock()
			final := status.admission.final
			status.admission.mu.Unlock()
			if final || ctx.Err() != nil {
				break
			}
			time.Sleep(time.Millisecond)
		}
		cancel()
		<-done
		if !status.admission.final || len(directory.announcements) != 0 || len(p.snapshot()) != 1 {
			t.Fatal("source refusal dispatched dependent presence", cause, status.admission.final, len(directory.announcements), len(p.snapshot()))
		}
		a.mu.Lock()
		refusal := a.refused
		a.mu.Unlock()
		if !errors.Is(refusal, cause) {
			t.Fatal("original source cause lost", refusal, cause)
		}
	}
}

func TestChatSourceEstablishedUnknownFieldWaitsForOriginalProof(t *testing.T) {
	a := new(agentToolAdmission)
	cause := &messages.Refusal{Word: "unknown_field"}
	a.confirm(time.Now().Add(time.Minute))
	a.refuse(cause, false)
	p := new(statusMessagesProbe)
	b := &messagesBackend{instance: "original-source", contextName: "original-context", status: &messagesInboxStatus{admission: a, chatSource: true}, native: func(context.Context) (nativeMessages, error) { return p, nil }}
	_, finish, _, err := b.chatSourceProof(t.Context())
	finish()
	if !agentVersionSkew(err) || agentPresenceFinal(t.Context(), err) || len(p.snapshot()) != 0 {
		t.Fatal("known skew dispatched or became permanently final", err, p.snapshot())
	}
	a.confirm(time.Now().Add(time.Minute))
	_, finish, outcome, err := b.chatSourceProof(t.Context())
	finish()
	if err != nil || outcome != messages.OutcomeOpened || len(p.snapshot()) != 1 {
		t.Fatal("same original source did not reprove", outcome, err, p.snapshot())
	}
}

// After a recoverable loss the same connection keeps its server and context:
// the old instance is left to lapse, tools answer unavailable, and a new instance
// opens the same context. Calls then use the new instance.
func TestAgentPresenceReadmitsSameContextUnderNewInstance(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	f := &agentPresenceFixture{announce: func(n int) (wire.ApplicationChange, error) {
		switch n {
		case 1:
			return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "first"}, nil
		case 2: // the runtime no longer knows the original lease
			return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeStale}, nil
		default:
			return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "second"}, nil
		}
	}}
	var opened []string
	open := func(_ context.Context, instance, contextName string) error {
		opened = append(opened, instance+"/"+contextName)
		return nil
	}
	never := make(chan time.Time)
	attempts := 0
	var unavailableBetween bool
	superviseAgentPresence(ctx, func(ctx context.Context, ready func(string), readmit bool, stop func()) error {
		attempts++
		ticks := make(chan time.Time, 1)
		if attempts == 1 {
			ticks <- time.Time{}
		} else {
			_, _, admitted := status.admission.begin(ctx)
			unavailableBetween = !admitted
		}
		return maintainAgentPresenceWithPolicy(ctx, f, open, "fixture-agent", "native-kept", func(instance string) {
			ready(instance)
			if instance == "second" {
				cancel()
			}
		}, ticks, agentPresencePolicy{admission: status.admission, readmit: readmit, instance: status.setInstance, after: func(time.Duration) <-chan time.Time { return never }}, stop)
	}, status, readmitImmediately, time.Now)
	if attempts != 2 {
		t.Fatal(attempts)
	}
	if !unavailableBetween {
		t.Fatal("tools stayed available while no inbox was open")
	}
	if status.currentInstance() != "second" || !reflect.DeepEqual(opened, []string{"first/native-kept", "second/native-kept"}) {
		t.Fatal(status.currentInstance(), opened)
	}
	// The lost instance lapses at the runtime; withdrawing it would tell
	// senders the chat closed while this connection re-admits.
	if !reflect.DeepEqual(f.withdrawals, []string{"fixture-agent:second"}) {
		t.Fatal(f.withdrawals)
	}
	b := &messagesBackend{instance: "first", status: status}
	if b.inboxInstance() != "second" {
		t.Fatal("backend kept the withdrawn instance")
	}
}

// supervisedWaits records each wait the supervisor asks for and cancels after
// the given count, answering every wait at once.
func supervisedWaits(cancel context.CancelFunc, limit int) (*[]time.Duration, func(time.Duration) <-chan time.Time) {
	var waits []time.Duration
	return &waits, func(d time.Duration) <-chan time.Time {
		waits = append(waits, d)
		if len(waits) >= limit {
			cancel()
		}
		return readmitImmediately(d)
	}
}

// Another holder keeping the context for longer than one lease length is
// refused and retried slowly; it is never taken over and the connection stays.
func TestAgentPresenceHeldIsRefusedAndRetriedSlowly(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	clock := time.Now()
	attempts := 0
	held := &agentInboxOutcomeError{outcome: messages.OutcomeHeld}
	waits, after := supervisedWaits(cancel, 4)
	superviseAgentPresence(ctx, func(_ context.Context, ready func(string), _ bool, _ func()) error {
		attempts++
		clock = clock.Add(20 * time.Second)
		if attempts == 1 {
			ready("first")
			status.admission.confirm(clock.Add(time.Hour))
			return &errAgentPresenceLost{cause: errors.New("agent presence original confirmed lease expired")}
		}
		return &errAgentPresenceLost{cause: held}
	}, status, after, func() time.Time { return clock })
	// Held within one lease length backs off as usual; held for longer is
	// refused and waits the slow cadence.
	want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, agentPresenceRefusedRetry}
	if !reflect.DeepEqual(*waits, want) || attempts != 4 {
		t.Fatal(*waits, attempts)
	}
	if _, _, ok := status.admission.begin(context.Background()); ok {
		t.Fatal("held context left tools available")
	}
	if state, detail := status.admission.unavailable(); state != agentToolRefused || detail == "" {
		t.Fatal(state, detail)
	}
}

// An enrollment refusal is final for the connection once a fresh attempt
// confirms it: presence is never retried and tools answer refused until the
// host closes stdio (architect review 10, B1). An announce refusal needs no
// confirmation. A first admission that fails, for example while the runtime
// is down, is retried with backoff until it succeeds.
func TestAgentPresenceRefusalIsFinalAndInitialFailuresAreRetried(t *testing.T) {
	for _, refusal := range []error{
		&agentInboxOutcomeError{outcome: messages.OutcomeForbidden},
		&agentInboxOutcomeError{outcome: messages.OutcomeInvalid},
		&agentInboxOutcomeError{outcome: messages.OutcomeEnded},
		establishedChatError(&messages.Refusal{Word: "bad_string"}),
		establishedChatError(messages.DispatchError("mismatched_response")),
		establishedChatError(&client.ResolutionError{Status: client.ResolutionErrorStatus("forbidden")}),
		establishedChatError(listen.ErrServerUntrusted),
		&agentPresenceTerminalError{cause: errors.New("native chat context changed")},
		&agentAnnounceOutcomeError{outcome: wire.ApplicationOutcomeForbidden},
	} {
		ctx, cancel := context.WithCancel(context.Background())
		status := &messagesInboxStatus{admission: new(agentToolAdmission)}
		attempts := 0
		var waits []time.Duration
		done := make(chan struct{})
		go func() {
			defer close(done)
			superviseAgentPresence(ctx, func(_ context.Context, ready func(string), _ bool, _ func()) error {
				attempts++
				if attempts == 1 {
					ready("first")
					return &errAgentPresenceLost{cause: errors.New("lost")}
				}
				return refusal
			}, status, func(d time.Duration) <-chan time.Time { waits = append(waits, d); return readmitImmediately(d) }, time.Now)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if state, _ := status.admission.unavailable(); state == agentToolRefused {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("refusal never recorded", refusal)
			}
			time.Sleep(5 * time.Millisecond)
		}
		time.Sleep(50 * time.Millisecond)
		cancel()
		<-done
		wantAttempts, want := 3, []time.Duration{time.Second, agentReadmitFirstDelay}
		var announce *agentAnnounceOutcomeError
		var inbox *agentInboxOutcomeError
		var terminal *agentPresenceTerminalError
		if errors.As(refusal, &announce) || errors.As(refusal, &inbox) && inbox.outcome == messages.OutcomeEnded || errors.As(refusal, &terminal) {
			wantAttempts, want = 2, []time.Duration{time.Second}
		}
		if attempts != wantAttempts || !reflect.DeepEqual(waits, want) {
			t.Fatal(refusal, attempts, waits)
		}
		status.admission.confirm(time.Now().Add(time.Minute))
		if _, _, ok := status.admission.begin(context.Background()); ok {
			t.Fatal("a final refusal was revived by a confirmation")
		}
	}
	initial := errors.New("runtime unavailable at start")
	ctx, cancel := context.WithCancel(context.Background())
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	attempts := 0
	waits, after := supervisedWaits(cancel, 7)
	superviseAgentPresence(ctx, func(_ context.Context, ready func(string), _ bool, _ func()) error {
		attempts++
		return initial
	}, status, after, time.Now)
	if want := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second, 30 * time.Second}; !reflect.DeepEqual(*waits, want) || attempts != 7 {
		t.Fatal(*waits, attempts)
	}
	if state, _ := status.admission.unavailable(); state != agentToolConnecting {
		t.Fatal("never admitted connection state", state)
	}
}

// A runtime newer than this helper (its decoder refuses an unknown field, as
// the 10-30 copy refused Inbox.reach) is named as version skew, with the
// update to make, and retried slowly.
func TestAgentPresenceVersionSkewIsNamed(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	waits, after := supervisedWaits(cancel, 2)
	superviseAgentPresence(ctx, func(context.Context, func(string), bool, func()) error {
		return fmt.Errorf("agent presence renewal inbox open: %w", &messages.Refusal{Word: "unknown_field", Offset: 40})
	}, status, after, time.Now)
	if want := []time.Duration{agentPresenceRefusedRetry, agentPresenceRefusedRetry}; !reflect.DeepEqual(*waits, want) {
		t.Fatal(*waits)
	}
	if state, detail := status.admission.unavailable(); state != agentToolRefused || !strings.Contains(detail, "Update the connection") {
		t.Fatal(state, detail)
	}
}

// A panic inside a presence attempt is recovered and retried.
func TestAgentPresencePanicIsRecovered(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	attempts := 0
	_, after := supervisedWaits(cancel, 2)
	superviseAgentPresence(ctx, func(context.Context, func(string), bool, func()) error {
		attempts++
		panic("synthetic presence fault")
	}, &messagesInboxStatus{admission: new(agentToolAdmission)}, after, time.Now)
	if attempts != 2 {
		t.Fatal(attempts)
	}
}
