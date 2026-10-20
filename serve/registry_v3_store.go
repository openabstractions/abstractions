package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	store "github.com/openabstractions/abstraction-cas/go/api"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	digestfmt "github.com/openabstractions/abstraction-storage/go/digest"
)

// This is the existing registration index publication point. The application
// entries retain manifest custody and compatibility bookkeeping; programs own
// the shared spec, revision, generation and lifecycle once accepted here.
type registrationProgramIndex struct {
	Version  int                                  `json:"version"`
	Entries  map[string]applicationV3Registration `json:"entries"`
	Programs []registrationProgramFile            `json:"programs"`
	Removals []registrationProgramFile            `json:"removals"`
	Revision string                               `json:"revision"`
	Prepared []registrationProgramFile            `json:"prepared,omitempty"`
}

type registrationProgramFile struct {
	Revision string `json:"revision"`
	Hash     string `json:"hash"`
	Bytes    int64  `json:"bytes,omitempty"`
}

type registrationProgramPayload struct {
	Registration  json.RawMessage              `json:"registration,omitempty"`
	Legacy        *registration.Registration   `json:"legacy,omitempty"`
	LegacyFiles   []registrationProgramFile    `json:"legacy_files,omitempty"`
	Image         *applicationV3Source         `json:"image,omitempty"`
	DeclaredBy    string                       `json:"declared_by,omitempty"`
	Compatibility []providerFile               `json:"compatibility,omitempty"`
	InputDigest   string                       `json:"input_digest,omitempty"`
	FacetSources  []registrationFacetSource    `json:"facet_sources,omitempty"`
	Approval      *registrationProgramApproval `json:"approval,omitempty"`
}

type registrationFacetSource struct {
	Facet    registration.FacetReference `json:"facet"`
	Source   registration.Source         `json:"source"`
	Digest   string                      `json:"digest,omitempty"`
	Disabled bool                        `json:"disabled,omitempty"`
	Pending  bool                        `json:"pending,omitempty"`
}
type registrationFacetCondition struct{ Disabled, Pending bool }

const registrationRecordFileLimit = 1 << 20
const registrationMaximumPrograms = maxProviders + 64
const registrationReferenceEncodingLimit = 34 + 48 + 64 + 19

// Preserve the old complete64KiB application index plus exactly the new
// bounded reference arrays and their fixed envelope. Payload bytes stay in
// their immutable files under each existing owner's actual measured CAS bound.
const registrationIndexLimit = applicationV3LegacyIndexLimit + (registrationMaximumPrograms+64+maxProviders+1)*(registrationReferenceEncodingLimit+1) + 80 + 48

// Existing global families:64 application manifests,128 program payloads,
// 64 legacy provider payloads,64 receipts and one interrupted64-facet import.
const registrationDirectoryPayloadLimit = (64 + 1) + registrationMaximumPrograms + maxProviders + 64 + maxProviders + 1
const registrationDirectoryEntryLimit = 2*registrationDirectoryPayloadLimit + 2

type registrationLegacyFacetPayload struct {
	File          providerFile                `json:"file"`
	ProviderFacet *registration.ProviderFacet `json:"provider_facet,omitempty"`
}

func registrationIntent(spec registration.RegistrationSpec, conditions map[registration.FacetReference]registrationFacetCondition, fallback registration.State) registration.State {
	refs := registrationReferences(spec)
	if len(refs) == 0 {
		return fallback
	}
	pending := false
	for _, ref := range refs {
		condition := conditions[ref]
		if !condition.Disabled {
			if !condition.Pending {
				return registration.StateEnabled
			}
			pending = true
		}
	}
	if pending {
		return registration.StatePendingApproval
	}
	return registration.StateDisabled
}

// Existing provider precedence is operator, product, installation. This
// aggregate observation supplies no target authority: edits consult each
// facet's actual receiving provenance retained in this same payload.
func registrationAggregateSource(sources map[registration.FacetReference]registration.Source, fallback registration.Source) registration.Source {
	for _, source := range sources {
		if source == registration.SourceBuiltIn {
			return source
		}
	}
	for _, source := range []registration.Source{registration.SourceOperator, registration.SourceProductRecord, registration.SourceInstallation, registration.SourceSelfRegistration} {
		for _, actual := range sources {
			if actual == source {
				return source
			}
		}
	}
	return fallback
}

