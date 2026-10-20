package main

import (
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesRetainedChatDiscoverySurvivesHelperExitAndKeepsMessagePermit(t *testing.T) {
	f := messagesObserveFixture(t)
	ai, canonical := messagesTypedChatAt(t, f, f.reader, "native-agent", "retained-session", "Closed review", "source-retained")
	f.run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	if err := f.run.d.attachApplicationV3(f.run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	if err := f.run.d.refreshMessageParties(f.run.ctx); err != nil {
		t.Fatal(err)
	}
	pin, err := f.reader.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, subject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	f.run.d.withdrawInSession(f.run.ctx, subject, session, "native-agent", ai)
	page, err := messagesBaseListing(f.other, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal(page, err)
	}
	found := 0
	for _, row := range page.Inboxes {
		if row.Context == canonical {
			found++
			if row.ChatTitle != "Closed review" || row.Watching || row.Reach == nil || *row.Reach != msg.InboxReachQueued {
				t.Fatal(row)
			}
		}
	}
	if found != 1 {
		t.Fatal("closed chat lost discovery without a running helper", page)
	}
	if err := f.run.f.a.rights.policy.Set(f.otherSubject, ActionApplicationMessage, "app:native-agent", false); err != nil {
		t.Fatal(err)
	}
	page, err = messagesBaseListing(f.other, msg.InboxQuery{})
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal(page, err)
	}
	for _, row := range page.Inboxes {
		if row.Context == canonical {
			t.Fatal("retained chat bypassed current message permission", row)
		}
	}
}
