package main

import (
	"context"
	"slices"

	asks "github.com/openabstractions/abstraction-asks/go"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

type providerSetupFailure string

func (e providerSetupFailure) Error() string { return "provider_setup:" + string(e) }

type providerBudgetReader interface {
	ProgramBudgets(string, string) (string, []inference.ProgramBudgetState, error)
}

func providerBindingEnabled(files []providerFile, b hostedcatalogue.Binding) bool {
	count := 0
	matched := false
	for _, file := range files {
		if file.Disabled || file.Declaration.Name != b.Name {
			continue
		}
		count++
		h := file.Declaration.Host
		matched = h != nil && h.Hosted && h.Base == b.Base && h.Kind == b.Kind && h.Credential == b.Credential
	}
	return count == 1 && matched
}

func providerOwnBudget(budgets providerBudgetReader, b hostedcatalogue.Binding, subject inference.Subject) bool {
	if budgets == nil {
		return false
	}
	_, entries, err := budgets.ProgramBudgets(b.Credential, subject.Account)
	if err != nil {
		return false
	}
	for _, entry := range entries {
		if identity.SameSubjectProgram(entry.Program, subject.Program) && entry.Budget != nil {
			return true
		}
	}
	return false
}

// Prepare outside Book/Rights locks. The only prospective permits belong to
// this complete retained occurrence; no request can supply a beneficiary.
func (c *runtimeCredentials) providerSetupGuard(ctx context.Context, p asks.PermissionRequest, hosts func() []*router.Host, files func() []providerFile, budgets providerBudgetReader) (func() error, error) {
	name, ok := asks.ProviderPermissionName(p)
	if !ok || c == nil || c.runtimeRights == nil || p.Targets[0].Account != c.owner || hosts == nil || files == nil {
		return nil, providerSetupFailure("binding")
	}
	binding, err := hostedcatalogue.Select(name)
	if err != nil || binding.Name != name || binding.Credential != name {
		return nil, providerSetupFailure("binding")
	}
	subject := inference.Subject{Account: p.Targets[0].Account, Program: p.Targets[0].Program}
	current := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !providerBindingEnabled(files(), binding) {
			return providerSetupFailure("binding")
		}
		if !providerOwnBudget(budgets, binding, subject) {
			return providerSetupFailure("budget")
		}
		return nil
	}
	if err := current(); err != nil {
		return nil, err
	}
	var host *router.Host
	count := 0
	for _, candidate := range hosts() {
		if candidate != nil && candidate.Name == name {
			count++
			host = candidate
		}
	}
	if count != 1 || !host.Hosted || host.Base != binding.Base || host.Wire != binding.Kind || host.Credential != binding.Credential || !host.Servable() || !slices.Contains(host.HostProfiles(), router.ProfileChat) {
		return nil, providerSetupFailure("host")
	}
	view := router.HostState{Host: host.Name, Base: host.Base, Wire: host.Wire, Credential: host.Credential, Hosted: host.Hosted, Servable: host.Servable()}
	if cause := c.subjectHostAvailability(ctx, subject, view, &p); cause != "" {
		return nil, providerSetupFailure("credential")
	}
	return current, nil
}

// The edit rechecks the same fixed declaration predicate as own-host listing.
func providerFixedBindingAvailable(host router.HostState, files []providerFile) bool {
	b, err := hostedcatalogue.Select(host.Host)
	if err != nil || !host.Hosted || host.Base != b.Base || host.Wire != b.Kind || host.Credential != b.Credential {
		return true
	}
	return providerBindingEnabled(files, b)
}