func decodeRetainedProgramEnvelope(raw []byte) (registrationProgramPayload, error) {
	projected, err := registrationRetainedProgramPayload(raw)
	if err != nil {
		return registrationProgramPayload{}, errors.New("registrations: invalid retained program payload")
	}
	var payload registrationProgramPayload
	if registrationStrict(projected, &payload) != nil {
		return payload, errors.New("registrations: invalid program payload")
	}
	return payload, nil
}

func decodeProgramPayload(raw []byte) (*registration.Registration, *applicationV3Source, error) {
	payload, err := decodeRetainedProgramEnvelope(raw)
	if err != nil {
		return nil, nil, err
	}
	var v *registration.Registration
	if payload.Legacy != nil {
		v = payload.Legacy
		if len(payload.Registration) != 0 || len(payload.LegacyFiles) > maxProviders || v.Key == "" || len(v.Revision) != 48 || v.Epoch == "" || !v.Source.Known() || !v.State.Known() || !v.Verification.Known() || len(registrationReferences(v.Spec)) != 0 {
			return nil, nil, errors.New("registrations: invalid compatibility-only program")
		}
		if _, err := time.Parse(time.RFC3339Nano, v.Accepted); err != nil {
			return nil, nil, err
		}
		if _, err := time.Parse(time.RFC3339Nano, v.Verified); err != nil {
			return nil, nil, err
		}
	} else {
		var err error
		v, err = registration.DecodeRegistration(payload.Registration)
		if err != nil {
			return nil, nil, err
		}
	}
	if payload.Image != nil && (!identity.ValidSubjectProgram(payload.Image.Program) || payload.Image.Program != v.Spec.Program || payload.Image.FileKey == "") {
		return nil, nil, errors.New("registrations: invalid image provenance")
	}
	if len(payload.Compatibility) > 64 {
		return nil, nil, errors.New("registrations: compatibility capacity")
	}
	for _, f := range payload.Compatibility {
		if f.Version != providerFileVersion || validProviderDeclaration(f.Declaration) != "" {
			return nil, nil, errors.New("registrations: invalid compatibility projection")
		}
	}
	return v, payload.Image, nil
}

func registrationJSON(v any) ([]byte, error) {
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	if err := e.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte{'\n'}), nil
}

func registrationStrict(raw []byte, v any) error {
	if applicationV3Strict(raw, v) == nil {
		return nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	canonical, err := registrationJSON(v)
	if err != nil || !bytes.Equal(raw, canonical) {
		return errors.New("registrations: invalid retained encoding")
	}
	return nil
}

func registrationTime(t time.Time) string { return registration.FormatTimestamp(t) }

func registrationEncoded[T any](encode func(*T) []byte, value *T) (b []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			b = nil
			err = errors.New("registration: encoded bound or shape")
		}
	}()
	return encode(value), nil
}

