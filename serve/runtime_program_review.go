package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"sync"

	asks "github.com/openabstractions/abstraction-asks/go"
	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
)

// One service-owned occurrence captures the complete unresolved snapshot. The
// native person answer acts only on these revisions, Specs and held image hashes.
// RuntimeBound questions lose this in-memory authority when the service exits.
type shippedProgramReview struct {
	effectMu                  sync.Mutex
	mu                        sync.Mutex
	d                         *applicationDirectory
	k                         *selfRegisteredQuestions
	latest                    []shippedImageReviewCandidate
	occurrence, question      string
	members                   []programReviewMember
	ctx                       context.Context
	report                    func(error)
	admissionErr              error
	failedKey, failedRevision string
	admit                     func(map[string]string) (asks.Record, string, error)
}

type programReviewMember struct {
	record     registration.Registration
	fact       asks.ProgramReviewTarget
	declaredBy string
}

func programReviewSpecDigest(spec registration.RegistrationSpec) string {
	raw, err := json.Marshal(spec)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func (d *applicationDirectory) programReviewRecord(ctx context.Context, key string) (record registration.Registration, removing bool, declaredBy string, ok bool) {
	if d == nil || !d.lock(ctx) {
		return
	}
	defer d.unlock()
	if d.v3 == nil {
		return
	}
	err := d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		record, ok = d.v3.programRecord(key)
		removing = d.v3.programApprovals[key].Removing
		declaredBy = d.v3.programDeclarers[key]
		if !ok {
			return nil
		}
		raw, err := registrationEncoded(registration.EncodeRegistration, &record)
		if err != nil {
			return err
		}
		copy, err := registration.DecodeRegistration(raw)
		if err == nil {
			record = *copy
		}
		return err
	})
	ok = ok && err == nil
	return
}

func captureProgramReviewImage(ctx context.Context, program string) (*heldApplicationManifest, string, error) {
	held, err := openHeldApplicationImage(ctx, fileid.Self, program)
	if err != nil {
		return nil, "", err
	}
	if applicationV3HeldKey(held) == "" || identity.NormalizeSubjectProgram(applicationV3HeldProgram(held)) != program {
		held.Close()
		return nil, "", errors.New("program review: image identity unavailable")
	}
	if _, err := registryHeldImageToolsRelative(ctx, held); err != nil {
		held.Close()
		return nil, "", err
	}
	digest, err := registryHeldImageSHA256(ctx, held)
	if err != nil {
		held.Close()
		return nil, "", err
	}
	// Existing native signature observation supplies no authority on failure.
	// A contained, held mismatch may still offer Repair while Keep stays absent.
	key, code, proof, verifyErr := observeRegistryLegacyImage(program)
	if verifyErr == nil && key == applicationV3HeldKey(held) {
		held.Code, held.Proof = code, proof
	}
	if err := held.Recheck(ctx); err != nil {
		held.Close()
		return nil, "", err
	}
	return held, digest, nil
}

func (g *shippedProgramReview) snapshot(ctx context.Context, candidates []shippedImageReviewCandidate) {
	g.effectMu.Lock()
	defer g.effectMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.latest = append([]shippedImageReviewCandidate{}, candidates...)
	if g.k != nil {
		if err := g.askLocked(ctx); err != nil {
			g.report(err)
		}
	}
}

func (g *shippedProgramReview) attach(k *selfRegisteredQuestions) {
	if g == nil || k == nil {
		return
	}
	g.effectMu.Lock()
	defer g.effectMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	g.k = k
	k.programReview = g
	g.d = k.d
	g.ctx = k.ctx
	if err := g.askLocked(k.ctx); err != nil {
		g.report(err)
	}
}

