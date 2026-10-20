//go:build windows

package main

import (
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// A permitted sender learns how the receiver's last inbox ended; a sender
// without the message right reads the generic word.
func TestMessagesNativePrepareReportsReceiverEndToPermittedSender(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	pin, err := f.other.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	prepare := func() msg.PrepareResult {
		t.Helper()
		got, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: "chat", Body: "x"})
		if err != nil || got.Outcome != msg.OutcomeUnavailable || got.SendRequest != nil {
			t.Fatal(got, err)
		}
		return got
	}
	if got := prepare(); got.Reason != "receiver_unavailable" {
		t.Fatal("no presence yet", got)
	}
	presence := f.run.d.announceInSession(t.Context(), subject, session, wire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: "chat"}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	if opened, e := f.other.InboxOpen(presence.Instance, "chat", ""); e != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, e)
	}
	if out := f.run.d.withdrawInSession(t.Context(), subject, session, "other-agent", presence.Instance); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if got := prepare(); got.Reason != "receiver_withdrawn" {
		t.Fatal("withdrawn", got)
	}
	presence = f.run.d.announceInSession(t.Context(), subject, session, wire.ApplicationPresence{Application: "other-agent", LeaseMs: 1000, Interfaces: []wire.ApplicationInterface{}, Contexts: []wire.ApplicationContext{{Name: "chat"}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	base := f.run.d.now
	// Past the receiver's one-second lease, within the sender's own.
	f.run.d.now = func() time.Time { return base().Add(2 * time.Second) }
	if got := prepare(); got.Reason != "receiver_lapsed" {
		t.Fatal("lapsed", got)
	}
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", false); err != nil {
		t.Fatal(err)
	}
	got, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Body: "x"})
	if err != nil || got.Outcome != msg.OutcomeForbidden || got.Reason != "access:other-agent" || got.SendRequest != nil || len(got.Choices) != 0 {
		t.Fatal("cause disclosed without the message right", got, err)
	}
}
