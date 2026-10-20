package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// ASK-N6: a person's block on abstraction.asks/notification.post stops one
// program's actions; no rule and a permit block nothing; another program is
// unaffected.
func TestNotificationBlockFollowsTheRightsRule(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install(askswire.ResourceActions, nil); err != nil {
		t.Fatal(err)
	}
	a := &runtimeAsks{rights: r}
	program := filepath.Join(t.TempDir(), "poster.exe")
	other := filepath.Join(t.TempDir(), "other.exe")
	ctx := context.Background()
	if err := a.notificationBlocked(ctx, program); err != nil {
		t.Fatal("no rule is no block", err)
	}
	if err := r.policy.Set(rwire.Subject{Account: r.owner, Program: program}, notificationPostAction, notificationPostResource, false); err != nil {
		t.Fatal(err)
	}
	if err := a.notificationBlocked(ctx, program); !errors.Is(err, asks.ErrNotificationSuppressed) {
		t.Fatal("a block suppresses", err)
	}
	if err := a.notificationBlocked(ctx, other); err != nil {
		t.Fatal("another program is unaffected", err)
	}
	if err := r.policy.Set(rwire.Subject{Account: r.owner, Program: program}, notificationPostAction, notificationPostResource, true); err != nil {
		t.Fatal(err)
	}
	if err := a.notificationBlocked(ctx, program); err != nil {
		t.Fatal("a permit blocks nothing", err)
	}
}
