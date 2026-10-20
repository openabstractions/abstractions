package main

import (
	"context"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sort"

	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func registrationHasFacet(s registration.RegistrationSpec, f registration.FacetReference) bool {
	switch f.Kind {
	case registration.FacetKindProvider:
		return slices.ContainsFunc(s.Providers, func(v registration.ProviderFacet) bool { return v.Name == f.Name })
	case registration.FacetKindHost:
		return slices.ContainsFunc(s.Servers, func(v registration.ServerFacet) bool { return v.Name == f.Name })
	case registration.FacetKindRemote:
		return slices.ContainsFunc(s.Remotes, func(v registration.RemoteFacet) bool { return v.Name == f.Name })
	case registration.FacetKindApplication:
		return slices.ContainsFunc(s.Applications, func(v registration.ApplicationFacet) bool { return v.Name == f.Name })
	}
	return false
}

func registrationReferences(s registration.RegistrationSpec) []registration.FacetReference {
	out := []registration.FacetReference{}
	for _, f := range s.Providers {
		out = append(out, registration.FacetReference{Kind: registration.FacetKindProvider, Name: f.Name})
	}
	for _, f := range s.Servers {
		out = append(out, registration.FacetReference{Kind: registration.FacetKindHost, Name: f.Name})
	}
	for _, f := range s.Remotes {
		out = append(out, registration.FacetReference{Kind: registration.FacetKindRemote, Name: f.Name})
	}
	for _, f := range s.Applications {
		out = append(out, registration.FacetReference{Kind: registration.FacetKindApplication, Name: f.Name})
	}
	return out
}

func registrationFacetValue(spec registration.RegistrationSpec, ref registration.FacetReference) any {
	switch ref.Kind {
	case registration.FacetKindProvider:
		for _, f := range spec.Providers {
			if f.Name == ref.Name {
				return f
			}
		}
	case registration.FacetKindHost:
		for _, f := range spec.Servers {
			if f.Name == ref.Name {
				return f
			}
		}
	case registration.FacetKindRemote:
		for _, f := range spec.Remotes {
			if f.Name == ref.Name {
				return f
			}
		}
	case registration.FacetKindApplication:
		for _, f := range spec.Applications {
			if f.Name == ref.Name {
				return f
			}
		}
	}
	return nil
}

func registrationWithoutFacet(spec registration.RegistrationSpec, ref registration.FacetReference) registration.RegistrationSpec {
	switch ref.Kind {
	case registration.FacetKindProvider:
		spec.Providers = slices.DeleteFunc(slices.Clone(spec.Providers), func(f registration.ProviderFacet) bool { return f.Name == ref.Name })
	case registration.FacetKindHost:
		spec.Servers = slices.DeleteFunc(slices.Clone(spec.Servers), func(f registration.ServerFacet) bool { return f.Name == ref.Name })
	case registration.FacetKindRemote:
		spec.Remotes = slices.DeleteFunc(slices.Clone(spec.Remotes), func(f registration.RemoteFacet) bool { return f.Name == ref.Name })
	case registration.FacetKindApplication:
		spec.Applications = slices.DeleteFunc(slices.Clone(spec.Applications), func(f registration.ApplicationFacet) bool { return f.Name == ref.Name })
	}
	return spec
}

func registrationReplaceFacet(spec, incoming registration.RegistrationSpec, ref registration.FacetReference) registration.RegistrationSpec {
	spec = registrationWithoutFacet(spec, ref)
	switch value := registrationFacetValue(incoming, ref).(type) {
	case registration.ProviderFacet:
		spec.Providers = append(spec.Providers, value)
	case registration.ServerFacet:
		spec.Servers = append(spec.Servers, value)
	case registration.RemoteFacet:
		spec.Remotes = append(spec.Remotes, value)
	case registration.ApplicationFacet:
		spec.Applications = append(spec.Applications, value)
	}
	return spec
}

func registrationFacetSourceOf(s *applicationV3Registrations, key string, facet registration.FacetReference) registration.Source {
	if source, ok := s.programFacetSources[key][facet]; ok {
		return source
	}
	v, _ := s.programRecord(key)
	return v.Source
}
func registrationProtectedSource(source registration.Source) bool {
	return source == registration.SourceInstallation || source == registration.SourceProductRecord || source == registration.SourceBuiltIn
}

// Every registry edit enters the existing descriptors CAS. Callers have already
// captured native evidence and authorization; no external owner runs under CAS.
func (d *applicationDirectory) editProgramRegistration(ctx context.Context, edit func(*applicationV3Registrations, map[string]applicationV3Registration) (registration.Change, error)) registration.Change {
	out := registration.Change{Outcome: registration.OutcomeUnavailable, Reason: "registration_owner"}
	if d == nil || !d.lock(ctx) {
		return out
	}
	changed := false
	defer func() {
		callback := d.programsChanged
		d.unlock()
		if changed {
			if err := d.reconcileProviderConfigGrants(ctx); err != nil && d.programConfigReport != nil {
				d.programConfigReport(err)
			}
		}
		if changed && callback != nil {
			callback()
		}
	}()
	if d.v3 == nil {
		return out
	}
	err := d.withRegistrationStorageLocked(ctx, func(legacyRows map[string]legacy.ApplicationDescriptor) error {
		next := maps.Clone(d.v3.entries)
		var err error
		out, err = edit(d.v3, next)
		if err != nil || out.Outcome != registration.OutcomeApplied {
			return err
		}
		if err := d.v3.persistLocked(ctx, next); err != nil {
			return err
		}
		return d.projectApplicationV3Locked(legacyRows)
	})
	if err != nil {
		return registration.Change{Outcome: registration.OutcomeUnavailable, Reason: "persistence"}
	}
	if out.Outcome == registration.OutcomeApplied {
		changed = true
		d.notifyLocked()
	}
	return out
}

// publishProgramRegistration accepts only the receiving adapter's evidence.
// A request supplies a spec; its state, source and clocks never cross that seam.
// It is an operator's registration, made on the person's action: one pending
// approval asks the person to keep it.
func (d *applicationDirectory) publishProgramRegistration(ctx context.Context, expected string, create bool, candidate registration.Registration, image *applicationV3Source, declaredBy string) registration.Change {
	return d.publishProgramRegistrationReview(ctx, expected, create, candidate, image, declaredBy, false)
}

// deferQuestion leaves the Keep question unasked: a registration another
// program made, which waits for the person's review in the Panel (VISION
// 2026-10-06, amended 2026-11-10), and the service's captured group review,
// which binds the freshly committed revision to its actual person answer.
func (d *applicationDirectory) publishProgramRegistrationReview(ctx context.Context, expected string, create bool, candidate registration.Registration, image *applicationV3Source, declaredBy string, deferQuestion bool) registration.Change {
	needsKeep := candidate.State == registration.StatePendingApproval
	const currentRecipeImage = "unchanged"
	out := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, next map[string]applicationV3Registration) (registration.Change, error) {
		if reason := registrationSpecValid(candidate.Spec); reason != "" {
			return registration.Change{Outcome: registration.OutcomeInvalid, Reason: reason}, nil
		}
		old, exists := s.programRecord(candidate.Key)
		if s.programApprovals[candidate.Key].Removing {
			return registration.Change{Outcome: registration.OutcomeConflict, Reason: "removing"}, nil
		}
		if create && exists {
			return registration.Change{Outcome: registration.OutcomeConflict, Revision: old.Revision, Reason: "registration"}, nil
		}
		if !create && (!exists || expected == "" || old.Revision != expected) {
			return registration.Change{Outcome: registration.OutcomeConflict, Revision: old.Revision, Reason: "revision"}, nil
		}
		if _, opaque := s.legacyPrograms[candidate.Key]; opaque {
			return registration.Change{Outcome: registration.OutcomeUnavailable, Reason: errRegistrationEncodedSize.Error()}, nil
		}
		if !create && image != nil && image.ImageSHA256 != "" && *image == s.programSources[candidate.Key] &&
			old.Verification == candidate.Verification && old.VerificationReason == candidate.VerificationReason &&
			reflect.DeepEqual(old.Spec, candidate.Spec) {
			// The existing approval and revision are current. Leave storage
			// unchanged and return the same receipt after releasing the CAS.
			return registration.Change{Outcome: registration.OutcomeConflict, Revision: old.Revision, Reason: currentRecipeImage}, nil
		}
		for key, v := range s.programs {
			if key == candidate.Key {
				continue
			}
			for _, f := range registrationReferences(candidate.Spec) {
				if registrationHasFacet(v.Spec, f) {
					return registration.Change{Outcome: registration.OutcomeConflict, Reason: "facet:name"}, nil
				}
			}
		}
		if !exists && len(s.programs)+len(s.legacyPrograms) >= registrationMaximumPrograms {
			return registration.Change{Outcome: registration.OutcomeUnavailable, Reason: "bound:registrations"}, nil
		}
		served := s.lastServed(candidate.Key, old, exists)
		if exists {
			for _, facet := range registrationReferences(old.Spec) {
				source := registrationFacetSourceOf(s, candidate.Key, facet)
				if registrationProtectedSource(source) && !reflect.DeepEqual(registrationFacetValue(old.Spec, facet), registrationFacetValue(candidate.Spec, facet)) {
					return registration.Change{Outcome: registration.OutcomeConflict, Reason: "source:" + source.String()}, nil
				}
			}
			candidate.State = old.State
			if needsKeep && old.State != registration.StateDisabled {
				candidate.State = registration.StatePendingApproval
			}
			if old.Source == registration.SourceSelfRegistration {
				candidate.State = registration.StatePendingApproval
			}
		}
		// Application facets enter only through independently held manifest custody.
		// The native application receiver publishes its payload and this same index.
		for _, f := range candidate.Spec.Applications {
			e, ok := next[f.Name]
			if !ok || e.FileRevision == "" {
				return registration.Change{Outcome: registration.OutcomeUnsupported, Reason: "unsupported:not_implemented"}, nil
			}
		}
		generation, err := applicationToken()
		if err != nil {
			return registration.Change{}, err
		}
		revision, err := applicationToken()
		if err != nil {
			return registration.Change{}, err
		}
		candidate.Epoch = generation
		candidate.Revision = revision
		candidate.Applications = []registration.ApplicationReading{}
		candidate.Described = []registration.ServiceState{}
		candidate.Checks = nil
		conditions := map[registration.FacetReference]registrationFacetCondition{}
		for _, facet := range registrationReferences(candidate.Spec) {
			condition := registrationFacetCondition{Disabled: candidate.State == registration.StateDisabled, Pending: candidate.State == registration.StatePendingApproval}
			if exists {
				condition = registrationConditionOf(s, candidate.Key, facet)
			}
			if needsKeep {
				condition.Pending = true
			}
			conditions[facet] = condition
		}
		s.programs = maps.Clone(s.programs)
		s.programs[candidate.Key] = candidate
		s.programFacetConditions[candidate.Key] = conditions
		if needsKeep {
			s.programApprovals[candidate.Key] = registrationProgramApproval{Pending: true}
		}
		// The registration that served keeps serving while this change waits,
		// for the same program image only.
		priorImage := s.programSources[candidate.Key]
		sameImage := image == nil || image.FileKey == served.ServedImage &&
			(priorImage.ImageSHA256 == "" || image.ImageSHA256 == priorImage.ImageSHA256)
		if candidate.State == registration.StatePendingApproval && served.Served != "" && sameImage {
			approval := s.programApprovals[candidate.Key]
			approval.Served, approval.ServedImage, approval.ServedBy = served.Served, served.ServedImage, served.ServedBy
			s.programApprovals[candidate.Key] = approval
		}
		facetSources := map[registration.FacetReference]registration.Source{}
		for _, facet := range registrationReferences(candidate.Spec) {
			source := candidate.Source
			if exists {
				oldSource := registrationFacetSourceOf(s, candidate.Key, facet)
				if registrationProtectedSource(oldSource) {
					source = oldSource
				}
			}
			facetSources[facet] = source
		}
		s.programFacetSources[candidate.Key] = facetSources
		digests := map[registration.FacetReference]string{}
		for facet, source := range facetSources {
			if registrationProtectedSource(source) {
				digests[facet] = s.programFacetDigests[candidate.Key][facet]
			}
		}
		s.programFacetDigests[candidate.Key] = digests
		s.programDeclarers[candidate.Key] = declaredBy
		delete(s.programCompatibility, candidate.Key)
		delete(s.programInputDigests, candidate.Key)
		if image != nil {
			s.keepProgramSource(candidate.Key, *image)
		}
		for _, f := range candidate.Spec.Applications {
			e := next[f.Name]
			e.Revision = revision
			next[f.Name] = e
		}
		return registration.Change{Outcome: registration.OutcomeApplied, Revision: revision}, nil
	})
	if out.Outcome == registration.OutcomeConflict && out.Reason == currentRecipeImage {
		out.Outcome = registration.OutcomeApplied
		return out
	}
	if out.Outcome == registration.OutcomeApplied && needsKeep {
		out.Reason = "approval:pending"
		if d.askKeep != nil && !deferQuestion {
			if err := d.askKeep("program:" + candidate.Key); err != nil {
				out.Reason = "approval:unavailable"
			}
		}
	}
	return out
}

