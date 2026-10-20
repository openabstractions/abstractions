package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
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

func TestRuntimeDecisionAdaptersShareEvaluatedSet(t *testing.T) {
	for _, outcome := range rwire.DecisionOutcomeValues() {
		t.Run(outcome.String(), func(t *testing.T) {
			evaluated := outcome == rwire.DecisionOutcomePermitted || outcome == rwire.DecisionOutcomeDenied || outcome == rwire.DecisionOutcomeNotGranted || outcome == rwire.DecisionOutcomeUnknownAction
			authorized := credentialBatchAuthorization(rwire.BatchDecision{Outcome: rwire.BatchCheckOutcomeChecked, Revision: "revision", Outcomes: []rwire.DecisionOutcome{outcome}})
			want := credentials.AuthorizationUnavailable
			if outcome == rwire.DecisionOutcomePermitted {
				want = credentials.Authorized
			} else if evaluated {
				want = credentials.AuthorizationDenied
			}
			if authorized.Outcome != want || authorized.RightsOutcome != outcome.String() {
				t.Fatal("credential mapping", authorized)
			}
			word, err := standingWord("rights:", outcome)
			if (err == nil) != evaluated || (word == "") != (outcome == rwire.DecisionOutcomePermitted || !evaluated) {
				t.Fatal("standing mapping", word, err)
			}
			permit, err := messagePartyPermit(outcome)
			if permit != (outcome == rwire.DecisionOutcomePermitted) || (err == nil) != (evaluated && outcome != rwire.DecisionOutcomeUnknownAction) {
				t.Fatal("message party mapping", permit, err)
			}
			decision := rwire.Decision{Outcome: outcome}
			if evaluated {
				decision.PolicyRevision = "revision"
			}
			err = remoteDecisionError(decision)
			if outcome == rwire.DecisionOutcomePermitted {
				if err != nil {
					t.Fatal("remote permit refused", err)
				}
			} else {
				var refusal *fwire.ServiceError
				want := fwire.ServiceErrorCode("unavailable")
				if evaluated {
					want = fwire.ServiceErrorCode("forbidden")
				}
				if !errors.As(err, &refusal) || refusal.Code != want {
					t.Fatal("remote refusal mapping", err)
				}
			}
		})
	}
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

func (c *countingRechecker) RecheckChecked(context.Context) error { c.calls.Add(1); return nil }

type checkedRecheckFunc func(context.Context) error

func (f checkedRecheckFunc) RecheckChecked(ctx context.Context) error { return f(ctx) }

func TestCheckedStandingCapturedCallbackCannotActAfterClose(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := r.install([]string{inference.ActionComplete}, nil); err != nil {
		t.Fatal(err)
	}
	entered, release := make(chan struct{}), make(chan struct{})
	r.policy.OnChange(func() { close(entered); <-release })
	provider := &countingRechecker{}
	stop := recheckInferenceOnChange(r, provider)
	edit := make(chan error, 1)
	go func() { edit <- r.policy.Set(r.self(), inference.ActionComplete, "host:test", false) }()
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("edit did not capture callbacks")
	}
	stop()
	close(release)
	select {
	case err := <-edit:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("captured callback result: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("edit did not finish")
	}
	if provider.calls.Load() != 0 {
		t.Fatal("captured callback acted after owner close")
	}
}

func TestCheckedStandingCloseJoinsBlockedDecisionAndCapturedCallback(t *testing.T) {
	for _, owner := range []string{"inference", "resource"} {
		t.Run(owner, func(t *testing.T) {
			r := testRights(t, t.TempDir())
			if err := r.install([]string{inference.ActionComplete}, nil); err != nil {
				t.Fatal(err)
			}
			entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			pass := func(ctx context.Context) error {
				close(entered)
				<-ctx.Done()
				close(cancelled)
				<-release
				return ctx.Err()
			}
			var stop func()
			if owner == "inference" {
				stop = recheckInferenceOnChange(r, checkedRecheckFunc(pass))
			} else {
				composed := composeResources("", router.New(), nil, func(error) {})
				defer composed.close()
				composed.path = t.TempDir()
				options := leaseBookOptions(composed, router.New(), r, &standInWake{})
				options.Hold = func(context.Context, *identity.Peer, string, string) error { return nil }
				options.Standing = func(ctx context.Context, _ resourceservice.Subject, _, _ string) error { return pass(ctx) }
				book, err := resourceservice.OpenBook(options)
				if err != nil {
					t.Fatal(err)
				}
				who := resourceservice.Subject{Account: r.owner, Program: standingProgram(t, t.TempDir())}
				if got := book.Acquire(t.Context(), who, &identity.Peer{}, instrument.Card0, 1<<20, 0); got.Lease == nil {
					t.Fatal(got)
				}
				stop = recheckLeasesOnChange(r, book)
			}
			edit := make(chan error, 1)
			go func() { edit <- r.policy.Set(r.self(), inference.ActionComplete, "host:test", false) }()
			select {
			case <-entered:
			case <-time.After(2 * time.Second):
				t.Fatal("pass did not enter")
			}
			closed := make(chan struct{})
			go func() { stop(); close(closed) }()
			select {
			case <-cancelled:
			case <-time.After(2 * time.Second):
				t.Fatal("close did not cancel")
			}
			select {
			case <-closed:
				t.Fatal("close escaped active decision")
			default:
			}
			select {
			case <-edit:
				t.Fatal("edit escaped active decision")
			default:
			}
			close(release)
			select {
			case <-closed:
			case <-time.After(2 * time.Second):
				t.Fatal("close did not join")
			}
			select {
			case err := <-edit:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("edit: %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("edit did not join")
			}
		})
	}
}

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
	r.firstUse = func(rwire.Subject, func() asks.Publisher, string, string) string { asked++; return "" }
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
