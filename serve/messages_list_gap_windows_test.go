//go:build windows

package main

import (
	"context"
	"fmt"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Owner R22, 2026-11-10: with eight OpenCode chats and the Codex chats live,
// a Claude listing's two reads differed on nearly every call. Every
// oa_messages_inboxes_list answered gap and Send to OpenCode answered
// discovery_incomplete. A receiver whose status changes during every listing
// must still be listed on a first page and found by Prepare.
func TestMessagesFirstListingAndPrepareAnswerWhileReceiversChange(t *testing.T) {
	f := messagesDiscoveryFixture(t)
	senderInstance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	receiverInstance, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", true); err != nil {
		t.Fatal(err)
	}
	if err := f.run.d.refreshMessageParties(f.run.ctx); err != nil {
		t.Fatal(err)
	}
	// Each listing's discovery warm-up runs after its open rows are read; the
	// receiver renews its inbox with a new status there, as a live receiver's
	// presence changes while a slow listing runs.
	changes := 0
	f.run.d.messageAttachWarm = func(context.Context, []string) bool {
		changes++
		if opened, err := receiver.InboxOpen(receiverInstance, "target", fmt.Sprintf("status %d", changes)); err != nil || opened.Outcome != msg.OutcomeOpened {
			t.Error("receiver renewal", opened, err)
		}
		return true
	}
	for range 3 {
		page, err := messagesBaseListing(f.reader, msg.InboxQuery{})
		if err != nil || page.Outcome != msg.OutcomePage {
			t.Fatal("first page did not answer while the receiver changed", page, err)
		}
		found := false
		for _, inbox := range page.Inboxes {
			found = found || inbox.Program == "app:other-agent" && inbox.Context == "target"
		}
		if !found {
			t.Fatal("receiver missing from the first page", page)
		}
	}
	before := changes
	prepared, err := sender.Prepare(senderInstance, "source", msg.PrepareRequest{Receiver: "app:other-agent", Body: "question"})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil || prepared.SendRequest.Context != "target" {
		t.Fatal("prepare did not resolve the changing receiver", prepared, err)
	}
	if changes == before || changes < 4 {
		t.Fatal("the receiver did not change during the listings", changes)
	}
}
