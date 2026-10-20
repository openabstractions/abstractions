package main

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// This is registration intent and a reference to the existing Asks answer.
// The Asks book remains the sole authority for the person's decision.
type registrationProgramApproval struct {
	Pending        bool   `json:"pending,omitempty"`
	Question       string `json:"question,omitempty"`
	Revision       string `json:"revision,omitempty"`
	Removing       bool   `json:"removing,omitempty"`
	KeptGeneration string `json:"kept_generation,omitempty"`
	// Review is the revision a grouped program review asked the person
	// about. That review carries across a restart; a registration nobody
	// asked about waits in the Panel (pendingProgramReviewCandidates).
	Review string `json:"review,omitempty"`
	// Served is the program's last registration that served, encoded, and
	// ServedImage the program image it served with. While a changed
	// registration of that same image waits for the person's Keep, the
	// runtime keeps serving Served unchanged and only the change waits
	// (fail open: a model host re-declared with video keeps answering chat).
	Served      string `json:"served,omitempty"`
	ServedImage string `json:"served_image,omitempty"`
	ServedBy    string `json:"served_by,omitempty"`
}

// lastServed is what serves key while a change waits: the registration,
// its image and who declared it (Served, ServedImage, ServedBy). It is the
// one an earlier pending change carries, else the current record when it
// serves now: enabled, with a verified or unsigned image, and nothing of it
// pending or being removed. The zero value serves nothing.
func (s *applicationV3Registrations) lastServed(key string, old registration.Registration, exists bool) registrationProgramApproval {
	none := registrationProgramApproval{}
	if !exists {
		return none
	}
	approval := s.programApprovals[key]
	if approval.Removing {
		return none
	}
	if approval.Served != "" && s.programPending(key, old) {
		return registrationProgramApproval{Served: approval.Served, ServedImage: approval.ServedImage, ServedBy: approval.ServedBy}
	}
	if approval.Pending || old.State != registration.StateEnabled || old.IdentityEvidence != registration.IdentityEvidenceProgram ||
		old.Verification == registration.VerificationChanged || old.Verification == registration.VerificationMissing {
		return none
	}
	for _, condition := range s.programFacetConditions[key] {
		if condition.Pending {
			return none
		}
	}
	source, ok := s.programSources[key]
	if _, opaque := s.legacyPrograms[key]; opaque || !ok || source.FileKey == "" {
		return none
	}
	raw, err := registrationEncoded(registration.EncodeRegistration, &old)
	if err != nil {
		return none
	}
	return registrationProgramApproval{Served: string(raw), ServedImage: source.FileKey, ServedBy: s.programDeclarers[key]}
}

// servedFiles projects the registration served while a change waits: its
// declarations unchanged and enabled unless the person turned a facet off.
// Each carries the models the waiting change adds to it (pending), which
// answer approval:pending until the person keeps the change.
func (s *applicationV3Registrations) servedFiles(key string, served registration.Registration, pending []providerFile) []providerFile {
	files := registrationProviderFiles(served)
	for i := range files {
		files[i].DeclaredBy = s.programApprovals[key].ServedBy
		kind := registration.FacetKindProvider
		if files[i].Declaration.host() {
			kind = registration.FacetKindHost
		}
		if files[i].Declaration.remote() {
			kind = registration.FacetKindRemote
		}
		ref := registration.FacetReference{Kind: kind, Name: files[i].Declaration.Name}
		files[i].Source = registrationSourceWord(registrationFacetSourceOf(s, key, ref))
		files[i].Disabled = files[i].Disabled || registrationConditionOf(s, key, ref).Disabled
		for _, change := range pending {
			if change.Declaration.Name != files[i].Declaration.Name {
				continue
			}
			for _, model := range change.Declaration.Models {
				if !slices.Contains(files[i].Declaration.Models, model) {
					files[i].waiting = append(files[i].waiting, model)
				}
			}
		}
	}
	return files
}

// servedWhilePending is the registration of key the runtime keeps serving
// while record's change waits for Keep: Served, when the image it served
// with is the image held now.
func (s *applicationV3Registrations) servedWhilePending(key string, record registration.Registration) (registration.Registration, bool) {
	approval := s.programApprovals[key]
	if approval.Served == "" || approval.Removing || !s.programPending(key, record) || s.programSources[key].FileKey != approval.ServedImage ||
		record.Verification == registration.VerificationChanged || record.Verification == registration.VerificationMissing {
		return registration.Registration{}, false
	}
	served, err := registration.DecodeRegistration([]byte(approval.Served))
	if err != nil {
		return registration.Registration{}, false
	}
	return *served, true
}

// A supported unsigned verdict proves no signer. It may enter ordinary PROGRAM
// intent under person Keep; invalid signatures and verifier errors never do.
func registrationProgramCodeAccepted(code identity.Code, proof identity.Proof) bool {
	return code.Trusted && proof == identity.ProofBound || !code.Trusted && proof == identity.ProofUnsigned
}

