package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// A message sent while OA's woken turn runs in that application and context
// carries the turn's origin chain; Activity shows it. Another context of the
// same application, or a message after the turn ended, carries none.
func TestMessageWakeOriginTagsMessagesOfTheWokenTurn(t *testing.T) {
	var wakes messageWakes
	end := wakes.begin("receiver", "chat-wake-fixture", "attempt-1", []string{"q-person"})
	if got := wakes.origin("receiver", "chat-wake-fixture", time.Now()); !slices.Equal(got, []string{"q-person"}) {
		t.Fatal(got)
	}
	if got := wakes.origin("receiver", "native-other", time.Now()); len(got) != 0 {
		t.Fatal("another context inherited the chain", got)
	}
	// A later start for the same context replaces it; the earlier end leaves it.
	later := wakes.begin("receiver", "chat-wake-fixture", "attempt-2", []string{"q-person", "q-second"})
	end()
	if got := wakes.origin("receiver", "chat-wake-fixture", time.Now()); len(got) != 2 {
		t.Fatal("earlier end removed the later turn", got)
	}
	later()
	if got := wakes.origin("receiver", "chat-wake-fixture", time.Now()); len(got) != 0 {
		t.Fatal("ended turn still tags messages", got)
	}

	s, err := openMessageStore(t.TempDir(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	sink := &messageAuditSink{}
	audit, err := newMessageAudit(sink)
	if err != nil {
		t.Fatal(err)
	}
	defer audit.close()
	s.audit = audit
	actor := messageActor{Account: "account", Program: `C:\receiver.exe`, Application: "receiver", Context: "chat-wake-fixture"}
	request := msg.SendRequest{To: "app:third", Context: "ctx", Kind: msg.KindQuestion, Subject: "s", Body: "b", IdempotencyKey: "woken-question", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), AnswerByUnixMs: time.Now().Add(time.Minute).UnixMilli()}
	if got := s.accept(withMessageOrigin(context.Background(), []string{"q-person"}), actor, `C:\third.exe`, request, false, ""); got.Outcome != msg.OutcomeSent {
		t.Fatal(got)
	}
	r, err := s.readRecord(context.Background(), s.key(actor, request.IdempotencyKey))
	if err != nil || r == nil || !slices.Equal(r.Origin, []string{"q-person"}) || r.Audit == nil || r.Audit.Origin != "q-person" {
		t.Fatal("woken turn's message lost its origin", r, err)
	}
	if err := audit.append(r.Audit); err != nil {
		t.Fatal(err)
	}
	if got := sink.records[len(sink.records)-1].Attrs["origin_chain"]; got != "q-person" {
		t.Fatal("Activity row without the origin chain", got)
	}
	for _, bad := range [][]string{{"a", "b", "c"}, {""}, {"a,b"}, {strings.Repeat("x", 129)}} {
		if validMessageOrigin(bad) {
			t.Fatal("stored chain accepted", bad)
		}
	}
}

// W4: an open chat's turn row ends 30 minutes after its last key; a shorter
// later chain keeps the longer one and restarts the lease; an empty key leaves
// a serve-run start's row in place.
func TestTurnWakeRowBounds(t *testing.T) {
	var wakes messageWakes
	t0 := time.Unix(1_800_000_000, 0)
	wakes.turn("chat-app", "chat-b", []string{"q1", "r1"}, t0)
	wakes.turn("chat-app", "chat-b", []string{"q9"}, t0.Add(10*time.Minute))
	if got := wakes.origin("chat-app", "chat-b", t0.Add(39*time.Minute)); !slices.Equal(got, []string{"q1", "r1"}) {
		t.Fatal("a shorter chain replaced the longer one, or the lease did not restart", got)
	}
	if got := wakes.origin("chat-app", "chat-b", t0.Add(40*time.Minute)); len(got) != 0 {
		t.Fatal("turn row outlived its lease", got)
	}
	// After the lease, a new key starts a fresh row with its own chain.
	wakes.turn("chat-app", "chat-b", []string{"q9"}, t0.Add(41*time.Minute))
	if got := wakes.origin("chat-app", "chat-b", t0.Add(42*time.Minute)); !slices.Equal(got, []string{"q9"}) {
		t.Fatal(got)
	}
	wakes.endTurn("chat-app", "chat-b")
	if got := wakes.origin("chat-app", "chat-b", t0.Add(42*time.Minute)); len(got) != 0 {
		t.Fatal("empty key left the turn row", got)
	}

	end := wakes.begin("receiver", "chat-c", "attempt-1", []string{"q-person"})
	wakes.endTurn("receiver", "chat-c")
	if got := wakes.origin("receiver", "chat-c", t0.Add(time.Hour)); !slices.Equal(got, []string{"q-person"}) {
		t.Fatal("an empty key ended a serve-run start's row", got)
	}
	wakes.turn("receiver", "chat-c", []string{"q-person", "q-second"}, t0)
	if got := wakes.origin("receiver", "chat-c", t0.Add(24*time.Hour)); len(got) != 2 {
		t.Fatal("a start's row lost the longer chain or took a lease", got)
	}
	end()
	if got := wakes.origin("receiver", "chat-c", t0); len(got) != 0 {
		t.Fatal("start's end left its row", got)
	}
}

// A question from a turn already two woken turns down the chain starts no
// receiver and asks the person nothing; Activity records the refusal. One
// step less still starts, and the started turn carries the longer chain.
func TestMessageStartHopLimit(t *testing.T) {
	for _, depth := range []int{1, messageWakeHopLimit} {
		h, key, r, calls, _ := messagesStartProgressFixture(t)
		sink := &messageAuditSink{}
		if err := h.configureMessageAudit(sink); err != nil {
			t.Fatal(err)
		}
		origin := []string{"q-person", "q-second"}[:depth]
		var launched []string
		h.directory.launchMessageStart = func(_ context.Context, request applicationMessageStartRequest, _ func(context.Context) error) error {
			calls.Add(1)
			launched = request.Origin
			return nil
		}
		if err := h.messages.updateMessageNotice(context.Background(), key, r.Token, func(current *messageRecord) {
			current.Origin = origin
			entry, err := h.messageAudit.prepare(current)
			if err != nil {
				t.Fatal(err)
			}
			current.Audit = entry
		}); err != nil {
			t.Fatal(err)
		}
		current, err := h.messages.readRecord(context.Background(), key)
		if err != nil {
			t.Fatal(err)
		}
		if depth < messageWakeHopLimit {
			allowMessageStartFixture(t, h, key, current)
			if err := h.progressMessageStartSettled(context.Background(), key, current); err != nil {
				t.Fatal(err)
			}
			if calls.Load() != 1 || !slices.Equal(launched, append(slices.Clone(origin), r.ID)) {
				t.Fatal("start below the hop limit", calls.Load(), launched)
			}
			h.messageAudit.close()
			continue
		}
		asked := false
		h.messageRights.firstUse = func(rights.Subject, func() asks.Publisher, string, string) string {
			asked = true
			return "pending"
		}
		if err := h.progressMessageStartSettled(context.Background(), key, current); err != nil {
			t.Fatal(err)
		}
		if calls.Load() != 0 || asked {
			t.Fatal("hop limit started a receiver or asked the person")
		}
		stored, err := h.messages.readRecord(context.Background(), key)
		if err != nil || stored.Start == nil || stored.Start.State != messageStartHopLimit {
			t.Fatal(stored, err)
		}
		var row map[string]string
		for _, record := range sink.records {
			if record.Attrs["message_id"] == r.ID+":start" {
				row = record.Attrs
			}
		}
		if row == nil || row["outcome"] != "receiver_start_hop_limit" || row["origin_chain"] != "q-person,q-second" {
			t.Fatal("hop limit Activity row", row)
		}
		// A later pass never starts it either.
		if err := h.progressMessageStartSettled(context.Background(), key, stored); err != nil || calls.Load() != 0 {
			t.Fatal("hop-limited question started later", err)
		}
		h.messageAudit.close()
	}
}

// The person's stop marks the woken start stopped: its authority refuses at
// the next check, nothing starts it again, and Activity records the stop. A
// question with no running start reports conflict.
func TestMessageStartStopForPerson(t *testing.T) {
	h, key, r, calls, _ := messagesStartProgressFixture(t)
	sink := &messageAuditSink{}
	if err := h.configureMessageAudit(sink); err != nil {
		t.Fatal(err)
	}
	defer h.messageAudit.close()
	if got := h.stopMessageStartForPerson(context.Background(), r.ID); got.Outcome != msg.OutcomeConflict {
		t.Fatal("stop before any start", got)
	}
	if err := h.messages.updateMessageNotice(context.Background(), key, r.Token, func(current *messageRecord) {
		entry, err := h.messageAudit.prepare(current)
		if err != nil {
			t.Fatal(err)
		}
		current.Audit = entry
	}); err != nil {
		t.Fatal(err)
	}
	current, err := h.messages.readRecord(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var authority, continuing func(context.Context) error
	h.directory.launchMessageStart = func(_ context.Context, request applicationMessageStartRequest, check func(context.Context) error) error {
		calls.Add(1)
		authority, continuing = check, request.Continue
		return nil
	}
	resource := allowMessageStartFixture(t, h, key, current)
	if err := h.progressMessageStartSettled(context.Background(), key, current); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 || authority == nil || authority(context.Background()) != nil || continuing == nil || continuing(context.Background()) != nil {
		t.Fatal("fixture start did not run under live authority", calls.Load())
	}
	// Revoking the direction from Activity also ends a running turn.
	subject := rights.Subject{Account: r.Actor.Account, Program: r.Actor.Program}
	if err := h.messageRights.policy.Revoke(subject, grants.ApplicationMessageStartAction, resource); err != nil {
		t.Fatal(err)
	}
	if continuing(context.Background()) == nil {
		t.Fatal("revoked direction kept the running turn")
	}
	if err := h.messageRights.policy.Set(subject, grants.ApplicationMessageStartAction, resource, true); err != nil {
		t.Fatal(err)
	}
	if got := h.stopMessageStartForPerson(context.Background(), r.ID); got.Outcome != msg.OutcomePage || got.Reason != "stopped" {
		t.Fatal(got)
	}
	if authority(context.Background()) == nil || continuing(context.Background()) == nil {
		t.Fatal("stopped turn kept its authority")
	}
	stored, err := h.messages.readRecord(context.Background(), key)
	if err != nil || stored.Start == nil || stored.Start.State != messageStartStopped {
		t.Fatal(stored, err)
	}
	if err := h.progressMessageStartSettled(context.Background(), key, stored); err != nil || calls.Load() != 1 {
		t.Fatal("stopped question started again", err, calls.Load())
	}
	if got := h.stopMessageStartForPerson(context.Background(), r.ID); got.Outcome != msg.OutcomeConflict {
		t.Fatal("second stop", got)
	}
	stoppedRow := false
	for _, record := range sink.records {
		stoppedRow = stoppedRow || record.Attrs["message_id"] == r.ID+":start" && record.Attrs["outcome"] == "receiver_start_stopped"
	}
	if !stoppedRow {
		t.Fatal("no Activity row for the stop")
	}
}

// A running woken turn is cancelled at the first refused authority check.
func TestMessageStartAuthoritySupervision(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var refuse atomic.Bool
	checks := atomic.Int32{}
	end := superviseMessageStartAuthority(ctx, func(context.Context) error {
		checks.Add(1)
		if refuse.Load() {
			return errors.New("stopped")
		}
		return nil
	}, 5*time.Millisecond, cancel)
	defer end()
	time.Sleep(30 * time.Millisecond)
	if ctx.Err() != nil || checks.Load() == 0 {
		t.Fatal("cancelled while authority held", checks.Load())
	}
	refuse.Store(true)
	select {
	case <-ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("refused authority did not cancel the turn")
	}
}
