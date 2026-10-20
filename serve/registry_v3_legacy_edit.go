package main

import (
	"context"
	"errors"
	"slices"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

func legacyProviderFacet(file providerFile) registration.FacetReference {
	kind := registration.FacetKindProvider
	if file.Declaration.host() {
		kind = registration.FacetKindHost
	}
	if file.Declaration.remote() {
		kind = registration.FacetKindRemote
	}
	return registration.FacetReference{Kind: kind, Name: file.Declaration.Name}
}

// Once the existing registration directory owns a program, old receiving
// methods edit that same index. Original declaration files remain migration
// inputs, never a competing publication point for these receiving mutations.
func (p *runtimeProviders) editCanonicalLegacy(ctx context.Context, operation, expected, name string, file providerFile, admit func() error, mutate providerMutation) (wire.DeclarationChange, bool) {
	if p.registrationDirectory == nil {
		return wire.DeclarationChange{}, false
	}
	d := p.registrationDirectory()
	if d == nil {
		return wire.DeclarationChange{}, false
	}
	if !d.lock(ctx) {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}, true
	}
	ready := d.v3 != nil
	d.unlock()
	if !ready {
		return wire.DeclarationChange{}, false
	}
	if mutate != nil {
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable, Reason: "unsupported:not_implemented"}, true
	}
	if operation == "remove" {
		p.forgetProducts()
	}
	p.productDeclarations()
	p.mu.Lock()
	if err := providerAdmit(ctx, admit); err != nil {
		out := p.editFailureLocked(err)
		p.mu.Unlock()
		return out, true
	}
	if p.closed {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}, true
	}
	_, _, before, err := d.programProviderSnapshot(ctx)
	files, revision, disabledNames, readErr := p.readWithDisabled(true)
	_, _, after, lastErr := d.programProviderSnapshot(ctx)
	refuse := func(outcome wire.DeclarationEditOutcome, reason string) (wire.DeclarationChange, bool) {
		p.mu.Unlock()
		return wire.DeclarationChange{Outcome: outcome, Revision: revision, Reason: reason}, true
	}
	if err != nil || readErr != nil || lastErr != nil {
		return refuse(wire.DeclarationEditOutcomeUnavailable, "registration_owner")
	}
	if before != after || expected != revision {
		return refuse(wire.DeclarationEditOutcomeConflict, "revision")
	}
	if p.retiring[name] != nil {
		return refuse(wire.DeclarationEditOutcomeConflict, "retiring")
	}
	selected := slices.IndexFunc(files, func(v providerFile) bool { return v.Declaration.Name == name })
	if operation == "add" {
		if selected >= 0 && !files[selected].Disabled {
			return refuse(wire.DeclarationEditOutcomeConflict, "name")
		}
		if selected < 0 && len(files) >= maxProviders {
			return refuse(wire.DeclarationEditOutcomeInvalid, "declarations")
		}
	} else {
		// A disabled operator declaration stays withdrawable: its registration
		// reads as disabled while its program is missing or changed, turned
		// off or pending, and the person removes exactly such leftovers. A
		// disabled product or installation name is already withdrawn.
		inert := selected >= 0 && files[selected].Disabled && !(operation == "remove" && files[selected].Source == declarationSourceOperator)
		if selected < 0 || inert {
			return refuse(wire.DeclarationEditOutcomeUnknown, "name")
		}
		if operation == "replace" && files[selected].Source != declarationSourceOperator {
			return refuse(wire.DeclarationEditOutcomeForbidden, "source")
		}
	}
	if operation != "remove" {
		if err := p.admitPairedName(name); err != nil {
			out := p.editFailureLocked(err)
			p.mu.Unlock()
			return out, true
		}
		for _, other := range files {
			if other.Declaration.Name != name && !other.Disabled && file.Declaration.Endpoint != "" && file.Declaration.Endpoint == other.Declaration.Endpoint {
				return refuse(wire.DeclarationEditOutcomeConflict, "endpoint")
			}
		}
	}
	var old providerFile
	if selected >= 0 {
		old = files[selected]
	}
	if operation == "remove" && old.Source != declarationSourceOperator {
		// The guarded disabled-name owner narrows product and installation
		// declarations, including current observations absent from the directory.
		p.mu.Unlock()
		return wire.DeclarationChange{}, false
	}
	p.mu.Unlock()
	var key string
	var part registration.RegistrationSpec
	var image *applicationV3Source
	verification := registration.VerificationUnverified
	reason := "identity:none"
	if operation != "remove" {
		var err error
		key, part, err = legacyRegistrationSpec(file)
		if err != nil {
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable, Reason: "unsupported:not_implemented"}, true
		}
		if part.Program != "" {
			verification, reason, image = observeRegistrationImage(part.Program, nil)
			if verification == 0 {
				return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable, Reason: reason}, true
			}
		}
	}
	domain := wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}
	var admissionErr error
	change := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		if s.revision != after {
			domain.Outcome = wire.DeclarationEditOutcomeConflict
			domain.Reason = "revision"
			return registration.Change{Outcome: registration.OutcomeConflict}, nil
		}
		if err := providerAdmit(ctx, admit); err != nil {
			admissionErr = err
			return registration.Change{}, err
		}
		oldKey := ""
		oldFacet := legacyProviderFacet(old)
		for _, candidate := range s.programKeys() {
			spec, err := s.programSpec(ctx, candidate, s.entries)
			if err != nil {
				return registration.Change{}, err
			}
			if registrationHasFacet(spec, oldFacet) {
				oldKey = candidate
				break
			}
		}
		revision, err := applicationToken()
		if err != nil {
			return registration.Change{}, err
		}
		if selected >= 0 && oldKey == "" && old.Source == declarationSourceOperator {
			return registration.Change{}, errors.New("registration: legacy input has not entered authority")
		}
		if operation == "remove" {
			if s.programApprovals[oldKey].Removing {
				// The person's Remove answer owns this registration now.
				domain.Outcome = wire.DeclarationEditOutcomeConflict
				domain.Reason = "removing"
				return registration.Change{Outcome: registration.OutcomeConflict}, nil
			}
			record, _ := s.programRecord(oldKey)
			spec, err := s.programSpec(ctx, oldKey, s.entries)
			if err != nil {
				return registration.Change{}, err
			}
			record.Spec = spec
			record.Spec = registrationWithoutFacet(record.Spec, oldFacet)
			s.programCompatibility[oldKey] = slices.DeleteFunc(s.programCompatibility[oldKey], func(v providerFile) bool { return legacyProviderFacet(v) == oldFacet })
			if len(s.removals) >= 64 {
				return registration.Change{Outcome: registration.OutcomeUnavailable, Reason: "bound:removals"}, nil
			}
			id, err := applicationToken()
			if err != nil {
				return registration.Change{}, err
			}
			s.removals = append(s.removals, registration.RemovalReceipt{ID: id, Facet: oldFacet, Revision: revision, Accepted: registrationTime(d.now()), Cleanup: []registration.Cleanup{{Owner: "abstraction.facade", State: registration.CleanupStatePending, Reason: "unsupported:not_implemented"}, {Owner: "abstraction.rights", State: registration.CleanupStatePending, Reason: "unsupported:not_implemented"}, {Owner: "abstraction.asks", State: registration.CleanupStatePending, Reason: "unsupported:not_implemented"}}})
			record.Revision = revision
			if registrationFacetCount(record.Spec) == 0 {
				delete(s.programs, oldKey)
				delete(s.legacyPrograms, oldKey)
			} else {
				s.keepProgramShape(oldKey, record, record.Spec)
			}
		} else {
			if oldKey != "" && oldKey != key {
				prior, _ := s.programRecord(oldKey)
				spec, err := s.programSpec(ctx, oldKey, s.entries)
				if err != nil {
					return registration.Change{}, err
				}
				prior.Spec = registrationWithoutFacet(spec, oldFacet)
				prior.Revision = revision
				s.programCompatibility[oldKey] = slices.DeleteFunc(s.programCompatibility[oldKey], func(v providerFile) bool { return legacyProviderFacet(v) == oldFacet })
				if registrationFacetCount(prior.Spec) == 0 {
					delete(s.programs, oldKey)
					delete(s.legacyPrograms, oldKey)
				} else {
					s.keepProgramShape(oldKey, prior, prior.Spec)
				}
			}
			record, exists := s.programRecord(key)
			if exists {
				spec, err := s.programSpec(ctx, key, s.entries)
				if err != nil {
					return registration.Change{}, err
				}
				record.Spec = spec
			} else {
				record = registration.Registration{Key: key, Spec: registration.RegistrationSpec{Program: part.Program, Providers: []registration.ProviderFacet{}, Servers: []registration.ServerFacet{}, Remotes: []registration.RemoteFacet{}, Applications: []registration.ApplicationFacet{}}, Source: registration.SourceOperator, State: registration.StateEnabled, IdentityEvidence: registration.IdentityEvidenceNone, Accepted: registrationTime(d.now()), Applications: []registration.ApplicationReading{}, Described: []registration.ServiceState{}}
			}
			facet := legacyProviderFacet(file)
			// The kept facet, epoch, image and condition before this edit,
			// for the runtime's allowlist reconcile (REG-CFG2).
			var kept *registration.ProviderFacet
			keptEpoch, keptSource, keptCondition := record.Epoch, s.programSources[key], registrationConditionOf(s, key, facet)
			if exists && oldKey == key {
				if v, ok := registrationFacetValue(record.Spec, facet).(registration.ProviderFacet); ok {
					c := cloneProviderFacet(v)
					kept = &c
				}
			}
			if selected >= 0 && oldKey == key {
				record.Spec = registrationWithoutFacet(record.Spec, oldFacet)
			}
			record.Spec = registrationReplaceFacet(record.Spec, part, facet)
			record.Revision = revision
			generation, err := applicationToken()
			if err != nil {
				return registration.Change{}, err
			}
			record.Epoch = generation
			record.Verified = registrationTime(d.now())
			record.Verification = verification
			record.VerificationReason = reason
			if image != nil {
				record.IdentityEvidence = registration.IdentityEvidenceProgram
				s.keepProgramSource(key, *image)
			}
			if s.programFacetSources[key] == nil {
				s.programFacetSources[key] = map[registration.FacetReference]registration.Source{}
			}
			actualSource := registration.SourceOperator
			s.programFacetSources[key][facet] = actualSource
			if s.programFacetConditions[key] == nil {
				s.programFacetConditions[key] = map[registration.FacetReference]registrationFacetCondition{}
			}
			s.programFacetConditions[key][facet] = registrationFacetCondition{}
			if s.programFacetDigests[key] == nil {
				s.programFacetDigests[key] = map[registration.FacetReference]string{}
			}
			s.programFacetDigests[key][facet] = ""
			// REG-CFG2: the runtime's own allowlist reconcile of a kept
			// declaration carries the person's Keep to the new epoch when it
			// widens nothing. Every other edit leaves the Keep behind.
			if file.runtimeAllowlist && operation == "replace" && kept != nil && image != nil && *image == keptSource && keptSource.FileKey != "" &&
				!keptCondition.Disabled && !keptCondition.Pending && record.State == registration.StateEnabled {
				approval := s.programApprovals[key]
				next, ok := registrationFacetValue(record.Spec, facet).(registration.ProviderFacet)
				if ok && approval.KeptGeneration != "" && approval.KeptGeneration == keptEpoch && !approval.Pending && !approval.Removing && allowlistOnly(*kept, next) {
					approval.KeptGeneration = generation
					s.programApprovals[key] = approval
				}
			}
			// The carry intent belongs to this edit alone; the stored
			// compatibility file never carries it into a later one.
			file.runtimeAllowlist = false
			file.Source = registrationSourceWord(actualSource)
			file.Generation = generation
			s.programCompatibility[key] = slices.DeleteFunc(s.programCompatibility[key], func(v providerFile) bool { return legacyProviderFacet(v) == facet })
			s.programCompatibility[key] = append(s.programCompatibility[key], file)
			s.programDeclarers[key] = file.DeclaredBy
			s.keepProgramShape(key, record, record.Spec)
		}
		domain.Outcome = wire.DeclarationEditOutcomeApplied
		return registration.Change{Outcome: registration.OutcomeApplied, Revision: revision}, nil
	})
	if change.Outcome != registration.OutcomeApplied {
		if admissionErr != nil {
			p.mu.Lock()
			domain = p.editFailureLocked(admissionErr)
			p.mu.Unlock()
			return domain, true
		}
		if domain.Outcome == wire.DeclarationEditOutcomeApplied {
			domain.Outcome = wire.DeclarationEditOutcomeUnavailable
		}
		p.mu.Lock()
		_, domain.Revision, _, _ = p.readWithDisabled(true)
		p.mu.Unlock()
		return domain, true
	}
	if operation == "add" {
		if err := p.setDisabledObserved(ctx, name, false, disabledNames, admit); err != nil {
			p.report(err)
			return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeUnavailable}, true
		}
	}
	p.refreshProgramRegistrations(ctx)
	p.mu.Lock()
	_, domain.Revision, _, err = p.readWithDisabled(true)
	p.mu.Unlock()
	if err != nil {
		domain.Outcome = wire.DeclarationEditOutcomeUnavailable
	}
	p.notify()
	if p.remotesChanged != nil {
		if err := p.remotesChanged(); err != nil {
			p.report(err)
			domain.Reason = "hosts:unavailable"
		}
	}
	return domain, true
}
