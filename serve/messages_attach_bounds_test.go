package main

import (
	"context"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// VISION 2026-10-31: C-68's bounds carry over to waking existing sessions.
// A note never starts a turn; one start per receiver per hour, also across
// two different sessions of that receiver; and each start attempt is charged
// to the pair budget.
func TestMessagesAttachStartKeepsC68Bounds(t *testing.T) {
	h, key, r, calls, _ := chatStartFixture(t)
	other, otherErr := h.messages.contextForChat(context.Background(), h.directory.owner, r.Receiver, "019a6c1e-2b3d-7e4f-8a5b-000000000002", &msg.ChatBinding{ChatID: "019a6c1e-2b3d-7e4f-8a5b-000000000002"})
	if otherErr != nil {
		t.Fatal(otherErr)
	}
	allowMessageStartFixture(t, h, key, r)
	send := func(requestKey, contextName string, kind msg.Kind) (string, *messageRecord) {
		t.Helper()
		request := msg.SendRequest{To: "app:receiver", Context: contextName, Kind: kind, Subject: "s", Body: "b", IdempotencyKey: requestKey, ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()}
		if kind == msg.KindQuestion {
			request.AnswerByUnixMs = time.Now().Add(time.Minute).UnixMilli()
		}
		if got := h.messages.accept(context.Background(), r.Actor, r.Receiver, request, false, ""); got.Outcome != msg.OutcomeSent {
			t.Fatal(requestKey, got)
		}
		k := h.messages.key(r.Actor, requestKey)
		record, err := h.messages.readRecord(context.Background(), k)
		if err != nil || record == nil {
			t.Fatal(requestKey, err)
		}
		return k, record
	}
	noteKey, note := send("note", chatFixtureContext(t, r), msg.KindNote)
	if err := h.progressMessageStartSettled(context.Background(), noteKey, note); err != nil || calls.Load() != 0 {
		t.Fatal("a note woke a session", err, calls.Load())
	}
	if err := h.progressMessageStartSettled(context.Background(), key, r); err != nil || calls.Load() != 1 {
		t.Fatal("first question", err, calls.Load())
	}
	secondKey, second := send("second", other, msg.KindQuestion)
	if err := h.progressMessageStartSettled(context.Background(), secondKey, second); err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatal("a second session of the same receiver started within the hour", calls.Load())
	}
	stored, err := h.messages.readRecord(context.Background(), secondKey)
	if err != nil || stored.Start == nil || stored.Start.State != "refused" {
		t.Fatal("hourly refusal not recorded", stored, err)
	}
	raw, err := cas.ReadLimitContext(context.Background(), h.messages.pairPath(r.Actor, r.Receiver), messageIndexLimit)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := readMessagePair(raw)
	if err != nil || len(pair.Starts) != 2 {
		t.Fatal("each start attempt is charged to the pair budget", pair.Starts, err)
	}
}
