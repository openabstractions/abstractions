//go:build windows

package main

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

type turnWakeRun struct {
	f                *messagesObserveRun
	instance, source string
	chat             string
	peer             messageActor
}

// turnWakeFixture opens chat B (native-agent) and the peer's inbox "sender"
// (other-agent), the two ends of an open-chat wake.
func turnWakeFixture(t *testing.T) *turnWakeRun {
	t.Helper()
	f := messagesObserveFixture(t)
	source := fmt.Sprintf("source-%d", time.Now().UnixNano())
	instance, chat := messagesTypedChatAt(t, f, f.reader, "native-agent", "turn-chat", "Turn chat", source)
	messagesStateOpen(t, f, f.other, "other-agent", "sender")
	peer := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "sender"}
	return &turnWakeRun{f: f, instance: instance, source: source, chat: chat, peer: peer}
}

// question accepts a peer question to chat B with this origin chain and
// returns it with the wake key B's headers project for it.
func (w *turnWakeRun) question(t *testing.T, key string, origin []string) (*messageRecord, string) {
	t.Helper()
	f := w.f
	request := msg.SendRequest{To: "app:native-agent", Context: w.chat, Kind: msg.KindQuestion, Subject: key, Body: "question " + key, IdempotencyKey: key, ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}
	sent := f.store.accept(withMessageOrigin(f.run.ctx, origin), w.peer, f.selfProgram, request, false, "")
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	record, _, err := f.store.readByID(f.run.ctx, sent.MessageID)
	if err != nil || record == nil {
		t.Fatal(record, err)
	}
	page := f.reader.headersWait(w.chat, "", 0)
	if page.Outcome != msg.OutcomePage {
		t.Fatal(page)
	}
	return record, page.Wake
}

func (w *turnWakeRun) storedOrigin(t *testing.T, sent msg.SendResult) []string {
	t.Helper()
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	record, _, err := w.f.store.readByID(w.f.run.ctx, sent.MessageID)
	if err != nil || record == nil {
		t.Fatal(record, err)
	}
	return record.Origin
}

func (w *turnWakeRun) request(key string, kind msg.Kind) msg.SendRequest {
	request := msg.SendRequest{To: "app:other-agent", Context: "sender", Kind: kind, Subject: key, Body: "from the turn " + key, IdempotencyKey: key, ExpiresUnixMs: w.f.now.Add(time.Hour).UnixMilli()}
	if kind == msg.KindQuestion {
		request.AnswerByUnixMs = w.f.now.Add(time.Minute).UnixMilli()
	}
	return request
}

// W1: an open chat woken by the key of question Q (chain c) sends through
// Send, SendChat and Reply; each stored Origin is c + Q. After an empty
// TurnWake the next send stores none.
func TestTurnWakeStampsEverySendPath(t *testing.T) {
	w := turnWakeFixture(t)
	f := w.f
	q, wake := w.question(t, "w1-question", []string{"q-person"})
	if wake == "" {
		t.Fatal("question projected no wake")
	}
	// As after a runtime restart: the key resolves through the durable order.
	f.store.wakeMu.Lock()
	clear(f.store.wakeIndex)
	f.store.wakeMu.Unlock()
	if got, err := f.reader.TurnWake(w.instance, w.chat, wake); err != nil || got.Outcome != msg.OutcomePage {
		t.Fatal(got, err)
	}
	want := []string{"q-person", q.ID}
	sent, err := f.reader.Send(w.instance, w.chat, w.request("w1-send", msg.KindNote))
	if err != nil || !slices.Equal(w.storedOrigin(t, sent), want) {
		t.Fatal("Send lost the turn's chain", sent, err)
	}
	sent, err = f.reader.SendChat(w.instance, w.source, msg.ChatSendRequest{Request: w.request("w1-sendchat", msg.KindNote), ReturnContext: w.chat})
	if err != nil || !slices.Equal(w.storedOrigin(t, sent), want) {
		t.Fatal("SendChat lost the turn's chain", sent, err)
	}
	sent, err = f.reader.Reply(w.instance, w.chat, q.ID, "answer")
	if err != nil || !slices.Equal(w.storedOrigin(t, sent), want) {
		t.Fatal("Reply lost the turn's chain", sent, err)
	}
	// A ticketless read changes nothing: the turn keeps its chain.
	if page := f.reader.headersWait(w.chat, "", 0); page.Outcome != msg.OutcomePage {
		t.Fatal(page)
	}
	if got, err := f.reader.TurnWake(w.instance, w.chat, ""); err != nil || got.Outcome != msg.OutcomePage {
		t.Fatal(got, err)
	}
	sent, err = f.reader.Send(w.instance, w.chat, w.request("w1-after", msg.KindNote))
	if err != nil || len(w.storedOrigin(t, sent)) != 0 {
		t.Fatal("a send after the turn ended carried a chain", sent, err)
	}
}

