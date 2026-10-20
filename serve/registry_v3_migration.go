package main

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"slices"
	"strings"
	"time"

	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	router "github.com/openabstractions/abstraction-router/go"
)

func legacyRegistrationSpec(file providerFile) (string, registration.RegistrationSpec, error) {
	d := file.Declaration
	spec := registration.RegistrationSpec{Providers: []registration.ProviderFacet{}, Servers: []registration.ServerFacet{}, Remotes: []registration.RemoteFacet{}, Applications: []registration.ApplicationFacet{}}
	profiles := d.mediatedInferenceProfiles()
	switch {
	case d.host():
		f := registration.ServerFacet{Name: d.Name, Base: d.Host.Base, Kind: d.Host.Kind, Hosted: d.Host.Hosted, Credential: d.Host.Credential, Modalities: slices.Clone(d.resources("profile"))}
		f.Ceiling = d.Host.wire().Ceiling
		spec.Servers = append(spec.Servers, f)
		return d.Name, spec, nil
	case d.remote():
		if d.Remote == nil || d.Remote.ServerKeySHA256 == "" || len(profiles) == 0 {
			return "", spec, errors.New("legacy remote proof/configuration unavailable")
		}
		spec.Remotes = append(spec.Remotes, registration.RemoteFacet{Name: d.Name, Endpoint: d.Endpoint, ServerName: d.Remote.ServerName, ServerKeySHA256: d.Remote.ServerKeySHA256, Credential: d.Remote.Credential, Modalities: profiles})
		return "remote:" + d.Remote.ServerKeySHA256, spec, nil
	default:
		spec.Program = identity.NormalizeSubjectProgram(d.Program)
		var facet registration.ProviderFacet
		if file.ProviderFacet != nil {
			facet = *file.ProviderFacet
		} else {
			activation, ok := registration.ParseActivation(d.Activation)
			if !ok {
				return "", spec, errors.New("legacy activation")
			}
			facet = registration.ProviderFacet{Name: d.Name, Arguments: slices.Clone(d.Arguments), Endpoint: d.Endpoint, Services: slices.Clone(d.Contracts), Guarantees: nonNil(slices.Clone(d.Guarantees)), Activation: activation, Resources: []string{}, Models: []registration.ScopedModel{}, RequiredExtensions: []registration.RequiredExtension{}, Engines: []registration.EngineFact{}, Recipes: []registration.ModelRecipe{}}
			for _, resource := range d.Resources {
				if !strings.HasPrefix(resource, "profile:") {
					facet.Resources = append(facet.Resources, resource)
				}
			}
			_, models := router.DeclaredModels(d.Models, profiles)
			for _, profile := range profiles {
				for _, model := range models[profile] {
					facet.Models = append(facet.Models, registration.ScopedModel{Modality: profile, Model: model})
				}
			}
			for _, extension := range d.SupportedExtensions {
				facet.RequiredExtensions = append(facet.RequiredExtensions, registration.RequiredExtension{Modality: extension.Profile, Key: extension.Key})
			}
		}
		spec.Providers = append(spec.Providers, facet)
		return spec.Program, spec, nil
	}
}

func registrationLegacySource(source string) registration.Source {
	switch source {
	case declarationSourceInstallation:
		return registration.SourceInstallation
	case declarationSourceProduct:
		return registration.SourceProductRecord
	default:
		return registration.SourceOperator
	}
}

