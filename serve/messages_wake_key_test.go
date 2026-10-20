package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesWakeOrdersFirstVisibleEligibilityAcrossDelayedReservationAndRestart(t *testing.T) {
	s, now, a, request := messagesStoreFixture(t)
	request.Kind, request.AnswerByUnixMs = msg.KindQuestion, now.Add(time.Minute).UnixMilli()
	ctx := context.Background()
	otherStore, err := openMessageStore(filepath.Dir(s.dir), func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	var reservations atomic.Int32
	s.boundary = func(point string) error {
		if point == "id_reserved" && reservations.Add(1) == 1 {
			close(entered)
			<-release
		}
		return nil
	}
	firstDone := make(chan msg.SendResult, 1)
	go func() { firstDone <- s.accept(ctx, a, "receiver", request, false, "") }()
	<-entered
	b := a
	b.Program, b.Application = "other-native", "other"
	second := request
	second.IdempotencyKey, second.Body = "second", "second body"
	newerID := otherStore.accept(ctx, b, "receiver", second, false, "")
	if newerID.Outcome != msg.OutcomeSent {
		close(release)
		t.Fatal(newerID)
	}
	observer := messageActor{Account: a.Account, Program: "receiver", Context: request.Context}
	r2, k2, err := s.readByID(ctx, newerID.MessageID)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	beforeState, err := cas.ReadLimitContext(ctx, s.statePath(k2, r2.Token), 8192)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	w1, err := s.wakeForRecord(ctx, observer, k2, r2)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	olderID := <-firstDone
	afterState, err := cas.ReadLimitContext(ctx, s.statePath(k2, r2.Token), 8192)
	if err != nil || !bytes.Equal(beforeState, afterState) {
		t.Fatal("wake migration changed closed prior state", err)
	}
	wakeRaw, err := cas.ReadLimitContext(ctx, s.wakeOrderPath(k2, r2.Token), 1024)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readMessageWakeOrder(wakeRaw, "foreign-binding"); err == nil {
		t.Fatal("wake sidecar crossed actor/context binding")
	}
	if olderID.Outcome != msg.OutcomeSent {
		t.Fatal(olderID)
	}
	r1, k1, err := s.readByID(ctx, olderID.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	if r1.AcceptedAt != r2.AcceptedAt {
		t.Fatal("fixture must use the same millisecond")
	}
	w2, err := s.wakeForRecord(ctx, observer, k1, r1)
	if err != nil || w2 <= w1 {
		t.Fatal("late lower ID reservation did not receive a newer visible order", olderID, newerID, w1, w2, err)
	}
	restarted, err := openMessageStore(filepath.Dir(s.dir), func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	stable, err := restarted.wakeForRecord(ctx, observer, k1, r1)
	if err != nil || stable != w2 {
		t.Fatal("restart changed wake identity", stable, w2, err)
	}
	older, err := restarted.wakeForRecord(ctx, observer, k2, r2)
	if err != nil || older != w1 || older >= stable {
		t.Fatal("revealed older item exceeded submitted key", older, stable, err)
	}
}

func TestMessagesWakeReservationRecoveryPublishesBeforeNextOrder(t *testing.T) {
	s, now, a, request := messagesStoreFixture(t)
	request.Kind, request.AnswerByUnixMs = msg.KindQuestion, now.Add(time.Minute).UnixMilli()
	ctx := context.Background()
	one := s.accept(ctx, a, "receiver", request, false, "")
	request.IdempotencyKey, request.Body = "two", "different"
	two := s.accept(ctx, a, "receiver", request, false, "")
	r1, k1, err := s.readByID(ctx, one.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	r2, k2, err := s.readByID(ctx, two.MessageID)
	if err != nil {
		t.Fatal(err)
	}
	observer := messageActor{Account: a.Account, Program: "receiver", Context: request.Context}
	s.boundary = func(point string) error {
		if point == "wake_order_reserved" {
			return errors.New("lost reservation reply")
		}
		return nil
	}
	if _, err := s.wakeForRecord(ctx, observer, k1, r1); err == nil {
		t.Fatal("inert crash not exercised")
	}
	s.boundary = nil
	newer, err := s.wakeForRecord(ctx, observer, k2, r2)
	if err != nil {
		t.Fatal(err)
	}
	prior, err := s.wakeForRecord(ctx, observer, k1, r1)
	if err != nil || prior >= newer {
		t.Fatal("pending reservation was reused or published after next order", prior, newer, err)
	}
}

func TestMessagesWakeExpiryKeepsSequenceAboveSubmittedHighWater(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	request.Kind, request.AnswerByUnixMs = msg.KindQuestion, now.Add(time.Minute).UnixMilli()
	ctx := context.Background()
	first := s.accept(ctx, actor, "receiver", request, false, "")
	r, key, err := s.readByID(ctx, first.MessageID)
	if err != nil || r == nil {
		t.Fatal(first, r, err)
	}
	observer := messageActor{Account: actor.Account, Program: "receiver", Context: request.Context}
	prior, err := s.wakeForRecord(ctx, observer, key, r)
	if err != nil {
		t.Fatal(err)
	}
	*now = now.Add(8 * 24 * time.Hour)
	if err := s.sweepExpiry(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(s.wakeOrderPath(key, r.Token)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("expiry retained order sidecar", err)
	}
	restarted, err := openMessageStore(filepath.Dir(s.dir), func() time.Time { return *now })
	if err != nil {
		t.Fatal(err)
	}
	request.IdempotencyKey, request.ExpiresUnixMs, request.AnswerByUnixMs = "after-all-expired", now.Add(time.Hour).UnixMilli(), now.Add(time.Minute).UnixMilli()
	second := restarted.accept(ctx, actor, "receiver", request, false, "")
	r2, key2, err := restarted.readByID(ctx, second.MessageID)
	if err != nil || r2 == nil {
		t.Fatal(second, r2, err)
	}
	next, err := restarted.wakeForRecord(ctx, observer, key2, r2)
	if err != nil || next <= prior {
		t.Fatal("expired all items reset sequence below submitted high-water", prior, next, err)
	}
	if _, err := os.Stat(s.wakeOrderPath(key, r.Token)); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("pending reservation resurrected expired sidecar", err)
	}
}

// Frozen closed state reader from the pre-wake state shape. Its exact known
// filename ignores sidecars; the independent struct rejects new old-file fields.
type messageLegacyClosedState struct {
	Profile, Token                               string
	Version                                      uint64
	Held, Refused                                bool
	TaskState                                    msg.TaskState
	Observed, ReceiverObserved                   uint16
	Events                                       []json.RawMessage
	ReplyKey, ReplyToken, CancelKey, CancelToken string
	ReplyAuthorized                              bool
	AcceptedTerminal                             string
	RemoteReply, SimpleReply                     *json.RawMessage
}

func TestMessagesWakeSidecarIsIgnoredByLegacyClosedStateReader(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	request.Kind, request.AnswerByUnixMs = msg.KindQuestion, now.Add(time.Minute).UnixMilli()
	ctx := context.Background()
	sent := s.accept(ctx, actor, "receiver", request, false, "")
	r, key, err := s.readByID(ctx, sent.MessageID)
	if err != nil || r == nil {
		t.Fatal(sent, r, err)
	}
	state, err := s.currentState(ctx, key, r)
	if err != nil {
		t.Fatal(err)
	}
	// Seed the historical closed format this reader actually understood.
	// Modern acceptance/delivery evidence belongs to the later R2 format.
	state.Acceptance, state.Delivery = nil, nil
	before, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	path := s.statePath(key, r.Token)
	if err := cas.ChangeLimitContext(ctx, path, 8192, func([]byte) ([]byte, error) { return before, nil }); err != nil {
		t.Fatal(err)
	}
	oldRead := func(raw []byte) error {
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		return decoder.Decode(new(messageLegacyClosedState))
	}
	if err := oldRead(before); err != nil {
		t.Fatal(err)
	}
	observer := messageActor{Account: actor.Account, Program: "receiver", Context: request.Context}
	if _, err := s.wakeForRecord(ctx, observer, key, r); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("closed old state changed", err)
	}
	if err := oldRead(after); err != nil {
		t.Fatal("old closed reader did not ignore sidecar", err)
	}
	if raw, err := os.ReadFile(s.wakeOrderPath(key, r.Token)); err != nil || len(raw) == 0 {
		t.Fatal("fixture created no sidecar", err)
	}
	mutant := append(append([]byte{}, before[:len(before)-1]...), []byte(`,"WakeOrder":1}`)...)
	if err := oldRead(mutant); err == nil {
		t.Fatal("legacy reader fixture accepted an added closed field")
	}
}

// Review 2026-11-09: every headers read took the store owner's lock for each
// eligible record. A record with a published wake order answers from its own
// sidecar: a second read neither reads nor changes the owner file, and a
// repeated expiry pass with nothing to do changes no expiry file.
func TestMessagesWakeOrderReadTouchesNoSharedFile(t *testing.T) {
	s, now, a, request := messagesStoreFixture(t)
	request.Kind, request.AnswerByUnixMs = msg.KindQuestion, now.Add(time.Minute).UnixMilli()
	ctx := context.Background()
	sent := s.accept(ctx, a, "receiver", request, false, "")
	r, k, err := s.readByID(ctx, sent.MessageID)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	observer := messageActor{Account: a.Account, Program: "receiver", Context: request.Context}
	first, err := s.wakeForRecord(ctx, observer, k, r)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.sweepExpiry(ctx); err != nil {
		t.Fatal(err)
	}
	stamps := func() map[string]time.Time {
		t.Helper()
		out := map[string]time.Time{}
		paths, err := filepath.Glob(filepath.Join(s.dir, "expiry", "*.json"))
		if err != nil || len(paths) == 0 {
			t.Fatal("expiry files", paths, err)
		}
		for _, path := range paths {
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			out[path] = info.ModTime()
		}
		return out
	}
	before := stamps()
	if err := s.sweepExpiry(ctx); err != nil {
		t.Fatal(err)
	}
	for path, at := range stamps() {
		if !before[path].Equal(at) {
			t.Fatal("an expiry pass with nothing to do rewrote", path)
		}
	}
	owner, err := os.ReadFile(s.ownerPath())
	if err != nil {
		t.Fatal(err)
	}
	// An unreadable owner file fails any read that still takes the owner.
	if err := os.WriteFile(s.ownerPath(), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	again, err := s.wakeForRecord(ctx, observer, k, r)
	if restore := os.WriteFile(s.ownerPath(), owner, 0o600); restore != nil {
		t.Fatal(restore)
	}
	if err != nil || again != first {
		t.Fatal("a published wake order was read under the store owner", again, first, err)
	}
}