func (s *applicationV3Registrations) readProgramIndex(ctx context.Context, raw []byte) (map[string]applicationV3Registration, error) {
	s.programs = map[string]registration.Registration{}
	s.programFiles = map[string]registrationProgramFile{}
	s.programSources = map[string]applicationV3Source{}
	s.programDeclarers = map[string]string{}
	s.programCompatibility = map[string][]providerFile{}
	s.programInputDigests = map[string]string{}
	s.programFacetSources = map[string]map[registration.FacetReference]registration.Source{}
	s.programFacetDigests = map[string]map[registration.FacetReference]string{}
	s.programFacetConditions = map[string]map[registration.FacetReference]registrationFacetCondition{}
	s.legacyPrograms = map[string]registration.Registration{}
	s.legacyProgramFiles = map[string][]registrationProgramFile{}
	s.programApprovals = map[string]registrationProgramApproval{}
	s.removalFiles = []registrationProgramFile{}
	s.removals = []registration.RemovalReceipt{}
	s.revision = ""
	s.indexRaw = slices.Clone(raw)
	s.preparedFiles = []registrationProgramFile{}
	entries := map[string]applicationV3Registration{}
	if raw == nil {
		return entries, nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		return nil, errors.New("registrations: corrupt index")
	}
	if version, ok := fields["version"]; !ok || len(version) > 0 && version[0] == '{' {
		if applicationV3Strict(raw, &entries) != nil {
			return nil, errors.New("applications@3: corrupt index")
		}
		return entries, nil
	}
	var index registrationProgramIndex
	if registrationStrict(raw, &index) != nil || index.Version != 1 || index.Programs == nil || index.Entries == nil || len(index.Programs) > registrationMaximumPrograms || len(index.Removals) > 64 || len(index.Revision) != 48 {
		return nil, errors.New("registrations: invalid program index")
	}
	if len(index.Prepared) > maxProviders+1 {
		return nil, errors.New("registrations: preparation capacity")
	}
	for _, ref := range index.Prepared {
		if !registrationFileReferenceValid(ref) || ref.Bytes < 1 {
			return nil, errors.New("registrations: invalid preparation reference")
		}
	}
	s.preparedFiles = index.Prepared
	providerCount := 0
	for _, ref := range index.Programs {
		b, err := s.readProgramFile(ctx, ref)
		if err != nil {
			return nil, err
		}
		v, image, err := decodeProgramPayload(b)
		payload, payloadErr := decodeRetainedProgramEnvelope(b)
		if payloadErr != nil {
			return nil, errors.New("registrations: invalid payload")
		}
		if err != nil || v.Revision == "" || v.Epoch == "" || payload.Legacy == nil && registrationSpecValid(v.Spec) != "" {
			return nil, errors.New("registrations: invalid program record")
		}
		if _, ok := s.programFiles[v.Key]; ok {
			return nil, errors.New("registrations: duplicate program")
		}
		if payload.Legacy == nil {
			providerCount += len(v.Spec.Providers) + len(v.Spec.Servers) + len(v.Spec.Remotes)
		} else {
			providerCount += len(payload.LegacyFiles)
		}
		if providerCount > maxProviders || len(payload.FacetSources) > maxProviders+64 {
			return nil, errors.New("registrations: existing directory capacity")
		}
		if payload.Legacy == nil {
			s.programs[v.Key] = *v
		} else {
			s.legacyPrograms[v.Key] = *v
			for _, ref := range payload.LegacyFiles {
				data, err := s.readMeasuredProgramFile(ctx, ref)
				if err != nil {
					return nil, err
				}
				file, err := decodeLegacyFacetPayload(data)
				if err != nil {
					return nil, err
				}
				s.programCompatibility[v.Key] = append(s.programCompatibility[v.Key], file)
			}
			s.legacyProgramFiles[v.Key] = payload.LegacyFiles
		}
		s.programFiles[v.Key] = ref
		if image != nil {
			s.programSources[v.Key] = *image
		}
		s.programDeclarers[v.Key] = payload.DeclaredBy
		if payload.Approval != nil {
			s.programApprovals[v.Key] = *payload.Approval
		}
		if payload.Legacy == nil {
			s.programCompatibility[v.Key] = payload.Compatibility
		}
		s.programInputDigests[v.Key] = payload.InputDigest
		sources := map[registration.FacetReference]registration.Source{}
		for _, ref := range registrationReferences(v.Spec) {
			sources[ref] = v.Source
		}
		for _, fact := range payload.FacetSources {
			if payload.Legacy == nil && !registrationHasFacet(v.Spec, fact.Facet) || !fact.Source.Known() || !fact.Facet.Kind.Known() || !providerName.MatchString(fact.Facet.Name) {
				return nil, errors.New("registrations: invalid facet provenance")
			}
			sources[fact.Facet] = fact.Source
		}
		s.programFacetSources[v.Key] = sources
		digests := map[registration.FacetReference]string{}
		for _, fact := range payload.FacetSources {
			digests[fact.Facet] = fact.Digest
		}
		s.programFacetDigests[v.Key] = digests
		conditions := map[registration.FacetReference]registrationFacetCondition{}
		for _, facet := range registrationReferences(v.Spec) {
			conditions[facet] = registrationFacetCondition{Disabled: v.State == registration.StateDisabled, Pending: v.State == registration.StatePendingApproval}
		}
		for _, fact := range payload.FacetSources {
			conditions[fact.Facet] = registrationFacetCondition{Disabled: fact.Disabled, Pending: fact.Pending}
		}
		s.programFacetConditions[v.Key] = conditions
	}
	for _, ref := range index.Removals {
		b, err := s.readProgramFile(ctx, ref)
		if err != nil {
			return nil, err
		}
		v, err := registration.DecodeRemovalReceipt(b)
		if err != nil {
			return nil, err
		}
		s.removals = append(s.removals, *v)
		s.removalFiles = append(s.removalFiles, ref)
	}
	s.revision = index.Revision
	return index.Entries, nil
}