func (d *applicationDirectory) changeProgramFacet(ctx context.Context, expected string, facet registration.FacetReference, enabled *bool) registration.Change {
	if expected == "" || !providerName.MatchString(facet.Name) || !facet.Kind.Known() {
		return registration.Change{Outcome: registration.OutcomeInvalid, Reason: "facet_or_revision"}
	}
	return d.editProgramRegistration(ctx, func(s *applicationV3Registrations, next map[string]applicationV3Registration) (registration.Change, error) {
		key := ""
		var v registration.Registration
		for k, record := range s.programs {
			if registrationHasFacet(record.Spec, facet) {
				key = k
				v = record
				break
			}
		}
		if key == "" {
			return registration.Change{Outcome: registration.OutcomeNotFound, Reason: "facet"}, nil
		}
		if v.Revision != expected {
			return registration.Change{Outcome: registration.OutcomeConflict, Revision: v.Revision, Reason: "revision"}, nil
		}
		if enabled != nil && *enabled && s.programPending(key, v) {
			return registration.Change{Outcome: registration.OutcomeForbidden, Reason: "approval", Refusal: &rights.RefusalDetail{Source: rights.RefusalSourceRights, Decision: rights.RefusalDecisionPersonOnly, Ask: rights.RefusalAskAvailable, Action: ActionProviderManage}}, nil
		}
		targetSource := registrationFacetSourceOf(s, key, facet)
		if enabled == nil && registrationProtectedSource(targetSource) {
			return registration.Change{Outcome: registration.OutcomeConflict, Revision: v.Revision, Reason: "source:" + targetSource.String()}, nil
		}
		revision, err := applicationToken()
		if err != nil {
			return registration.Change{}, err
		}
		v.Revision = revision
		out := registration.Change{Outcome: registration.OutcomeApplied, Revision: revision}
		if enabled != nil {
			v.State = registration.StateDisabled
			if *enabled {
				v.State = registration.StateEnabled
			}
			conditions := maps.Clone(s.programFacetConditions[key])
			if conditions == nil {
				conditions = map[registration.FacetReference]registrationFacetCondition{}
			}
			for _, ref := range registrationReferences(v.Spec) {
				condition := registrationConditionOf(s, key, ref)
				condition.Disabled = !*enabled
				conditions[ref] = condition
			}
			s.programFacetConditions[key] = conditions
		} else {
			switch facet.Kind {
			case registration.FacetKindProvider:
				v.Spec.Providers = slices.DeleteFunc(slices.Clone(v.Spec.Providers), func(f registration.ProviderFacet) bool { return f.Name == facet.Name })
			case registration.FacetKindHost:
				v.Spec.Servers = slices.DeleteFunc(slices.Clone(v.Spec.Servers), func(f registration.ServerFacet) bool { return f.Name == facet.Name })
			case registration.FacetKindRemote:
				v.Spec.Remotes = slices.DeleteFunc(slices.Clone(v.Spec.Remotes), func(f registration.RemoteFacet) bool { return f.Name == facet.Name })
			case registration.FacetKindApplication:
				v.Spec.Applications = slices.DeleteFunc(slices.Clone(v.Spec.Applications), func(f registration.ApplicationFacet) bool { return f.Name == facet.Name })
				delete(next, facet.Name)
			}
			id, err := applicationToken()
			if err != nil {
				return registration.Change{}, err
			}
			receipt := registration.RemovalReceipt{ID: id, Facet: facet, Revision: revision, Accepted: registrationTime(d.now()), Cleanup: []registration.Cleanup{{Owner: "abstraction.facade", State: registration.CleanupStatePending, Reason: "unsupported:not_implemented"}, {Owner: "abstraction.rights", State: registration.CleanupStatePending, Reason: "unsupported:not_implemented"}, {Owner: "abstraction.asks", State: registration.CleanupStatePending, Reason: "unsupported:not_implemented"}}}
			if len(s.removals) >= 64 {
				return registration.Change{Outcome: registration.OutcomeUnavailable, Reason: "bound:removals"}, nil
			}
			s.removals = append(slices.Clone(s.removals), receipt)
			out.Removal = &receipt
		}
		s.programs = maps.Clone(s.programs)
		if registrationFacetCount(v.Spec) == 0 {
			delete(s.programs, key)
		} else {
			s.programs[key] = v
		}
		for _, f := range v.Spec.Applications {
			if e, ok := next[f.Name]; ok {
				e.Revision = revision
				if enabled != nil {
					if *enabled {
						e.State = "enabled"
					} else {
						e.State = "disabled"
					}
				}
				next[f.Name] = e
			}
		}
		return out, nil
	})
}

