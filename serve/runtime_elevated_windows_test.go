//go:build windows

package main

import (
	"errors"
	"sync"
	"testing"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

type elevatedLogs struct {
	mu      sync.Mutex
	records []logging.Record
}

func (l *elevatedLogs) Write(r logging.Record) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.records = append(l.records, r)
	return nil
}

func (l *elevatedLogs) events(event string) int {
	l.mu.Lock()
	defer l.mu.Unlock()
	n := 0
	for _, r := range l.records {
		if r.Attrs["oa_event"] == event {
			n++
		}
	}
	return n
}

// The elevated-callers default (rights RIGHTS-R27, owner 2026-11-11) through
// the runtime's real policy, decision point and asks book, with the peer a
// real named-pipe listener bound and only the elevation classifier injected:
// an elevated caller of this account is permitted with no Book occurrence and
// one Activity line per program per session; a medium-integrity peer of an
// admin still asks; the person's Block still denies; switched off, asking comes
// back; a peer of another account, SYSTEM included, is forbidden.
func TestElevatedCallerIsNotAsked(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install([]string{host.ModelLookupAction}, nil); err != nil {
		t.Fatal(err)
	}
	a, book := firstUseBook(t)
	a.rights = r
	r.firstUse = a.admitFirstUse
	logs := &elevatedLogs{}
	r.useLogs(logs)
	elevated := true
	r.peerElevated = func(*identity.Peer) bool { return elevated }
	peer, _ := applicationV3NativePeer(t)
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || subject.Account != r.owner {
		t.Fatal(subject, r.owner, err)
	}
	questions := func() int {
		t.Helper()
		records, _, err := book.ApplicationHistory()
		if err != nil {
			t.Fatal(err)
		}
		return len(records)
	}
	require := func() string {
		t.Helper()
		err := r.Require(t.Context(), peer, host.ModelLookupAction, "hf")
		var decision *rightsclient.DecisionError
		switch {
		case err == nil:
			return "permitted"
		case errors.As(err, &decision):
			return decision.Outcome
		}
		t.Fatal(err)
		return ""
	}

	for range 2 {
		if got := require(); got != "permitted" {
			t.Fatal("an elevated caller of this account was refused", got)
		}
	}
	if n := questions(); n != 0 {
		t.Fatal("an elevated caller raised a Book occurrence", n)
	}
	if logs.events(elevatedSessionEvent) != 1 || logs.events(elevatedAdmitEvent) != 2 {
		t.Fatal("one Activity line per program and one log line per admission", logs.records)
	}
	if snapshot, err := r.policy.OperatorSnapshot(); err != nil || len(snapshot.Rules) != 0 {
		t.Fatal("the default wrote a rule", snapshot.Rules, err)
	}
	if !slicesHasDefault(t, r, rights.DefaultElevatedCallers, true) {
		t.Fatal("elevated-callers is not on after a fresh install")
	}

	// The person's own Don't allow decides first.
	if err := r.policy.Set(subject, host.ModelLookupAction, "hf", false); err != nil {
		t.Fatal(err)
	}
	if got := require(); got != "denied" {
		t.Fatal("an explicit Block lost to the elevated default", got)
	}
	if err := r.policy.Revoke(subject, host.ModelLookupAction, "hf"); err != nil {
		t.Fatal(err)
	}

	// An admin's ordinary medium-integrity process is not elevated: it asks.
	elevated = false
	if got := require(); got != "not_granted" || questions() != 1 {
		t.Fatal("a medium-integrity peer was not asked", got, questions())
	}
	elevated = true

	// Another account, SYSTEM and an over-the-shoulder administrator
	// included, is forbidden at the receiving boundary, elevated or not, and
	// the elevated default never covers it.
	admitted := logs.events(elevatedAdmitEvent)
	elevatedCtx := rights.WithBoundPeer(t.Context(), peer)
	for _, account := range []string{"S-1-5-18", "S-1-5-21-1-2-3-500"} {
		other := rwire.Subject{Account: account, Program: subject.Program}
		if got := r.CheckLegacyOperationContext(elevatedCtx, other, rwire.LegacyOperationRequest{LegacyAction: host.ModelLookupAction, LegacyResource: "hf"}).Outcome; got != rwire.DecisionOutcomeForbidden {
			t.Fatal("another account's peer was not forbidden", account, got)
		}
		if got := r.CheckBatchContext(elevatedCtx, other, rwire.CheckBatchRequest{}).Outcome; got != rwire.BatchCheckOutcomeForbidden {
			t.Fatal("another account's typed check was not forbidden", account, got)
		}
		if got := r.decideTypedAsking(elevatedCtx, other, rwire.ConcreteResource{}).Outcome; got != rwire.BatchCheckOutcomeForbidden {
			t.Fatal("another account's typed decision was not forbidden", account, got)
		}
		if got := r.decide(elevatedCtx, other, host.ModelLookupAction, "hf").Outcome; got == rwire.DecisionOutcomePermitted {
			t.Fatal("the elevated default covered another account", account)
		}
	}
	if logs.events(elevatedAdmitEvent) != admitted {
		t.Fatal("another account was admitted")
	}

	// Switched off, asking comes back.
	revision, _, err := r.policy.DefaultsSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if edit, err := r.policy.SetDefault(revision, rights.DefaultElevatedCallers, false, r.self(), func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	if got := require(); got != "not_granted" {
		t.Fatal("switched off, an elevated caller was not asked", got)
	}
	if logs.events(elevatedAdmitEvent) != 2 {
		t.Fatal("a refused decision was logged as admitted", logs.records)
	}
}

func slicesHasDefault(t *testing.T, r *runtimeRights, name string, on bool) bool {
	t.Helper()
	_, defaults, err := r.policy.DefaultsSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range defaults {
		if d.Name == name {
			return d.On == on && d.EveryResource && d.Action == rights.EveryActionName
		}
	}
	return false
}