// projectApplicationEntry projects one applications@3 entry into its
// program's record in states: the application facet, its revision, source
// and condition. A manifest kept under an earlier spelling is read through
// the retained projection. A file written before the registry recorded
// acceptance times (2026-11-02) takes its file's modification time for both;
// such a file whose bytes are no manifest is left out, as before.
func (s *applicationV3Registrations) projectApplicationEntry(states map[string]registration.Registration, name string, e, old applicationV3Registration, exists bool, f applicationV3ManifestFile, loading bool) error {
	raw, _ := applicationV3RetainedManifest(f.Raw)
	manifest, err := registration.DecodeManifest(raw)
	accepted, verified := f.Accepted, f.Verified
	if err != nil && (accepted == "" || verified == "") {
		// A file without acceptance times whose bytes are no manifest has
		// no record to project.
		return nil
	}
	if err != nil {
		return err
	}
	if accepted == "" || verified == "" {
		info, err := os.Stat(s.filePath(e.FileRevision))
		if err != nil {
			return errors.New("registrations: manifest custody")
		}
		at := registrationTime(info.ModTime())
		if accepted == "" {
			accepted = at
		}
		if verified == "" {
			verified = at
		}
	}
	key := identity.NormalizeSubjectProgram(f.Source.Program)
	source := registration.SourceOperator
	if e.Source == "self" {
		source = registration.SourceSelfRegistration
	}
	if e.Source == "installation" {
		source = registration.SourceInstallation
	}
	v, kept := states[key]
	if kept && loading {
		// A load adds the facet to the program's existing record and leaves
		// its approval, verification and state as recorded; the entry takes
		// the record's revision, as a write gives it.
		facet := registration.FacetReference{Kind: registration.FacetKindApplication, Name: name}
		v.Spec = registrationReplaceFacet(v.Spec, registration.RegistrationSpec{Applications: []registration.ApplicationFacet{{Name: name, Manifest: *manifest}}}, facet)
		if s.programFacetSources[key] == nil {
			s.programFacetSources[key] = map[registration.FacetReference]registration.Source{}
		}
		s.programFacetSources[key][facet] = source
		if s.programFacetConditions[key] == nil {
			s.programFacetConditions[key] = map[registration.FacetReference]registrationFacetCondition{}
		}
		s.programFacetConditions[key][facet] = registrationFacetCondition{Disabled: e.State == "disabled" || e.State == "removing", Pending: e.State == "pending"}
		e.Revision = v.Revision
		s.entries[name] = e
		states[key] = v
		return nil
	}
	if !kept {
		v = registration.Registration{Key: key, Spec: registration.RegistrationSpec{Program: key, Providers: []registration.ProviderFacet{}, Servers: []registration.ServerFacet{}, Remotes: []registration.RemoteFacet{}, Applications: []registration.ApplicationFacet{}}, Source: source, IdentityEvidence: registration.IdentityEvidenceProgram, Accepted: accepted, Verified: verified, Verification: registration.VerificationVerified}
	}
	facet := registration.FacetReference{Kind: registration.FacetKindApplication, Name: name}
	part := registration.RegistrationSpec{Applications: []registration.ApplicationFacet{{Name: name, Manifest: *manifest}}}
	v.Spec = registrationReplaceFacet(v.Spec, part, facet)
	v.Revision = e.Revision
	if !kept || old.FileRevision != e.FileRevision {
		v.Epoch = e.FileRevision
		v.Accepted = accepted
		v.Verified = verified
		v.Verification = registration.VerificationVerified
		v.VerificationReason = ""
		s.programApprovals[key] = registrationProgramApproval{}
	}
	if s.programFacetSources[key] == nil {
		s.programFacetSources[key] = map[registration.FacetReference]registration.Source{}
	}
	s.programFacetSources[key][facet] = source
	if s.programFacetConditions[key] == nil {
		s.programFacetConditions[key] = map[registration.FacetReference]registrationFacetCondition{}
	}
	if !exists || old.State != e.State || old.FileRevision != e.FileRevision {
		s.programFacetConditions[key][facet] = registrationFacetCondition{Disabled: e.State == "disabled" || e.State == "removing", Pending: e.State == "pending"}
	}
	s.keepProgramSource(key, f.Source)
	states[key] = v
	return nil
}

