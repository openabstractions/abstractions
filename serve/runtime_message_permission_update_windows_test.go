//go:build windows

package main

import (
	"context"
	"testing"
	"time"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Owner R12, 2026-11-08: an OA connector update re-recorded the receiver's
// enrollment after the person's Allow, whose save had failed. That Allow is
// no decision about the updated party: a request asks the person afresh and
// never stops at "unavailable".
func TestMessagePermissionUpdatedPartyAsksAfresh(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	intent := messagePermissionIntent("other-agent", "before-update")
	first, err := client.RequestContext(ctx, intent)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	// The update re-records the receiver's enrollment under a new revision.
	current := f.run.d.currentAgentEnrollment(ctx, "other-agent")
	if current.Enrollment == nil {
		t.Fatal(current)
	}
	disabled := agent1478Set(t, f.run, "other-agent", current.Enrollment.Revision, false)
	agent1478Set(t, f.run, "other-agent", disabled.Revision, true)
	answer(t, f.run.f, agent1478Question(t, f.run, "other-agent").ID, "keep")
	agent1478Wait(t, f.run, "other-agent", "enrolled")
	if err := f.run.d.refreshMessageParties(ctx); err != nil {
		t.Fatal(err)
	}
	fresh, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "after-update"))
	if err != nil || fresh.Outcome != askswire.PermissionOutcomePending || fresh.Answer == nil || fresh.Answer.ID == first.Answer.ID {
		t.Fatal("updated party did not get a fresh question", fresh, err)
	}
	// The original key names the old parties; the gateway renews the key on
	// this conflict, so the agent's same idempotency_key reaches the fresh one.
	if same, err := client.RequestContext(ctx, intent); err != nil || same.Outcome != askswire.PermissionOutcomeConflict {
		t.Fatal("old key did not report its changed parties", same, err)
	}
}

// Owner R12, 2026-11-08: a registration write cleared the published messaging
// facts while the receiver was closed, and access requests read "native roles
// unavailable". Requesting access needs only recorded enrollments.
func TestMessagePermissionRequestRebuildsClearedMessagingFacts(t *testing.T) {
	f, client, _ := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	refresh := a.rights.policy.RefreshMetaHolder
	a.rights.policy.RefreshMetaHolder = func(ctx context.Context) error {
		err := refresh(ctx)
		if roles := f.run.d.roles.Load(); roles != nil {
			cleared := *roles
			cleared.messaging = nil
			f.run.d.roles.Store(&cleared)
		}
		return err
	}
	defer func() { a.rights.policy.RefreshMetaHolder = refresh }()
	// The receiver has never opened an inbox: it is not running.
	out, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "receiver-closed"))
	if err != nil || out.Outcome != askswire.PermissionOutcomePending || out.Answer == nil {
		t.Fatal("request while the receiver is closed", out, err)
	}
}

// Architect, R12 batch: an OA update that replaces a connector keeps every
// saved Messages permission. The rule names the sender program path and the
// receiver's registered name, which an OA connector update keeps.
func TestMessagePermissionSavedRuleSurvivesConnectorUpdate(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	self := messages1608Presence(t, f.run, f.reader.call, "", "source")
	first, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "saved-before-update"))
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, first.Answer.ID, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	// The update replaces the receiver's image (a new file key) and
	// re-records its enrollment under a new revision.
	read := f.run.d.agentImage
	if read == nil {
		read = readApplicationAgentImage
	}
	f.run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		image, err := read(ctx, program, binding)
		if program == f.otherProgram {
			image.Key = "updated-connector-image"
		}
		return image, err
	}
	f.run.d.agentImages.forget()
	current := f.run.d.currentAgentEnrollment(ctx, "other-agent")
	if current.Enrollment == nil {
		t.Fatal(current)
	}
	disabled := agent1478Set(t, f.run, "other-agent", current.Enrollment.Revision, false)
	agent1478Set(t, f.run, "other-agent", disabled.Revision, true)
	answer(t, f.run.f, agent1478Question(t, f.run, "other-agent").ID, "keep")
	agent1478Wait(t, f.run, "other-agent", "enrolled")
	if err := f.run.d.refreshMessageParties(ctx); err != nil {
		t.Fatal(err)
	}
	if updated := f.run.d.currentAgentEnrollment(ctx, "other-agent"); updated.Enrollment == nil || updated.Enrollment.Revision == current.Enrollment.Revision {
		t.Fatal("update did not re-record the receiver", updated)
	}
	// The saved rule still permits: a fresh request asks nothing, and
	// Prepare to the receiver's live chat succeeds.
	again, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "after-update"))
	if err != nil || again.Outcome != askswire.PermissionOutcomePermitted {
		t.Fatal("saved rule lost on update", again, err)
	}
	if pending, err := a.book.PendingTypedPermissions(); err != nil || len(pending) != 0 {
		t.Fatal("update asked the person again", pending, err)
	}
	pin, err := f.other.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, otherSubject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := f.run.d.announceInSession(ctx, otherSubject, session, fwire.ApplicationPresence{Application: "other-agent", LeaseMs: 60000, Contexts: []fwire.ApplicationContext{{Name: "updated-chat"}}})
	if presence.Outcome != fwire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	for _, row := range []struct {
		r                 *messagesReceiver
		instance, context string
	}{{f.reader, self, "source"}, {f.other, presence.Instance, "updated-chat"}} {
		if out, err := row.r.InboxOpen(row.instance, row.context, ""); err != nil || out.Outcome != msg.OutcomeOpened {
			t.Fatal(out, err)
		}
	}
	prepared, err := f.reader.Prepare(self, "source", msg.PrepareRequest{Receiver: "app:other-agent", Kind: new(msg.KindNote), Body: "after the update"})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil {
		t.Fatal("prepare after the update", prepared, err)
	}
	if sent, err := f.reader.Send(self, "source", *prepared.SendRequest); err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal("send after the update", sent, err)
	}
}
