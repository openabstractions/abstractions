package main

import (
	"context"
	"maps"
	"slices"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	hostdeclaration "github.com/openabstractions/abstraction-provider-modelhost/go/declaration"
)

// ownModelHost is this installation's model host program, installed beside
// the runtime; empty when none is.
var ownModelHost = func() string { return siblingProgram(modelHostProgram) }

// upgradeModelHostContext names model_context on each chat facet of a kept
// registration of this installation's own model host that predates the key
// (declaration.ReadsContext; the declaration named it from 2026-11-10). The
// person kept that program, and Setup's next `models host` writes the same
// facet, so the record keeps its generation and its Keep. The read rule is
// written the way Keep writes it. Before this upgrade such a record stayed
// undesignated for its own setting after every update, and each load ran at
// Automatic.
func (d *applicationDirectory) upgradeModelHostContext(ctx context.Context, program string) error {
	key := hostdeclaration.ContextKey
	if d == nil || program == "" || d.programConfigKeyKnown == nil || !d.programConfigKeyKnown(key) || d.programConfigKeep == nil {
		return nil
	}
	var upgraded []string
	out := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		upgraded = nil
		for _, name := range s.programKeys() {
			record, ok := s.programs[name]
			source, has := s.programSources[name]
			approval := s.programApprovals[name]
			if !ok || !has || !samePrograms(source.Program, program) || record.State != registration.StateEnabled ||
				record.IdentityEvidence != registration.IdentityEvidenceProgram || approval.Pending || approval.Removing ||
				approval.KeptGeneration == "" || approval.KeptGeneration != record.Epoch ||
				record.Verification == registration.VerificationChanged || record.Verification == registration.VerificationMissing {
				continue
			}
			spec := record.Spec
			spec.Providers = slices.Clone(spec.Providers)
			changed := false
			for i, facet := range spec.Providers {
				if hostdeclaration.ReadsContext(facet) && !slices.Contains(facet.ConfigKeys, key) {
					spec.Providers[i].ConfigKeys = append(slices.Clone(facet.ConfigKeys), key)
					changed = true
				}
			}
			if !changed || registrationSpecValid(spec) != "" {
				continue
			}
			revision, err := applicationToken()
			if err != nil {
				return registration.Change{}, err
			}
			if approval.Revision == record.Revision {
				approval.Revision = revision
				s.programApprovals[name] = approval
			}
			record.Revision = revision
			s.programs = maps.Clone(s.programs)
			s.keepProgramShape(name, record, spec)
			upgraded = append(upgraded, source.Program)
		}
		if len(upgraded) == 0 {
			return registration.Change{Outcome: registration.OutcomeConflict, Reason: "kept"}, nil
		}
		return registration.Change{Outcome: registration.OutcomeApplied}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		return nil
	}
	defer d.reconcileProviderConfigAttempt(ctx)
	for _, program := range upgraded {
		if err := d.programConfigKeep(ctx, program, []string{key}); err != nil {
			return err
		}
	}
	return nil
}