// W2: another context's, program's or account's key is forbidden, a non-owner
// caller is forbidden, a malformed or future key is invalid; none changes a
// row.
func TestTurnWakeRefusesForeignKey(t *testing.T) {
	w := turnWakeFixture(t)
	f := w.f
	_, wake := w.question(t, "w2-question", nil)
	if got, err := f.reader.TurnWake(w.instance, w.chat, wake); err != nil || got.Outcome != msg.OutcomePage {
		t.Fatal(got, err)
	}
	d := f.run.d
	before := d.messageWakes.origin("native-agent", w.chat, d.now())
	if len(before) != 1 {
		t.Fatal("turn row missing", before)
	}
	_, digits, _ := strings.Cut(wake, "-")
	actor, _, _, ok := f.reader.owner(w.instance, w.chat)
	if !ok {
		t.Fatal("chat owner missing")
	}
	foreign := func(edit func(*messageActor)) string {
		a := actor
		edit(&a)
		return messageWakeBinding(a) + "-" + digits
	}
	for name, key := range map[string]string{
		"context": foreign(func(a *messageActor) { a.Context = "sender" }),
		"program": foreign(func(a *messageActor) { a.Program = f.otherProgram }),
		"account": foreign(func(a *messageActor) { a.Account += "-other" }),
	} {
		if got, err := f.reader.TurnWake(w.instance, w.chat, key); err != nil || got.Outcome != msg.OutcomeForbidden {
			t.Fatal("foreign", name, "key", got, err)
		}
	}
	// The peer holds no ownership of chat B: neither its key nor an empty one.
	for _, key := range []string{wake, ""} {
		if got, err := f.other.TurnWake(w.instance, w.chat, key); err != nil || got.Outcome != msg.OutcomeForbidden {
			t.Fatal("non-owner", key, got, err)
		}
	}
	future := messageWakeBinding(actor) + "-" + fmt.Sprintf("%020d", uint64(1)<<40)
	for _, key := range []string{"malformed", strings.Repeat("a", 64) + "-1", messageWakeBinding(actor) + "-" + strings.Repeat("0", 20), future} {
		if got, err := f.reader.TurnWake(w.instance, w.chat, key); err != nil || got.Outcome != msg.OutcomeInvalid {
			t.Fatal("bad key", key, got, err)
		}
	}
	if after := d.messageWakes.origin("native-agent", w.chat, d.now()); !slices.Equal(before, after) {
		t.Fatal("a refused TurnWake changed the row", before, after)
	}
	f.reader.remote = &messageRemoteCall{}
	got, err := f.reader.TurnWake(w.instance, w.chat, "")
	f.reader.remote = nil
	if err != nil || got.Outcome != msg.OutcomeForbidden {
		t.Fatal("remote caller", got, err)
	}
}

