package main

import (
	"context"
	"errors"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-router/go/hostedcatalogue"
)

func (o *inferenceOperator) ProgramBudgets(ctx context.Context, caller inference.Subject, credential string) wire.ProgramBudgetPage {
	refuse := func(outcome wire.ProgramBudgetReadOutcome) wire.ProgramBudgetPage {
		return wire.ProgramBudgetPage{Outcome: outcome, Entries: []wire.ProgramBudgetEntry{}, Profiles: []string{}}
	}
	if word := o.gate(ctx, caller, inference.ActionHostManage); word != "" {
		if word == "forbidden" {
			return refuse(wire.ProgramBudgetReadOutcomeForbidden)
		}
		return refuse(wire.ProgramBudgetReadOutcomeUnavailable)
	}
	if !credentials.ValidName(credential) {
		return refuse(wire.ProgramBudgetReadOutcomeInvalid)
	}
	if o.ceilings == nil {
		return refuse(wire.ProgramBudgetReadOutcomeUnavailable)
	}
	revision, states, err := o.ceilings.ProgramBudgets(credential, caller.Account)
	if err != nil {
		o.report(err)
		return refuse(wire.ProgramBudgetReadOutcomeUnavailable)
	}
	entries := make([]wire.ProgramBudgetEntry, 0, len(states))
	for _, state := range states {
		entry := wire.ProgramBudgetEntry{Program: state.Program, CostUnreported: state.CostUnreported, Spend: wire.Spend{Day: state.Day, Tokens: state.Tokens, Micros: state.Micros, Requests: state.Requests, Images: state.Images, AudioSeconds: state.AudioSeconds, Characters: state.Characters}}
		if state.Budget != nil {
			b := state.Budget
			entry.Budget = &wire.ProgramBudget{TokensPerDay: b.TokensPerDay, MicrosPerDay: b.MicrosPerDay, RequestsPerDay: b.RequestsPerDay, ImagesPerDay: b.ImagesPerDay, AudioSecondsPerDay: b.AudioSecondsPerDay, CharactersPerDay: b.CharactersPerDay}
		}
		entries = append(entries, entry)
	}
	if word := o.gate(ctx, caller, inference.ActionHostManage); word != "" {
		if word == "forbidden" {
			return refuse(wire.ProgramBudgetReadOutcomeForbidden)
		}
		return refuse(wire.ProgramBudgetReadOutcomeUnavailable)
	}
	return wire.ProgramBudgetPage{Outcome: wire.ProgramBudgetReadOutcomePage, Revision: revision, Entries: entries, Profiles: []string{"chat", "embed", "image", "transcription", "speech", "live", "video"}}
}

func (o *inferenceOperator) SetProgramBudget(ctx context.Context, caller inference.Subject, request wire.ProgramBudgetChange) wire.ProgramBudgetResult {
	if word := o.gate(ctx, caller, inference.ActionHostManage); word != "" {
		if word == "forbidden" {
			return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeForbidden}
		}
		return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeUnavailable}
	}
	if !credentials.ValidName(request.Credential) {
		return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeInvalid, Reason: "credential"}
	}
	if !identity.ValidSubjectProgram(request.Program) {
		return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeInvalid, Reason: "program"}
	}
	var budget *inference.Ceiling
	if b := request.Budget; b != nil {
		budget = &inference.Ceiling{TokensPerDay: b.TokensPerDay, MicrosPerDay: b.MicrosPerDay, RequestsPerDay: b.RequestsPerDay, ImagesPerDay: b.ImagesPerDay, AudioSecondsPerDay: b.AudioSecondsPerDay, CharactersPerDay: b.CharactersPerDay}
		if budget.Negative() {
			return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeInvalid, Reason: "budget"}
		}
		if reason := unreportedProgramBudgetUnit(o.hostFiles(), request.Credential, *budget); reason != "" {
			return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeInvalid, Reason: reason}
		}
	}
	if o.ceilings == nil {
		return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeUnavailable}
	}
	subject := inference.Subject{Account: caller.Account, Program: identity.NormalizeSubjectProgram(request.Program)}
	revision, applied, err := o.ceilings.SetProgramBudget(request.ExpectedRevision, request.Credential, subject, budget, o.providerEditAdmission(ctx, caller, inference.ActionHostManage))
	if err != nil {
		var admission providerAdmissionError
		if errors.As(err, &admission) && admission == "forbidden" {
			return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeForbidden}
		}
		o.report(err)
		return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeUnavailable}
	}
	if !applied {
		return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeConflict, Revision: revision}
	}
	return wire.ProgramBudgetResult{Outcome: wire.ProgramBudgetEditOutcomeApplied, Revision: revision}
}

// Shared credentials must be observable through every active binding. A custom
// endpoint cannot inherit a curated provider's reporting declaration by name.
func unreportedProgramBudgetUnit(files []providerFile, credential string, budget inference.Ceiling) string {
	if budget.MicrosPerDay == 0 && budget.TokensPerDay == 0 {
		return ""
	}
	found, tokens, micros := false, true, true
	for _, file := range files {
		host := file.Declaration.Host
		if file.Disabled || host == nil || host.Credential != credential {
			continue
		}
		found = true
		units := hostedcatalogue.Observations(file.Declaration.Name, host.Base, host.Kind, host.Credential)
		tokens = tokens && units.Tokens
		micros = micros && units.Micros
	}
	if budget.MicrosPerDay > 0 && (!found || !micros) {
		return "micros_per_day:unreported"
	}
	if budget.TokensPerDay > 0 && (!found || !tokens) {
		return "tokens_per_day:unreported"
	}
	return ""
}
