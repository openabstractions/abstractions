package main

import (
	"context"
	"slices"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

type registrationProgramDescriptions func(context.Context, []registration.Registration) []registration.Registration

// The directory calls this copied observation overlay after releasing its CAS
// and mutex. The holder lock precedes its current directory-binding read.
func (p *runtimeProviders) describeProgramRegistrations(ctx context.Context, records []registration.Registration) []registration.Registration {
	if p == nil || ctx.Err() != nil {
		return records
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	files, _, err := p.readCached()
	if err != nil {
		return records
	}
	current := map[string]providerFile{}
	for _, file := range files {
		if !file.Disabled {
			current[file.Declaration.Name] = file
		}
	}
	for i := range records {
		record := &records[i]
		if record.State == registration.StatePendingApproval && p.programReviewUnavailable != nil && p.programReviewUnavailable(record.Key, record.Revision) {
			record.Reason = "approval:unavailable"
		}
		if p.programReviewQuestion != nil {
			if question := p.programReviewQuestion(record.Key, record.Revision); question != "" {
				record.Reason = "Review the open OpenAbstractions question " + question + "."
			}
		}
		record.Described = []registration.ServiceState{}
		record.Checks = nil
		facets := slices.Clone(record.Spec.Providers)
		slices.SortFunc(facets, func(a, b registration.ProviderFacet) int {
			if a.Name < b.Name {
				return -1
			}
			if a.Name > b.Name {
				return 1
			}
			return 0
		})
		for _, facet := range facets {
			file, ok := current[facet.Name]
			if !ok || file.RegistrationKey != record.Key || file.Generation != record.Epoch {
				continue
			}
			supervisor := p.supervisors[facet.Name]
			if supervisor == nil || supervisor.file.bindingID() != file.bindingID() {
				continue
			}
			description, observed := supervisor.registrationDescription(file.bindingID())
			if !observed {
				continue
			}
			// Checks are one producer's complete same-generation list. An unsupported
			// or refusing Description never supplies borrowed success fields.
			if description.Outcome == registration.DescriptionOutcomeDescribed {
				record.Described = description.Services
				record.Checks = description.Checks
			} else if record.Reason == "" {
				record.Reason = description.Reason
				if record.Reason == "" {
					record.Reason = "endpoint:" + description.Outcome.String()
				}
			}
			break
		}
	}
	return records
}