// W3: an own-binding key with no retained record sets the chain at the hop
// limit: the turn's question projects no wake and starts nothing.
func TestTurnWakeUnresolvedKeyAtLimit(t *testing.T) {
	w := turnWakeFixture(t)
	f := w.f
	actor, _, _, ok := f.reader.owner(w.instance, w.chat)
	if !ok {
		t.Fatal("chat owner missing")
	}
	var order uint64
	if err := f.store.changeOwner(context.Background(), func(head *messageStoreOwner) error {
		*head.Next += 3
		order = *head.Next
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	wake := fmt.Sprintf("%s-%020d", messageWakeBinding(actor), order)
	if got, err := f.reader.TurnWake(w.instance, w.chat, wake); err != nil || got.Outcome != msg.OutcomePage {
		t.Fatal(got, err)
	}
	sent, err := f.reader.Send(w.instance, w.chat, w.request("w3-question", msg.KindQuestion))
	if err != nil {
		t.Fatal(err)
	}
	origin := w.storedOrigin(t, sent)
	record, _, err := f.store.readByID(f.run.ctx, sent.MessageID)
	if err != nil || len(origin) != messageWakeHopLimit || messageChainWakes(record) {
		t.Fatal("unresolved key below the hop limit", origin, err)
	}
	if page := f.other.headersWait("sender", "", 0); page.Outcome != msg.OutcomePage || page.Wake != "" || page.Unread == nil || *page.Unread != 1 {
		t.Fatal("a question at the hop limit woke the peer", page)
	}
}

// W7 (serve composition): two open idle chats, A (native-agent) and B
// (other-agent), wake each other only within the hop limit. Each step is the
// call the gateway makes for its adapter: a woken turn states its wake key
// with TurnWake at its ticketed read, and a person's turn clears it.
func TestTwoOpenChatsWakeChainStops(t *testing.T) {
	f := messagesObserveFixture(t)
	instanceA, chatA := messagesTypedChatAt(t, f, f.reader, "native-agent", "chat-a", "Chat A", fmt.Sprintf("source-a-%d", time.Now().UnixNano()))
	instanceB, chatB := messagesTypedChatAt(t, f, f.other, "other-agent", "chat-b", "Chat B", fmt.Sprintf("source-b-%d", time.Now().UnixNano()))
	question := func(key string) msg.SendRequest {
		return msg.SendRequest{To: "app:other-agent", Context: chatB, Kind: msg.KindQuestion, Subject: key, Body: key, IdempotencyKey: key, ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}
	}
	stored := func(sent msg.SendResult, err error) *messageRecord {
		t.Helper()
		if err != nil || sent.Outcome != msg.OutcomeSent {
			t.Fatal(sent, err)
		}
		r, _, err := f.store.readByID(f.run.ctx, sent.MessageID)
		if err != nil || r == nil {
			t.Fatal(r, err)
		}
		return r
	}
	turn := func(receiver *messagesReceiver, instance, chat, wake string) {
		t.Helper()
		if got, err := receiver.TurnWake(instance, chat, wake); err != nil || got.Outcome != msg.OutcomePage {
			t.Fatal(got, err)
		}
	}

	// A's person turn asks Q1: no chain, and B wakes.
	q1 := stored(f.reader.Send(instanceA, chatA, question("q1")))
	if len(q1.Origin) != 0 {
		t.Fatal("a person's question carried a chain", q1.Origin)
	}
	wakeB := f.other.headersWait(chatB, "", 0).Wake
	if wakeB == "" {
		t.Fatal("Q1 did not wake B")
	}
	// B's woken turn reads with its ticket and replies R1 with chain [Q1].
	turn(f.other, instanceB, chatB, wakeB)
	r1 := stored(f.other.Reply(instanceB, chatB, q1.ID, "r1"))
	if !slices.Equal(r1.Origin, []string{q1.ID}) {
		t.Fatal("R1 chain", r1.Origin)
	}
	turn(f.other, instanceB, chatB, "") // B's hook: turn ended.
	wakeA := f.reader.headersWait(chatA, "", 0).Wake
	if wakeA == "" {
		t.Fatal("R1 did not wake A")
	}
	// A's woken turn asks Q2: chain [Q1, R1] is at the limit, B stays asleep.
	turn(f.reader, instanceA, chatA, wakeA)
	q2 := stored(f.reader.Send(instanceA, chatA, question("q2")))
	if !slices.Equal(q2.Origin, []string{q1.ID, r1.ID}) || messageChainWakes(q2) {
		t.Fatal("Q2 chain", q2.Origin)
	}
	page := f.other.headersWait(chatB, "", 0)
	if page.Wake > wakeB {
		t.Fatal("Q2 woke B past the hop limit", page.Wake, wakeB)
	}
	if page.PendingQuestions == nil || *page.PendingQuestions < 1 {
		t.Fatal("Q2 is not waiting unread for B", page)
	}
	// A person's prompt in A clears its chain; Q3 starts at zero and wakes B.
	turn(f.reader, instanceA, chatA, "")
	q3 := stored(f.reader.Send(instanceA, chatA, question("q3")))
	if len(q3.Origin) != 0 {
		t.Fatal("a person's turn inherited the woken chain", q3.Origin)
	}
	if next := f.other.headersWait(chatB, "", 0).Wake; next <= wakeB {
		t.Fatal("Q3 from a person's turn did not wake B", next, wakeB)
	}
}