// projectUncoveredLocked gives every applications@3 entry that no program
// record covers its record in memory. An entry kept before the registry
// projected applications (2026-11-02), such as a self-registered build under
// the earlier manifest spelling, otherwise refused every applications@3 read
// that could see it, and with them its removal. The next write persists the
// record.
func (s *applicationV3Registrations) projectUncoveredLocked(ctx context.Context) error {
	covered := map[string]bool{}
	for _, v := range s.programs {
		for _, f := range v.Spec.Applications {
			covered[f.Name] = true
		}
	}
	for key := range s.legacyPrograms {
		spec, err := s.programSpec(ctx, key, s.entries)
		if err != nil {
			return err
		}
		for _, f := range spec.Applications {
			covered[f.Name] = true
		}
	}
	for _, name := range slices.Sorted(maps.Keys(s.entries)) {
		if covered[name] {
			continue
		}
		e := s.entries[name]
		key := identity.NormalizeSubjectProgram(s.files[name].Source.Program)
		_, kept := s.programs[key]
		if err := s.projectApplicationEntry(s.programs, name, e, applicationV3Registration{}, false, s.files[name], true); err != nil {
			return err
		}
		if v, ok := s.programs[key]; ok && !kept {
			v.State = registrationIntent(v.Spec, s.programFacetConditions[key], v.State)
			s.programs[key] = v
		}
	}
	return nil
}

// dropMovedApplicationFacets keeps each applications@3 entry's facet on the
// record of the program its manifest names, holders[name], and takes it off
// every other program's record. An entry re-pinned to another program, such
// as Setup's update of an unsigned app to a new version folder, moves its
// facet to the new program's record; the earlier program's record then holds
// no facet of it. A record left without facets is deleted, and a record that
// keeps other facets takes a new revision.
func dropMovedApplicationFacets(states map[string]registration.Registration, holders map[string]string) error {
	for _, name := range slices.Sorted(maps.Keys(holders)) {
		holder := holders[name]
		if holder == "" {
			continue
		}
		facet := registration.FacetReference{Kind: registration.FacetKindApplication, Name: name}
		for _, key := range slices.Sorted(maps.Keys(states)) {
			v := states[key]
			if key == holder || !registrationHasFacet(v.Spec, facet) {
				continue
			}
			v.Spec = registrationWithoutFacet(v.Spec, facet)
			if registrationFacetCount(v.Spec) == 0 {
				delete(states, key)
				continue
			}
			revision, err := applicationToken()
			if err != nil {
				return err
			}
			v.Revision = revision
			states[key] = v
		}
	}
	return nil
}

