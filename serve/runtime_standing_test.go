package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	resourcewire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// standingProgram is a stand-in program file under dir, as a rule names it.
func standingProgram(t *testing.T, dir string) string {
	t.Helper()
	program := filepath.Join(dir, "app.exe")
	if err := os.WriteFile(program, []byte("stand-in"), 0o600); err != nil {
		t.Fatal(err)
	}
	return program
}

// RES-L7, RIGHTS-R23: blocking a program's hold on a card ends its card lease
// before the Block returns, through the runtime's rights change hook, and the
// lease's next renewal is refused. Removing the Block lets the next acquire
// through again; a removed rule alone (no default permits hold) refuses the
// next renewal as well.
func TestBlockingCardHoldEndsTheProgramsCardLease(t *testing.T) {
	dir := t.TempDir()
	r := testRights(t, dir)
	if err := r.install([]string{resourceservice.ActionHold}, nil); err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: r.owner, Program: standingProgram(t, dir)}
	who := resourceservice.Subject{Account: subject.Account, Program: subject.Program}

	composed := composeResources("", router.New(), nil, func(error) {})
	t.Cleanup(composed.close)
	composed.path = t.TempDir()
	options := leaseBookOptions(composed, router.New(), r, &standInWake{})
	// The test's peers carry no identity: Hold decides for the subject the
	// test acquires as, through the same in-process policy.
	options.Hold = func(ctx context.Context, _ *identity.Peer, action, resource string) error {
		if d := r.decide(ctx, subject, action, resource); d.Outcome != rwire.DecisionOutcomePermitted {
			return errors.New(d.Outcome.String())
		}
		return nil
	}
	book, err := resourceservice.OpenBook(options)
	if err != nil {
		t.Fatal(err)
	}
	recheckLeasesOnChange(r, book)
	cardLeases := func() int {
		n := 0
		for _, lease := range book.Leases() {
			if lease.Resource == instrument.Card0 {
				n++
			}
		}
		return n
	}

	if err := r.policy.Set(subject, resourceservice.ActionHold, instrument.Card0, true); err != nil {
		t.Fatal(err)
	}
	acquired := book.Acquire(context.Background(), who, &identity.Peer{}, instrument.Card0, 1<<20, 0)
	if acquired.Lease == nil {
		t.Fatalf("card lease read %s", acquired.Outcome)
	}
	if !book.Renew(who, acquired.Lease.ID).Applied {
		t.Fatal("a permitted card lease did not renew")
	}
	// Block: the person's Don't allow in the Panel.
	if err := r.policy.Set(subject, resourceservice.ActionHold, instrument.Card0, false); err != nil {
		t.Fatal(err)
	}
	if n := cardLeases(); n != 0 {
		t.Fatalf("the Block left %d card leases", n)
	}
	if book.Renew(who, acquired.Lease.ID).Applied {
		t.Fatal("the blocked card lease renewed")
	}
	if again := book.Acquire(context.Background(), who, &identity.Peer{}, instrument.Card0, 1<<20, 0); again.Outcome != resourcewire.AcquireOutcomeNotPermitted {
		t.Fatalf("the next card acquire read %s", again.Outcome)
	}

	// A revoke that removes the permit, with no change hook in between: the
	// next renewal decides again by itself.
	if err := r.policy.Set(subject, resourceservice.ActionHold, instrument.Card0, true); err != nil {
		t.Fatal(err)
	}
	second := book.Acquire(context.Background(), who, &identity.Peer{}, instrument.Card0, 1<<20, 0)
	if second.Lease == nil {
		t.Fatalf("card lease after the permit read %s", second.Outcome)
	}
	if err := r.policy.Revoke(subject, resourceservice.ActionHold, instrument.Card0); err != nil {
		t.Fatal(err)
	}
	if book.Renew(who, second.Lease.ID).Applied {
		t.Fatal("a card lease renewed after its permit was removed")
	}
}

// countingRechecker records each Recheck the rights change hook makes.
type countingRechecker struct{ calls atomic.Int64 }

func (c *countingRechecker) Recheck(context.Context) { c.calls.Add(1) }

// INF-A6, RIGHTS-R23: the runtime's inference standing decides complete on
// the server and use of the credential the runtime applied, asking nobody, so
// a Block of either refuses a running operation; every committed rights change
// rechecks the provider until the hook is removed.
func TestInferenceStandingDecidesCompleteAndCredentialAgain(t *testing.T) {
	dir := t.TempDir()
	r := testRights(t, dir)
	if err := r.install([]string{inference.ActionComplete, credentials.ActionApply}, nil); err != nil {
		t.Fatal(err)
	}
	// The runtime's catalogue with the credentials receiver present, as
	// composition sets it, so use is decided as Apply decides it.
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{Credentials: true})
	if err != nil {
		t.Fatal(err)
	}
	r.policy.Definitions = catalog
	asked := 0
	r.firstUse = func(rwire.Subject, func() asks.Publisher, string, string) { asked++ }
	c := &runtimeCredentials{runtimeRights: r}
	program := standingProgram(t, dir)
	subject := rwire.Subject{Account: r.owner, Program: program}
	s := inference.Subject{Account: subject.Account, Program: subject.Program}
	provider := &countingRechecker{}
	unhook := recheckInferenceOnChange(r, provider)

	standing := func(credential string) string {
		t.Helper()
		refusal, err := c.standingInference(context.Background(), s, "openrouter", credential)
		if err != nil {
			t.Fatalf("standing: %v", err)
		}
		return refusal
	}
	if got := standing(""); got != "rights:not_granted" {
		t.Fatalf("with no rule the standing read %q", got)
	}
	if err := r.policy.Set(subject, inference.ActionComplete, inference.ResourceHost("openrouter"), true); err != nil {
		t.Fatal(err)
	}
	if err := r.policy.Set(subject, credentials.ActionApply, credentials.ResourceFor("openrouter"), true); err != nil {
		t.Fatal(err)
	}
	if got := standing("openrouter"); got != "" {
		t.Fatalf("with both rules the standing read %q", got)
	}
	if err := r.policy.Set(subject, credentials.ActionApply, credentials.ResourceFor("openrouter"), false); err != nil {
		t.Fatal(err)
	}
	if got := standing("openrouter"); got != "credential:not_permitted" {
		t.Fatalf("after the credential Block the standing read %q", got)
	}
	if got := standing(""); got != "" {
		t.Fatalf("an operation that applied no credential read %q", got)
	}
	if err := r.policy.Set(subject, inference.ActionComplete, inference.ResourceHost("openrouter"), false); err != nil {
		t.Fatal(err)
	}
	if got := standing(""); got != "rights:denied" {
		t.Fatalf("after the complete Block the standing read %q", got)
	}
	if asked != 0 {
		t.Fatalf("the standing decision asked %d questions", asked)
	}
	if n := provider.calls.Load(); n != 4 {
		t.Fatalf("four committed changes rechecked the provider %d times", n)
	}
	unhook()
	if err := r.policy.Revoke(subject, inference.ActionComplete, inference.ResourceHost("openrouter")); err != nil {
		t.Fatal(err)
	}
	if n := provider.calls.Load(); n != 4 {
		t.Fatalf("a removed hook still rechecked: %d calls", n)
	}
}
