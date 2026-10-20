package main

import (
	"context"
	"errors"
	"slices"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

// callerHostListing projects current native host-use and credential eligibility.
// The router service supplies a freshly rechecked peer after the survey. This
// path evaluates the holder in process and never reads or returns a key.
func (c *runtimeCredentials) callerHostListing(ctx context.Context, peer *identity.Peer, hosts []router.HostState) ([]router.HostState, error) {
	subject, err := inferenceservice.SubjectFromPeer(peer)
	if err != nil || c == nil || c.holder == nil || c.runtimeRights == nil || subject.Account != c.owner {
		return nil, errors.New("native host caller unavailable")
	}
	return projectCallerHosts(hosts, func(host router.HostState) router.AvailabilityCause {
		return c.subjectHostAvailability(ctx, subject, host, nil)
	}), nil
}

// The native listing and the guarded operator commit share one subject-taking
// eligibility owner. Prospective is private to the pinned provider pair: only
// that pair's pending apply and host-use permits are treated as granted.
func (c *runtimeCredentials) subjectHostAvailability(ctx context.Context, subject inference.Subject, host router.HostState, retained *asks.PermissionRequest) router.AvailabilityCause {
	if c == nil || c.holder == nil || c.runtimeRights == nil || subject.Account != c.owner {
		return router.AvailabilityCredential
	}
	prospective := false
	if retained != nil {
		name, ok := asks.ProviderPermissionName(*retained)
		if !ok || retained.Targets[0].Account != subject.Account || !identity.SameSubjectProgram(retained.Targets[0].Program, subject.Program) || name != host.Host {
			return router.AvailabilityRights
		}
		binding, err := hostedcatalogue.Select(name)
		if err != nil || !host.Hosted || host.Base != binding.Base || host.Wire != binding.Kind || host.Credential != binding.Credential {
			return router.AvailabilityConfiguration
		}
		prospective = true
	}
	if !prospective {
		if refusal, err := c.standingInference(ctx, subject, host.Host, ""); err != nil || refusal != "" {
			return router.AvailabilityRights
		}
	}
	if host.Hosted && host.Credential != "" {
		checked := c.holder.Check(ctx, cwire.Use{Subject: cwire.Subject{Account: subject.Account, Program: subject.Program}, Name: host.Credential, Consumer: router.CredentialConsumer, Target: router.Target(host.Base)}, func(ctx context.Context, useSubject cwire.Subject, action, resource string) (string, error) {
			if action != credentials.ActionApply {
				return rwire.DecisionOutcomeUnavailable.String(), nil
			}
			if prospective && subject.Account == useSubject.Account && identity.SameSubjectProgram(subject.Program, useSubject.Program) && resource == credentials.ResourceFor(host.Credential) {
				return rwire.DecisionOutcomePermitted.String(), nil
			}
			return c.useDecision(ctx, rwire.Subject{Account: useSubject.Account, Program: useSubject.Program}, resource).String(), nil
		})
		if checked.Outcome != cwire.ApplyOutcomeApplied || checked.Revision == "" {
			return router.AvailabilityCredential
		}
	}
	return ""
}

func projectCallerHosts(hosts []router.HostState, check func(router.HostState) router.AvailabilityCause) []router.HostState {
	out := slices.Clone(hosts)
	for i, host := range out {
		if cause := check(host); cause != "" {
			out[i].Servable = false
			out[i].AvailabilityCause = cause
			out[i].Why = "not available to this program"
		}
	}
	return out
}

// A survey may predate a registry edit. Recheck each hosted binding against
// the current native declarations before attesting that it can serve a caller.
func projectCallerHostBindings(hosts []router.HostState, files []providerFile) []router.HostState {
	return projectCallerHosts(hosts, func(host router.HostState) router.AvailabilityCause {
		if !host.Hosted {
			return ""
		}
		if !providerFixedBindingAvailable(host, files) {
			return router.AvailabilityConfiguration
		}
		matches := 0
		for _, file := range files {
			d := file.Declaration
			if d.Name == host.Host && !file.Disabled && d.Host != nil && d.Host.Hosted && d.Host.Base == host.Base && d.Host.Kind == host.Wire && d.Host.Credential == host.Credential {
				matches++
			}
		}
		if matches != 1 {
			return router.AvailabilityConfiguration
		}
		return ""
	})
}
