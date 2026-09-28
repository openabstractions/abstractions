package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-resource/go/abstraction/resource"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	router "github.com/openabstractions/abstraction-router/go"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The resource table is who holds a scarce resource on this machine. The
// runtime composes it from three sources and adds nothing of its own: the
// platform instrument, which measures a process's hold and attributes it to a
// program and an account; the host adapters' own model lists, which are the
// engines' claims that a model is resident and carry no amount
// (CONTRACT.md RES-T3, research/resources/MEASUREMENT-2026-09-22.md); and the
// lease book, whose live leases are rows of the resource they were granted on.

// claimedHostPrefix names a claimed row's holder when the holder is an
// attached HTTP engine. A host declaration carries a base URL and no image, so
// there is no absolute path and no account to give it, and CONTRACT.md RES-T1
// forbids inventing one.
const claimedHostPrefix = "host:"

// unloadableHosts are the local engines this release can yield on behalf of,
// through each host's own mechanism (abstraction-router/go/unload.go). The set
// is fixed because the rules that say whether a host may be unloaded are
// written by installation, and a rule can only be written for a host that has
// a name before any survey runs.
var unloadableHosts = []string{"lmstudio", "ollama", "comfyui"}

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
			if !h.Up {
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
}

func (r *runtimeResources) close() {
	if r != nil {
		//unchecked: shutdown path with no caller to report a close failure to
		r.audit.Close()
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
	// left standing on the router's last survey (CONTRACT.md RES-T3). Ten
	// live surveys of LM Studio on this machine measured 3.1-16.6ms each,
	// far inside the table's own 2s age bound and the 30s per-call budget
	// (research/agent-history.md, RES-T3 measurement 2026-09-22).
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
	book, err := resourceservice.OpenBook(resourceservice.BookOptions{
		Table:    composed.table,
		Attached: attachedHosts(r),
		Audit:    composed.audit,
		Path:     filepath.Join(composed.path, leaseBookFile),
		Hold:     holdPolicy(rights),
		Yield:    yieldPolicy(rights),
	})
	if err != nil {
		report(fmt.Errorf("runtime resources: %w", err))
		return composed
	}
	composed.book = book
	return composed
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

// yieldPolicy decides ActionYield for this runtime's own program on
// host:<name> before an attached holder is yielded on its behalf. The holder
// proves nothing to this service and answers no lease call, so the rule that
// says whether its models may be unloaded is a rule about that host, held by
// the program that would do the unloading (RES-L3).
func yieldPolicy(rights *runtimeRights) resourceservice.HolderPolicy {
	return func(ctx context.Context, holder string) error {
		if rights == nil {
			return resourceservice.ErrPolicyUnavailable
		}
		decision := rights.decide(ctx, rights.self(), resourceservice.ActionYield, holder)
		switch decision.Outcome {
		case rightswire.DecisionOutcomePermitted:
			return nil
		case rightswire.DecisionOutcomeDenied, rightswire.DecisionOutcomeNotGranted:
			return errors.New("no rule permits yielding " + holder)
		}
		return fmt.Errorf("%w: %s", resourceservice.ErrPolicyUnavailable, decision.Outcome)
	}
}

// holdRefusal separates an evaluated refusal from a decision that was never
// obtained. Only the second reads unavailable; an unknown action and a policy
// that would not read are both decisions nobody made.
func holdRefusal(err error) error {
	var decision *rightsclient.DecisionError
	if errors.As(err, &decision) {
		switch decision.Outcome {
		case "denied", "not_granted", "forbidden":
			return err
		}
	}
	return fmt.Errorf("%w: %v", resourceservice.ErrPolicyUnavailable, err)
}

// attachedHosts are the machine's own engines as attached holders. Each one
// resolves its live router host when it is asked, so a host declared after the
// book was opened is still reachable and a host that is gone is simply not
// holding anything.
func attachedHosts(r *router.Router) []resourceservice.Attached {
	var out []resourceservice.Attached
	for _, name := range unloadableHosts {
		out = append(out, attachedHost{name: name, router: r})
	}
	return out
}

// attachedHost is one local engine, yielded through its own API.
type attachedHost struct {
	name   string
	router *router.Router
}

func (a attachedHost) Holder() string   { return claimedHostPrefix + a.name }
func (a attachedHost) Resource() string { return instrument.Card0 }

// Holding is what this host says it holds now, read fresh: the ordering and
// the decision to ask at all both depend on it, and a survey from a minute ago
// would have the service unload a host that let go by itself.
func (a attachedHost) Holding(context.Context) ([]string, error) {
	states, _, _, _, _ := a.router.Residency(true)
	for _, state := range states {
		if state.Host != a.name {
			continue
		}
		if !state.Up {
			return nil, errors.New("router: " + a.name + " is not answering")
		}
		return state.Resident, nil
	}
	return nil, errors.New("router: no host called " + a.name)
}

// Yield asks the host's own API to let go, through the router's adapter for
// it. What it freed is read from the instrument afterwards, never from this
// answer.
func (a attachedHost) Yield(ctx context.Context) error {
	for _, h := range a.router.Hosts() {
		if h.Name == a.name {
			return h.Unload(ctx)
		}
	}
	return errors.New("router: no host called " + a.name)
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

// giB is bytes as the two decimals the router's residency answer reports.
func giB(bytes int64) float64 {
	return float64(int64(float64(bytes)/(1<<30)*100+0.5)) / 100
}

// resourceTableEndpoint follows the runtime's endpoint selection for table@1
// and leases@1, which share one endpoint.
func resourceTableEndpoint(options runtimeFlags) (string, error) {
	switch {
	case options.isolated != "":
		return bootstrap.Endpoint(options.isolated + "-resource-table")
	case options.endpoint != "":
		return options.endpoint + "-resource-table", nil
	}
	return options.defaultEndpoint("resource-table-v1")
}

// resourceRules are the exact rules installation writes for each operator
// program: holding the card and the wake, and yielding each engine this
// release can yield on behalf of. A host with no rule is never unloaded, so a
// rule written once is what makes arbitration work at all; a person who does
// not want an engine touched denies the one rule that names it.
func resourceRules() []installationRule {
	rules := []installationRule{
		{resourceservice.ActionHold, instrument.Card0},
		{resourceservice.ActionHold, resourceservice.ResourceAwake},
	}
	for _, name := range unloadableHosts {
		rules = append(rules, installationRule{resourceservice.ActionYield, claimedHostPrefix + name})
	}
	// The model host holds a lease and answers its own yield requests through
	// leases@1 Observe and Answer, which the book decides no rule for. This is
	// the rule RES-L3 names for that host all the same: the person's switch
	// for whether this runtime may make it let go, read the day the host is
	// asked back as an attached holder rather than as a lease holder.
	return append(rules, installationRule{resourceservice.ActionYield, claimedHostPrefix + modelHostName})
}
