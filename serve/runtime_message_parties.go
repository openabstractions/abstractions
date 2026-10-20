package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"slices"
	"sort"

	asks "github.com/openabstractions/abstraction-asks/go"
	cas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

type messagePartyAgent struct{ name, program string }
type messagePartyInput struct {
	path  string
	limit int64
	raw   []byte
}

// These immutable facts belong to applicationRoles, never a second store.
// Inputs are the bounded files the registration owner already admitted.
type messagePartyFacts struct {
	owner  string
	agents []messagePartyAgent
	inputs []messagePartyInput
}

// Equality here preserves an identical immutable fact projection. Program
// identity is still decided by the shared matcher during authorization.
func (f *messagePartyFacts) same(other *messagePartyFacts) bool {
	if other == nil || f.owner != other.owner || !slices.Equal(f.agents, other.agents) || len(f.inputs) != len(other.inputs) {
		return false
	}
	for i, input := range f.inputs {
		if (input.raw == nil) != (other.inputs[i].raw == nil) || !bytes.Equal(input.raw, other.inputs[i].raw) {
			return false
		}
	}
	return true
}

func (f *messagePartyFacts) current(ctx context.Context) error {
	if f == nil || len(f.inputs) < 3 || len(f.inputs) > 67 || len(f.agents) > 64 {
		return closedError("asks: messaging facts unavailable")
	}
	for _, input := range f.inputs {
		raw, err := cas.ReadLimitContext(ctx, input.path, input.limit)
		if err != nil {
			return err
		}
		if (raw == nil) != (input.raw == nil) || !bytes.Equal(raw, input.raw) {
			return closedError("asks: messaging facts changed")
		}
	}
	return ctx.Err()
}

// currentMessagingFacts returns the published messaging facts, preparing them
// again when a registration write or a concurrent republication has cleared
// them. Call it outside Book, directory and policy locks. A receiver's
// enrollment is recorded, so neither party needs to be running.
func (d *applicationDirectory) currentMessagingFacts(ctx context.Context) (*messagePartyFacts, error) {
	if roles := d.roles.Load(); roles != nil && roles.messaging != nil {
		return roles.messaging, nil
	}
	facts, err := d.refreshMessagePartyFacts(ctx)
	if err != nil {
		return nil, classedError{"messages: native roles unavailable", err}
	}
	return facts, nil
}

type messagingFactsKey struct{}

// withMessagingFacts carries facts prepared outside locks into the policy
// owner's permit admission, which must not prepare them under its locks.
func withMessagingFacts(ctx context.Context, facts *messagePartyFacts) context.Context {
	return context.WithValue(ctx, messagingFactsKey{}, facts)
}

// messagingFactsFor is the published facts, or those this operation prepared.
// Either must still be current where it is used.
func (d *applicationDirectory) messagingFactsFor(ctx context.Context) *messagePartyFacts {
	if roles := d.roles.Load(); roles != nil && roles.messaging != nil {
		return roles.messaging
	}
	facts, _ := ctx.Value(messagingFactsKey{}).(*messagePartyFacts)
	return facts
}

// refreshMessageParties runs only in outer eligibility preparation, before any
// Book, directory or policy edit lock. Native and person-answer checks stay at
// their existing owner. Publication reuses the exact descriptor CAS fence.
func (d *applicationDirectory) refreshMessageParties(ctx context.Context) error {
	_, err := d.refreshMessagePartyFacts(ctx)
	return err
}

// refreshMessagePartyFacts publishes and returns the prepared facts, so a
// caller keeps them even when another writer clears the published roles.
func (d *applicationDirectory) refreshMessagePartyFacts(ctx context.Context) (published *messagePartyFacts, err error) {
	defer func() {
		if err != nil {
			d.roles.Store(nil)
		}
	}()
	if !d.lock(ctx) {
		return nil, ctx.Err()
	}
	var names []string
	var marks map[string]selfMark
	facts := &messagePartyFacts{owner: d.owner}
	err = d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		if d.v3 == nil {
			return errors.New("asks: messaging registration owner unavailable")
		}
		currentMarks, base, e := d.readSelfMarksAtDescriptorLocked(ctx)
		if e != nil {
			return e
		}
		marks = currentMarks
		for name := range legacy {
			names = append(names, name)
		}
		for name := range d.v3.entries {
			names = append(names, name)
		}
		sort.Strings(names)
		paths := []messagePartyInput{
			{path: filepath.Join(d.dir, "descriptors.json"), limit: d.store.MaxBytes},
			{path: filepath.Join(d.dir, selfRegisteredFile), limit: d.store.MaxBytes, raw: bytes.Clone(base.Data)},
			{path: filepath.Join(d.v3.dir, "index.json"), limit: applicationV3IndexLimit},
		}
		for _, name := range names {
			if entry, ok := d.v3.entries[name]; ok {
				paths = append(paths, messagePartyInput{path: d.v3.filePath(entry.FileRevision), limit: applicationV3ManifestFileLimit})
			}
		}
		for i := range paths {
			if i == 1 {
				continue
			}
			raw, e := cas.ReadLimitContext(ctx, paths[i].path, paths[i].limit)
			if e != nil {
				return e
			}
			paths[i].raw = raw
		}
		facts.inputs = paths
		return nil
	})
	d.unlock()
	if err != nil {
		return nil, err
	}
	for _, name := range names {
		current := d.currentAgentEnrollment(ctx, name)
		if current.Outcome != applications.OutcomePage || current.Enrollment == nil {
			return nil, errors.New("asks: current messaging enrollment unavailable")
		}
		mark := marks[name]
		if mark.Kind == "agent" && mark.Agent != nil && mark.Agent.State == "enrolled" && current.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
			return nil, errors.New("asks: enrolled messaging identity unavailable")
		}
		if current.Enrollment.State == applications.AgentEnrollmentStateEnrolled {
			if current.Enrollment.Program == "" {
				return nil, errors.New("asks: messaging program unavailable")
			}
			facts.agents = append(facts.agents, messagePartyAgent{name, current.Enrollment.Program})
		}
	}
	if !d.lock(ctx) {
		return nil, ctx.Err()
	}
	defer d.unlock()
	if err := d.withRegistrationMessageFactsLocked(ctx, func(map[string]wire.ApplicationDescriptor) error { return nil }, facts); err != nil {
		return nil, err
	}
	d.agentClass.Store(facts)
	return facts, nil
}

