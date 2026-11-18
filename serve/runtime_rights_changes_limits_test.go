package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// changeGateUnit composes the change gate over the runtime's real policy and
// asks Book, with no peer the Panel's.
func changeGateUnit(t *testing.T, withBook bool) (*rightsChanges, *runtimeRights) {
	t.Helper()
	r := testRights(t, t.TempDir())
	r.operatorDirect = nil
	var a *runtimeAsks
	if withBook {
		a, _ = firstUseBook(t)
		a.rights = r
		a.scope = "asks-owner-program@1:runtime"
	}
	ctx, cancel := context.WithCancel(context.Background())
	var o host.Options
	c := r.configureChanges(ctx, &o, a)
	t.Cleanup(func() { cancel(); c.wait() })
	return c, r
}

// Security review 2026-11-22, LOW: a retire whose Undo could not restore
// every rule (more than sixteen) is refused to a request and to an exempt
// caller; the Panel window still retires it.
func TestLargeRetireIsThePanelsAlone(t *testing.T) {
	c, r := changeGateUnit(t, true)
	action := "example.app/use"
	if err := r.policy.RegisterAction(action); err != nil {
		t.Fatal(err)
	}
	for i := range maxChangeUndoRules + 1 {
		if err := r.policy.Set(rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "app.exe")}, action, fmt.Sprint(i), true); err != nil {
			t.Fatal(err)
		}
	}
	retire := rwire.PolicyChange{Kind: rwire.ChangeKindRetireAction, Action: action}
	if got := c.prevalidate(context.Background(), retire); got != rwire.ChangeRequestOutcomeInvalid {
		t.Fatalf("a large retire would ask the person: %v", got)
	}
	if got := c.applyNow(context.Background(), r.self(), retire, routeExempt).State.Outcome; got != rwire.ChangeRequestOutcomeInvalid {
		t.Fatalf("an exempt caller retired a large action: %v", got)
	}
	if got := c.applyNow(context.Background(), r.self(), retire, routeDirect).State.Outcome; got != rwire.ChangeRequestOutcomeApplied {
		t.Fatalf("the Panel window could not retire: %v", got)
	}
}
