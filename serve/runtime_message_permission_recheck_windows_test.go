//go:build windows

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The person allowed Claude to message OpenCode, but the Panel's separate
// rule commit never landed (owner R10, 2026-11-08). The agent's same-key
// recheck must save that recorded Allow, and the next Prepare must succeed.
func TestMessagePermissionRecheckSavesRecordedAllow(t *testing.T) {
	const receiver = "oa-opencode-integration"
	f, client, operator := messagePermissionFixtureFor(t, messagesObserveFixtureNamed(t, receiver, receiver+".exe", true), "recheck")
	ctx := f.run.ctx
	self := messages1608Presence(t, f.run, f.reader.call, "", "source")
	pin, err := f.other.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	_, otherSubject, session, _, err := nativeInboxPeer(pin, time.Now())
	pin.Close()
	if err != nil {
		t.Fatal(err)
	}
	presence := f.run.d.announceInSession(ctx, otherSubject, session, fwire.ApplicationPresence{Application: receiver, LeaseMs: 60000, Contexts: []fwire.ApplicationContext{{Name: "opencode-chat"}}})
	if presence.Outcome != fwire.ApplicationOutcomeApplied {
		t.Fatal(presence)
	}
	for _, row := range []struct {
		r                 *messagesReceiver
		instance, context string
	}{{f.reader, self, "source"}, {f.other, presence.Instance, "opencode-chat"}} {
		if out, err := row.r.InboxOpen(row.instance, row.context, ""); err != nil || out.Outcome != msg.OutcomeOpened {
			t.Fatal(out, err)
		}
	}
	prepare := func() msg.PrepareResult {
		t.Helper()
		out, err := f.reader.Prepare(self, "source", msg.PrepareRequest{Receiver: "app:" + receiver, Kind: new(msg.KindNote), Body: "hello"})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if out := prepare(); out.Outcome != msg.OutcomeForbidden || out.Reason != "access:"+receiver {
		t.Fatal("first prepare", out)
	}
	intent := messagePermissionIntent(receiver, "person-allowed")
	first, err := client.RequestContext(ctx, intent)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	// The answer is recorded; its rule commit is lost, as on the owner's install.
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	if out := prepare(); out.Outcome != msg.OutcomeForbidden {
		t.Fatal("recorded Allow alone authorized Prepare", out)
	}
	recheck, err := client.RequestContext(ctx, intent)
	if err != nil || recheck.Outcome != askswire.PermissionOutcomeAnswered || recheck.Answer == nil || recheck.Answer.ID != first.Answer.ID || recheck.Answer.Option != "allow" {
		t.Fatal("same-key recheck lost the answer", recheck, err)
	}
	if observed, err := client.ObserveQuestionContext(ctx, first.Answer.ID, 0); err != nil || observed.Observation.Outcome != askswire.PermissionOutcomePermitted {
		t.Fatal("recheck did not save the recorded Allow", observed, err)
	}
	// The saved rule records the person's Allow as its consent; Undo below
	// removes only rules carrying this decision ID.
	if applied, err := f.run.f.a.rights.policy.AppliedFirstUseAllow(first.Answer.ID); err != nil || !applied {
		t.Fatal("saved rule does not record the person's Allow", applied, err)
	}
	if pending, err := f.run.f.a.book.PendingTypedPermissions(); err != nil || len(pending) != 0 {
		t.Fatal("recheck asked the person again", pending, err)
	}
	if out := prepare(); out.Outcome != msg.OutcomePage || out.SendRequest == nil || out.SendRequest.To != "app:"+receiver {
		t.Fatal("prepare after saved Allow", out)
	}
	// A fresh key reads the saved permission directly.
	if fresh, err := client.RequestContext(ctx, messagePermissionIntent(receiver, "fresh-key")); err != nil || fresh.Outcome != askswire.PermissionOutcomePermitted {
		t.Fatal(fresh, err)
	}
	// Undo keeps the completion receipt: a later recheck never re-applies it.
	a := f.run.f.a
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	undo, err := a.undoPermissionEffect(ctx, f.run.f.panel, revision, first.Answer.ID, func() error { return nil })
	if err != nil || undo.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(undo, err)
	}
	if _, err := client.RequestContext(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if out := prepare(); out.Outcome != msg.OutcomeForbidden {
		t.Fatal("recheck re-granted an undone Allow", out)
	}
}

// A recorded Allow naming two receivers is saved whole on the recheck; its
// Undo removes both pairs, and a later recheck never applies it again.
func TestMessagePermissionRecheckSavesEveryRecordedPair(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	enrollExtraMessageAgent(t, f, "second-agent")
	intent := messagePermissionIntent("other-agent", "recheck-both")
	first, err := client.RequestContext(ctx, intent)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	pending, err := a.book.PendingTypedPermissions()
	if err != nil || len(pending) != 1 || len(pending[0].TypedPermission.Effect.Selections) != 2 {
		t.Fatal("the question does not name both receivers", pending, err)
	}
	subject := pending[0].TypedPermission.Effect.Subject
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	allowed := func(name string) bool {
		return a.rights.decide(ctx, subject, grants.ApplicationMessageAction, "app:"+name).Outcome == rwire.DecisionOutcomePermitted
	}
	if allowed("other-agent") || allowed("second-agent") {
		t.Fatal("the recorded answer alone wrote a rule")
	}
	recheck, err := client.RequestContext(ctx, intent)
	if err != nil || recheck.Answer == nil || recheck.Answer.ID != first.Answer.ID {
		t.Fatal(recheck, err)
	}
	if !allowed("other-agent") || !allowed("second-agent") {
		t.Fatal("the recheck did not save every recorded pair")
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if undo, err := a.undoPermissionEffect(ctx, f.run.f.panel, revision, first.Answer.ID, func() error { return nil }); err != nil || undo.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(undo, err)
	}
	if _, err := client.RequestContext(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if allowed("other-agent") || allowed("second-agent") {
		t.Fatal("a recheck re-applied an undone Allow")
	}
}

// Architect ruling A: only the latest decision for an exact pair is written.
// A later Don't allow for the same pair wins over an earlier unsaved Allow.
func TestMessagePermissionLaterDontAllowWinsOverUnsavedAllow(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	intent := messagePermissionIntent("other-agent", "earlier-allow")
	first, err := client.RequestContext(ctx, intent)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	record, found, err := a.book.ApplicationRecord(first.Answer.ID)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	subject := record.TypedPermission.Effect.Subject
	// A later question for the same pair, answered Don't allow.
	later, outcome, err := a.book.AskTypedPermissionAuthorizedFrom(asksservice.PermissionScope(subject.Account, subject.Program), "later", "later-decision", *record.TypedPermission, record.Via, asks.Publisher{}, nil)
	if err != nil || outcome != "pending" {
		t.Fatal(outcome, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, later.ID, "refuse"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	if _, err := client.RequestContext(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if a.rights.decide(ctx, subject, grants.ApplicationMessageAction, "app:other-agent").Outcome == rwire.DecisionOutcomePermitted {
		t.Fatal("an earlier Allow overrode the later Don't allow")
	}
	if applied, err := a.rights.policy.AppliedFirstUseAllow(first.Answer.ID); err != nil || applied {
		t.Fatal("earlier Allow was completed", applied, err)
	}
}

// Architect ruling A saves a recorded Allow on the agent's recheck. When
// that save fails too, the Allow is released and the person is asked
// afresh (owner, 2026-11-09); the fresh question saves normally.
func TestMessagePermissionFailedRecheckSaveAsksAfresh(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	intent := messagePermissionIntent("other-agent", "failing-save")
	first, err := client.RequestContext(ctx, intent)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	// Fail the guarded save's own metadata refresh, the second refresh of
	// the request; the request's preparation refresh still succeeds.
	refresh := a.rights.policy.RefreshMetaHolder
	var calls atomic.Int64
	a.rights.policy.RefreshMetaHolder = func(ctx context.Context) error {
		if calls.Add(1) == 2 {
			return errors.New("injected save failure")
		}
		return refresh(ctx)
	}
	failed, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "new-key-after-allow"))
	a.rights.policy.RefreshMetaHolder = refresh
	if err != nil || failed.Outcome != askswire.PermissionOutcomePending || failed.Answer == nil || failed.Answer.ID == first.Answer.ID {
		t.Fatal("failed save did not ask afresh", failed, err)
	}
	if _, found, err := a.book.ApplicationRecord(first.Answer.ID); err != nil || found {
		t.Fatal("the unsaved Allow still stands", found, err)
	}
	// The same key reaches a fresh question too, never the released Allow.
	again, err := client.RequestContext(ctx, intent)
	if err != nil || again.Outcome != askswire.PermissionOutcomePending || again.Answer == nil || again.Answer.ID == first.Answer.ID {
		t.Fatal("the same key reused the released Allow", again, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, failed.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	if _, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "new-key-after-allow")); err != nil {
		t.Fatal(err)
	}
	if observed, err := client.ObserveQuestionContext(ctx, failed.Answer.ID, 0); err != nil || observed.Observation.Outcome != askswire.PermissionOutcomePermitted {
		t.Fatal("the fresh Allow was not saved", observed, err)
	}
}

// Architect review of b17570889: a later Allow for the same pair that was
// saved and then removed is the latest decision; the earlier unsaved Allow
// is never written by a recheck.
func TestMessagePermissionRemovedLaterAllowWinsOverUnsavedAllow(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	intent := messagePermissionIntent("other-agent", "unsaved-allow")
	first, err := client.RequestContext(ctx, intent)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	record, found, err := a.book.ApplicationRecord(first.Answer.ID)
	if err != nil || !found {
		t.Fatal(found, err)
	}
	subject := record.TypedPermission.Effect.Subject
	later, outcome, err := a.book.AskTypedPermissionAuthorizedFrom(asksservice.PermissionScope(subject.Account, subject.Program), "later", "later-allow", *record.TypedPermission, record.Via, asks.Publisher{}, nil)
	if err != nil || outcome != "pending" {
		t.Fatal(outcome, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, later.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, later.ID, func() error { return nil })
	if err != nil || saved.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(saved, err)
	}
	removed, err := a.undoPermissionEffect(ctx, f.run.f.panel, saved.Revision, later.ID, func() error { return nil })
	if err != nil || removed.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(removed, err)
	}
	if _, err := client.RequestContext(ctx, intent); err != nil {
		t.Fatal(err)
	}
	if a.rights.decide(ctx, subject, grants.ApplicationMessageAction, "app:other-agent").Outcome == rwire.DecisionOutcomePermitted {
		t.Fatal("an earlier unsaved Allow was re-granted after a later Remove")
	}
}