func (g *shippedProgramReview) askLocked(ctx context.Context) error {
	candidates := append([]shippedImageReviewCandidate{}, g.latest...)
	// Fresh pending custody remains a grouped review across restart even when
	// the newly captured FileKey already matches the current image.
	pending, err := g.d.pendingProgramReviewCandidates(ctx)
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, candidate := range candidates {
		seen[candidate.Key] = true
	}
	for _, candidate := range pending {
		if !seen[candidate.Key] {
			candidates = append(candidates, candidate)
			seen[candidate.Key] = true
		}
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].Key < candidates[j].Key })
	if len(candidates) == 0 {
		return g.retireLocked()
	}
	members := []programReviewMember{}
	facts := asks.ProgramReviewFacts{}
	for _, candidate := range candidates {
		if candidate.Cause == "shipped:outside" {
			continue
		}
		record, removing, declaredBy, ok := g.d.programReviewRecord(ctx, candidate.Key)
		if !ok || removing || record.State == registration.StateDisabled || record.Revision != candidate.Revision || !programNeedsGroupReview(record, candidate.Cause) {
			continue
		}
		fact := asks.ProgramReviewTarget{Program: record.Spec.Program, Revision: record.Revision, Cause: candidate.Cause, Spec: programReviewSpecDigest(record.Spec)}
		held, digest, captureErr := captureProgramReviewImage(ctx, record.Spec.Program)
		if captureErr != nil {
			continue
		}
		fact.Image = digest
		fact.FileKey = applicationV3HeldKey(held)
		if record.State != registration.StateDisabled && record.IdentityEvidence == registration.IdentityEvidenceProgram && len(record.Spec.Providers) > 0 && len(record.Spec.Applications) == 0 && len(record.Spec.Remotes) == 0 {
			fact.Supported = registrationProgramCodeAccepted(held.Code, held.Proof)
		}
		held.Close()
		facts.Targets = append(facts.Targets, fact)
		members = append(members, programReviewMember{record, fact, declaredBy})
	}
	if len(members) == 0 {
		return g.retireLocked()
	}
	if len(members) > 16 {
		err := errors.Join(errors.New("program review: unresolved snapshot exceeds native question bound"), g.retireLocked())
		g.members = members
		g.admissionErr = err
		return err
	}
	slots, err := asks.ProgramReviewSlots(facts)
	if err != nil {
		return errors.Join(err, g.retireLocked())
	}
	if slots["occurrence"] == g.occurrence && !g.questionGoneLocked() {
		return g.admissionErr
	}
	if err := g.retireLocked(); err != nil {
		return err
	}
	g.members = members
	g.occurrence = slots["occurrence"]
	for _, member := range members {
		_, _, mark, ok := g.d.programSelfEntry(member.record.Key)
		if !ok || mark.Question == "" {
			continue
		}
		question := mark.Question
		_, err := g.k.a.book.WithdrawProgramReviewPredecessor(question, selfRegisteredSlot(member.fact.Program), func() error {
			if !g.d.programReviewPredecessorCurrent(ctx, member.record.Key, member.fact.Revision, member.fact.Program, question) {
				return errors.New("program review: prior question changed")
			}
			return nil
		})
		if err != nil {
			g.admissionErr = err
			return err
		}
	}
	request, err := selfRegisteredRequestKey()
	if err != nil {
		g.admissionErr = err
		return err
	}
	var record asks.Record
	var outcome string
	if g.admit != nil {
		record, outcome, err = g.admit(slots)
	} else {
		record, outcome, err = g.k.a.book.AskApplicationFrom(selfRegistrationScope(g.k.a.scope, slots["occurrence"]), g.k.a.display, request, asks.ProgramReviewKey, slots, listen.Seen{Why: "service-owned review of the complete unresolved shipped-program snapshot"}, asks.Publisher{})
	}
	if err != nil || outcome != "pending" {
		if err == nil {
			err = errors.New("program review: native question unavailable")
		}
		g.admissionErr = err
		return err
	}
	g.occurrence = slots["occurrence"]
	g.question = record.ID
	g.failedKey, g.failedRevision = "", ""
	g.members = members
	if err := g.d.markProgramReview(ctx, members); err != nil {
		g.report(err)
	}
	go g.follow(record.ID, slots["occurrence"])
	return nil
}

