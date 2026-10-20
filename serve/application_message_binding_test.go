package main

import (
	"context"
	"errors"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestMessageBindingV3RebindReservationRestartAndSamePrincipal(t *testing.T) {
	ctx := context.Background()
	state := t.TempDir()
	d := registration1459Directory(t, state)
	program := registration1459Program("old-message.exe")
	revision := registration1459Publish(t, d, "receiver", program, "Receiver", "operator", "")
	calls := 0
	d.messageBlock = func(context.Context, string) error { calls++; return nil }
	// Updating the same retained native principal preserves its permissions.
	v, same := registration1459File(t, "receiver", program, "Updated title")
	updated := d.publishApplicationV3(ctx, v, same, "operator", revision)
	if updated.Outcome != wire.ApplicationOutcomeApplied || calls != 0 {
		t.Fatal(updated, calls)
	}
	v, replacement := registration1459File(t, "receiver", registration1459Program("new-message.exe"), "New receiver")
	d.messageBlock = func(_ context.Context, name string) error {
		calls++
		if name != "receiver" || d.v3.entries[name].State != "removing" {
			t.Fatal("unreserved target", d.v3.entries[name])
		}
		return errors.New("lost Rights reply")
	}
	failed := d.publishApplicationV3(ctx, v, replacement, "operator", updated.Revision)
	if failed.Outcome != wire.ApplicationOutcomeUnavailable || calls != 1 {
		t.Fatal(failed, calls)
	}
	d = registration1459Directory(t, state)
	d.messageBlock = func(context.Context, string) error { calls++; return nil }
	wrong, other := registration1459File(t, "receiver", registration1459Program("unrelated.exe"), "Other")
	if got := d.publishApplicationV3(ctx, wrong, other, "operator", updated.Revision); got.Outcome == wire.ApplicationOutcomeApplied || calls != 1 {
		t.Fatal("different retry consumed intent", got, calls)
	}
	final := d.publishApplicationV3(ctx, v, replacement, "operator", updated.Revision)
	if final.Outcome != wire.ApplicationOutcomeApplied || calls != 2 || d.v3.entries["receiver"].State != "enabled" {
		t.Fatal(final, calls)
	}
	// A late retry of the old request cannot replace or invalidate the result.
	if got := d.publishApplicationV3(ctx, v, replacement, "operator", updated.Revision); got.Outcome == wire.ApplicationOutcomeApplied || calls != 2 {
		t.Fatal(got, calls)
	}
}

func TestMessageBindingV3RemoveReservationAndRecreate(t *testing.T) {
	ctx := context.Background()
	d := registration1459Directory(t, t.TempDir())
	revision := registration1459Publish(t, d, "receiver", registration1459Program("old-remove.exe"), "Receiver", "operator", "")
	calls := 0
	d.messageBlock = func(context.Context, string) error { calls++; return errors.New("unavailable") }
	operator := rights.Subject{Account: "account", Program: "operator"}
	if got := d.changeApplicationV3(ctx, operator, "receiver", revision, "remove"); got.Outcome == wire.ApplicationOutcomeApplied || d.v3.entries["receiver"].State != "removing" {
		t.Fatal(got)
	}
	d.messageBlock = func(context.Context, string) error { calls++; return nil }
	if got := d.changeApplicationV3(ctx, operator, "receiver", revision, "remove"); got.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(got)
	}
	if _, exists := d.v3.entries["receiver"]; exists || calls != 2 {
		t.Fatal(exists, calls)
	}
	registration1459Publish(t, d, "receiver", registration1459Program("recreated.exe"), "Recreated", "operator", "")
	if calls != 3 {
		t.Fatal(calls)
	}
}
