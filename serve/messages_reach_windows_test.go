//go:build windows

package main

import (
	"context"
	"strings"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// A Codex integration whose open inbox has no bound waker lists as queued and
// prepares with reason queued; its watch makes it open again.
func TestMessagesCodexInboxWithoutWatchListsAndPreparesQueued(t *testing.T) {
	f := messagesObserveFixtureNamed(t, "oa-codex-integration", "oa-codex-integration.exe")
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "oa-codex-integration", "chat")
	reach := func() (msg.Inbox, msg.PrepareResult) {
		t.Helper()
		page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
		if err != nil || page.Outcome != msg.OutcomePage {
			t.Fatal(page, err)
		}
		var found *msg.Inbox
		for i := range page.Inboxes {
			if page.Inboxes[i].Program == "app:oa-codex-integration" && page.Inboxes[i].Context == "chat" {
				found = &page.Inboxes[i]
			}
		}
		if found == nil || found.Reach == nil {
			t.Fatal("Codex inbox not listed", page)
		}
		prepared, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:oa-codex-integration", Body: "question"})
		if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil {
			t.Fatal(prepared, err)
		}
		return *found, prepared
	}
	inbox, prepared := reach()
	if *inbox.Reach != msg.InboxReachQueued || inbox.Watching || prepared.Reason != "queued" {
		t.Fatal("unwatched Codex inbox", inbox, prepared)
	}
	f.reader.host.messageWatchMu.Lock()
	f.reader.host.messageWatches = map[*nativeMessageWatch]struct{}{{actor: messageActor{Program: f.otherProgram, Context: "chat"}, ctx: context.Background()}: {}}
	f.reader.host.messageWatchMu.Unlock()
	inbox, prepared = reach()
	if *inbox.Reach != msg.InboxReachOpen || prepared.Reason != "" {
		t.Fatal("watched Codex inbox", inbox, prepared)
	}
	// The receiver reports through its presence status that Codex did not
	// confirm its wake: the listing and Prepare tell the sender.
	if opened, err := receiver.InboxOpen(ri, "chat", "Watching | "+messageWakeUnconfirmedStatus); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	inbox, prepared = reach()
	if *inbox.Reach != msg.InboxReachQueued || !strings.Contains(inbox.Status, "queued; Codex did not confirm the wake") || prepared.Reason != "wake_unconfirmed" {
		t.Fatal("unconfirmed Codex wake", inbox, prepared)
	}
}