func recaptureProgramKeepImage(ctx context.Context, source applicationV3Source) (activationImageHold, error) {
	held, err := openRegistryProgramImage(ctx, fileid.Self, source.Program)
	if err != nil {
		return nil, err
	}
	if applicationV3HeldKey(held) != source.FileKey || held.Proof != source.Proof || held.Code != source.Code {
		return nil, errors.Join(errors.New("registration: captured image changed"), held.Close())
	}
	if source.ImageSHA256 != "" {
		hash, err := registryHeldImageSHA256(ctx, held)
		if err != nil || hash != source.ImageSHA256 {
			return nil, errors.Join(errors.New("registration: configured recipe image changed"), err, held.Close())
		}
	}
	return held, nil
}

func (s *applicationV3Registrations) programPending(key string, record registration.Registration) bool {
	if s.programApprovals[key].Pending || s.programApprovals[key].Removing || record.State == registration.StatePendingApproval {
		return true
	}
	for _, condition := range s.programFacetConditions[key] {
		if condition.Pending {
			return true
		}
	}
	for name, e := range s.entries {
		if e.State == "pending" && s.programSources[key].sameProgram(s.files[name].Source) {
			return true
		}
	}
	return false
}

func (s *applicationV3Registrations) programSelfEntryLocked(key string) (registration.Registration, applicationV3Source, selfMark, bool) {
	record, ok := s.programRecord(key)
	source, hasImage := s.programSources[key]
	if !ok || !hasImage || !registrationProgramCodeAccepted(source.Code, source.Proof) || source.FileKey == "" || record.IdentityEvidence != registration.IdentityEvidenceProgram || !s.programPending(key, record) {
		return registration.Registration{}, applicationV3Source{}, selfMark{}, false
	}
	approval := s.programApprovals[key]
	mark := newSelfMark(source.Program, asks.Publisher{Subject: source.Code.Subject, Trusted: source.Code.Trusted, Description: source.Code.Description, OriginalName: source.Code.OriginalName})
	mark.ProgramKey = source.FileKey
	mark.V3Revision = record.Revision
	mark.Removing = approval.Removing
	if approval.Revision == record.Revision {
		mark.Question = approval.Question
	}
	return record, source, mark, true
}

// reaskKeep raises the Keep question of program key again at a need for it:
// a registration that waits for approval always has a question the person
// can answer. The question asked for it is followed while pending; one that
// expired (15 minutes, asks DefaultPendingLifetime) or is gone is asked
// anew, once. A registration nobody asked about yet waits for the person's
// review in the Panel, and a Remove in progress asks nothing.
func (d *applicationDirectory) reaskKeep(key string) {
	if d == nil || d.askKeep == nil || key == "" {
		return
	}
	_, _, mark, ok := d.programSelfEntry(key)
	if !ok || mark.Question == "" || mark.Removing {
		return
	}
	if err := d.askKeep(selfRegisteredProgramName(key)); err != nil && d.programConfigReport != nil {
		d.programConfigReport(fmt.Errorf("registration: asking again to keep %s: %w", key, err))
	}
}

func (d *applicationDirectory) programSelfEntry(key string) (registration.Registration, applicationV3Source, selfMark, bool) {
	ctx := context.Background()
	if d == nil || !d.lock(ctx) {
		return registration.Registration{}, applicationV3Source{}, selfMark{}, false
	}
	defer d.unlock()
	if d.v3 == nil {
		return registration.Registration{}, applicationV3Source{}, selfMark{}, false
	}
	var record registration.Registration
	var source applicationV3Source
	var mark selfMark
	var found bool
	err := d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		record, source, mark, found = d.v3.programSelfEntryLocked(key)
		if found {
			if _, opaque := d.v3.legacyPrograms[key]; !opaque {
				raw, err := registrationEncoded(registration.EncodeRegistration, &record)
				if err != nil {
					return err
				}
				copy, err := registration.DecodeRegistration(raw)
				if err != nil {
					return err
				}
				record = *copy
			}
		}
		return nil
	})
	return record, source, mark, found && err == nil
}

func (d *applicationDirectory) programSelfEntries() map[string]selfMark {
	out := map[string]selfMark{}
	ctx := context.Background()
	if d == nil || !d.lock(ctx) {
		return out
	}
	defer d.unlock()
	if d.v3 == nil {
		return out
	}
	err := d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		for _, key := range d.v3.programKeys() {
			_, _, mark, ok := d.v3.programSelfEntryLocked(key)
			if ok {
				out[key] = mark
			}
		}
		return nil
	})
	if err != nil {
		return map[string]selfMark{}
	}
	return out
}