var errRegistrationEncodedSize = errors.New("unsupported:encoded_size")

func (d *applicationDirectory) programRegistrations(ctx context.Context) ([]registration.Registration, []registration.RemovalReceipt, map[string]string, string, <-chan struct{}, error) {
	return d.programRegistrationsFor(ctx, false)
}

func (d *applicationDirectory) programRegistrationsFor(ctx context.Context, allowUnadmitted bool) ([]registration.Registration, []registration.RemovalReceipt, map[string]string, string, <-chan struct{}, error) {
	var describe registrationProgramDescriptions
	records, removals, declarers, revision, changed, err := func() ([]registration.Registration, []registration.RemovalReceipt, map[string]string, string, <-chan struct{}, error) {
		if d == nil || !d.lock(ctx) {
			return nil, nil, nil, "", nil, errors.New("registration owner")
		}
		defer d.unlock()
		if d.v3 == nil {
			return nil, nil, nil, "", d.changed, errors.New("registration owner")
		}
		describe = d.v3.describe
		var records []registration.Registration
		err := d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
			if !allowUnadmitted && len(d.v3.legacyPrograms) > 0 {
				return errRegistrationEncodedSize
			}
			d.expireLocked()
			for _, key := range slices.Sorted(maps.Keys(d.v3.programs)) {
				v := d.v3.programs[key]
				b, err := registrationEncoded(registration.EncodeRegistration, &v)
				if err != nil {
					return err
				}
				copy, err := registration.DecodeRegistration(b)
				if err != nil {
					return err
				}
				copy.Applications = []registration.ApplicationReading{}
				for _, facet := range copy.Spec.Applications {
					reading := registration.ApplicationReading{Name: facet.Name, ManifestDigest: d.v3.files[facet.Name].Digest, Instances: []legacy.ApplicationInstance{}, OwnedInstances: []string{}}
					for _, lease := range d.instances {
						if lease.application == facet.Name {
							instance := lease.value
							instance.Interfaces = slices.Clone(instance.Interfaces)
							instance.Contexts = slices.Clone(instance.Contexts)
							reading.Instances = append(reading.Instances, instance)
						}
					}
					sort.Slice(reading.Instances, func(i, j int) bool { return reading.Instances[i].Instance < reading.Instances[j].Instance })
					copy.Applications = append(copy.Applications, reading)
				}
				records = append(records, *copy)
			}
			return nil
		})
		return records, slices.Clone(d.v3.removals), maps.Clone(d.v3.programDeclarers), d.v3.revision, d.changed, err
	}()
	// Supervisor observation re-enters the directory's current binding reader.
	// Release the directory lock before taking the provider holder lock.
	if err == nil && describe != nil {
		records = describe(ctx, records)
	}
	return records, removals, declarers, revision, changed, err
}

