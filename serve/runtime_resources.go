package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	identity "github.com/openabstractions/abstraction-identity"
	job "github.com/openabstractions/abstraction-job/go"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
)

// runtimeDownloadWakeEnforcer designates this runtime process, which retains
// authenticated job receipts. Another process running the same image gains no
// designation. The original subject still needs its existing awake rule.
func runtimeDownloadWakeEnforcer(ctx context.Context, peer *identity.Peer, action, resource string) bool {
	if ctx.Err() != nil || action != resourceservice.ActionHold || resource != resourceservice.ResourceAwake {
		return false
	}
	caller, err := identity.SubjectOf(peer, resourceservice.Bound)
	if err != nil {
		return false
	}
	me, err := identity.Self()
	if err != nil {
		return false
	}
	account, err := me.Account()
	if err != nil || caller.Account != account {
		return false
	}
	process, err := peer.Process.AtLeast(resourceservice.Bound.Process)
	return err == nil && !process.Recycled && process.PID == os.Getpid()
}

// The resource table is who holds a scarce resource on this machine. The
// runtime composes it from three sources and adds nothing of its own: the
// platform instrument, which measures a process's hold and attributes it to a
// program and an account; the host adapters' own model lists, which are the
// engines' claims that a model is resident and carry no amount
// (CONTRACT.md RES-T3, research/resources/MEASUREMENT-2026-09-22.md); and the
// lease book, whose live leases are rows of the resource they were granted on.

// claimedHostPrefix names a claimed row's holder when the holder is an HTTP
// engine the router reads. A host declaration carries a base URL and no
// image, so there is no absolute path and no account to give it, and
// CONTRACT.md RES-T1 forbids inventing one.
const claimedHostPrefix = "host:"

// resourceFiles are the lease book and its audit inside the runtime's state
// directory.
const (
	leaseBookFile  = "leases.json"
	leaseAuditFile = "audit.jsonl"
)

// hostClaims is each host adapter's resident model list as claims of card:0.
// hosts is the router's last survey, read and never provoked: the router's own
// residency read goes the other way, through tableResidency, and a claims
// callback that surveyed would make the two a loop. A host that is down claims
// nothing; its last model list is not evidence that it still holds anything.
func hostClaims(hosts func() []router.HostState) resourceservice.Claims {
	return func() []resourceservice.Claim {
		var out []resourceservice.Claim
		for _, h := range hosts() {
			// A native provider holds a lease of its own for each resident
			// (MODELHOST-H1); a claim beside it would list it twice.
			if !h.Up || h.Wire == router.WireNative {
				continue
			}
			for _, name := range h.Resident {
				out = append(out, resourceservice.Claim{Resource: instrument.Card0,
					Program: claimedHostPrefix + h.Host, Detail: name})
			}
		}
		return out
	}
}

// runtimeResources is the table this runtime serves and the lease book beside
// it. The book is nil when the runtime has no state directory to keep leases
// in: a lease that does not survive a restart is not a lease (RES-L4), and a
// runtime without durable state serves the read-only table alone.
type runtimeResources struct {
	table *resourceservice.Table
	book  *resourceservice.Book
	audit *resourceservice.Audit
	path  string
	// unhookStanding removes the rights change hook that rechecks the
	// book's leases (recheckLeasesOnChange).
	unhookStanding func()
}

func (r *runtimeResources) close() {
	if r != nil {
		r.stopStanding()
		//unchecked: shutdown path with no caller to report a close failure to
		r.audit.Close()
	}
}

func (r *runtimeResources) stopStanding() {
	if r != nil && r.unhookStanding != nil {
		r.unhookStanding()
	}
}