func (d *applicationDirectory) programSelfName(name string) (string, bool) {
	ctx := context.Background()
	if d == nil || !d.lock(ctx) {
		return "", false
	}
	defer d.unlock()
	if d.v3 == nil {
		return "", false
	}
	key := ""
	err := d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		f, ok := d.v3.files[name]
		if !ok {
			return nil
		}
		for _, candidate := range d.v3.programKeys() {
			source, has := d.v3.programSources[candidate]
			if has && registrationProgramCodeAccepted(source.Code, source.Proof) && source.sameProgram(f.Source) {
				key = candidate
				break
			}
		}
		return nil
	})
	return key, key != "" && err == nil
}

// The program a question, Keep or Remove names is compared with the retained
// image provenance as subject programs compare: a caller may name it by
// another spelling of the same file, such as an 8.3 alias. The caller's file
// key is read before the edit, and the retained source keeps its own
// (FileKey), so the comparison under CAS reads no file
// (applicationRegistrationPrograms).
func (d *applicationDirectory) setProgramQuestion(ctx context.Context, key, program, question, revision string) bool {
	if question == "" || len(question) > 1024 || strings.ContainsAny(question, "\x00\r\n") {
		return false
	}
	programKey := identity.ProgramKey(program)
	out := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		record, source, _, ok := s.programSelfEntryLocked(key)
		if !ok || !applicationRegistrationPrograms(source.Program, program, source.FileKey, programKey) || record.Revision != revision || s.programApprovals[key].Removing {
			return registration.Change{Outcome: registration.OutcomeConflict}, nil
		}
		approval := s.programApprovals[key]
		approval.Question = question
		approval.Revision = revision
		s.programApprovals[key] = approval
		return registration.Change{Outcome: registration.OutcomeApplied, Revision: revision}, nil
	})
	return out.Outcome == registration.OutcomeApplied
}

func (d *applicationDirectory) keepProgramOwned(ctx context.Context, key, program, question string) bool {
	// Capture outside CAS, then retain this exact image through the conditional
	// answer. A replacement of the locator never consumes the original Keep.
	programKey := identity.ProgramKey(program)
	before, capturedSource, mark, found := d.programSelfEntry(key)
	if !found || !applicationRegistrationPrograms(capturedSource.Program, program, capturedSource.FileKey, programKey) || mark.Question != question {
		return false
	}
	open := d.programKeepImage
	if open == nil {
		open = recaptureProgramKeepImage
	}
	held, err := open(ctx, capturedSource)
	if err != nil || held == nil {
		return false
	}
	defer held.Close()
	if held.Recheck(ctx) != nil {
		return false
	}
	keys, err := providerConfigKeys(before.Spec, d.programConfigKeyKnown)
	if err != nil {
		return false
	}
	// Until the conditional registry commit below, these rules cannot pass the
	// kept-generation proof. A failed grant resumes through this same retained
	// Keep answer and the Rights owner's existing one-shot installation marker.
	if len(keys) > 0 {
		defer d.reconcileProviderConfigAttempt(ctx)
		if d.programConfigKeep == nil || d.programConfigKeep(ctx, program, keys) != nil || held.Recheck(ctx) != nil {
			return false
		}
	}
	out := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, next map[string]applicationV3Registration) (registration.Change, error) {
		record, exists := s.programRecord(key)
		source := s.programSources[key]
		approval := s.programApprovals[key]
		if !exists || record.Revision != before.Revision || source != capturedSource || question == "" || approval.Question != question || approval.Revision != record.Revision || approval.Removing || !applicationRegistrationPrograms(source.Program, program, source.FileKey, programKey) || !registrationProgramCodeAccepted(source.Code, source.Proof) || source.FileKey == "" || record.Verification == registration.VerificationChanged || record.Verification == registration.VerificationMissing {
			return registration.Change{Outcome: registration.OutcomeConflict}, nil
		}
		if approval.KeptGeneration == record.Epoch {
			return registration.Change{Outcome: registration.OutcomeApplied, Revision: record.Revision}, nil
		}
		spec, err := s.programSpec(ctx, key, next)
		if err != nil {
			return registration.Change{}, err
		}
		revision, err := applicationToken()
		if err != nil {
			return registration.Change{}, err
		}
		conditions := maps.Clone(s.programFacetConditions[key])
		if conditions == nil {
			conditions = map[registration.FacetReference]registrationFacetCondition{}
		}
		for _, facet := range registrationReferences(spec) {
			condition := registrationConditionOf(s, key, facet)
			if condition.Pending || record.State == registration.StatePendingApproval {
				condition.Pending = false
			}
			conditions[facet] = condition
			if facet.Kind == registration.FacetKindApplication {
				if e, ok := next[facet.Name]; ok && e.State == "pending" {
					if e.KeepIdentity == "" {
						e.KeepIdentity = e.FileRevision
					}
					e.State = "enabled"
					if condition.Disabled {
						e.State = "disabled"
					}
					e.Question = ""
					e.Revision = revision
					next[facet.Name] = e
				}
			}
		}
		s.programFacetConditions[key] = conditions
		record.State = registrationIntent(spec, conditions, registration.StateEnabled)
		record.Revision = revision
		approval.Revision = revision
		approval.KeptGeneration = record.Epoch
		approval.Pending = false
		approval.Served, approval.ServedImage, approval.ServedBy = "", "", ""
		s.programApprovals[key] = approval
		s.keepProgramShape(key, record, spec)
		return registration.Change{Outcome: registration.OutcomeApplied, Revision: revision}, nil
	})
	if out.Outcome != registration.OutcomeApplied {
		return false
	}
	// A kept OA integration image holds the card and memory (owner ruling
	// 2026-11-12, architect R35). A failed write leaves the Keep standing.
	d.writeKeptHolds(ctx)
	return true
}

