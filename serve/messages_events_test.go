package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesEventsObservationCannotRetireActiveTransition(t *testing.T) {
	s, _, actor, request := messagesStoreFixture(t)
	request.Kind, request.AnswerByUnixMs = msg.KindQuestion, s.now().Add(time.Minute).UnixMilli()
	if sent := s.accept(context.Background(), actor, "receiver", request, true, ""); sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	key := s.key(actor, request.IdempotencyKey)
	record, err := s.readRecord(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	reserved, release := make(chan struct{}), make(chan struct{})
	s.boundary = func(at string) error {
		if at == "state_event_reserved" {
			close(reserved)
			<-release
		}
		return nil
	}
	transition := make(chan error, 1)
	go func() { transition <- s.transitionState(context.Background(), key, record, "release") }()
	<-reserved
	type observed struct {
		refs []messageEventRef
		err  error
	}
	observation := make(chan observed, 1)
	go func() {
		refs, err := s.messageEventCandidates(context.Background(), actor, s.stateEventCheckFor(actor))
		observation <- observed{refs, err}
	}()
	var early *observed
	select {
	case result := <-observation:
		early = &result
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-transition; err != nil {
		t.Fatal("observation fenced the active publisher", err)
	}
	if early != nil {
		t.Fatal("observation recovered an active reservation", *early)
	}
	result := <-observation
	if result.err != nil || len(result.refs) != 1 {
		t.Fatal("committed transition missing", result)
	}
	state, err := s.currentState(context.Background(), key, record)
	if err != nil || state.Held || state.Version != 1 {
		t.Fatal(state, err)
	}
}

func eventRef(s *messageStore, n int) messageEventRef {
	return messageEventRef{Key: messageDigest(fmt.Sprint(n)), Token: "acceptance", Change: fmt.Sprint("change-", n), Version: uint64(n + 1), ExpiresUnixMs: s.now().Add(time.Hour).UnixMilli()}
}
func TestMessagesEventsLostReplyAndIndividualAck(t *testing.T) {
	s, _, actor, _ := messagesStoreFixture(t)
	ctx := context.Background()
	states := map[messageEventRef]messageEventDisposition{}
	check := func(_ context.Context, r messageEventRef) (messageEventDisposition, error) { return states[r], nil }
	refs := []messageEventRef{eventRef(s, 0), eventRef(s, 1)}
	commits := 0
	for _, r := range refs {
		if err := s.reserveMessageEvent(ctx, actor, r, check); err != nil {
			t.Fatal(err)
		}
		if err := s.reserveMessageEvent(ctx, actor, r, check); err != nil {
			t.Fatal("lost reserve reply", err)
		}
		if err := s.publishMessageEvent(ctx, actor, r, func() error { commits++; states[r] = messageEventReady; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 2; i++ {
		got, err := s.messageEventCandidates(ctx, actor, check)
		if err != nil || len(got) != 2 || got[0] != refs[0] || got[1] != refs[1] {
			t.Fatal(got, err)
		}
	}
	states[refs[1]] = messageEventObserved
	got, err := s.messageEventCandidates(ctx, actor, check)
	if err != nil || len(got) != 1 || got[0] != refs[0] {
		t.Fatal("ack must not imply prior versions", got, err)
	}
	if err = s.publishMessageEvent(ctx, actor, refs[1], func() error { commits++; return nil }); err == nil || commits != 2 {
		t.Fatal("old publisher repeated", commits, err)
	}
}
func TestMessagesEventsCrashAndReservationFence(t *testing.T) {
	for _, point := range []string{"event_pointer_appended", "event_state_committed"} {
		t.Run(point, func(t *testing.T) {
			s, _, actor, _ := messagesStoreFixture(t)
			ctx := context.Background()
			r := eventRef(s, 0)
			state := messageEventPending
			check := func(context.Context, messageEventRef) (messageEventDisposition, error) { return state, nil }
			if point == "event_pointer_appended" {
				s.boundary = func(p string) error {
					if p == point {
						return errors.New("crash")
					}
					return nil
				}
			}
			err := s.reserveMessageEvent(ctx, actor, r, check)
			if point == "event_state_committed" {
				if err != nil {
					t.Fatal(err)
				}
				s.boundary = func(p string) error {
					if p == point {
						return errors.New("crash")
					}
					return nil
				}
				err = s.publishMessageEvent(ctx, actor, r, func() error { state = messageEventReady; return nil })
			}
			if err == nil {
				t.Fatal("crash not reached")
			}
			s.boundary = nil
			reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
			if err != nil {
				t.Fatal(err)
			}
			got, err := reopened.messageEventCandidates(ctx, actor, check)
			if err != nil {
				t.Fatal(err)
			}
			if point == "event_state_committed" {
				if len(got) != 1 || got[0] != r {
					t.Fatal("canonical commit lost", got)
				}
			} else if len(got) != 0 {
				t.Fatal("pending became visible", got)
			}
			called := false
			if err = reopened.publishMessageEvent(ctx, actor, r, func() error { called = true; return nil }); err == nil || called {
				t.Fatal("reconciled publisher not fenced", err, called)
			}
		})
	}
}
func TestMessagesEventsNewReservationFencesAbandonedPublisher(t *testing.T) {
	s, _, a, _ := messagesStoreFixture(t)
	ctx := context.Background()
	check := func(context.Context, messageEventRef) (messageEventDisposition, error) {
		return messageEventPending, nil
	}
	old, next := eventRef(s, 0), eventRef(s, 1)
	if err := s.reserveMessageEvent(ctx, a, old, check); err != nil {
		t.Fatal(err)
	}
	if err := s.reserveMessageEvent(ctx, a, next, check); err != nil {
		t.Fatal(err)
	}
	called := false
	if err := s.publishMessageEvent(ctx, a, old, func() error { called = true; return nil }); err == nil || called {
		t.Fatal(err, called)
	}
	if err := s.publishMessageEvent(ctx, a, next, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
}
func TestMessagesEventsExpiryAliasAcceptanceAndContextScope(t *testing.T) {
	s, now, a, _ := messagesStoreFixture(t)
	ctx := context.Background()
	r := eventRef(s, 0)
	ready := func(context.Context, messageEventRef) (messageEventDisposition, error) { return messageEventReady, nil }
	if err := s.reserveMessageEvent(ctx, a, r, ready); err != nil {
		t.Fatal(err)
	}
	if err := s.publishMessageEvent(ctx, a, r, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	// Application aliases do not change source Account+Program+Context ownership.
	alias := a
	alias.Application = "other-label"
	got, err := s.messageEventCandidates(ctx, alias, ready)
	if err != nil || len(got) != 1 {
		t.Fatal(got, err)
	}
	for _, field := range []string{"account", "program", "context"} {
		other := a
		switch field {
		case "account":
			other.Account += "x"
		case "program":
			other.Program += "x"
		case "context":
			other.Context += "x"
		}
		got, err = s.messageEventCandidates(ctx, other, ready)
		if err != nil || len(got) != 0 {
			t.Fatal(field, got, err)
		}
	}
	*now = now.Add(2 * time.Hour)
	called := false
	got, err = s.messageEventCandidates(ctx, a, func(context.Context, messageEventRef) (messageEventDisposition, error) {
		called = true
		return messageEventReady, nil
	})
	if err != nil || len(got) != 0 || called {
		t.Fatal("original expiry extended", got, err, called)
	}
}
func TestMessagesEventsCanonicalFailureAndDeletedPageRefuse(t *testing.T) {
	s, _, a, _ := messagesStoreFixture(t)
	ctx := context.Background()
	r := eventRef(s, 0)
	ready := func(context.Context, messageEventRef) (messageEventDisposition, error) { return messageEventReady, nil }
	if err := s.reserveMessageEvent(ctx, a, r, ready); err != nil {
		t.Fatal(err)
	}
	if err := s.publishMessageEvent(ctx, a, r, func() error { return nil }); err != nil {
		t.Fatal(err)
	}
	if _, err := s.messageEventCandidates(ctx, a, func(context.Context, messageEventRef) (messageEventDisposition, error) {
		return messageEventPending, errors.New("canonical unavailable")
	}); err == nil {
		t.Fatal("unavailable became empty")
	}
	raw, err := cas.ReadLimitContext(ctx, filepath.Join(s.eventPath(a), "head.json"), 4096)
	if err != nil {
		t.Fatal(err)
	}
	h, err := readMessageEventHead(raw)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(s.eventPath(a), h.First+".json")); err != nil {
		t.Fatal(err)
	}
	if _, err = s.messageEventCandidates(ctx, a, ready); err == nil {
		t.Fatal("lost page became empty")
	}
}
func TestMessagesEventsBoundedPagesAndCleanupProgress(t *testing.T) {
	s, _, a, _ := messagesStoreFixture(t)
	ctx := context.Background()
	dir := s.eventPath(a)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	tokens := []string{fmt.Sprintf("%048x", 1), fmt.Sprintf("%048x", 2), fmt.Sprintf("%048x", 3)}
	for i, token := range tokens {
		p := messageEventPage{Profile: "messages-events-page@1", Entries: []messageEventEntry{}}
		if i+1 < len(tokens) {
			p.Next = tokens[i+1]
		}
		for j := 0; j < 128; j++ {
			p.Entries = append(p.Entries, messageEventEntry{Ref: eventRef(s, i*128+j)})
		}
		raw, _ := json.Marshal(p)
		if err := os.WriteFile(filepath.Join(dir, token+".json"), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	raw, _ := json.Marshal(messageEventHead{Profile: "messages-events-head@1", First: tokens[0], Last: tokens[2]})
	if err := os.WriteFile(filepath.Join(dir, "head.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	checks := 0
	observed := func(context.Context, messageEventRef) (messageEventDisposition, error) {
		checks++
		return messageEventObserved, nil
	}
	if got, err := s.messageEventCandidates(ctx, a, observed); !errors.Is(err, errMessageEventsProgress) || len(got) != 0 || checks > 256 {
		t.Fatal(got, err, checks)
	}
	checks = 0
	got, err := s.messageEventCandidates(ctx, a, func(context.Context, messageEventRef) (messageEventDisposition, error) {
		checks++
		return messageEventReady, nil
	})
	if err != nil || len(got) != 50 || checks > 256 {
		t.Fatal(got, err, checks)
	}
}
func TestMessagesEventsCancellationAndInvalidStoredData(t *testing.T) {
	s, _, a, _ := messagesStoreFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := eventRef(s, 0)
	if err := s.reserveMessageEvent(ctx, a, r, nil); err == nil {
		t.Fatal("canceled reserve")
	}
	if _, err := readMessageEventHead([]byte(`{"Profile":"messages-events-head@1","First":"../outside","Last":"x"}`)); err == nil {
		t.Fatal("invalid page accepted")
	}
	if _, err := readMessageEventPage([]byte(`{"Profile":"messages-events-page@1","Entries":[],"extra":true}`)); err == nil {
		t.Fatal("unknown stored field accepted")
	}
}

func TestMessagesEventsPrunedPendingHeadCrashIsRecoverable(t *testing.T) {
	s, _, actor, _ := messagesStoreFixture(t)
	ctx := context.Background()
	r := eventRef(s, 0)
	check := func(context.Context, messageEventRef) (messageEventDisposition, error) {
		return messageEventPending, nil
	}
	if err := s.reserveMessageEvent(ctx, actor, r, check); err != nil {
		t.Fatal(err)
	}
	s.boundary = func(point string) error {
		if point == "event_candidates_page_written" {
			return errors.New("crash after page before head")
		}
		return nil
	}
	if _, err := s.messageEventCandidates(ctx, actor, check); err == nil {
		t.Fatal("crash boundary missing")
	}
	s.boundary = nil
	reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.messageEventCandidates(ctx, actor, check)
	if err != nil || len(got) != 0 {
		t.Fatal("pruned pending head stuck", got, err)
	}
	called := false
	if err = reopened.publishMessageEvent(ctx, actor, r, func() error { called = true; return nil }); err == nil || called {
		t.Fatal("old operation escaped fence", err, called)
	}
}

func TestMessagesEventsOldAcceptanceCannotHideReusedKey(t *testing.T) {
	s, _, actor, _ := messagesStoreFixture(t)
	ctx := context.Background()
	old, current := eventRef(s, 0), eventRef(s, 1)
	current.Key = old.Key
	current.Token = "different-acceptance"
	states := map[messageEventRef]messageEventDisposition{}
	check := func(_ context.Context, ref messageEventRef) (messageEventDisposition, error) { return states[ref], nil }
	for _, ref := range []messageEventRef{old, current} {
		if err := s.reserveMessageEvent(ctx, actor, ref, check); err != nil {
			t.Fatal(err)
		}
		if err := s.publishMessageEvent(ctx, actor, ref, func() error { states[ref] = messageEventReady; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	states[old] = messageEventExpired
	got, err := s.messageEventCandidates(ctx, actor, check)
	if err != nil || len(got) != 1 || got[0] != current {
		t.Fatal("acceptance token lost", got, err)
	}
}