// observeRegistrationImage performs the existing Identity owners' real file and
// platform-signature reads. A supported negative signature result remains an
// observation of an unverified initial legacy image; an accepted prior image's
// contradictory evidence becomes changed and never broadens eligibility.
func observeRegistrationImage(program string, prior *applicationV3Source) (registration.Verification, string, *applicationV3Source) {
	key, code, proof, verifyErr := observeRegistryLegacyImage(program)
	err := verifyErr
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return registration.VerificationMissing, "identity:missing", nil
		}
		if key == "" {
			return 0, "identity:unavailable", nil
		}
	}
	source := &applicationV3Source{Program: program, FileKey: key, Code: code, Proof: proof}
	if prior != nil && prior.FileKey != key {
		return registration.VerificationChanged, "identity:changed", source
	}
	if errors.Is(verifyErr, identity.ErrUnimplemented) {
		return registration.VerificationUnverified, "platform:unverified", source
	}
	if verifyErr != nil {
		return 0, "identity:unavailable", nil
	}
	if prior != nil && prior.Code.Trusted && (proof != identity.ProofBound || !code.Trusted || prior.Code.Subject != code.Subject || prior.Code.Issuer != code.Issuer || prior.Code.TeamID != code.TeamID) {
		return registration.VerificationChanged, "identity:changed", source
	}
	if verifyErr == nil && proof == identity.ProofBound && code.Trusted {
		return registration.VerificationVerified, "", source
	}
	return registration.VerificationUnverified, "identity:unverified", source
}

func shippedPublisherContinuous(prior *applicationV3Source, code identity.Code, proof identity.Proof, runtimeCode identity.Code, runtimeProof identity.Proof) bool {
	continuous := func(old identity.Code) bool {
		return proof == identity.ProofBound && code.Trusted && old.Subject == code.Subject && old.Issuer == code.Issuer && old.TeamID == code.TeamID
	}
	if prior != nil && prior.Code.Trusted && !continuous(prior.Code) {
		return false
	}
	if runtimeCode.Trusted && (runtimeProof != identity.ProofBound || !continuous(runtimeCode)) {
		return false
	}
	return true
}

