package main

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestSavedDefaultsSettleOnlyTheCompletePendingTypedEffect(t *testing.T) {
	f := applicationPermissionRuntime(t)
	permission, err := grants.ApplicationPermission(f.comfy, "app:editor", "Editor", "read")
	if err != nil {
		t.Fatal(err)
	}
	defaults := make([]rights.AccountDefault, 0, len(permission.Effect.Selections))
	for i, selection := range permission.Effect.Selections {
		if err := f.a.rights.policy.RegisterAction(selection.Permission.Action); err != nil {
			t.Fatal(err)
		}
		defaults = append(defaults, rights.AccountDefault{Name: fmt.Sprintf("typed-default-%d", i), Action: selection.Permission.Action, Resource: "app:editor"})
	}
	if len(defaults) < 2 {
		t.Fatal("fixture must require a complete multi-action effect")
	}
	if err := f.a.rights.policy.ConfigureDefaults(f.answerer.Account, defaults, nil); err != nil {
		t.Fatal(err)
	}
	set := func(name string, enabled bool) {
		t.Helper()
		revision, _, err := f.a.rights.policy.DefaultsSnapshot(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		edit, err := f.a.rights.policy.SetDefault(revision, name, enabled, f.panel, func() error { return nil })
		if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
			t.Fatal(edit, err)
		}
	}
	for _, d := range defaults {
		set(d.Name, false)
	}
	record, outcome, err := f.a.book.AskTypedPermissionOccurrenceAuthorizedFrom(f.a.scope, "runtime", "saved-default-effect", permission, listen.Seen{}, asks.Publisher{}, func() error { return nil })
	if err != nil || outcome != "pending" {
		t.Fatal(outcome, err)
	}
	passes := make(chan struct{}, 16)
	f.a.settler.done = func() { passes <- struct{}{} }
	stop := f.a.followRights(t.Context())
	defer stop()
	waitPass(t, passes)
	for i, d := range defaults {
		set(d.Name, true)
		waitPass(t, passes)
		pending, err := f.a.book.PendingTypedPermissions()
		if err != nil {
			t.Fatal(err)
		}
		if i < len(defaults)-1 && (len(pending) != 1 || pending[0].ID != record.ID) {
			t.Fatal("partial default effect withdrew request", pending)
		}
		if i == len(defaults)-1 && len(pending) != 0 {
			t.Fatal("complete saved default left request pending", pending)
		}
	}
	if _, found, err := f.a.book.ApplicationRecord(record.ID); err != nil || found {
		t.Fatal("settled occurrence remained", found, err)
	}
}

// settledRuntime is typedRuntime following rights changes as composeAsks
// does, with a channel that receives after every settling pass.
func settledRuntime(t *testing.T) (*runtimeAsks, rwire.Subject, <-chan struct{}) {
	t.Helper()
	a, subject := typedRuntime(t)
	passes := make(chan struct{}, 64)
	a.settler.mu.Lock()
	a.settler.done = func() { passes <- struct{}{} }
	a.settler.mu.Unlock()
	t.Cleanup(a.followRights(context.Background()))
	waitPass(t, passes)
	return a, subject, passes
}

// ASK-A13: stop removes the change subscription, cancels the pass in flight
// and waits for it. A pass blocked in its decision returns on cancellation
// without editing the Book, and no change afterwards starts another.
func TestStopEndsTheSettlingPassBeforeTheBookCloses(t *testing.T) {
	a, subject := typedRuntime(t)
	record := pendingTyped(t, a, subject, false)
	entered, released := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	a.settler.decide = func(ctx context.Context, effect rwire.PermissionEffect) rwire.BatchDecision {
		calls.Add(1)
		select {
		case entered <- struct{}{}:
		default:
		}
		<-ctx.Done() // a decision that never reads in time
		close(released)
		return a.rights.policy.CheckPermissionEffectContext(context.Background(), effect, "")
	}
	stop := a.followRights(context.Background())
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the start pass did not reach its decision")
	}
	// Grant the access while the pass blocks: had the pass gone on, it would
	// withdraw the request.
	setSelector(t, a, subject, typedTestSelection(false), true)
	stopped := make(chan struct{})
	go func() { stop(); close(stopped) }()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("stop did not return while a pass was blocked")
	}
	select {
	case <-released:
	default:
		t.Fatal("stop returned before the blocked pass ended")
	}
	if _, found, err := a.book.ApplicationRecord(record.ID); err != nil || !found {
		t.Fatalf("a cancelled pass edited the Book: %v %v", found, err)
	}
	before := calls.Load()
	setSelector(t, a, subject, typedTestSelection(true), false)
	a.settleSoon()
	time.Sleep(100 * time.Millisecond)
	if calls.Load() != before {
		t.Fatal("a pass started after stop")
	}
}