// composeResources builds the table this runtime serves over this platform's
// instrument, and the lease book that grants and asks back what the table
// reports. The book's claims go into the table, so one reader sees the grants
// and the holds together.
func composeResources(state string, r *router.Router, rights *runtimeRights, report func(error)) *runtimeResources {
	hosts := func() []router.HostState {
		states, _, _, _, _ := r.Residency(false)
		return states
	}
	composed := &runtimeResources{}
	fromBook := func() []resourceservice.Claim {
		if composed.book == nil {
			return nil
		}
		return composed.book.Claims()()
	}
	composed.table = resourceservice.NewTable(instrument.Detect(),
		resourceservice.JoinClaims(hostClaims(hosts), fromBook), 0)
	// A fresh read re-surveys the hosts hostClaims reads from, so a claimed
	// host: row a yield already cleared is gone from a fresh read and not
	// left standing on the router's last survey (CONTRACT.md RES-T3).
	// On 2026-09-22, ten LM Studio Router.Survey calls after one warm-up,
	// with no model loaded, measured 3.1-16.6ms each (commit
	// 15f1dc984736b574a034e4da09c5d4d2d308e41f; see
	// openabstractions-flat/abstraction-resource/HISTORY.md, 2026-09-23 F5).
	// These measurements describe that historical no-model survey.
	composed.table.SetRefresh(r.Survey)
	if state == "" {
		return composed
	}
	composed.path = filepath.Join(state, "resources")
	audit, err := resourceservice.OpenAudit(filepath.Join(composed.path, leaseAuditFile))
	if err != nil {
		report(fmt.Errorf("runtime resources: %w", err))
	} else {
		audit.OnError = func(err error) { report(fmt.Errorf("runtime resources: audit: %w", err)) }
		composed.audit = audit
	}
	book, err := resourceservice.OpenBook(leaseBookOptions(composed, r, rights, job.PlatformGPU, job.Platform))
	if err != nil {
		report(fmt.Errorf("runtime resources: %w", err))
		return composed
	}
	composed.book = book
	composed.unhookStanding = recheckLeasesOnChange(rights, book)
	return composed
}

// leaseBookOptions is the lease book composeResources opens: the table, the
// audit and book files under the runtime's state, the
// rights decisions, and wake, the platform's idle-sleep inhibitor an
// acquired wake lease keeps (resource CONTRACT.md RES-A1). GPU work alone
// takes the wake lease (RES-G5), so the runtime passes job.PlatformGPU,
// which also keeps the display on: on a Modern Standby machine the display
// turning off is the standby entry. Tests pass a stand-in.
func leaseBookOptions(composed *runtimeResources, r *router.Router, rights *runtimeRights, wake job.Holder, system ...job.Holder) resourceservice.BookOptions {
	systemWake := wake
	if len(system) > 0 {
		systemWake = system[0]
	}
	return resourceservice.BookOptions{
		Table:           composed.table,
		Audit:           composed.audit,
		Path:            filepath.Join(composed.path, leaseBookFile),
		Hold:            holdPolicy(rights),
		Yield:           yieldPolicy(rights),
		KeepAwake:       keepAwake(wake),
		KeepSystemAwake: keepAwake(systemWake),
		Standing:        standingPolicy(rights),
	}
}

// standingPolicy decides ActionHold again for a live lease's holder by the
// subject the lease names, so the book ends the card and wake leases of a
// program whose grant was revoked, blocked or expired (resource CONTRACT.md
// RES-L7, RES-A1). It asks nobody: the holder's next acquire is what asks
// again.
func standingPolicy(rights *runtimeRights) resourceservice.SubjectPolicy {
	return func(ctx context.Context, who resourceservice.Subject, action, resource string) error {
		if rights == nil {
			return resourceservice.ErrPolicyUnavailable
		}
		decision := rights.decide(ctx, rightswire.Subject{Account: who.Account, Program: who.Program}, action, resource)
		return resourceStandingRefusal(decision.Outcome, who, resource)
	}
}

func resourceStandingRefusal(outcome rightswire.DecisionOutcome, who resourceservice.Subject, resource string) error {
	if outcome == rightswire.DecisionOutcomePermitted {
		return nil
	}
	if rightsclient.Evaluated(outcome) {
		return errors.New("no rule permits " + who.Program + " to hold " + resource)
	}
	return fmt.Errorf("%w: %s", resourceservice.ErrPolicyUnavailable, outcome)
}

// recheckLeasesOnChange has every committed rights change end the leases it
// revoked before the edit replies, so the person's revoke in the Panel or on
// the command line ends a card or wake hold at once (resource RES-L7, RES-A1;
// rights RIGHTS-R23). An expiry commits nothing; the resource host's clock
// ends it. The returned function removes the hook.
func recheckLeasesOnChange(rights *runtimeRights, book *resourceservice.Book) func() {
	if rights == nil || rights.policy == nil || book == nil {
		return func() {}
	}
	return recheckRightsOnChange(rights, book.RecheckChecked)
}

