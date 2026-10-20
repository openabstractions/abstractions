//go:build windows

package main

import (
	"runtime"
	"sync"
	"testing"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// clearMessagingFacts models registration writers that republish the roles
// index without messaging facts while a permission operation runs, as the
// owner's two Claude chats, Codex and OpenCode connectors do (R13).
func clearMessagingFacts(f *messagesObserveRun) (stop func()) {
	done := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-done:
				return
			default:
			}
			if roles := f.run.d.roles.Load(); roles != nil && roles.messaging != nil {
				cleared := *roles
				cleared.messaging = nil
				f.run.d.roles.CompareAndSwap(roles, &cleared)
			}
			runtime.Gosched()
		}
	}()
	return func() { close(done); wg.Wait() }
}

// Owner R13, 2026-11-08: "permission Allow ... not saved: messages: target
// preparation unavailable". The permit admission read the published roles,
// which a concurrent registration write had cleared. The Allow now saves with
// the facts its own operation prepared and validated.
func TestMessagePermissionAllowSavesWhileRolesAreRepublished(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	stop := clearMessagingFacts(f)
	out, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "roles-republished"))
	if err != nil || out.Outcome != askswire.PermissionOutcomePending || out.Answer == nil {
		stop()
		t.Fatal("request while roles are republished", out, err)
	}
	if answered, err := operator.AnswerPermissionIfPendingContext(ctx, out.Answer.ID, "allow"); err != nil || answered.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		stop()
		t.Fatal(answered, err)
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		stop()
		t.Fatal(err)
	}
	edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, out.Answer.ID, func() error { return nil })
	stop()
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal("Allow not saved while roles are republished", edit, err)
	}
	if got := a.rights.policy.Decide(f.self, ActionApplicationMessage, "app:other-agent"); got.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatal(got)
	}
}
