//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"testing"
)

func TestMessagesPrepareRestartsSnapshotAfterStatusChurn(t *testing.T) {
	f := messagesPrepareGapFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	changed := false
	f.run.d.messageStartProfile = func(_ context.Context, descriptor wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		if descriptor.Name == "other-agent" && !changed {
			changed = true
			if out, err := receiver.InboxOpen(ri, "target", "renewed status"); err != nil || out.Outcome != msg.OutcomeOpened {
				t.Fatal(out, err)
			}
		}
		return applicationMessageStartRequest{}, errors.New("no fixture start profile")
	}
	out, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: "target", Body: "fixture question"})
	if !changed {
		t.Fatal("fixture did not cross native snapshot captures")
	}
	if err != nil || out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != "target" {
		t.Fatal(out, err)
	}
}

func messagesPrepareGapFixture(t *testing.T) *messagesObserveRun {
	t.Helper()
	f := messagesObserveFixture(t)
	f.run.d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	if err := f.run.d.attachApplicationV3(f.run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	if err := f.run.d.refreshMessageParties(f.run.ctx); err != nil {
		t.Fatal(err)
	}
	return f
}

// Owner R22, 2026-11-10: a receiver whose status changes on every read is
// the same conversation. Prepare resolves it; before, persistent status churn
// answered discovery_incomplete and Send to OpenCode failed every time.
func TestMessagesPreparePersistentStatusChurnResolves(t *testing.T) {
	f := messagesPrepareGapFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	changes := 0
	f.run.d.messageStartProfile = func(_ context.Context, descriptor wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		if descriptor.Name == "other-agent" {
			changes++
			if out, err := receiver.InboxOpen(ri, "target", fmt.Sprintf("status %d", changes)); err != nil || out.Outcome != msg.OutcomeOpened {
				t.Fatal(out, err)
			}
		}
		return applicationMessageStartRequest{}, errors.New("no fixture start profile")
	}
	out, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Context: "target", Body: "fixture question"})
	if err != nil || changes < 2 || out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.Context != "target" {
		t.Fatal(out, err, changes)
	}
}

func TestMessagesPrepareChurnCannotRetainRevokedMetadata(t *testing.T) {
	f := messagesPrepareGapFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	messagesStateOpen(t, f, f.other, "other-agent", "secret-context")
	revoked := false
	f.run.d.messageStartProfile = func(_ context.Context, descriptor wire.ApplicationDescriptor) (applicationMessageStartRequest, error) {
		if descriptor.Name == "other-agent" && !revoked {
			revoked = true
			if err := f.reader.host.messageRights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", false); err != nil {
				t.Fatal(err)
			}
		}
		return applicationMessageStartRequest{}, errors.New("no fixture start profile")
	}
	out, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Body: "fixture question"})
	if err != nil || !revoked || out.Outcome == msg.OutcomePage || out.SendRequest != nil || len(out.Choices) != 0 || out.Reason == "ambiguous_receiver" {
		t.Fatal(out, err)
	}
}
