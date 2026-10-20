//go:build windows

package main

import (
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesSendChatReconcilesOriginalNativeProcessAfterReadmission(t *testing.T) {
	f := messagesObserveFixture(t)
	ai, route := messagesTypedChatAt(t, f, f.reader, "native-agent", "restart-chat", "", "original-source")
	_, _ = messagesStateOpen(t, f, f.other, "other-agent", "target")
	request := msg.ChatSendRequest{Request: msg.SendRequest{To: "app:other-agent", Context: "target", Kind: msg.KindNote, Body: "one accepted request", IdempotencyKey: "restart-key", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}, ReturnContext: route}
	first, err := f.reader.SendChat(ai, "original-source", request)
	if err != nil || first.Outcome != msg.OutcomeSent {
		t.Fatal(first, err)
	}
	withdraw := func(receiver *messagesReceiver, instance string) {
		t.Helper()
		pin, err := receiver.call.RetainProcess()
		if err != nil {
			t.Fatal(err)
		}
		_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
		pin.Close()
		if err != nil {
			t.Fatal(err)
		}
		if out := f.run.d.withdrawInSession(f.run.ctx, subject, session, "native-agent", instance); out.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(out)
		}
	}
	withdraw(f.reader, ai)
	bi, again := messagesTypedChatAt(t, f, f.reader, "native-agent", "restart-chat", "", "original-source")
	if bi == ai || again != route {
		t.Fatal("readmission did not retain route with fresh presence", ai, bi, route, again)
	}
	reconciled, err := f.reader.SendChat(bi, "original-source", request)
	if err != nil || reconciled.Outcome != msg.OutcomeSent || reconciled.MessageID != first.MessageID {
		t.Fatal("original live native process lost uncertain-send reconciliation", first, reconciled, err)
	}
	withdraw(f.reader, bi)
	call, _ := messages1608Call(t)
	fresh := &messagesReceiver{host: f.reader.host, call: call, ctx: f.run.ctx}
	ci, _ := messagesTypedChatAt(t, f, fresh, "native-agent", "restart-chat", "", "original-source")
	if out, err := fresh.SendChat(ci, "original-source", request); err != nil || out.Outcome != msg.OutcomeConflict {
		t.Fatal("fresh native process impersonated original source occurrence", out, err)
	}
}

func chatPageHasReply(page msg.Page, body string) bool {
	for _, message := range page.Messages {
		if message.Header.Kind == msg.KindReply && message.Body == body {
			return true
		}
	}
	return false
}

func TestMessagesSendChatTakeoverRetainsActualSourceAndCanonicalReply(t *testing.T) {
	f := messagesObserveFixture(t)
	ai, route := messagesTypedChat(t, f, f.reader, "native-agent", "canonical-chat", "First")
	call, _ := messages1608Call(t)
	b := &messagesReceiver{host: f.reader.host, call: call, ctx: f.run.ctx}
	bi, again := messagesTypedChatAt(t, f, b, "native-agent", "canonical-chat", "Review\u202e\n plan", "source-b")
	if route != again {
		t.Fatal(route, again)
	}
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	prepared, err := b.Prepare(bi, "source-b", msg.PrepareRequest{Receiver: "app:other-agent", Context: "target", Body: "question"})
	if err != nil || prepared.SendRequest == nil {
		t.Fatal(prepared, err)
	}
	request := msg.ChatSendRequest{Request: *prepared.SendRequest, ReturnContext: route}
	sent, err := b.SendChat(bi, "source-b", request)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	replay, err := b.SendChat(bi, "source-b", request)
	if err != nil || replay.MessageID != sent.MessageID {
		t.Fatal(replay, err)
	}
	changed := request
	changed.ReturnContext = "foreign"
	if out, _ := b.SendChat(bi, "source-b", changed); out.Outcome != msg.OutcomeConflict {
		t.Fatal(out)
	}
	record, _, err := f.store.readByID(t.Context(), sent.MessageID)
	if err != nil || record == nil || record.Actor.Context != "source-b" || record.Actor.Chat == nil || record.Actor.Chat.SourceInstance != bi {
		t.Fatal(record, err)
	}
	page, err := receiver.Wait(ri, "target", msg.ViewBody, "", "", 0)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Header.FromContext != route {
		t.Fatal(page, err)
	}
	if out, err := receiver.Reply(ri, "target", sent.MessageID, "answer"); err != nil || out.Outcome != msg.OutcomeSent {
		t.Fatal(out, err)
	}
	if out, err := b.Wait(bi, route, msg.ViewBody, "", "", 0); err != nil || !chatPageHasReply(out, "answer") {
		t.Fatal(out, err)
	}
	if out, err := f.reader.Wait(ai, route, msg.ViewBody, "", "", 0); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("retired owner received reply", out, err)
	}
	if out, err := b.Wait(bi, "source-b", msg.ViewBody, "", "", 0); err != nil || chatPageHasReply(out, "answer") {
		t.Fatal("reply leaked to source", out, err)
	}
	if out, _ := receiver.SendChat(ri, "target", request); out.Outcome != msg.OutcomeForbidden {
		t.Fatal("foreign source took route", out)
	}
	listed, err := receiver.InboxesList(msg.InboxQuery{})
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, row := range listed.Inboxes {
		if row.Program == "app:native-agent" {
			count++
			if row.Context != route || row.ChatTitle != "Review plan" {
				t.Fatal(row)
			}
		}
	}
	if count != 1 {
		t.Fatal(listed)
	}
}