func (d *applicationDirectory) removeProgramSelfRegistered(ctx context.Context, key, program, question string, block func() error) error {
	if block == nil {
		return errors.New("registration: Rights owner unavailable")
	}
	programKey := identity.ProgramKey(program)
	before, _, _, found := d.programSelfEntry(key)
	if !found {
		return errors.New("registration: stale Remove answer")
	}
	reserved := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, _ map[string]applicationV3Registration) (registration.Change, error) {
		record, source, _, ok := s.programSelfEntryLocked(key)
		approval := s.programApprovals[key]
		if !ok || !applicationRegistrationPrograms(source.Program, program, source.FileKey, programKey) || question == "" || approval.Question != question || approval.Revision != record.Revision {
			return registration.Change{Outcome: registration.OutcomeConflict}, nil
		}
		if approval.Removing {
			return registration.Change{Outcome: registration.OutcomeApplied, Revision: record.Revision}, nil
		}
		revision, err := applicationToken()
		if err != nil {
			return registration.Change{}, err
		}
		record.Revision = revision
		approval.Revision = revision
		approval.Removing = true
		s.programApprovals[key] = approval
		if _, opaque := s.legacyPrograms[key]; opaque {
			s.legacyPrograms[key] = record
		} else {
			s.programs[key] = record
		}
		return registration.Change{Outcome: registration.OutcomeApplied, Revision: revision}, nil
	})
	if reserved.Outcome != registration.OutcomeApplied {
		return errors.New("registration: stale Remove answer")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if d.programConfigRemove != nil {
		if err := d.programConfigRemove(ctx, program); err != nil {
			return err
		}
	} else {
		for _, facet := range before.Spec.Providers {
			if len(facet.ConfigKeys) > 0 {
				return errors.New("registration: configuration Rights owner unavailable")
			}
		}
	}
	if err := block(); err != nil {
		return err
	}
	removed := d.editProgramRegistration(ctx, func(s *applicationV3Registrations, next map[string]applicationV3Registration) (registration.Change, error) {
		record, ok := s.programRecord(key)
		approval := s.programApprovals[key]
		if !ok || !approval.Removing || approval.Question != question || approval.Revision != record.Revision || !applicationRegistrationPrograms(s.programSources[key].Program, program, s.programSources[key].FileKey, programKey) {
			return registration.Change{Outcome: registration.OutcomeConflict}, nil
		}
		spec, err := s.programSpec(ctx, key, next)
		if err != nil {
			return registration.Change{}, err
		}
		for _, facet := range registrationReferences(spec) {
			condition := registrationConditionOf(s, key, facet)
			if !condition.Pending && record.State != registration.StatePendingApproval {
				continue
			}
			if registrationProtectedSource(registrationFacetSourceOf(s, key, facet)) {
				condition.Pending = false
				condition.Disabled = true
				s.programFacetConditions[key][facet] = condition
				continue
			}
			spec = registrationWithoutFacet(spec, facet)
			if facet.Kind == registration.FacetKindApplication {
				delete(next, facet.Name)
			}
		}
		if registrationFacetCount(spec) == 0 {
			delete(s.programs, key)
			delete(s.legacyPrograms, key)
			delete(s.programApprovals, key)
		} else {
			revision, err := applicationToken()
			if err != nil {
				return registration.Change{}, err
			}
			record.Revision = revision
			record.Epoch = revision
			record.State = registrationIntent(spec, s.programFacetConditions[key], registration.StateDisabled)
			s.programApprovals[key] = registrationProgramApproval{}
			s.keepProgramShape(key, record, spec)
		}
		return registration.Change{Outcome: registration.OutcomeApplied, Revision: record.Revision}, nil
	})
	if removed.Outcome != registration.OutcomeApplied {
		return errors.New("registration: Remove persistence")
	}
	// A removed integration no longer holds the card and memory.
	d.writeKeptHolds(ctx)
	return nil
}