// questionGoneLocked reports that the review question asked for the current
// occurrence expired (asks DefaultPendingLifetime) or the Book no longer
// holds it. The same review is then asked anew; a pending or answered
// question stands.
func (g *shippedProgramReview) questionGoneLocked() bool {
	if g.question == "" || g.k == nil {
		return false
	}
	_, found, err := g.k.a.book.ApplicationRecord(g.question)
	return err == nil && !found
}

// need raises the program review of registration key at a need for one of
// its models: a program whose image changed always has a live question the
// person can answer. The review asked is followed while pending; one that
// expired is asked anew, and a key the start did not review joins it.
func (g *shippedProgramReview) need(key string) {
	if g == nil || key == "" {
		return
	}
	g.effectMu.Lock()
	defer g.effectMu.Unlock()
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.k == nil || g.d == nil {
		return
	}
	record, removing, _, ok := g.d.programReviewRecord(g.ctx, key)
	if !ok || removing || !programNeedsGroupReview(record, "") {
		return
	}
	known := false
	for i, candidate := range g.latest {
		if candidate.Key == key {
			g.latest[i].Revision = record.Revision
			known = true
		}
	}
	if !known {
		g.latest = append(g.latest, shippedImageReviewCandidate{key, record.Revision, "identity:unavailable"})
	}
	if err := g.askLocked(g.ctx); err != nil {
		g.report(err)
	}
}

func (g *shippedProgramReview) retireLocked() error {
	if g.question != "" {
		if _, err := g.k.a.book.WithdrawProgramReview(g.question); err != nil {
			return err
		}
	}
	g.occurrence = ""
	g.question = ""
	g.members = nil
	g.admissionErr = nil
	return nil
}

func (g *shippedProgramReview) follow(id, occurrence string) {
	select {
	case <-g.ctx.Done():
		return
	case <-g.k.a.book.Decided(id):
	}
	record, found, err := g.k.a.book.ApplicationRecord(id)
	if err != nil {
		g.report(err)
		return
	}
	if !found || record.Pending() || record.Key != asks.ProgramReviewKey || record.AskerProgram != "" || record.AnsweredBy != asks.AnsweredByOperator || record.About != occurrence || record.Option != "keep" {
		return
	}
	g.effectMu.Lock()
	defer g.effectMu.Unlock()
	g.mu.Lock()
	if g.question != id || g.occurrence != occurrence || record.Content == nil {
		g.mu.Unlock()
		return
	}
	members := append([]programReviewMember{}, g.members...)
	g.mu.Unlock()
	facts, err := asks.ProgramReviewFactsFromSlots(record.Content.Slots)
	if err != nil || len(facts.Targets) != len(members) {
		return
	}
	for i := range facts.Targets {
		if facts.Targets[i] != members[i].fact {
			return
		}
	}
	if err := g.keepCaptured(g.ctx, id, members); err != nil {
		g.report(err)
	}
}