func (s *applicationV3Registrations) encodeProgramIndex(ctx context.Context, entries map[string]applicationV3Registration) ([]byte, error) {
	states := maps.Clone(s.programs)
	if states == nil {
		states = map[string]registration.Registration{}
	}
	for key, v := range s.legacyPrograms {
		spec, err := s.programSpec(ctx, key, entries)
		if err != nil {
			return nil, err
		}
		v.Spec = spec
		states[key] = v
	}
	for name := range s.entries {
		if _, exists := entries[name]; exists {
			continue
		}
		for key, v := range states {
			if !registrationHasFacet(v.Spec, registration.FacetReference{Kind: registration.FacetKindApplication, Name: name}) {
				continue
			}
			v.Spec = registrationWithoutFacet(v.Spec, registration.FacetReference{Kind: registration.FacetKindApplication, Name: name})
			if registrationFacetCount(v.Spec) == 0 {
				delete(states, key)
			} else {
				revision, err := applicationToken()
				if err != nil {
					return nil, err
				}
				v.Revision = revision
				states[key] = v
			}
		}
	}
	holders := map[string]string{}
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		e := entries[name]
		old, exists := s.entries[name]
		f := s.files[name]
		if exists && old == e {
			holders[name] = identity.NormalizeSubjectProgram(f.Source.Program)
			continue
		}
		if !exists || old.FileRevision != e.FileRevision {
			b, err := cas.ReadLimitContext(ctx, s.filePath(e.FileRevision), applicationV3ManifestFileLimit)
			if err != nil || applicationV3Strict(b, &f) != nil {
				return nil, errors.New("registrations: manifest custody")
			}
		}
		holders[name] = identity.NormalizeSubjectProgram(f.Source.Program)
		if err := s.projectApplicationEntry(states, name, e, old, exists, f, false); err != nil {
			return nil, err
		}
	}
	if err := dropMovedApplicationFacets(states, holders); err != nil {
		return nil, err
	}
	providers := 0
	for key, v := range states {
		providers += len(v.Spec.Providers) + len(v.Spec.Servers) + len(v.Spec.Remotes)
		conditions := s.programFacetConditions[key]
		v.State = registrationIntent(v.Spec, conditions, v.State)
		for _, f := range v.Spec.Applications {
			if e, ok := entries[f.Name]; ok {
				e.Revision = v.Revision
				if e.State != "removing" {
					condition, has := conditions[registration.FacetReference{Kind: registration.FacetKindApplication, Name: f.Name}]
					if has {
						switch {
						case condition.Pending:
							e.State = "pending"
						case condition.Disabled:
							e.State = "disabled"
						default:
							e.State = "enabled"
						}
						if !condition.Pending {
							e.Question = ""
						}
					}
				}
				entries[f.Name] = e
			}
		}
		states[key] = v
	}
	if providers > maxProviders || len(states) > registrationMaximumPrograms {
		return nil, errors.New("registrations: existing directory capacity")
	}
	revision, err := applicationToken()
	if err != nil {
		return nil, err
	}
	index := registrationProgramIndex{Version: 1, Entries: entries, Programs: []registrationProgramFile{}, Removals: []registrationProgramFile{}, Revision: revision}
	allocated := 0
	live := map[string]bool{}
	for _, key := range slices.Sorted(maps.Keys(states)) {
		v := states[key]
		refs := registrationReferences(v.Spec)
		sources := map[registration.FacetReference]registration.Source{}
		for _, facet := range refs {
			source := v.Source
			if actual, ok := s.programFacetSources[key][facet]; ok {
				source = actual
			}
			sources[facet] = source
		}
		v.Source = registrationAggregateSource(sources, v.Source)
		v.Applications = []registration.ApplicationReading{}
		v.Described = []registration.ServiceState{}
		v.Checks = nil
		payload := registrationProgramPayload{DeclaredBy: s.programDeclarers[key], InputDigest: s.programInputDigests[key]}
		if approval := s.programApprovals[key]; approval != (registrationProgramApproval{}) {
			payload.Approval = &approval
		}
		for _, facet := range refs {
			condition := registrationConditionOf(s, key, facet)
			payload.FacetSources = append(payload.FacetSources, registrationFacetSource{Facet: facet, Source: sources[facet], Digest: s.programFacetDigests[key][facet], Disabled: condition.Disabled, Pending: condition.Pending})
		}
		if image, ok := s.programSources[key]; ok {
			payload.Image = &image
		}
		if registrationSpecValid(v.Spec) == "" {
			encoded, err := registrationEncoded(registration.EncodeRegistration, &v)
			if err != nil {
				return nil, err
			}
			payload.Registration = encoded
			payload.Compatibility = s.programCompatibility[key]
		} else {
			s.completeCompatibility(key, v)
			prior := map[string]registrationProgramFile{}
			for _, ref := range s.legacyProgramFiles[key] {
				prior[ref.Hash] = ref
			}
			for _, file := range s.programCompatibility[key] {
				encoded, err := legacyFacetPayload(file)
				if err != nil {
					return nil, err
				}
				hash := applicationV3Hash(encoded)
				ref, err := s.keepProgramFileLimit(ctx, encoded, prior[hash], &allocated, int64(len(encoded)))
				if err != nil {
					return nil, err
				}
				payload.LegacyFiles = append(payload.LegacyFiles, ref)
				live[ref.Revision] = true
			}
			metadata := v
			metadata.Spec = registration.RegistrationSpec{Program: v.Spec.Program}
			payload.Legacy = &metadata
		}
		encoded, err := registrationJSON(payload)
		if err != nil {
			return nil, err
		}
		ref, err := s.keepProgramFile(ctx, encoded, s.programFiles[key], &allocated)
		if err != nil {
			return nil, err
		}
		index.Programs = append(index.Programs, ref)
		live[ref.Revision] = true
	}
	for i, v := range s.removals {
		encoded, err := registrationEncoded(registration.EncodeRemovalReceipt, &v)
		if err != nil {
			return nil, err
		}
		var prior registrationProgramFile
		if i < len(s.removalFiles) {
			prior = s.removalFiles[i]
		}
		ref, err := s.keepProgramFile(ctx, encoded, prior, &allocated)
		if err != nil {
			return nil, err
		}
		index.Removals = append(index.Removals, ref)
		live[ref.Revision] = true
	}
	// Superseded large compatibility files keep their measured custody until the
	// next bounded reclaim. Small ordinary payloads use the existing fixed bound.
	retired := map[string]bool{}
	for _, files := range s.legacyProgramFiles {
		for _, ref := range files {
			if !live[ref.Revision] && ref.Bytes > registrationRecordFileLimit && !retired[ref.Revision] {
				index.Prepared = append(index.Prepared, ref)
				retired[ref.Revision] = true
			}
		}
	}
	return registrationJSON(index)
}