func (p *runtimeProviders) adoptProgramRegistrations(ctx context.Context) error {
	if p.registrationDirectory == nil {
		return nil
	}
	d := p.registrationDirectory()
	if d == nil {
		return nil
	}
	if !d.lock(ctx) {
		return ctx.Err()
	}
	if d.v3 == nil {
		d.unlock()
		return nil
	}
	d.v3.describe = p.describeProgramRegistrations
	d.programConfigManaged = p.managedProviderConfigProof
	d.programsChanged = func() { p.refreshProgramRegistrations(context.Background()) }
	d.unlock()
	p.productDeclarations()
	_, removals, _, err := d.programProviderSnapshot(ctx)
	if err != nil {
		return err
	}
	p.mu.Lock()
	files, _, _, err := p.readInputsWithDisabled(true, removals...)
	p.mu.Unlock()
	if err != nil {
		return err
	}
	type incoming struct {
		spec  registration.RegistrationSpec
		files []providerFile
	}
	grouped := map[string]incoming{}
	for _, file := range files {
		key, spec, err := legacyRegistrationSpec(file)
		if err != nil {
			p.report(err)
			continue
		}
		group, ok := grouped[key]
		if !ok {
			group.spec = spec
		} else {
			group.spec.Providers = append(group.spec.Providers, spec.Providers...)
			group.spec.Servers = append(group.spec.Servers, spec.Servers...)
			group.spec.Remotes = append(group.spec.Remotes, spec.Remotes...)
		}
		group.files = append(group.files, file)
		grouped[key] = group
	}
	var unresolved []shippedImageReviewCandidate
	updated := false
	defer func() {
		if updated && p.shippedImageUpdated != nil {
			p.shippedImageUpdated()
		}
	}()
	collectUnresolved := func(key, revision, cause string) {
		if cause == "shipped:outside" {
			return
		}
		unresolved = append(unresolved, shippedImageReviewCandidate{key, revision, cause})
	}
	for _, key := range slices.Sorted(maps.Keys(grouped)) {
		group := grouped[key]
		// Legacy fields retain their original admission bounds. A complete spec
		// beyond @3's bound remains a compatibility-only state in this same CAS.
		raw, err := registrationJSON(group.spec)
		if err != nil {
			return err
		}
		compatibility, _ := json.Marshal(group.files)
		inputDigest := applicationV3Hash(append(raw, compatibility...))
		incomingSources := map[registration.FacetReference]registration.Source{}
		incomingDigests := map[registration.FacetReference]string{}
		incomingFiles := map[registration.FacetReference]providerFile{}
		for _, file := range group.files {
			_, individual, _ := legacyRegistrationSpec(file)
			encoded, _ := registrationEncoded(registration.EncodeRegistrationSpec, &individual)
			oldBytes, _ := registrationJSON(struct {
				File     providerFile
				Source   string
				Disabled bool
			}{file, file.Source, file.Disabled})
			digest := applicationV3Hash(append(append(encoded, oldBytes...), []byte(file.Source)...))
			for _, facet := range registrationReferences(individual) {
				incomingSources[facet] = registrationLegacySource(file.Source)
				incomingDigests[facet] = digest
				incomingFiles[facet] = file
			}
		}
		state := registration.StateDisabled
		for _, file := range group.files {
			if !file.Disabled {
				state = registration.StateEnabled
			}
		}
		source := registration.SourceOperator
		switch group.files[0].Source {
		case declarationSourceInstallation:
			source = registration.SourceInstallation
		case declarationSourceProduct:
			source = registration.SourceProductRecord
		}
		candidate := registration.Registration{Key: key, Spec: group.spec, Source: source, State: state, IdentityEvidence: registration.IdentityEvidenceNone, Accepted: registrationTime(d.now()), Verified: registrationTime(d.now()), Verification: registration.VerificationUnverified, VerificationReason: "identity:none", Applications: []registration.ApplicationReading{}, Described: []registration.ServiceState{}}
		if stamp := group.files[0].DeclaredAt; stamp > 0 {
			candidate.Accepted = registrationTime(time.UnixMilli(stamp))
		}
		var image *applicationV3Source
		var imageRecheck, imageClose func() error
		shippedRebind := false
		shippedCause := "identity:unavailable"
		prior, observedRevision, err := d.programImage(ctx, key)
		if err != nil {
			return err
		}
		if group.spec.Program != "" {
			candidate.Verification, candidate.VerificationReason, image = observeRegistrationImage(group.spec.Program, prior)
			if candidate.Verification == registration.VerificationChanged {
				observe := p.observeShippedImage
				var verification registration.Verification
				var shipped *applicationV3Source
				var recheck, close func() error
				if observe == nil {
					verification, shipped, recheck, close, shippedCause = observeShippedRegistrationImageCause(ctx, group.spec.Program, prior)
				} else {
					verification, shipped, recheck, close = observe(ctx, group.spec.Program, prior)
				}
				if shipped != nil {
					candidate.Verification, candidate.VerificationReason, image = verification, "", shipped
					if verification == registration.VerificationUnverified {
						candidate.VerificationReason = "identity:unverified"
					}
					imageRecheck, imageClose, shippedRebind = recheck, close, true
				}
			}
			if candidate.Verification == 0 {
				p.report(errors.New(candidate.VerificationReason))
				continue
			}
			candidate.Verified = registrationTime(d.now())
			if image != nil {
				candidate.IdentityEvidence = registration.IdentityEvidenceProgram
			}
		}
		if source == registration.SourceProductRecord {
			candidate.ProductName = group.files[0].DeclaredBy
		}
		out := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
			old, exists := s.programRecord(key)
			if exists {
				spec, err := s.programSpec(ctx, key, s.entries)
				if err != nil {
					return registration.Change{}, err
				}
				old.Spec = spec
			}
			sources := maps.Clone(s.programFacetSources[key])
			if sources == nil {
				sources = map[registration.FacetReference]registration.Source{}
			}
			digests := maps.Clone(s.programFacetDigests[key])
			if digests == nil {
				digests = map[registration.FacetReference]string{}
			}
			keptFiles := slices.Clone(s.programCompatibility[key])
			conditions := maps.Clone(s.programFacetConditions[key])
			if conditions == nil {
				conditions = map[registration.FacetReference]registrationFacetCondition{}
			}
			if exists {
				if old.Revision != observedRevision {
					return registration.Change{Outcome: registration.OutcomeConflict, Reason: "revision"}, nil
				}
				candidate.Spec = old.Spec
				candidate.State = old.State
				candidate.Accepted = old.Accepted
				candidate.Epoch = old.Epoch
				candidate.Revision = old.Revision
			}
			// An unanswered or retained Remove question remains bound to its old
			// image. Shipped-byte proof cannot consume that person's answer for a
			// replacement image; the existing review owner must renew that binding.
			if shippedRebind && exists && s.programPending(key, old) {
				candidate.Verification, candidate.VerificationReason, image = registration.VerificationChanged, "identity:changed", nil
				shippedCause = "approval:pending"
			}
			if imageRecheck != nil {
				if err := imageRecheck(); err != nil {
					return registration.Change{Outcome: registration.OutcomeConflict, Reason: "identity:changed"}, nil
				}
			}
			// A proven shipped image receives its installation's changed
			// declarations in this same pass: an upgrade replaces both, and the
			// runtime adopts once per start. The facet rule below keeps a
			// person's edit (empty digest) and the removal filter above keeps a
			// removed declaration out. A record whose image stays changed, by a
			// failed shipped proof or a review bound to the old image, keeps its
			// spec until the person's review renews the image.
			changed := !exists
			heldForReview := exists && candidate.Verification == registration.VerificationChanged
			for _, facet := range registrationReferences(group.spec) {
				if heldForReview {
					continue
				}
				if exists && registrationHasFacet(old.Spec, facet) && (digests[facet] == "" || digests[facet] == incomingDigests[facet] && sources[facet] == incomingSources[facet]) {
					continue
				}
				candidate.Spec = registrationReplaceFacet(candidate.Spec, group.spec, facet)
				sources[facet] = incomingSources[facet]
				digests[facet] = incomingDigests[facet]
				changed = true
				file := incomingFiles[facet]
				conditions[facet] = registrationFacetCondition{Disabled: file.Disabled}
				keptFiles = slices.DeleteFunc(keptFiles, func(old providerFile) bool {
					return old.Declaration.Name == file.Declaration.Name && old.Declaration.role() == file.Declaration.role()
				})
				keptFiles = append(keptFiles, file)
			}
			if !changed && (!shippedRebind || image == nil) && old.Verification == candidate.Verification && old.VerificationReason == candidate.VerificationReason {
				return registration.Change{Outcome: registration.OutcomeConflict, Revision: old.Revision, Reason: "kept"}, nil
			}
			for other, v := range s.programs {
				if other == key {
					continue
				}
				for _, facet := range registrationReferences(candidate.Spec) {
					if registrationHasFacet(v.Spec, facet) {
						return registration.Change{Outcome: registration.OutcomeConflict, Reason: "source:precedence"}, nil
					}
				}
			}
			if changed {
				revision, err := applicationToken()
				if err != nil {
					return registration.Change{}, err
				}
				generation, err := applicationToken()
				if err != nil {
					return registration.Change{}, err
				}
				candidate.Revision = revision
				candidate.Epoch = generation
			}
			s.programs = maps.Clone(s.programs)
			s.keepProgramShape(key, candidate, candidate.Spec)
			s.programCompatibility[key] = keptFiles
			s.programDeclarers[key] = group.files[0].DeclaredBy
			s.programInputDigests[key] = inputDigest
			s.programFacetSources[key] = sources
			s.programFacetDigests[key] = digests
			s.programFacetConditions[key] = conditions
			if image != nil && candidate.Verification != registration.VerificationChanged {
				s.keepProgramSource(key, *image)
			}
			return registration.Change{Outcome: registration.OutcomeApplied, Revision: candidate.Revision}, nil
		})
		if imageClose != nil {
			if err := imageClose(); err != nil {
				p.report(err)
			}
		}
		if out.Outcome == registration.OutcomeUnavailable {
			return errors.New("registration migration: " + out.Reason)
		}
		if shippedRebind && image != nil && out.Outcome == registration.OutcomeApplied {
			updated = true
		}
		// The fallback owner rereads current revision and held identity before any
		// review. This notification runs after the directory CAS releases its lock.
		if candidate.Verification == registration.VerificationChanged {
			collectUnresolved(key, out.Revision, shippedCause)
		}
	}
	if err := p.observeRetainedShippedPrograms(ctx, d, func(key string) bool { _, ok := grouped[key]; return ok }, collectUnresolved, func() { updated = true }); err != nil {
		return err
	}
	// A failed upgrade leaves the record as it was; its reads stay refused
	// until Setup declares the model host again.
	if err := d.upgradeModelHostContext(ctx, ownModelHost()); err != nil {
		p.report(err)
	}
	p.refreshProgramRegistrations(ctx)
	if p.shippedImagesUnresolved != nil {
		p.shippedImagesUnresolved(ctx, unresolved)
	}
	return nil
}