// Preflight all supported members before the first conditional edit. Every
// member retains its native image hold through staging and the existing Keep.
func (g *shippedProgramReview) keepCaptured(ctx context.Context, question string, members []programReviewMember) error {
	helds := make([]*heldApplicationManifest, len(members))
	defer func() {
		for _, held := range helds {
			if held != nil {
				held.Close()
			}
		}
	}()
	for i, member := range members {
		current, removing, declaredBy, ok := g.d.programReviewRecord(ctx, member.record.Key)
		if !ok || removing || current.Revision != member.fact.Revision || declaredBy != member.declaredBy || current.State == registration.StateDisabled || current.Verification != member.record.Verification || !programNeedsGroupReview(current, member.fact.Cause) || !reflect.DeepEqual(current.Spec, member.record.Spec) {
			return errors.New("program review: revision or intent changed")
		}
		held, digest, err := captureProgramReviewImage(ctx, member.fact.Program)
		if err != nil {
			return err
		}
		helds[i] = held
		if digest != member.fact.Image || applicationV3HeldKey(held) != member.fact.FileKey || member.fact.Supported && !registrationProgramCodeAccepted(held.Code, held.Proof) {
			return errors.New("program review: current image changed")
		}
	}
	for i, member := range members {
		held := helds[i]
		if held == nil || !member.fact.Supported {
			continue
		}
		if held.Recheck(ctx) != nil {
			return errors.New("program review: image custody changed")
		}
		candidate := member.record
		candidate.State = registration.StatePendingApproval
		candidate.Verification = registration.VerificationVerified
		candidate.VerificationReason = ""
		candidate.Verified = registrationTime(g.d.now())
		if held.Proof == identity.ProofUnsigned {
			candidate.Verification = registration.VerificationUnverified
			candidate.VerificationReason = "identity:unsigned"
		}
		source := applicationV3Source{Program: member.fact.Program, FileKey: applicationV3HeldKey(held), Code: held.Code, Proof: held.Proof}
		change := g.d.publishProgramRegistrationReview(ctx, member.fact.Revision, false, candidate, &source, member.declaredBy, true)
		if change.Outcome != registration.OutcomeApplied {
			return errors.New("program review: conditional replacement refused")
		}
		if !g.d.setProgramQuestion(ctx, member.record.Key, member.fact.Program, question, change.Revision) || !g.d.keepProgramOwned(ctx, member.record.Key, member.fact.Program, question) {
			return errors.New("program review: current-image Keep refused")
		}
	}
	return nil
}

func (g *shippedProgramReview) openQuestion(key, revision string) string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.k == nil {
		return ""
	}
	for _, member := range g.members {
		if member.record.Key == key && member.record.Revision == revision {
			record, found, err := g.k.a.book.ApplicationRecord(g.question)
			if err == nil && found && record.Pending() {
				return g.question
			}
		}
	}
	return ""
}

func (g *shippedProgramReview) providerQuestion(name string) string {
	if g == nil {
		return ""
	}
	g.mu.Lock()
	key, revision := "", ""
	for _, member := range g.members {
		for _, facet := range member.record.Spec.Providers {
			if facet.Name == name {
				key, revision = member.record.Key, member.record.Revision
			}
		}
	}
	g.mu.Unlock()
	if key == "" {
		return ""
	}
	return g.openQuestion(key, revision)
}

func (g *shippedProgramReview) captures(key, revision string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, member := range g.members {
		if member.record.Key == key && member.record.Revision == revision {
			return true
		}
	}
	return false
}

func programNeedsGroupReview(record registration.Registration, _ string) bool {
	return record.Verification == registration.VerificationChanged || record.State == registration.StatePendingApproval && (record.Verification == registration.VerificationVerified || record.Verification == registration.VerificationUnverified)
}