func (s *applicationV3Registrations) readProgramFile(ctx context.Context, ref registrationProgramFile) ([]byte, error) {
	return s.readProgramFileBound(ctx, ref, registrationRecordFileLimit)
}

func (s *applicationV3Registrations) readMeasuredProgramFile(ctx context.Context, ref registrationProgramFile) ([]byte, error) {
	if ref.Bytes < 1 {
		return nil, errors.New("registrations: missing measured bound")
	}
	return s.readProgramFileBound(ctx, ref, ref.Bytes)
}

func (s *applicationV3Registrations) readProgramFileBound(ctx context.Context, ref registrationProgramFile, limit int64) ([]byte, error) {
	if !registrationFileReferenceValid(ref) {
		return nil, errors.New("registrations: invalid payload reference")
	}
	if ref.Bytes != 0 {
		if ref.Bytes < 1 || ref.Bytes > limit {
			return nil, errors.New("registrations: payload bound")
		}
		limit = ref.Bytes
	}
	b, err := cas.ReadLimitContext(ctx, s.filePath(ref.Revision), limit)
	if err != nil || b == nil || ref.Bytes > 0 && int64(len(b)) != ref.Bytes || applicationV3Hash(b) != ref.Hash {
		return nil, errors.New("registrations: corrupt payload")
	}
	return b, nil
}

func (s *applicationV3Registrations) keepProgramFile(ctx context.Context, b []byte, prior registrationProgramFile, allocated *int) (registrationProgramFile, error) {
	return s.keepProgramFileLimit(ctx, b, prior, allocated, registrationRecordFileLimit)
}

func (s *applicationV3Registrations) keepProgramFileLimit(ctx context.Context, b []byte, prior registrationProgramFile, allocated *int, limit int64) (registrationProgramFile, error) {
	if limit < 1 || int64(len(b)) > limit {
		return registrationProgramFile{}, errors.New("registrations: payload capacity")
	}
	hash := applicationV3Hash(b)
	if prior.Revision != "" && prior.Hash == hash {
		return prior, nil
	}
	if *allocated >= maxProviders+1 {
		return registrationProgramFile{}, errors.New("registrations: publication capacity")
	}
	revision, err := applicationToken()
	if err != nil {
		return registrationProgramFile{}, err
	}
	ref := registrationProgramFile{Revision: revision, Hash: hash, Bytes: int64(len(b))}
	if err := s.prepareProgramFile(ctx, ref); err != nil {
		return registrationProgramFile{}, err
	}
	if err := (store.BoundedFileStore{MaxBytes: limit}).WriteContext(ctx, s.filePath(revision), store.Value{}, b); err != nil {
		return registrationProgramFile{}, err
	}
	*allocated++
	return ref, nil
}

