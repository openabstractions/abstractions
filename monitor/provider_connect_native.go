package main

import (
	"context"
	"slices"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	facade "github.com/openabstractions/abstraction-facade/go"
	inference "github.com/openabstractions/abstraction-inference/go"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

type providerFormFactory func(context.Context, *askclient.PermissionRequest, func(context.Context) error, func(context.Context) error) (*providerKeyForm, error)

type providerHostConflict struct{ label string }

type providerHostOperator interface {
	Hosts(context.Context) (iclient.HostList, error)
	AddHost(context.Context, string, iclient.HostEntry) (iclient.HostChange, error)
}

func (e *providerHostConflict) Error() string {
	return "You already have a " + e.label + " server set up differently. Review it on the Models page."
}
func (e *providerHostConflict) NativeCredentialMessage() string { return e.Error() }

func providerHostBindingMatches(entry iclient.HostEntry, binding hostedcatalogue.Binding) bool {
	return entry.Name == binding.Name && entry.Hosted && entry.Base == binding.Base && entry.Kind == binding.Kind && entry.Credential == binding.Credential
}

func configureProviderHost(ctx context.Context, operator providerHostOperator, provider string) error {
	binding, err := hostedcatalogue.Select(provider)
	if err != nil {
		return errProviderHost
	}
	preset, _ := hostedcatalogue.Lookup(provider)
	for attempt := 0; attempt < 2; attempt++ {
		list, err := operator.Hosts(ctx)
		if err != nil || list.Outcome.String() != "page" || list.Revision == "" {
			return errProviderHost
		}
		for _, host := range list.Hosts {
			if host.Entry.Name == binding.Name {
				if !providerHostBindingMatches(host.Entry, binding) {
					return &providerHostConflict{label: preset.Label}
				}
				// Existing profile and host-wide restrictions are preserved. This
				// operation selects no caller-supplied endpoint or credential.
				return nil
			}
		}
		change, err := operator.AddHost(ctx, list.Revision, iclient.HostEntry{Name: binding.Name, Hosted: true, Base: binding.Base, Kind: binding.Kind, Credential: binding.Credential})
		if err != nil {
			return errProviderHost
		}
		if change.Outcome.String() == "applied" {
			return nil
		}
		if change.Outcome.String() != "conflict" {
			return errProviderHost
		}
	}
	return errProviderHost
}

func providerMetadataMatches(metadata *cwire.Metadata, binding hostedcatalogue.Binding) bool {
	return metadata != nil && metadata.Name == binding.Credential && metadata.Revision != "" && metadata.State == cwire.StateActive && metadata.Kind == "bearer" && slices.Equal(metadata.Scope.Targets, []string{binding.Target}) && slices.Contains(metadata.Scope.Consumers, router.CredentialConsumer) && slices.Contains(metadata.Scope.Consumers, inference.Contract)
}

func providerForPermission(permission *askclient.PermissionRequest) (string, bool) {
	if permission == nil {
		return "", false
	}
	name, ok := asks.ProviderPermissionName(*permission)
	if !ok {
		return "", false
	}
	binding, err := hostedcatalogue.Select(name)
	return binding.Name, err == nil && binding.Name == name && binding.Credential == name
}

func providerPairPermission(permission *askclient.PermissionRequest) bool {
	if permission == nil {
		return false
	}
	_, ok := asks.ProviderPermissionName(*permission)
	return ok
}

func nativeProviderForm(ctx context.Context, permission *askclient.PermissionRequest, recheck, grant func(context.Context) error) (*providerKeyForm, error) {
	provider, ok := providerForPermission(permission)
	if !ok {
		return nil, nil
	}
	binding, _ := hostedcatalogue.Select(provider)
	holder, err := panelMachine().ResolveCredentials(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, errProviderConnectionChanged
	}
	read := func(ctx context.Context) (*cwire.Metadata, error) {
		cursor := ""
		for page := 0; page < 64; page++ {
			result, err := holder.List(ctx, cursor, 64)
			if err != nil || result.Outcome != cwire.PageOutcomePage {
				return nil, errProviderConnectionChanged
			}
			for _, m := range result.Records {
				if m.Name == binding.Credential {
					copy := m
					return &copy, nil
				}
			}
			if result.Complete {
				return nil, nil
			}
			if result.Next == "" || result.Next == cursor {
				return nil, errProviderConnectionChanged
			}
			cursor = result.Next
		}
		return nil, errProviderConnectionChanged
	}
	metadata, err := read(ctx)
	if err != nil {
		return nil, err
	}
	revision := ""
	if metadata != nil {
		// A name alone never proves a provider binding. Preserve conflicting or
		// expired entries for the Credentials owner to review separately.
		if !providerMetadataMatches(metadata, binding) {
			return nil, nil
		}
		revision = metadata.Revision
	}
	c := &providerConnectController{provider: provider, missing: metadata == nil, validate: router.ValidateHostedCandidate, grant: grant}
	var budgetChosen *int64
	budgetOperator := func(ctx context.Context) (*iclient.Operator, error) {
		if permission.Targets[0].Account != panelAccount(ctx) {
			return nil, errProviderConnectionChanged
		}
		operator, err := panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			return nil, errProviderBudget
		}
		return operator, nil
	}
	initialOperator, err := budgetOperator(ctx)
	if err != nil {
		return nil, err
	}
	initialPage, err := readProviderBudgetPage(ctx, initialOperator, binding.Credential)
	if err != nil {
		return nil, err
	}
	c.initialBudget, err = providerConfiguredBudgetChoice(initialPage, provider, permission.Targets[0].Program)
	if err != nil {
		return nil, err
	}
	c.preflight = func(ctx context.Context) error {
		operator, err := budgetOperator(ctx)
		if err != nil {
			return err
		}
		_, err = readProviderBudgetPage(ctx, operator, binding.Credential)
		return err
	}
	c.budget = func(ctx context.Context, amount int64) error {
		operator, err := budgetOperator(ctx)
		if err != nil {
			return err
		}
		if err := setProviderProgramBudget(ctx, operator, provider, permission.Targets[0].Program, amount); err != nil {
			return err
		}
		budgetChosen = &amount
		return nil
	}
	c.configure = func(ctx context.Context) error {
		operator, err := panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			return errProviderHost
		}
		return configureProviderHost(ctx, operator, provider)
	}
	c.recheck = func(ctx context.Context) error {
		if err := recheck(ctx); err != nil {
			return err
		}
		current, err := read(ctx)
		if err != nil {
			return err
		}
		if current == nil && revision == "" {
			return nil
		}
		if !providerMetadataMatches(current, binding) || current.Revision != revision {
			return errProviderConnectionChanged
		}
		return nil
	}
	c.store = func(ctx context.Context, key []byte) error {
		stored, err := holder.Store(ctx, "", cwire.Registration{Name: binding.Credential, Kind: "bearer", Scope: cwire.Scope{Targets: []string{binding.Target}, Consumers: presetConsumers}, Secret: key})
		if err != nil || stored.Outcome != cwire.StoreOutcomeStored || stored.Revision == "" {
			return errProviderConnectionChanged
		}
		revision = stored.Revision
		return nil
	}
	c.readback = func(ctx context.Context) error {
		if budgetChosen == nil {
			return errProviderBudget
		}
		if err := c.recheck(ctx); err != nil {
			return err
		}
		operator, err := budgetOperator(ctx)
		if err != nil {
			return err
		}
		if err := checkProviderBudgetConfiguration(ctx, operator, provider, permission.Targets[0].Program, *budgetChosen, false); err != nil {
			return err
		}
		rules, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
		if err != nil {
			return errProviderConnectionChanged
		}
		for _, target := range permission.Targets {
			read, err := rules.ReadRuleContext(ctx, rightsclient.Subject{Account: target.Account, Program: target.Program}, target.Action, target.Resource)
			if err == nil && (read.Outcome == rightsclient.RuleReadOutcomeUnknown || read.Outcome == rightsclient.RuleReadOutcomeExpired || read.Outcome == rightsclient.RuleReadOutcomeFound && read.Record != nil && !read.Record.Rule.Permit) {
				return errProviderPermissionRemoved
			}
			if err != nil || read.Outcome != rightsclient.RuleReadOutcomeFound || read.Record == nil || !read.Record.Rule.Permit {
				return errProviderConnectionChanged
			}
		}
		return nil
	}
	return c.form()
}