// A Panel's normal conditional Replace has already captured a fresh image.
// For a program in this service-owned snapshot, update the existing group
// rather than issuing a parallel one-program prompt. The Replace supplies no
// Keep: the new group still requires its own actual native person answer.
func (g *shippedProgramReview) reviewRegistered(ctx context.Context, key, revision string) (bool, error) {
	if g == nil {
		return false, nil
	}
	g.effectMu.Lock()
	defer g.effectMu.Unlock()
	g.mu.Lock()
	known := false
	for _, member := range g.members {
		known = known || member.record.Key == key
	}
	g.mu.Unlock()
	if !known {
		return false, nil
	}
	record, removing, _, ok := g.d.programReviewRecord(ctx, key)
	if !ok || removing || record.Revision != revision || record.State != registration.StatePendingApproval {
		return false, nil
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	finish := func() (bool, error) {
		err := g.askLocked(ctx)
		current := false
		for _, member := range g.members {
			current = current || member.record.Key == key && member.record.Revision == revision
		}
		if err == nil && (!current || g.question == "") {
			err = errors.New("program review: native question unavailable")
		}
		if err == nil {
			question, found, readErr := g.k.a.book.ApplicationRecord(g.question)
			if readErr != nil {
				err = readErr
			} else if !found || !question.Pending() {
				err = errors.New("program review: no live native question")
			}
		}
		if err != nil {
			g.failedKey, g.failedRevision = key, revision
		}
		return true, err
	}
	for i, candidate := range g.latest {
		if candidate.Key == key {
			g.latest[i].Revision = revision
			return finish()
		}
	}
	g.latest = append(g.latest, shippedImageReviewCandidate{key, revision, "approval:pending"})
	return finish()
}

func (g *shippedProgramReview) unavailable(key, revision string) bool {
	if g == nil {
		return false
	}
	g.mu.Lock()
	failed := g.failedKey == key && g.failedRevision == revision
	g.mu.Unlock()
	if failed {
		return true
	}
	return g.captures(key, revision) && g.openQuestion(key, revision) == ""
}

func (d *applicationDirectory) programReviewPredecessorCurrent(ctx context.Context, key, revision, program, question string) bool {
	if d == nil || !d.lock(ctx) {
		return false
	}
	defer d.unlock()
	if d.v3 == nil {
		return false
	}
	current := false
	err := d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		record, ok := d.v3.programRecord(key)
		approval := d.v3.programApprovals[key]
		current = ok && record.Revision == revision && record.Spec.Program == program && !approval.Removing && approval.Question == question && approval.Revision == revision && d.v3.programPending(key, record)
		return nil
	})
	return err == nil && current
}

func (d *applicationDirectory) pendingProgramReviewCandidates(ctx context.Context) ([]shippedImageReviewCandidate, error) {
	if d == nil || !d.lock(ctx) {
		return nil, errors.New("program review: registration owner unavailable")
	}
	defer d.unlock()
	if d.v3 == nil {
		return nil, errors.New("program review: registration owner unavailable")
	}
	rows := []shippedImageReviewCandidate{}
	err := d.withRegistrationReadLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		for key, record := range d.v3.programs {
			// Only a review the person already started carries across a
			// restart; a registration nobody reviewed yet waits in the
			// Panel and asks nothing at start.
			approval := d.v3.programApprovals[key]
			asked := approval.Question != "" || approval.Review == record.Revision
			if record.State == registration.StatePendingApproval && record.IdentityEvidence == registration.IdentityEvidenceProgram && len(record.Spec.Providers) > 0 && !approval.Removing && asked {
				rows = append(rows, shippedImageReviewCandidate{key, record.Revision, "approval:pending"})
			}
		}
		return nil
	})
	return rows, err
}

// markProgramReview records, with each member's registration, the revision a
// grouped review asked the person about, so pendingProgramReviewCandidates
// raises that review again after a restart. The mark changes nothing the
// registry projects, so it persists without the programs-changed callback.
func (d *applicationDirectory) markProgramReview(ctx context.Context, members []programReviewMember) error {
	if d == nil || len(members) == 0 || !d.lock(ctx) {
		return nil
	}
	defer d.unlock()
	if d.v3 == nil {
		return nil
	}
	return d.withRegistrationStorageLocked(ctx, func(map[string]legacy.ApplicationDescriptor) error {
		changed := false
		for _, member := range members {
			key := member.record.Key
			record, ok := d.v3.programRecord(key)
			approval := d.v3.programApprovals[key]
			if !ok || record.Revision != member.record.Revision || approval.Removing || approval.Review == record.Revision {
				continue
			}
			approval.Review = record.Revision
			d.v3.programApprovals[key] = approval
			changed = true
		}
		if !changed {
			return nil
		}
		if err := d.v3.persistLocked(ctx, maps.Clone(d.v3.entries)); err != nil {
			return fmt.Errorf("program review: recording the asked review: %w", err)
		}
		return nil
	})
}