// PROGRAMs received directly through registry@3 need no legacy declaration
// file. Their complete shape and intent stay under the same registration CAS.
func (p *runtimeProviders) observeRetainedShippedPrograms(ctx context.Context, d *applicationDirectory, alreadyObserved func(string) bool, unresolved func(string, string, string), updated func()) error {
	rows, _, _, _, _, err := d.programRegistrationsFor(ctx, true)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Spec.Program == "" || alreadyObserved(row.Key) {
			continue
		}
		prior, revision, err := d.programImage(ctx, row.Key)
		if err != nil {
			return err
		}
		verification, reason, _ := observeRegistrationImage(row.Spec.Program, prior)
		if verification != registration.VerificationChanged {
			continue
		}
		cause := "identity:unavailable"
		var image *applicationV3Source
		var recheck, close func() error
		if p.observeShippedImage != nil {
			verification, image, recheck, close = p.observeShippedImage(ctx, row.Spec.Program, prior)
		} else {
			verification, image, recheck, close, cause = observeShippedRegistrationImageCause(ctx, row.Spec.Program, prior)
		}
		if image == nil {
			verification, reason = registration.VerificationChanged, "identity:changed"
		} else {
			reason = ""
			if verification == registration.VerificationUnverified {
				reason = "identity:unverified"
			}
		}
		out := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
			current, ok := s.programRecord(row.Key)
			if !ok || current.Revision != revision {
				return registration.Change{Outcome: registration.OutcomeConflict, Reason: "revision"}, nil
			}
			if image != nil && s.programPending(row.Key, current) {
				verification, reason, image, cause = registration.VerificationChanged, "identity:changed", nil, "approval:pending"
			}
			if recheck != nil {
				if err := recheck(); err != nil {
					return registration.Change{Outcome: registration.OutcomeConflict, Reason: "identity:changed"}, nil
				}
			}
			if image == nil && current.Verification == verification && current.VerificationReason == reason {
				return registration.Change{Outcome: registration.OutcomeConflict, Reason: "kept"}, nil
			}
			current.Verification, current.VerificationReason, current.Verified = verification, reason, registrationTime(d.now())
			spec, err := s.programSpec(ctx, row.Key, s.entries)
			if err != nil {
				return registration.Change{}, err
			}
			s.keepProgramShape(row.Key, current, spec)
			if image != nil {
				s.keepProgramSource(row.Key, *image)
			}
			return registration.Change{Outcome: registration.OutcomeApplied, Revision: current.Revision}, nil
		})
		if close != nil {
			if err := close(); err != nil {
				p.report(err)
			}
		}
		if out.Outcome == registration.OutcomeUnavailable {
			return errors.New("registration observation: " + out.Reason)
		}
		if image != nil && out.Outcome == registration.OutcomeApplied {
			updated()
		}
		if image == nil {
			unresolved(row.Key, revision, cause)
		}
	}
	return nil
}
