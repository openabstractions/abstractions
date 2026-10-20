package main

import (
	"context"
	"slices"

	iclient "github.com/openabstractions/abstraction-inference/go/client"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

type providerBudgetOperator interface {
	ProgramBudgets(context.Context, string) (iclient.ProgramBudgetPage, error)
	SetProgramBudget(context.Context, iclient.ProgramBudgetChange) (iclient.ProgramBudgetResult, error)
}

func providerBudgetChoice(provider string, amount int64) (*iclient.ProgramBudget, error) {
	preset, ok := hostedcatalogue.Lookup(provider)
	binding, err := hostedcatalogue.Select(provider)
	if !ok || err != nil || amount < 0 {
		return nil, errProviderBudget
	}
	units := hostedcatalogue.Observations(binding.Name, binding.Base, binding.Kind, binding.Credential)
	budget := &iclient.ProgramBudget{}
	switch {
	case preset.Budget == "money" && units.Micros:
		budget.MicrosPerDay = amount
	case preset.Budget == "tokens" && units.Tokens:
		budget.TokensPerDay = amount
	default:
		return nil, errProviderBudget
	}
	// A present zero record is the person's explicit No limit choice.
	return budget, nil
}

func readProviderBudgetPage(ctx context.Context, operator providerBudgetOperator, credential string) (iclient.ProgramBudgetPage, error) {
	page, err := operator.ProgramBudgets(ctx, credential)
	if err != nil || page.Outcome != iclient.ProgramBudgetReadOutcomePage || page.Revision == "" || !slices.Contains(page.Profiles, "chat") {
		return iclient.ProgramBudgetPage{}, errProviderBudget
	}
	return page, nil
}

func setProviderProgramBudget(ctx context.Context, operator providerBudgetOperator, provider, program string, amount int64) error {
	budget, err := providerBudgetChoice(provider, amount)
	binding, bindingErr := hostedcatalogue.Select(provider)
	if err != nil || bindingErr != nil || program == "" {
		return errProviderBudget
	}
	for range 2 {
		page, err := readProviderBudgetPage(ctx, operator, binding.Credential)
		if err != nil {
			return err
		}
		for _, entry := range page.Entries {
			if entry.Program == program && entry.Budget != nil && *entry.Budget == *budget {
				return nil
			}
		}
		result, err := operator.SetProgramBudget(ctx, iclient.ProgramBudgetChange{ExpectedRevision: page.Revision, Credential: binding.Credential, Program: program, Budget: budget})
		if err != nil {
			return errProviderBudget
		}
		if result.Outcome == iclient.ProgramBudgetEditOutcomeApplied {
			return checkProviderProgramBudget(ctx, operator, provider, program, amount)
		}
		if result.Outcome != iclient.ProgramBudgetEditOutcomeConflict {
			return errProviderBudget
		}
	}
	return errProviderBudget
}

func providerConfiguredBudgetChoice(page iclient.ProgramBudgetPage, provider, program string) (*int64, error) {
	preset, ok := hostedcatalogue.Lookup(provider)
	if !ok {
		return nil, errProviderBudget
	}
	for _, entry := range page.Entries {
		if entry.Program != program || entry.Budget == nil {
			continue
		}
		amount := entry.Budget.TokensPerDay
		if preset.Budget == "money" {
			amount = entry.Budget.MicrosPerDay
		}
		expected, err := providerBudgetChoice(provider, amount)
		if err != nil || amount > maxPresetBudget || *expected != *entry.Budget {
			return nil, errProviderBudget
		}
		return &amount, nil
	}
	return nil, nil
}

func checkProviderProgramBudget(ctx context.Context, operator providerBudgetOperator, provider, program string, amount int64) error {
	return checkProviderBudgetConfiguration(ctx, operator, provider, program, amount, true)
}

func checkProviderBudgetConfiguration(ctx context.Context, operator providerBudgetOperator, provider, program string, amount int64, costPreflight bool) error {
	budget, err := providerBudgetChoice(provider, amount)
	binding, bindingErr := hostedcatalogue.Select(provider)
	if err != nil || bindingErr != nil || program == "" {
		return errProviderBudget
	}
	page, err := readProviderBudgetPage(ctx, operator, binding.Credential)
	if err != nil {
		return err
	}
	for _, entry := range page.Entries {
		if costPreflight && entry.Program == program && entry.CostUnreported && budget.MicrosPerDay > 0 {
			return errProviderCostUnreported
		}
		if entry.Program == program && entry.Budget != nil && *entry.Budget == *budget {
			return nil
		}
	}
	return errProviderBudget
}
