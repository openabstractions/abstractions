//go:build windows

package main

import (
	"context"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesExplicitReceivingDefaultAndMultipleChats(t *testing.T) {
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
	open := func(name string, managed bool) string {
		t.Helper()
		if managed {
			_, canonical := messagesTypedChat(t, f, f.other, "other-agent", name, name)
			return canonical
		}
		p := f.run.d.announceInSession(t.Context(), subject, session, wire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: name}}})
		if p.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(p)
		}
		if out, err := f.other.InboxOpen(p.Instance, name, ""); err != nil || out.Outcome != msg.OutcomeOpened {
			t.Fatal(out, err)
		}
		return name
	}
	bound := open("bound-chat", true)
	open("legacy-default", false)
	f.run.d.messageStartProfile = func(context.Context, wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		return applicationMessageStartRequest{Session: session, Context: "legacy-default", ProfileFingerprint: "approved-default", Validate: func(context.Context) error { return nil }}, nil
	}
	prepare := func(context string) msg.PrepareResult {
		out, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: context, Body: "ordinary question"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Both the legacy inbox and canonical chat are eligible while live.
	if out := prepare(""); out.Outcome != msg.OutcomePage || out.SendRequest == nil {
		t.Fatal("program-only route unavailable", out)
	}
	if out := prepare(bound); out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != bound {
		t.Fatal("explicit bound chat lost", out)
	}
	if out := prepare("legacy-default"); out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != "legacy-default" {
		t.Fatal("explicit legacy destination lost", out)
	}
	if out := prepare(bound); out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != bound {
		t.Fatal("explicit bound choice failed", out)
	}
}

// Deprecated InboxOpen retains receiving ownership. Typed empty ChatBinding
// opens an independently authorized send-only source.
func TestMessagesSendOnlySourceCurrentlyRequiresAdvertisedInbox(t *testing.T) {
	f := messagesObserveFixture(t)
	_, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	_ = receiver
	pin, err := f.reader.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := f.run.d.announceInSession(t.Context(), subject, session, wire.ApplicationPresence{Application: "native-agent", LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: "source"}}})
	if presence.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	request := msg.PrepareRequest{Receiver: "app:other-agent", Body: "valid ordinary message"}
	if out, err := f.reader.Prepare(presence.Instance, "source", request); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("send-only absence counterexample changed", out, err)
	}
	if out, err := f.reader.InboxOpen(presence.Instance, "source", ""); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	if out, err := f.reader.Prepare(presence.Instance, "source", request); err != nil || out.Outcome != msg.OutcomePage || out.SendRequest == nil {
		t.Fatal("ordinary preparation unavailable after source opens", out, err)
	}
	page, err := messagesBaseListing(f.other, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal(page, err)
	}
	found := false
	for _, inbox := range page.Inboxes {
		found = found || inbox.Program == "app:native-agent" && inbox.Context == "source"
	}
	if !found {
		t.Fatal("source authorization no longer implies receiving advertisement")
	}
	if out, err := f.reader.InboxOpenChat(presence.Instance, "source", "", msg.ChatBinding{}); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	visible := func(contextName string) bool {
		t.Helper()
		page, err := messagesBaseListing(f.other, msg.InboxQuery{})
		if err != nil || page.Outcome != msg.OutcomePage {
			t.Fatal(page, err)
		}
		for _, row := range page.Inboxes {
			if row.Program == "app:native-agent" && row.Context == contextName {
				return true
			}
		}
		return false
	}
	if visible("source") {
		t.Fatal("source-only presence advertised receiving")
	}
	if out, err := f.reader.Prepare(presence.Instance, "source", request); err != nil || out.Outcome != msg.OutcomePage {
		t.Fatal("source-only lost native message authorization", out, err)
	}
	chatInstance, chat := messagesTypedChatAt(t, f, f.reader, "native-agent", "receiving-chat", "Receiving chat", "typed-source")
	if !visible(chat) || visible("typed-source") {
		t.Fatal("typed receiving and source disclosure disagreed")
	}
	f.run.d.mu.Lock()
	row := *f.run.d.nativeInboxes.rows["native-agent\x00"+chat]
	lease := f.run.d.instances[chatInstance]
	check := func(name string, change func(*applicationLease)) {
		copy := lease
		change(&copy)
		f.run.d.instances[chatInstance] = copy
		got := f.run.d.receivingNativeInboxLocked(row)
		f.run.d.instances[chatInstance] = lease
		if got {
			t.Errorf("receiving observation escaped %s fence", name)
		}
	}
	check("context", func(l *applicationLease) { l.value.Contexts = []wire.ApplicationContext{{Name: "foreign"}} })
	check("expiry", func(l *applicationLease) { l.deadline = f.run.d.now().Add(-time.Second) })
	check("native-account", func(l *applicationLease) { l.owner.Account = "foreign" })
	delete(f.run.d.instances, chatInstance)
	if f.run.d.receivingNativeInboxLocked(row) {
		t.Error("receiving observation survived missing native instance")
	}
	f.run.d.instances[chatInstance] = lease
	f.run.d.mu.Unlock()
	if changed := f.run.d.announceInSession(t.Context(), subject, session, wire.ApplicationPresence{Application: "native-agent", Instance: chatInstance, LeaseMs: 60000, Contexts: []wire.ApplicationContext{{Name: "typed-source"}}}); changed.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(changed)
	}
	if visible(chat) {
		t.Fatal("withdrawn readiness stayed advertised")
	}
	if out, err := f.reader.Prepare(chatInstance, "typed-source", request); err != nil || out.Outcome != msg.OutcomePage {
		t.Fatal("withdrawing receiving discarded source owner", out, err)
	}
}
