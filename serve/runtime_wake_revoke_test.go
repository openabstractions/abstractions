package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// Revoking a program's "may hold awake" grant ends its wake leases and the
// platform wake request they kept, before the revoke returns; an expired
// grant ends them at the resource host's next clock; the program's next
// acquire is decided again (resource CONTRACT.md RES-A1, rights RIGHTS-R13,
// owner rule 2026-10-02 on per-application wake revoke).
func TestRevokingWakeEndsTheProgramsKeepAwakeHold(t *testing.T) {
	dir := t.TempDir()
	program := filepath.Join(dir, "app.exe")
	if err := os.WriteFile(program, []byte("stand-in"), 0o600); err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "decisions.json"),
		[]string{resourceservice.ActionHold})
	if err != nil {
		t.Fatal(err)
	}
	var clockMu sync.Mutex
	now := time.Now().UTC()
	policy.Clock = func() time.Time { clockMu.Lock(); defer clockMu.Unlock(); return now }
	r := &runtimeRights{policy: policy, owner: "S-1-5-21-7-1001"}
	subject := rightswire.Subject{Account: r.owner, Program: program}
	who := resourceservice.Subject{Account: subject.Account, Program: subject.Program}

	composed := composeResources("", router.New(), nil, func(error) {})
	t.Cleanup(composed.close)
	composed.path = t.TempDir()
	wake := &standInWake{}
	options := leaseBookOptions(composed, router.New(), r, wake)
	if options.Standing == nil {
		t.Fatal("the runtime's lease book rechecks no wake lease")
	}
	// The test's peers carry no identity: Hold decides for the subject the
	// test acquires as, through the same in-process policy.
	options.Hold = func(ctx context.Context, _ *identity.Peer, action, resource string) error {
		if resource == instrument.Card0 {
			return nil
		}
		if d := r.decide(ctx, subject, action, resource); d.Outcome != rightswire.DecisionOutcomePermitted {
			return errors.New(d.Outcome.String())
		}
		return nil
	}
	book, err := resourceservice.OpenBook(options)
	if err != nil {
		t.Fatal(err)
	}
	recheckLeasesOnChange(r, book)

	hold := func() string {
		t.Helper()
		if card := book.Acquire(context.Background(), who, &identity.Peer{}, instrument.Card0, 1<<20, 0); card.Lease == nil {
			t.Fatalf("card lease read %s", card.Outcome)
		}
		lease := book.Acquire(context.Background(), who, &identity.Peer{}, resourceservice.ResourceAwake, 0, 0)
		if lease.Lease == nil {
			t.Fatalf("wake lease read %s", lease.Outcome)
		}
		return lease.Lease.ID
	}
	wakeLeases := func() int {
		n := 0
		for _, h := range book.AwakeHolds() {
			if h.Program == who.Program {
				n++
			}
		}
		return n
	}

	// The card lease beside the wake lease is decided again too (RES-L7);
	// its own rule stands throughout.
	if err := policy.Set(subject, resourceservice.ActionHold, instrument.Card0, true); err != nil {
		t.Fatal(err)
	}
	// Revoke: the person's Off in the Panel or `rights revoke`.
	if err := policy.Set(subject, resourceservice.ActionHold, resourceservice.ResourceAwake, true); err != nil {
		t.Fatal(err)
	}
	id := hold()
	if taken, _ := wake.counts(); taken != 1 || wakeLeases() != 1 {
		t.Fatalf("granted wake: %d platform requests, %d leases", taken, wakeLeases())
	}
	if err := policy.Revoke(subject, resourceservice.ActionHold, resourceservice.ResourceAwake); err != nil {
		t.Fatal(err)
	}
	if n := wakeLeases(); n != 0 {
		t.Fatalf("the revoke left %d wake leases", n)
	}
	if taken, released := wake.counts(); taken != 1 || released != 1 {
		t.Fatalf("after the revoke: %d platform requests taken, %d released", taken, released)
	}
	if book.Renew(who, id).Applied {
		t.Fatal("the revoked wake lease renewed")
	}
	if again := book.Acquire(context.Background(), who, &identity.Peer{}, resourceservice.ResourceAwake, 0, 0); again.Outcome != rwire.AcquireOutcomeNotPermitted {
		t.Fatalf("the next wake acquire read %s", again.Outcome)
	}

	// Expiry: a grant for a while, then its end with no edit at all.
	snapshot, err := policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	permit := true
	timed, err := policy.ChangeRule(snapshot.Revision, subject, resourceservice.ActionHold, resourceservice.ResourceAwake,
		rights.RuleEdit{Permit: &permit, By: subject, TTL: time.Minute}, func() error { return nil })
	if err != nil || timed.Outcome != rightswire.PolicyEditOutcomeApplied {
		t.Fatalf("timed grant: %+v %v", timed, err)
	}
	hold()
	if wakeLeases() != 1 {
		t.Fatal("no wake lease under the timed grant")
	}
	clockMu.Lock()
	now = now.Add(2 * time.Minute)
	clockMu.Unlock()
	book.Recheck(context.Background()) // the resource host's clock
	if n := wakeLeases(); n != 0 {
		t.Fatalf("the expired grant left %d wake leases", n)
	}
	if taken, released := wake.counts(); taken != 2 || released != 2 {
		t.Fatalf("after the expiry: %d platform requests taken, %d released", taken, released)
	}
}
