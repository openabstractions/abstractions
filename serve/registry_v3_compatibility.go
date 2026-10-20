package main

import (
	"context"
	"errors"
	"maps"
	"slices"

	cas "github.com/openabstractions/abstraction-cas/go"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
)

func decodeLegacyFacetPayload(raw []byte) (providerFile, error) {
	var payload registrationLegacyFacetPayload
	if registrationStrict(raw, &payload) != nil || payload.File.Version != providerFileVersion {
		return providerFile{}, errors.New("registrations: invalid legacy payload")
	}
	payload.File.ProviderFacet = payload.ProviderFacet
	if payload.ProviderFacet != nil {
		if validProviderFacet(payload.File.Declaration.Program, *payload.ProviderFacet) != "" {
			return providerFile{}, errors.New("registrations: invalid typed projection")
		}
	} else if validProviderDeclaration(payload.File.Declaration) != "" {
		return providerFile{}, errors.New("registrations: invalid legacy declaration")
	}
	return payload.File, nil
}

func (s *applicationV3Registrations) programRecord(key string) (registration.Registration, bool) {
	if v, ok := s.programs[key]; ok {
		return v, true
	}
	v, ok := s.legacyPrograms[key]
	return v, ok
}

func (s *applicationV3Registrations) programKeys() []string {
	keys := map[string]bool{}
	for key := range s.programs {
		keys[key] = true
	}
	for key := range s.legacyPrograms {
		keys[key] = true
	}
	return slices.Sorted(maps.Keys(keys))
}

func (s *applicationV3Registrations) programSpec(ctx context.Context, key string, entries map[string]applicationV3Registration) (registration.RegistrationSpec, error) {
	if record, ok := s.programs[key]; ok {
		return record.Spec, nil
	}
	record, ok := s.legacyPrograms[key]
	if !ok {
		return registration.RegistrationSpec{}, errors.New("registrations: program missing")
	}
	spec := registration.RegistrationSpec{Program: record.Spec.Program, Providers: []registration.ProviderFacet{}, Servers: []registration.ServerFacet{}, Remotes: []registration.RemoteFacet{}, Applications: []registration.ApplicationFacet{}}
	for _, file := range s.programCompatibility[key] {
		_, part, err := legacyRegistrationSpec(file)
		if err != nil {
			return spec, err
		}
		spec.Providers = append(spec.Providers, part.Providers...)
		spec.Servers = append(spec.Servers, part.Servers...)
		spec.Remotes = append(spec.Remotes, part.Remotes...)
	}
	for name, e := range entries {
		f := s.files[name]
		if old, ok := s.entries[name]; !ok || old.FileRevision != e.FileRevision {
			raw, err := cas.ReadLimitContext(ctx, s.filePath(e.FileRevision), applicationV3ManifestFileLimit)
			if err != nil || applicationV3Strict(raw, &f) != nil {
				return spec, errors.New("registrations: manifest custody")
			}
		}
		if identity.NormalizeSubjectProgram(f.Source.Program) != key {
			continue
		}
		manifest, err := registration.DecodeManifest(f.Raw)
		if err != nil {
			return spec, err
		}
		spec.Applications = append(spec.Applications, registration.ApplicationFacet{Name: name, Manifest: *manifest})
	}
	return spec, nil
}

// A compatibility-only state retains its entire old projection under the same
// index/CAS. It has no publishable partial Spec. Every subsequent receiving edit
// rebuilds the whole spec and promotes it when the generated bound admits it.
func (s *applicationV3Registrations) keepProgramShape(key string, record registration.Registration, spec registration.RegistrationSpec) {
	if registrationSpecValid(spec) == "" {
		record.Spec = spec
		s.programs[key] = record
		delete(s.legacyPrograms, key)
		return
	}
	record.Spec = registration.RegistrationSpec{Program: spec.Program}
	s.legacyPrograms[key] = record
	delete(s.programs, key)
}

// keepProgramSource records the image provenance of the program keyed key, the
// record's own program. The program index requires retained provenance to name
// the program exactly as the record does. A source whose normalized spelling
// is the record's, such as an 8.3 alias of it, is stored under the record's
// spelling; any other source keeps its own, and the index refuses it as
// before.
func (s *applicationV3Registrations) keepProgramSource(key string, image applicationV3Source) {
	if image.Program != key && identity.NormalizeSubjectProgram(image.Program) == key {
		image.Program = key
	}
	// A legacy observation or manifest facet carries no recipe approval pin.
	// Keep the expected bytes while the managed recipe remains in the program.
	if prior := s.programSources[key]; image.ImageSHA256 == "" && prior.ImageSHA256 != "" {
		_, opaque := s.legacyPrograms[key]
		if opaque || managedPyTorchRecipes(s.programs[key].Spec) {
			image.ImageSHA256 = prior.ImageSHA256
		}
	}
	s.programSources[key] = image
}

func (s *applicationV3Registrations) completeCompatibility(key string, record registration.Registration) {
	files := slices.Clone(s.programCompatibility[key])
	for _, file := range registrationProviderFiles(record) {
		if slices.ContainsFunc(files, func(old providerFile) bool {
			return old.Declaration.Name == file.Declaration.Name && old.Declaration.role() == file.Declaration.role()
		}) {
			continue
		}
		file.DeclaredBy = s.programDeclarers[key]
		files = append(files, file)
	}
	s.programCompatibility[key] = files
}

func legacyFacetPayload(file providerFile) ([]byte, error) {
	return registrationJSON(registrationLegacyFacetPayload{File: file, ProviderFacet: file.ProviderFacet})
}

func registrationConditionOf(s *applicationV3Registrations, key string, facet registration.FacetReference) registrationFacetCondition {
	if condition, ok := s.programFacetConditions[key][facet]; ok {
		return condition
	}
	record, _ := s.programRecord(key)
	return registrationFacetCondition{Disabled: record.State == registration.StateDisabled, Pending: record.State == registration.StatePendingApproval}
}