func (d *applicationDirectory) programProviderSnapshot(ctx context.Context) ([]providerFile, []registration.RemovalReceipt, string, error) {
	if d == nil || !d.lock(ctx) {
		return nil, nil, "", errors.New("registration owner")
	}
	defer d.unlock()
	if d.v3 == nil {
		return nil, nil, "", nil
	}
	files := []providerFile{}
	err := d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		seen := map[string]bool{}
		for _, key := range d.v3.programKeys() {
			record, _ := d.v3.programRecord(key)
			if _, opaque := d.v3.legacyPrograms[key]; opaque {
				spec, err := d.v3.programSpec(ctx, key, d.v3.entries)
				if err != nil {
					return err
				}
				record.Spec = spec
			}
			projected := registrationProviderFiles(record)
			for i := range projected {
				kind := registration.FacetKindProvider
				if projected[i].Declaration.host() {
					kind = registration.FacetKindHost
				}
				if projected[i].Declaration.remote() {
					kind = registration.FacetKindRemote
				}
				ref := registration.FacetReference{Kind: kind, Name: projected[i].Declaration.Name}
				projected[i].Source = registrationSourceWord(registrationFacetSourceOf(d.v3, key, ref))
				condition := registrationConditionOf(d.v3, key, ref)
				projected[i].Disabled = projected[i].Disabled || condition.Disabled || condition.Pending || d.v3.programApprovals[key].Pending
				projected[i].disabledWhy = registrationDisabledWhy(record, condition.Pending || d.v3.programApprovals[key].Pending)
			}
			for _, legacy := range d.v3.programCompatibility[key] {
				if legacy.Declaration.remote() && registrationHasFacet(record.Spec, registration.FacetReference{Kind: registration.FacetKindRemote, Name: legacy.Declaration.Name}) {
					legacy.Generation = record.Epoch
					legacy.Source = registrationSourceWord(registrationFacetSourceOf(d.v3, key, registration.FacetReference{Kind: registration.FacetKindRemote, Name: legacy.Declaration.Name}))
					legacy.Disabled = record.State != registration.StateEnabled || record.Verification == registration.VerificationMissing || record.Verification == registration.VerificationChanged
					legacy.disabledWhy = registrationDisabledWhy(record, false)
					legacy.RegistrationKey = record.Key
					projected = append(projected, legacy)
					continue
				}
				for i := range projected {
					if projected[i].Declaration.Name == legacy.Declaration.Name && projected[i].Declaration.role() == legacy.Declaration.role() {
						facet := projected[i].ProviderFacet
						state := projected[i]
						legacy.Generation = record.Epoch
						legacy.Source = state.Source
						legacy.Disabled = state.Disabled
						legacy.disabledWhy = state.disabledWhy
						legacy.ProviderFacet = facet
						legacy.RegistrationKey = record.Key
						projected[i] = legacy
						break
					}
				}
			}
			// A waiting change lists as declared, and serving uses the
			// registration that served before it (servingView).
			if served, ok := d.v3.servedWhilePending(key, record); ok {
				serving := d.v3.servedFiles(key, served, projected)
				for i := range projected {
					for j := range serving {
						if serving[j].Declaration.Name == projected[i].Declaration.Name {
							projected[i].serving = &serving[j]
						}
					}
				}
			}
			for _, file := range projected {
				if seen[file.Declaration.Name] {
					continue
				}
				seen[file.Declaration.Name] = true
				if file.DeclaredBy == "" && !slices.ContainsFunc(d.v3.programCompatibility[key], func(original providerFile) bool {
					return original.Declaration.Name == file.Declaration.Name && original.Declaration.role() == file.Declaration.role()
				}) {
					file.DeclaredBy = d.v3.programDeclarers[key]
				}
				files = append(files, file)
			}
		}
		return nil
	})
	return files, slices.Clone(d.v3.removals), d.v3.revision, err
}

func (d *applicationDirectory) programImage(ctx context.Context, key string) (*applicationV3Source, string, error) {
	if d == nil || !d.lock(ctx) {
		return nil, "", errors.New("registration owner")
	}
	defer d.unlock()
	if d.v3 == nil {
		return nil, "", errors.New("registration owner")
	}
	var image *applicationV3Source
	var revision string
	err := d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		if source, ok := d.v3.programSources[key]; ok {
			copy := source
			image = &copy
		}
		v, _ := d.v3.programRecord(key)
		revision = v.Revision
		return nil
	})
	return image, revision, err
}