func waitPass(t *testing.T, passes <-chan struct{}) {
	t.Helper()
	select {
	case <-passes:
	case <-time.After(5 * time.Second):
		t.Fatal("no settling pass after a rights change")
	}
}

// pendingTyped admits the service-origin typed request a refused operation
// raises and returns it.
func pendingTyped(t *testing.T, a *runtimeAsks, subject rwire.Subject, all bool) asks.Record {
	t.Helper()
	selection := typedTestSelection(all)
	operation := rwire.ConcreteResource{Permission: selection.Permission, Target: selection.Selector.Target}
	if all {
		operation.Target = typedTestSelection(false).Selector.Target
	}
	a.rights.decideTypedAsking(context.Background(), subject, operation)
	pending, err := a.book.PendingTypedPermissions()
	if err != nil || len(pending) != 1 {
		t.Fatalf("pending typed requests %+v %v", pending, err)
	}
	return pending[0]
}

func setSelector(t *testing.T, a *runtimeAsks, subject rwire.Subject, selection rwire.PermissionSelection, permit bool) {
	t.Helper()
	revision, _, err := a.rights.policy.SelectorSnapshotContext(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	edit, err := a.rights.policy.SetSelectorRuleContext(context.Background(), revision, rwire.SelectorRule{Subject: subject, Selection: selection, Permit: permit},
		rights.RuleEdit{By: subject, Why: "saved in the Panel"}, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("selector rule %+v %v", edit, err)
	}
}

// ASK-A13, RIGHTS-R23: a pending typed request leaves the Book once a saved
// rule permits its exact effect, or a Block refuses it; a change that leaves
// it ungranted keeps it pending.
func TestASavedRuleOrBlockSettlesAPendingTypedRequest(t *testing.T) {
	t.Run("allow", func(t *testing.T) {
		a, subject, passes := settledRuntime(t)
		record := pendingTyped(t, a, subject, false)
		setSelector(t, a, subject, typedTestSelection(false), true)
		waitPass(t, passes)
		if pending, err := a.book.PendingTypedPermissions(); err != nil || len(pending) != 0 {
			t.Fatalf("a request the saved rule grants is still pending: %+v %v", pending, err)
		}
		if _, found, err := a.book.ApplicationRecord(record.ID); err != nil || found {
			t.Fatalf("the withdrawn request is retained: %v %v", found, err)
		}
	})
	t.Run("block", func(t *testing.T) {
		a, subject, passes := settledRuntime(t)
		pendingTyped(t, a, subject, false)
		setSelector(t, a, subject, typedTestSelection(false), false)
		waitPass(t, passes)
		if pending, err := a.book.PendingTypedPermissions(); err != nil || len(pending) != 0 {
			t.Fatalf("a request a Block refuses is still pending: %+v %v", pending, err)
		}
	})
	t.Run("unrelated change", func(t *testing.T) {
		a, subject, passes := settledRuntime(t)
		record := pendingTyped(t, a, subject, false)
		other := typedTestSelection(false)
		other.Selector.Target = rwire.EncodeRequest(&rwire.Request{Op: "named-target", Name: "another saved value"})
		setSelector(t, a, subject, other, true)
		waitPass(t, passes)
		if pending, err := a.book.PendingTypedPermissions(); err != nil || len(pending) != 1 || pending[0].ID != record.ID {
			t.Fatalf("an unrelated rule withdrew the request: %+v %v", pending, err)
		}
	})
	t.Run("answered", func(t *testing.T) {
		a, subject, passes := settledRuntime(t)
		record := pendingTyped(t, a, subject, false)
		if _, _, err := a.book.AnswerTypedPermissionIfPendingAuthorized(record.ID, "refuse", func(asks.Record, string) error { return nil }); err != nil {
			t.Fatal(err)
		}
		setSelector(t, a, subject, typedTestSelection(false), true)
		waitPass(t, passes)
		if _, found, err := a.book.ApplicationRecord(record.ID); err != nil || !found {
			t.Fatalf("an answered request was withdrawn: %v %v", found, err)
		}
	})
}