func registrationFileReferenceValid(ref registrationProgramFile) bool {
	if len(ref.Revision) != 48 || len(ref.Hash) != 64 || ref.Bytes < 0 || ref.Bytes == 1<<63-1 {
		return false
	}
	for _, c := range ref.Revision + ref.Hash {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// The existing index retains the exact allocation bound before any payload
// write. A crash leaves reclaimable preparations with no registration authority.
func (s *applicationV3Registrations) prepareProgramFile(ctx context.Context, ref registrationProgramFile) error {
	var index registrationProgramIndex
	if len(s.indexRaw) > 0 {
		var fields map[string]json.RawMessage
		if json.Unmarshal(s.indexRaw, &fields) != nil {
			return errors.New("registrations: index")
		}
		if version, ok := fields["version"]; ok && len(version) > 0 && version[0] != '{' {
			if registrationStrict(s.indexRaw, &index) != nil {
				return errors.New("registrations: index")
			}
		}
	}
	if index.Version == 0 {
		revision, err := applicationToken()
		if err != nil {
			return err
		}
		index = registrationProgramIndex{Version: 1, Entries: maps.Clone(s.entries), Programs: []registrationProgramFile{}, Removals: []registrationProgramFile{}, Revision: revision}
		if index.Entries == nil {
			index.Entries = map[string]applicationV3Registration{}
		}
	}
	if len(index.Prepared) >= maxProviders+1 {
		return errors.New("registrations: preparation capacity")
	}
	index.Prepared = append(index.Prepared, ref)
	raw, err := registrationJSON(index)
	if err != nil || len(raw) > applicationV3IndexLimit {
		return errors.New("registrations: index capacity")
	}
	path := filepath.Join(s.dir, "index.json")
	if err := (store.BoundedFileStore{MaxBytes: applicationV3IndexLimit}).WriteContext(ctx, path, store.Value{Data: s.indexRaw}, raw); err != nil {
		return err
	}
	s.indexRaw = raw
	s.preparedFiles = index.Prepared
	return nil
}

func (s *applicationV3Registrations) clearProgramPreparations(ctx context.Context) error {
	if len(s.preparedFiles) == 0 {
		return nil
	}
	var index registrationProgramIndex
	if registrationStrict(s.indexRaw, &index) != nil {
		return errors.New("registrations: prepared index")
	}
	index.Prepared = nil
	raw, err := registrationJSON(index)
	if err != nil {
		return err
	}
	if err := (store.BoundedFileStore{MaxBytes: applicationV3IndexLimit}).WriteContext(ctx, filepath.Join(s.dir, "index.json"), store.Value{Data: s.indexRaw}, raw); err != nil {
		return err
	}
	s.indexRaw = raw
	s.preparedFiles = nil
	return nil
}

func registrationFacetCount(s registration.RegistrationSpec) int {
	return len(s.Providers) + len(s.Servers) + len(s.Remotes) + len(s.Applications)
}

func registrationSpecValid(spec registration.RegistrationSpec) string {
	if n := registrationFacetCount(spec); n < 1 || n > 64 {
		return "facets"
	}
	if _, err := registrationEncoded(registration.EncodeRegistrationSpec, &spec); err != nil {
		return "spec"
	}
	names := map[registration.FacetReference]bool{}
	add := func(kind registration.FacetKind, name string) bool {
		f := registration.FacetReference{Kind: kind, Name: name}
		if !providerName.MatchString(name) || names[f] {
			return false
		}
		names[f] = true
		return true
	}
	recipes := 0
	for _, f := range spec.Providers {
		if reason := validProviderFacet(spec.Program, f); reason != "" {
			return "provider:" + reason
		}
		if !add(registration.FacetKindProvider, f.Name) {
			return "provider:name"
		}
		recipes += len(f.Recipes)
		engines := map[string]bool{}
		for _, e := range f.Engines {
			if engines[e.ID] {
				return "engine:name"
			}
			engines[e.ID] = true
		}
		for _, r := range f.Recipes {
			if !engines[r.Engine] {
				return "recipe:engine"
			}
			for _, m := range r.Modalities {
				if !slices.ContainsFunc(f.Models, func(v registration.ScopedModel) bool { return v.Modality == m && v.Model == r.Model }) {
					return "recipe:model"
				}
			}
			for _, o := range r.Objects {
				if o.Digest != "" {
					if o.Store != "" || o.Object != "" || !digestfmt.Valid(o.Digest) {
						return "recipe:object"
					}
				} else if o.Store == "" || o.Object == "" {
					return "recipe:object"
				}
			}
		}
	}
	if recipes > 64 {
		return "recipes"
	}
	for _, f := range spec.Servers {
		if !add(registration.FacetKindHost, f.Name) {
			return "host:name"
		}
		if reason := validHostDeclaration(serverFacetCompatibility(f)); reason != "" {
			return "host:" + reason
		}
	}
	for _, f := range spec.Remotes {
		if !add(registration.FacetKindRemote, f.Name) {
			return "remote:name"
		}
	}
	for _, f := range spec.Applications {
		if !add(registration.FacetKindApplication, f.Name) || f.Name != f.Manifest.Name {
			return "application:name"
		}
	}
	if len(spec.Remotes) > 0 && (len(spec.Providers) > 0 || len(spec.Applications) > 0 || spec.Program != "") {
		return "identity:kinds"
	}
	if (len(spec.Providers) > 0 || len(spec.Applications) > 0) && !identity.ValidSubjectProgram(spec.Program) {
		return "program"
	}
	if len(spec.Providers)+len(spec.Applications) == 0 && spec.Program != "" {
		return "program"
	}
	return ""
}
