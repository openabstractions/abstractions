//go:build windows

package main

import (
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"testing"
	"time"
)

// Test-only evidence for the existing API: a deterministic chat context reopens
// after native retirement, but a concurrent send-only occurrence cannot borrow
// its body authority or choose that context as its outgoing reply address.
func TestMessagesChatOwnerReuseAndConcurrentSourceCounterexample(t *testing.T) {
	f := messagesObserveFixture(t)
	ai, a := messagesStateOpen(t, f, f.reader, "native-agent", "stable-chat")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	callB, _ := messages1608Call(t)
	b := &messagesReceiver{host: f.reader.host, call: callB, ctx: f.run.ctx}
	pin, err := callB.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, bs, sessionB, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presenceB := f.run.d.announceInSession(t.Context(), bs, sessionB, wire.ApplicationPresence{Application: "native-agent", LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: "stable-chat"}, {Name: "source-b"}}})
	if presenceB.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presenceB)
	}
	bi := presenceB.Instance
	if got, _ := b.InboxOpen(bi, "stable-chat", ""); got.Outcome != msg.OutcomeHeld {
		t.Fatal("concurrent occurrence acquired canonical owner", got)
	}
	if got, _ := b.Wait(bi, "stable-chat", msg.ViewBody, "", "", 0); got.Outcome != msg.OutcomeForbidden {
		t.Fatal("send-only occurrence borrowed canonical body authority", got)
	}
	if got, _ := b.InboxOpen(bi, "source-b", ""); got.Outcome != msg.OutcomeOpened {
		t.Fatal(got)
	}
	prepared, err := b.Prepare(bi, "source-b", msg.PrepareRequest{Receiver: "app:other-agent", Context: "target", Body: "question from concurrent source"})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil {
		t.Fatal(prepared, err)
	}
	if got, _ := b.Send(bi, "stable-chat", *prepared.SendRequest); got.Outcome != msg.OutcomeForbidden {
		t.Fatal("canonical source spoof accepted", got)
	}
	sent, err := b.Send(bi, "source-b", *prepared.SendRequest)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	page, err := receiver.Wait(ri, "target", msg.ViewBody, "", "", 0)
	if err != nil || len(page.Messages) != 1 || page.Messages[0].Header.FromContext != "source-b" {
		t.Fatal("native FromContext ceased to be actual owned source", page, err)
	}
	if got, err := receiver.Reply(ri, "target", sent.MessageID, "answer to source-b"); err != nil || got.Outcome != msg.OutcomeSent {
		t.Fatal(got, err)
	}
	if page, err := a.Wait(ai, "stable-chat", msg.ViewBody, "", "", 0); err != nil || messagesReceiverHasReply(page, "answer to source-b") {
		t.Fatal("reply implicitly routed to different canonical owner", page, err)
	}
	if page, err := b.Wait(bi, "source-b", msg.ViewBody, "", "", 0); err != nil || !messagesReceiverHasReply(page, "answer to source-b") {
		t.Fatal("reply not returned to exact original source", page, err)
	}
	// Only retiring A's own native instance releases the deterministic context.
	pin, err = f.reader.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, as, sessionA, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got := f.run.d.withdrawInSession(t.Context(), as, sessionA, "native-agent", ai); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	if got, _ := b.InboxOpen(bi, "stable-chat", ""); got.Outcome != msg.OutcomeOpened {
		t.Fatal("later same-program owner could not reopen dormant context", got)
	}
	stale := f.run.d.announceInSession(t.Context(), as, sessionA, wire.ApplicationPresence{Application: "native-agent", Instance: ai, LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: "stable-chat"}}})
	if stale.Outcome != wire.ApplicationOutcomeStale {
		t.Fatal("retired instance renewed", stale)
	}
	if got, _ := a.InboxOpen(ai, "stable-chat", ""); got.Outcome == msg.OutcomeOpened {
		t.Fatal("old instance reacquired new owner's inbox", got)
	}
	if got := f.run.d.withdrawInSession(t.Context(), as, sessionA, "native-agent", ai); got.Outcome != wire.ApplicationOutcomeStale {
		t.Fatal(got)
	}
	if page, err := b.Wait(bi, "stable-chat", msg.ViewBody, "", "", 0); err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal("old cleanup removed new native owner", page, err)
	}
	t.Log("existing ownership supports fenced retirement/reopen; concurrent source replies remain source-b and cannot be redirected to canonical owner")
}