func (a *runtimeAsks) checkAnswerParties(ctx context.Context, who answerer, p asks.Parties) error {
	if a.rights == nil || a.rights.applications == nil {
		return errors.New("asks: answerer facts unavailable")
	}
	d := a.rights.applications
	roles := d.roles.Load()
	check, err := a.rights.answererCheck(who.subject.Account, who.subject.Program, who.publisher)
	if err != nil {
		return err
	}
	if d.roles.Load() != roles {
		return errors.New("asks: answerer roles changed")
	}
	if err := check.Party(p); err != nil {
		return err
	}
	related, err := a.rights.messageRelatedParties(ctx, p.Target)
	if err != nil {
		return err
	}
	if d.roles.Load() != roles {
		return errors.New("asks: answerer roles changed")
	}
	return check.PartyWithRelated(p, related)
}

// Locked answer callbacks read immutable facts and bounded CAS inputs only.
// Exact application.message decisions are non-meta reads; they do not refresh,
// acquire a policy edit lock, or enter Book/directory/native inventory owners.
func (r *runtimeRights) messageRelatedParties(ctx context.Context, target string) ([]asks.Parties, error) {
	if r == nil || r.applications == nil || r.policy == nil {
		return nil, errors.New("asks: messaging relationship metadata unavailable")
	}
	d := r.applications
	roles := d.roles.Load()
	if roles == nil || roles.messaging == nil || roles.messaging.owner != r.owner {
		return nil, errors.New("asks: messaging eligibility preparation unavailable")
	}
	facts := roles.messaging
	if err := facts.current(ctx); err != nil {
		d.roles.CompareAndSwap(roles, nil)
		return nil, err
	}
	var related []asks.Parties
	seen := map[string]bool{}
	revision := ""
	for _, source := range facts.agents {
		if !identity.SameSubjectProgram(source.program, target) {
			continue
		}
		for _, other := range facts.agents {
			if identity.SameSubjectProgram(source.program, other.program) {
				continue
			}
			forward := r.decide(ctx, rights.Subject{Account: r.owner, Program: source.program}, ActionApplicationMessage, "app:"+other.name)
			reverse := r.decide(ctx, rights.Subject{Account: r.owner, Program: other.program}, ActionApplicationMessage, "app:"+source.name)
			for _, decision := range []rights.Decision{forward, reverse} {
				if decision.PolicyRevision == "" || revision != "" && revision != decision.PolicyRevision {
					return nil, errors.New("asks: messaging policy changed")
				}
				revision = decision.PolicyRevision
			}
			fp, err := messagePartyPermit(forward.Outcome)
			if err != nil {
				return nil, err
			}
			rp, err := messagePartyPermit(reverse.Outcome)
			if err != nil {
				return nil, err
			}
			if (fp || rp) && !seen[other.program] {
				seen[other.program] = true
				related = append(related, asks.Parties{Target: other.program})
			}
		}
	}
	if err := facts.current(ctx); err != nil {
		d.roles.CompareAndSwap(roles, nil)
		return nil, err
	}
	if d.roles.Load() != roles {
		return nil, errors.New("asks: messaging roles changed")
	}
	return related, ctx.Err()
}

func messagePartyPermit(outcome rights.DecisionOutcome) (bool, error) {
	switch {
	case outcome == rights.DecisionOutcomePermitted:
		return true, nil
	case rightsclient.Evaluated(outcome) && outcome != rights.DecisionOutcomeUnknownAction:
		return false, nil
	default:
		// An evaluated UnknownAction supplies no trusted relationship metadata.
		// ASK-R6 cannot interpret that uncertainty as an absent relationship.
		return false, errors.New("asks: current messaging grant unavailable")
	}
}