// keepAwake adapts a job.Holder to the lease book's KeepAwake: one platform
// request, released by the book when the last acquired wake lease ends.
func keepAwake(holder job.Holder) func(why string) (func(), error) {
	return func(why string) (func(), error) {
		held, err := holder.Hold("openabstractions", why)
		if err != nil {
			return nil, err
		}
		return func() {
			//unchecked: the platform holder's Release reports nothing; the request ends with the call
			held.Release()
		}, nil
	}
}

// holdPolicy decides ActionHold for the asking caller on the resource it asked
// for (CONTRACT.md RES-L1). A runtime with no policy decides nothing, and
// every Acquire reads unavailable: an absent policy is never permission.
func holdPolicy(rights *runtimeRights) resourceservice.Policy {
	return func(ctx context.Context, peer *identity.Peer, action, resource string) error {
		if rights == nil {
			return resourceservice.ErrPolicyUnavailable
		}
		if err := rights.Require(ctx, peer, action, resource); err != nil {
			return holdRefusal(err)
		}
		return nil
	}
}

// yieldPolicy decides ActionYield for this runtime's own program on a lease
// holder's program, the person's switch for whether that app's leases may be
// asked back (RES-L3). The rule is about that app, held by the program that
// would do the asking. It is on by default: only the person's deny keeps the
// app's memory, and no rule, an unknown action or a decision that cannot be
// read asks the app as before.
func yieldPolicy(rights *runtimeRights) resourceservice.HolderPolicy {
	return func(ctx context.Context, program string) error {
		if rights == nil {
			return resourceservice.ErrPolicyUnavailable
		}
		decision := rights.decide(ctx, rights.self(), resourceservice.ActionYield, program)
		return yieldRefusal(decision.Outcome, program)
	}
}

func yieldRefusal(outcome rightswire.DecisionOutcome, program string) error {
	switch {
	case outcome == rightswire.DecisionOutcomeDenied:
		return errors.New("the person keeps " + program + "'s memory")
	case rightsclient.Evaluated(outcome):
		return nil
	}
	return fmt.Errorf("%w: %s", resourceservice.ErrPolicyUnavailable, outcome)
}

// holdRefusal separates an evaluated refusal from a decision that was never
// obtained. Only the second reads unavailable; Rights owns the evaluated set.
func holdRefusal(err error) error {
	var decision *rightsclient.DecisionError
	if errors.As(err, &decision) && rightsclient.Evaluated(decision.DecisionOutcome()) {
		return err
	}
	return fmt.Errorf("%w: %v", resourceservice.ErrPolicyUnavailable, err)
}

// tableResidency is how the router learns who holds the card: the table's
// verified rows for card:0, in the GiB the router reports. A claimed row is
// left out, because the router already carries what each host says it holds
// as that host's resident model list.
type tableResidency struct{ table *resourceservice.Table }

func (t tableResidency) Holders(fresh bool) ([]router.Holder, error) {
	state, err := t.table.Holders(instrument.Card0, fresh)
	if err != nil {
		return nil, err
	}
	var out []router.Holder
	for _, row := range state.Holders {
		if row.Evidence != rwire.EvidenceVerified {
			continue
		}
		out = append(out, router.Holder{Program: row.Program, Account: row.Account, GiB: giB(row.Amount)})
	}
	return out, nil
}

// holds reports whether program holds resource under a verified lease, a
// loaded engine; a table that cannot answer counts as held, so a caller that
// would restart the holder waits.
func (r *runtimeResources) holds(program, resource string) bool {
	state, err := r.table.Holders(resource, false)
	if err != nil {
		return true
	}
	for _, row := range state.Holders {
		if row.Evidence == rwire.EvidenceVerified && samePrograms(row.Program, program) {
			return true
		}
	}
	return false
}

// giB is bytes as the two decimals the router's residency answer reports.
func giB(bytes int64) float64 {
	return float64(int64(float64(bytes)/(1<<30)*100+0.5)) / 100
}

// resourceTableEndpoint follows the runtime's endpoint selection for table@1
// and leases@1, which share one endpoint.
func resourceTableEndpoint(options runtimeFlags) (string, error) {
	return options.companionEndpoint("resource-table-v1", "resource-table", "resource-table")
}

// resourceRules are the exact rules installation writes for each operator
// program: holding the card and the wake. Asking a lease back needs no
// installed rule: the yield switch is on by default, and a person who wants
// one app left alone denies ActionYield on that app's program (RES-L3).
func resourceRules() []installationRule {
	return []installationRule{
		{resourceservice.ActionHold, instrument.Card0},
		{resourceservice.ActionHold, resourceservice.ResourceAwake},
	}
}
