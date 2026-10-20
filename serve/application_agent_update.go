package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	askwire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Prepared update state shares the directory's existing self-registration CAS.
// Only the first old member retains the bounded immutable intent. Each final
// enrollment retains its service-created occurrence code after atomic commit.
type applicationAgentUpdate struct {
	ID            string                          `json:"id"`
	Question      string                          `json:"question,omitempty"`
	Request       applications.AgentUpdateRequest `json:"request"`
	Source        agentUpdateImage                `json:"source"`
	Members       []agentUpdateCapture            `json:"members"`
	Committed     bool                            `json:"committed,omitempty"`
	CommittedMs   int64                           `json:"committed_ms,omitempty"`
	ReadyAt       int64                           `json:"ready_at,omitempty"`
	Maintenance   bool                            `json:"maintenance,omitempty"`
	SourceRelease *agentNativeRelease             `json:"source_release,omitempty"`
	UndoAction    *askwire.NotificationResult     `json:"undo_action,omitempty"`
}
type agentUpdateImage struct {
	Program, Key, Hash string
	Bytes              int64
}
type agentUpdateCapture struct {
	Application, Revision, Binding, Title string
	Old, Staged                           agentUpdateImage
	Publisher                             asks.Publisher
	Release                               *agentNativeRelease
	Maintenance                           *agentMaintenanceCapture
	Backup                                string
	CompanionsBefore, CompanionsAfter     json.RawMessage
	CommittedRevision                     string
	CompanionBackup                       json.RawMessage
}

func (u *applicationAgentUpdate) valid() bool {
	if u.Request.ReviewOf != "" && (!agentHex(u.Request.ReviewOf, 24) || u.Request.UndoOf != "" || u.Maintenance) {
		return false
	}
	if !agentHex(u.ID, 24) || len(u.Question) > 1024 || len(u.Members) < 1 || len(u.Members) > 8 || u.Request.UndoOf == "" && len(u.Request.Members) != len(u.Members) || u.Request.UndoOf != "" && len(u.Request.Members) != 0 {
		return false
	}
	if u.SourceRelease != nil && (!u.SourceRelease.provenFor(u.Source) || u.SourceRelease.Origin == agentReleaseOriginKept) || u.Maintenance && (u.Question != "" || u.SourceRelease == nil) {
		return false
	}
	if u.Maintenance && u.Committed && u.CommittedMs <= 0 {
		return false
	}
	if u.ReadyAt != 0 && (!u.Committed || u.CommittedMs <= 0 || u.ReadyAt < u.CommittedMs) {
		return false
	}
	for _, m := range u.Members {
		if !providerName.MatchString(m.Application) || !agentHex(m.Revision, 24) || !agentHex(m.Binding, 32) || m.Old.Key == "" || m.Staged.Key == "" || !agentHex(m.Old.Hash, 32) || !agentHex(m.Staged.Hash, 32) || m.Title == "" || utf8.RuneCountInString(m.Title) > 64 {
			return false
		}
		if m.Release != nil && !m.Release.provenFor(m.Staged) {
			return false
		}
		if m.Backup != "" && m.Backup != m.Staged.Program+".previous" {
			return false
		}
		if len(m.CompanionsBefore) > 0 && !companionDeclarationBounded(m.CompanionsBefore) || len(m.CompanionsAfter) > 0 && !companionDeclarationBounded(m.CompanionsAfter) {
			return false
		}
		if m.CommittedRevision != "" && !agentHex(m.CommittedRevision, 24) {
			return false
		}
		if len(m.CompanionBackup) > 0 && (len(m.CompanionBackup) > 256<<10 || !json.Valid(m.CompanionBackup)) {
			return false
		}
		if m.Maintenance != nil && (!m.Maintenance.Kept.valid() || m.Maintenance.Kept.Current.proven() && !m.Maintenance.Kept.Current.provenFor(m.Old) || m.Maintenance.Question == "" || m.Maintenance.AnsweredMs <= 0) {
			return false
		}
		if u.Maintenance {
			// A Keep that recorded no companions updates its image alone.
			if len(m.CompanionsBefore) > 0 && len(m.CompanionsAfter) == 0 || len(m.CompanionsBefore) == 0 && (m.Maintenance == nil || m.Maintenance.CompanionDigest != "") {
				return false
			}
			if u.Request.UndoOf != "" {
				if !agentHex(u.Request.UndoOf, 24) || u.UndoAction == nil || u.UndoAction.Action != "undo" || u.UndoAction.DecidedMs <= 0 || u.UndoAction.DecidedBy == "" || !m.Release.provenFor(m.Staged) {
					return false
				}
			} else {
				eligible, refuse := agentMaintenancePolicy(m.Maintenance, m.Old, applicationAgentImage{Release: m.Release}, m.Staged)
				if !eligible || refuse || !agentReleaseSameContent(m.Release, u.SourceRelease) {
					return false
				}
			}
		}
	}
	return u.Source.Key != "" && agentHex(u.Source.Hash, 32)
}

// normalizeAgentCompanions reads a JSON null companion declaration as none.
// Intents carry companions without omitempty, so an update that recorded none
// wrote null, and a commit copied it into the mark. Read back as four bytes,
// it became a recorded declaration: its digest no longer matched any readback
// and Ready never came.
func normalizeAgentCompanions(a *applicationAgentMark) {
	if a == nil {
		return
	}
	none := func(raw *json.RawMessage) {
		if string(*raw) == "null" {
			*raw = nil
		}
	}
	none(&a.Companions)
	none(&a.CompanionPending)
	for _, u := range append([]*applicationAgentUpdate{a.Update}, a.UpdateHistory...) {
		if u == nil {
			continue
		}
		for i := range u.Members {
			none(&u.Members[i].CompanionsBefore)
			none(&u.Members[i].CompanionsAfter)
			none(&u.Members[i].CompanionBackup)
		}
	}
}

func agentUpdateResult(out applications.Outcome, reason string) applications.AgentUpdateResult {
	return applications.AgentUpdateResult{Outcome: out, Reason: reason, Enrollments: []applications.AgentEnrollment{}, StagedImages: []applications.AgentUpdateImage{}}
}

// A member metadata mutation retires its entire batch in the same self-mark CAS.
// The batch owner preserves reservations while writing its own exact proof.
func (d *applicationDirectory) guardAgentUpdateMarks(before, after map[string]selfMark) error {
	retired := map[string]bool{}
	for name, m := range before {
		if m.Agent != nil && m.Agent.UpdateReservation != "" && !d.agentUpdateEdits[m.Agent.UpdateReservation] && !reflect.DeepEqual(m, after[name]) {
			retired[m.Agent.UpdateReservation] = true
		}
	}
	retireAgentUpdateMarks(after, retired)
	return nil
}

func retireAgentUpdateMarks(marks map[string]selfMark, retired map[string]bool) {
	for name, m := range marks {
		if m.Agent == nil || !retired[m.Agent.UpdateReservation] && (m.Agent.Update == nil || !retired[m.Agent.Update.ID]) {
			continue
		}
		a := *m.Agent
		a.UpdateReservation = ""
		a.Update = nil
		m.Agent = &a
		marks[name] = m
	}
}

// Directory unlock supplies only work captured by a successful self-mark CAS,
// after releasing the directory and registration/storage locks.
func drainRetiredAgentUpdateQuestions(k *agentEnrollmentQuestions, retired []applicationAgentUpdate) {
	if k == nil {
		return
	}
	for _, u := range retired {
		r, found, err := k.a.book.ApplicationRecord(u.Question)
		if err != nil {
			k.a.report(err)
			continue
		}
		if !found || r.Key != asks.AgentUpdateKey || r.About != u.ID {
			continue
		}
		_, out, err := k.a.book.RetirePendingApplicationAuthorized(u.Question, nil)
		if err != nil {
			k.a.report(err)
		} else if out != "retired" && out != "unknown" && out != "answered" {
			k.a.report(errors.New("agent update: question retirement unavailable"))
		}
	}
}

// Registration Remove may reserve its index before editing the enrollment.
// Retire the exact member's entire batch through the same self-mark CAS owner.
func (d *applicationDirectory) retireAgentUpdateMemberLocked(ctx context.Context, name string, legacy map[string]wire.ApplicationDescriptor) error {
	marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
	if err != nil {
		return err
	}
	m := marks[name]
	if m.Agent == nil || m.Agent.UpdateReservation == "" {
		return nil
	}
	retired := map[string]bool{m.Agent.UpdateReservation: true}
	next := maps.Clone(marks)
	retireAgentUpdateMarks(next, retired)
	d.selfBase = base
	return d.persistAgentUpdate(ctx, next, legacy, retired)
}
func (d *applicationDirectory) persistAgentUpdate(ctx context.Context, next map[string]selfMark, legacy map[string]wire.ApplicationDescriptor, ids map[string]bool) error {
	d.agentUpdateEdits = ids
	defer func() { d.agentUpdateEdits = nil }()
	return d.persistUnconfirmedAtDescriptorLocked(ctx, next, legacy)
}

// agentUpdateHistoryLimit bounds the committed intents an anchor mark keeps
// for discovery and Undo.
const agentUpdateHistoryLimit = 8

// retainAgentUpdate appends a replaced committed intent to the bounded history
// and drops the oldest entries beyond the limit. A full history must never
// stop the next update from asking its question.
func retainAgentUpdate(history []*applicationAgentUpdate, replaced *applicationAgentUpdate) []*applicationAgentUpdate {
	next := append(append([]*applicationAgentUpdate(nil), history...), replaced)
	if len(next) > agentUpdateHistoryLimit {
		next = next[len(next)-agentUpdateHistoryLimit:]
	}
	return next
}

func agentUpdateIndex(marks map[string]selfMark) map[string]*applicationAgentUpdate {
	result := map[string]*applicationAgentUpdate{}
	for _, m := range marks {
		if m.Agent != nil {
			for _, u := range m.Agent.UpdateHistory {
				result[u.ID] = u
			}
		}
		if m.Agent != nil && m.Agent.Update != nil {
			result[m.Agent.Update.ID] = m.Agent.Update
		}
	}
	return result
}
func agentUpdateHasMember(u *applicationAgentUpdate, name string) bool {
	for _, m := range u.Members {
		if m.Application == name {
			return true
		}
	}
	return false
}

func (d *applicationDirectory) findAgentUpdate(ctx context.Context, s rights.Subject, request applications.AgentUpdateRequest) applications.AgentUpdateResult {
	if out := d.agentOperator(ctx, s); out != wire.ApplicationOutcomeApplied {
		parsed, _ := applications.ParseOutcome(out.String())
		return agentUpdateResult(parsed, "")
	}
	if len(request.Members) < 1 || len(request.Members) > 8 || request.UndoOf != "" || d.agentQuestions == nil {
		return agentUpdateResult(applications.OutcomeInvalid, "members")
	}
	discovery := request.SourceProgram == ""
	seen := map[string]bool{}
	for _, m := range request.Members {
		if !providerName.MatchString(m.Application) || seen[m.Application] || discovery && (m.ExpectedRevision != "" || m.StagedProgram != "") {
			return agentUpdateResult(applications.OutcomeInvalid, "members")
		}
		seen[m.Application] = true
	}
	request.Members = append([]applications.AgentUpdateMember(nil), request.Members...)
	sort.Slice(request.Members, func(i, j int) bool { return request.Members[i].Application < request.Members[j].Application })
	if !d.lock(ctx) {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	var u *applicationAgentUpdate
	err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		index := agentUpdateIndex(marks)
		if !discovery {
			for _, candidate := range index {
				if reflect.DeepEqual(candidate.Request, request) {
					copy := *candidate
					u = &copy
					break
				}
			}
			return nil
		}
		for _, member := range request.Members {
			mark := marks[member.Application]
			if mark.Agent == nil {
				continue
			}
			id := mark.Agent.UpdateReservation
			if id == "" && mark.Agent.Update != nil {
				id = mark.Agent.Update.ID
			}
			candidate := index[id]
			if candidate == nil {
				for _, retained := range index {
					if retained.Committed && agentUpdateHasMember(retained, member.Application) && committedAgentUpdateCurrent(d, legacy, marks, retained) {
						candidate = retained
						break
					}
				}
			}
			if candidate == nil || !agentUpdateHasMember(candidate, member.Application) {
				continue
			}
			if candidate.Committed && !committedAgentUpdateCurrent(d, legacy, marks, candidate) {
				continue
			}
			if u != nil && u.ID != candidate.ID {
				return errors.New("agent update: multiple intents")
			}
			copy := *candidate
			u = &copy
		}
		return nil
	})
	d.unlock()
	if err != nil {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	if u == nil {
		return agentUpdateResult(applications.OutcomeUnknown, "intent")
	}
	view := d.agentUpdateView(ctx, u)
	if discovery && agentUpdateQuestionExpired(u, view) {
		// ASK-A7 removed its unanswered question. Prepare supersedes such an
		// intent; discovery reports none so every caller reaches Prepare
		// (R24: an expired R23 question held every later update back).
		return agentUpdateResult(applications.OutcomeUnknown, "intent")
	}
	return view
}

// agentUpdateQuestionExpired reports an uncommitted, person-asked intent whose
// question expired unanswered. It carries no answer in either direction.
func agentUpdateQuestionExpired(u *applicationAgentUpdate, view applications.AgentUpdateResult) bool {
	return !u.Committed && !u.Maintenance && view.Outcome == applications.OutcomeConflict && view.Reason == "question_gone"
}
func updateImage(ctx context.Context, program string) (pin agentUpdateImage, err error) {
	h, err := fileid.Open(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks, Read: true})
	if err != nil {
		return pin, err
	}
	defer func() { err = errors.Join(err, h.Close()) }()
	info, err := h.File().Stat()
	if err != nil {
		return pin, err
	}
	if h.FileID().IsDir() || h.FileID().Links() != 1 || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > 256<<20 {
		return pin, errors.New("agent update: bounded regular image required")
	}
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(h.File(), info.Size()+1))
	if err != nil {
		return pin, err
	}
	if n != info.Size() {
		return pin, errors.New("agent update: image changed")
	}
	pin = agentUpdateImage{Program: h.FileID().Path(), Key: h.FileID().Key(), Hash: hex.EncodeToString(hash.Sum(nil)), Bytes: n}
	named, err := fileid.Resolve(ctx, fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return pin, err
	}
	if named.Key() != pin.Key {
		return pin, errors.New("agent update: image replaced")
	}
	return pin, ctx.Err()
}
func sameUpdateImage(a, b agentUpdateImage) bool {
	return a.Key == b.Key && a.Hash == b.Hash && a.Bytes == b.Bytes
}
func updateImageMatches(ctx context.Context, expected agentUpdateImage, program string) bool {
	now, err := updateImage(ctx, program)
	return err == nil && sameUpdateImage(expected, now)
}

func (d *applicationDirectory) prepareAgentUpdate(ctx context.Context, s rights.Subject, request applications.AgentUpdateRequest) applications.AgentUpdateResult {
	if out := d.agentOperator(ctx, s); out != wire.ApplicationOutcomeApplied {
		parsed, _ := applications.ParseOutcome(out.String())
		return agentUpdateResult(parsed, "")
	}
	// A child Undo requires its authenticated native action and exact retained
	// prior-byte source owner. An unhandled designation never enters a normal
	// fresh Update question or borrows the installed source's authority.
	if request.ReviewOf != "" && (!agentHex(request.ReviewOf, 24) || request.UndoOf != "") {
		return agentUpdateResult(applications.OutcomeConflict, "review_reference")
	}
	if request.UndoOf != "" {
		return d.prepareAgentUndo(ctx, s, request)
	}
	if len(request.Members) < 1 || len(request.Members) > 8 || request.SourceProgram == "" || d.agentQuestions == nil || d.agentExcluded == nil {
		return agentUpdateResult(applications.OutcomeInvalid, "members")
	}
	request.Members = append([]applications.AgentUpdateMember(nil), request.Members...)
	sort.Slice(request.Members, func(i, j int) bool { return request.Members[i].Application < request.Members[j].Application })
	seen := map[string]bool{}
	for _, m := range request.Members {
		if !providerName.MatchString(m.Application) || m.ExpectedRevision == "" || m.StagedProgram == "" || seen[m.Application] {
			return agentUpdateResult(applications.OutcomeInvalid, "members")
		}
		seen[m.Application] = true
	}
	// An identical retained intent reuses its actual question, including a
	// decline. Background polling never renews a person's Not now answer.
	if !d.lock(ctx) {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	priors := map[string]*applicationAgentUpdate{}
	orphans := map[string]bool{}
	var matching *applicationAgentUpdate
	err := d.withRegistrationReadLocked(ctx, func(_ map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		index := agentUpdateIndex(marks)
		for _, candidate := range index {
			if reflect.DeepEqual(candidate.Request, request) {
				copy := *candidate
				matching = &copy
				return nil
			}
		}
		for _, member := range request.Members {
			m := marks[member.Application]
			if m.Agent == nil || m.Agent.UpdateReservation == "" {
				continue
			}
			id := m.Agent.UpdateReservation
			candidate := index[id]
			if candidate == nil {
				orphans[id] = true
				continue
			}
			if !agentUpdateHasMember(candidate, member.Application) {
				return errors.New("agent update: invalid reservation member")
			}
			copy := *candidate
			priors[id] = &copy
		}
		return nil
	})
	d.unlock()
	if err != nil {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	if matching != nil {
		if matching.Question == "" && !matching.Maintenance {
			return d.askAgentUpdate(ctx, matching)
		}
		view := d.agentUpdateView(ctx, matching)
		if agentUpdateQuestionExpired(matching, view) {
			// The same staged copies ask once more under the same intent.
			return d.askAgentUpdate(ctx, matching)
		}
		return view
	}
	source, err := updateImage(ctx, request.SourceProgram)
	if err != nil {
		return agentUpdateResult(applications.OutcomeUnavailable, "source")
	}
	installed, err := d.agentExcluded(ctx, source.Program, source.Key)
	if err != nil || !installed {
		return agentUpdateResult(applications.OutcomeForbidden, "installed_source")
	}
	for _, prior := range priors {
		view := d.agentUpdateView(ctx, prior)
		if view.Outcome == applications.OutcomeUnavailable {
			return view
		}
		if view.Outcome != applications.OutcomeApplied && view.Reason != "question_gone" {
			return view
		}
		if prior.Source.Hash == source.Hash && view.Reason != "not_now" && view.Reason != "question_gone" {
			return agentUpdateResult(applications.OutcomeConflict, "update_pending")
		}
		if request.ReviewOf == "" && prior.Source.Hash == source.Hash && view.Reason == "not_now" && agentReviewMemberSubset(request, prior) {
			return view
		}
	}
	id, err := applicationToken()
	if err != nil {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	u := &applicationAgentUpdate{ID: id, Request: request, Source: source, Members: []agentUpdateCapture{}, Maintenance: d.agentMaintenance}
	read := d.agentImage
	if read == nil {
		read = readApplicationAgentImage
	}
	sourceNative, sourceErr := read(ctx, source.Program, "")
	if sourceErr == nil {
		u.SourceRelease = d.agentSourceRelease(ctx, sourceNative, source)
	}
	var review *applicationAgentUpdate
	if request.ReviewOf != "" {
		review, err = d.agentReviewReference(ctx, request, source, u.SourceRelease)
		if err != nil {
			return agentUpdateResult(applications.OutcomeConflict, "review_reference")
		}
		u.Maintenance = false
	}
	if held, err := d.agentUndoSuppression(ctx, request, u.SourceRelease); review == nil && err != nil {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	} else if review == nil && held != "" {
		result := agentUpdateResult(applications.OutcomeApplied, "not_now")
		result.Intent = held
		return result
	}
	for _, member := range request.Members {
		var oldNative applicationAgentImage
		old := d.currentAgentEnrollmentImage(ctx, member.Application, &oldNative)
		if old.Outcome != applications.OutcomePage || old.Enrollment == nil || old.Enrollment.State != applications.AgentEnrollmentStateEnrolled || old.Enrollment.Revision != member.ExpectedRevision {
			return agentUpdateResult(applications.OutcomeConflict, "enrollment:"+member.Application)
		}
		v, binding, err := d.agentSnapshot(ctx, member.Application)
		if err != nil {
			return agentUpdateResult(applications.OutcomeUnavailable, "registration")
		}
		oldPin, err := updateImage(ctx, v.Program)
		if err != nil {
			return agentUpdateResult(applications.OutcomeUnavailable, "old_image")
		}
		stage, err := updateImage(ctx, member.StagedProgram)
		if err != nil || stage.Key == oldPin.Key || stage.Hash != source.Hash || stage.Bytes != source.Bytes {
			return agentUpdateResult(applications.OutcomeConflict, "staged_image")
		}
		parent, err := fileid.Resolve(ctx, fileid.Self, filepath.Dir(oldPin.Program), fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			return agentUpdateResult(applications.OutcomeUnavailable, "directory")
		}
		stageParent, err := fileid.Resolve(ctx, fileid.Self, filepath.Dir(stage.Program), fileid.Options{Links: fileid.RefuseLinks})
		if err != nil || !parent.Equal(stageParent) {
			return agentUpdateResult(applications.OutcomeConflict, "staged_directory")
		}
		native, err := read(ctx, stage.Program, binding)
		if err != nil || native.Key != stage.Key {
			return agentUpdateResult(applications.OutcomeConflict, "staged_identity")
		}
		if !native.Release.validFor(stage) && native.Proof != identity.ProofInvalid {
			// Staged bytes equal to the installed copy carry its facts.
			native.Release = agentStagedRelease(u.SourceRelease, stage)
		}
		maintenance, err := d.captureAgentMaintenance(ctx, member.Application, member.ExpectedRevision)
		if err != nil {
			return agentUpdateResult(applications.OutcomeUnavailable, "state")
		}
		var companionsBefore, companionsAfter json.RawMessage
		if maintenance != nil && maintenance.CompanionDigest != "" {
			before, readErr := d.readAgentCompanions(ctx, oldPin.Program, oldPin.Key)
			if readErr != nil || companionDeclarationDigest(before) != maintenance.CompanionDigest {
				return agentUpdateResult(applications.OutcomeConflict, "registration")
			}
			companionsBefore = before
			companionsAfter, _ = d.successorAgentCompanions(ctx, before, oldPin.Program, stage.Key)
		}
		if d.agentMaintenance {
			if oldNative.Proof != identity.ProofInvalid {
				deriveKeptRelease(ctx, maintenance, oldPin)
			}
			eligible, refuse := agentMaintenancePolicy(maintenance, oldPin, native, stage)
			if maintenance != nil && (oldNative.Proof == identity.ProofInvalid || sourceNative.Proof == identity.ProofInvalid) {
				refuse = true
			}
			if refuse {
				return agentUpdateResult(applications.OutcomeConflict, "staged_identity")
			}
			if sourceErr != nil || !agentReleaseSameContent(u.SourceRelease, native.Release) {
				eligible = false
			}
			u.Maintenance = u.Maintenance && eligible
			if maintenance == nil || maintenance.CompanionDigest != "" && (len(companionsBefore) == 0 || len(companionsAfter) == 0) {
				u.Maintenance = false
			}
		}
		if strings.TrimSpace(v.Title) == "" {
			return agentUpdateResult(applications.OutcomeUnavailable, "title")
		}
		title := selfRegisteredTitle(v)
		if utf8.RuneCountInString(title) > 64 {
			title = string([]rune(title)[:63]) + "…"
		}
		captured := agentUpdateCapture{Application: member.Application, Revision: member.ExpectedRevision, Binding: binding, Title: title, Old: oldPin, Staged: stage, Publisher: native.Publisher, Release: cloneAgentRelease(native.Release), Maintenance: maintenance, Backup: stage.Program + ".previous", CompanionsBefore: companionsBefore, CompanionsAfter: companionsAfter}
		if len(companionsBefore) > 0 {
			captured.CompanionBackup, err = d.captureAgentCompanionBackup(ctx, companionsBefore, u.ID, func() error { return d.agentCaptureAuthority(ctx, captured) })
			if err != nil {
				u.Maintenance = false
			}
		}
		u.Members = append(u.Members, captured)
	}
	for _, prior := range priors {
		if prior.Question != "" {
			_, out, err := d.agentQuestions.a.book.RetireApplicationAuthorized(prior.Question, func() error {
				if d.agentOperator(ctx, s) != wire.ApplicationOutcomeApplied {
					return errors.New("agent update: operator changed")
				}
				return nil
			})
			if err != nil || (out != "retired" && out != "unknown") {
				return agentUpdateResult(applications.OutcomeUnavailable, "prior_question")
			}
		}
	}
	if len(orphans) > 0 {
		history, _, err := d.agentQuestions.a.book.ApplicationHistory()
		if err != nil {
			return agentUpdateResult(applications.OutcomeUnavailable, "question")
		}
		for _, r := range history {
			if r.Key != asks.AgentUpdateKey || !orphans[r.About] {
				continue
			}
			_, out, err := d.agentQuestions.a.book.RetireApplicationAuthorized(r.ID, func() error {
				if d.agentOperator(ctx, s) != wire.ApplicationOutcomeApplied {
					return errors.New("agent update: operator changed")
				}
				return nil
			})
			if err != nil || out != "retired" {
				return agentUpdateResult(applications.OutcomeUnavailable, "orphan_question")
			}
		}
	}
	if !d.lock(ctx) {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	err = d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		index := agentUpdateIndex(marks)
		if review != nil && (!reflect.DeepEqual(index[review.ID], review) || !d.agentReviewMembersCurrent(legacy, marks, request, review)) {
			return errors.New("agent update: review reference changed")
		}
		allowed := map[string]bool{u.ID: true}
		for id, prior := range priors {
			if !reflect.DeepEqual(index[id], prior) {
				return errors.New("agent update: prior intent changed")
			}
			allowed[id] = true
		}
		for id := range orphans {
			if index[id] != nil {
				return errors.New("agent update: orphan became live")
			}
			allowed[id] = true
		}
		marks = maps.Clone(marks)
		for name, m := range marks {
			if m.Agent == nil || (!orphans[m.Agent.UpdateReservation] && priors[m.Agent.UpdateReservation] == nil) {
				continue
			}
			a := *m.Agent
			a.UpdateReservation = ""
			if a.Update != nil && allowed[a.Update.ID] {
				a.Update = nil
			}
			m.Agent = &a
			marks[name] = m
		}
		if !d.updateRegistrationsCurrent(legacy, marks, u, false) {
			return errors.New("agent update: changed registration")
		}
		next := maps.Clone(marks)
		for _, c := range u.Members {
			m := next[c.Application]
			a := *m.Agent
			a.UpdateReservation = u.ID
			m.Agent = &a
			next[c.Application] = m
		}
		anchor := next[u.Members[0].Application]
		a := *anchor.Agent
		if a.Update != nil && a.Update.ID != u.ID {
			if !a.Update.Committed {
				return errors.New("agent update: live parent intent")
			}
			a.UpdateHistory = retainAgentUpdate(a.UpdateHistory, a.Update)
		}
		a.Update = u
		anchor.Agent = &a
		next[u.Members[0].Application] = anchor
		d.selfBase = base
		return d.persistAgentUpdate(ctx, next, legacy, allowed)
	})
	d.unlock()
	if err != nil {
		return agentUpdateResult(applications.OutcomeConflict, "registration")
	}
	if u.Maintenance {
		return d.agentUpdateView(ctx, u)
	}
	return d.askAgentUpdate(ctx, u)
}

func (d *applicationDirectory) updateRegistrationsCurrent(legacy map[string]wire.ApplicationDescriptor, marks map[string]selfMark, u *applicationAgentUpdate, final bool) bool {
	for _, c := range u.Members {
		v, binding, ok := d.agentEntryLocked(legacy, c.Application)
		m := marks[c.Application]
		if !ok || binding != c.Binding || !applicationRegistrationPrograms(v.Program, c.Old.Program, m.ProgramKey, c.Old.Key) || m.Kind != "agent" || m.Agent == nil || m.Removing || m.Agent.Activation != nil || m.Agent.State != "enrolled" || m.Agent.Account != d.owner || m.Agent.Binding != c.Binding || m.RegistrationRevision != c.Revision || m.ProgramKey != c.Old.Key {
			return false
		}
		if u.Maintenance && !agentMaintenanceCurrent(m, c.Maintenance) {
			return false
		}
		if m.Agent.UpdateReservation != "" && m.Agent.UpdateReservation != u.ID {
			return false
		}
		if final && m.Agent.UpdateReservation != u.ID {
			return false
		}
	}
	return true
}
func (d *applicationDirectory) askAgentUpdate(ctx context.Context, u *applicationAgentUpdate) applications.AgentUpdateResult {
	labels := []string{}
	for _, m := range u.Members {
		labels = append(labels, m.Title)
	}
	list := strings.Join(labels, ", ")
	if len(labels) > 1 {
		list = strings.Join(labels[:len(labels)-1], ", ") + " and " + labels[len(labels)-1]
	}
	parts := []string{"", "", ""}
	runes := []rune(list)
	for i := range parts {
		n := min(200, len(runes))
		parts[i] = string(runes[:n])
		runes = runes[n:]
	}
	r, out, err := d.agentQuestions.a.book.AskApplicationFrom(agentEnrollmentScope(d.agentQuestions.a.scope, u.ID), d.agentQuestions.a.display, "agent-update:"+u.ID, asks.AgentUpdateKey, map[string]string{"code": u.ID, "members": parts[0], "membersmore": parts[1], "memberslast": parts[2]}, listen.Seen{Why: "explicit operator request for one exact person-kept connection update"}, asks.Publisher{})
	if err != nil || (out != "pending" && out != "answered") || r.ID == "" {
		return agentUpdateResult(applications.OutcomeUnavailable, "question")
	}
	if !d.lock(ctx) {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	err = d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		name := u.Members[0].Application
		m := marks[name]
		if m.Agent == nil || m.Agent.Update == nil || m.Agent.Update.ID != u.ID || !d.updateRegistrationsCurrent(legacy, marks, u, false) {
			return errors.New("agent update: stale question")
		}
		copy := *m.Agent.Update
		copy.Question = r.ID
		a := *m.Agent
		a.Update = &copy
		m.Agent = &a
		next := maps.Clone(marks)
		next[name] = m
		d.selfBase = base
		return d.persistAgentUpdate(ctx, next, legacy, map[string]bool{u.ID: true})
	})
	d.unlock()
	if err != nil {
		return agentUpdateResult(applications.OutcomeConflict, "registration")
	}
	u.Question = r.ID
	return d.agentUpdateView(ctx, u)
}
func (d *applicationDirectory) agentUpdateView(ctx context.Context, u *applicationAgentUpdate) applications.AgentUpdateResult {
	result := agentUpdateResult(applications.OutcomeApplied, "permission_pending")
	result.Intent = u.ID
	result.Question = u.Question
	phase := applications.AgentUpdatePhaseStaged
	if u.Committed {
		phase = applications.AgentUpdatePhaseCommitted
	}
	if u.ReadyAt > 0 {
		phase = applications.AgentUpdatePhaseReady
		at := u.ReadyAt
		result.ReadyUnixMs = &at
	}
	result.Phase = &phase
	for _, m := range u.Members {
		result.StagedImages = append(result.StagedImages, applications.AgentUpdateImage{Application: m.Application, Program: m.Staged.Program, FileKey: m.Staged.Key})
	}
	if u.Maintenance {
		if !d.agentMaintenance {
			return agentUpdateResult(applications.OutcomeUnavailable, "state")
		}
		result.Reason = "update_ready"
		if u.Committed {
			result.Reason = "updated"
		}
		return result
	}
	if u.Question == "" {
		result.Reason = "question_pending"
		return result
	}
	r, found, err := d.agentQuestions.a.book.ApplicationRecord(u.Question)
	if err != nil {
		result.Outcome = applications.OutcomeUnavailable
		result.Reason = "question"
		return result
	}
	if !found {
		result.Outcome = applications.OutcomeConflict
		result.Reason = "question_gone"
		return result
	}
	if r.Key != asks.AgentUpdateKey || r.About != u.ID {
		result.Outcome = applications.OutcomeConflict
		result.Reason = "question"
		return result
	}
	if r.Pending() {
		return result
	}
	if r.AnsweredBy != asks.AnsweredByOperator || !r.Yes || r.Option != "update" {
		result.Reason = "not_now"
		return result
	}
	result.Reason = "update_ready"
	if u.Committed {
		result.Reason = "updated"
	}
	return result
}

func (d *applicationDirectory) commitAgentUpdate(ctx context.Context, s rights.Subject, id string) applications.AgentUpdateResult {
	if out := d.agentOperator(ctx, s); out != wire.ApplicationOutcomeApplied {
		parsed, _ := applications.ParseOutcome(out.String())
		return agentUpdateResult(parsed, "")
	}
	if !agentHex(id, 24) || d.agentQuestions == nil {
		return agentUpdateResult(applications.OutcomeInvalid, "intent")
	}
	if !d.lock(ctx) {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	var u *applicationAgentUpdate
	err := d.withRegistrationReadLocked(ctx, func(_ map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		for _, m := range marks {
			if m.Agent != nil && m.Agent.Update != nil && m.Agent.Update.ID == id {
				copy := *m.Agent.Update
				u = &copy
				break
			}
		}
		return nil
	})
	d.unlock()
	if err != nil {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	if u == nil {
		return agentUpdateResult(applications.OutcomeUnknown, "intent")
	}
	result := d.agentUpdateView(ctx, u)
	if result.Reason != "update_ready" && result.Reason != "updated" {
		return result
	}
	if !updateImageMatches(ctx, u.Source, u.Source.Program) {
		return agentUpdateResult(applications.OutcomeConflict, "source_changed")
	}
	installed, err := d.agentExcluded(ctx, u.Source.Program, u.Source.Key)
	if err != nil || !installed {
		return agentUpdateResult(applications.OutcomeConflict, "source_changed")
	}
	if u.Maintenance && !d.agentMaintenanceProof(ctx, u) {
		return agentUpdateResult(applications.OutcomeConflict, "person_answer")
	}
	allFinal := true
	for _, m := range u.Members {
		v, _, err := d.agentSnapshot(ctx, m.Application)
		if err != nil {
			return agentUpdateResult(applications.OutcomeConflict, "registration")
		}
		if updateImageMatches(ctx, m.Staged, v.Program) {
			if u.Maintenance && !updateImageMatches(ctx, m.Old, m.Backup) {
				return agentUpdateResult(applications.OutcomeConflict, "image_changed:"+m.Application)
			}
			continue
		}
		if u.Committed || !updateImageMatches(ctx, m.Old, m.Old.Program) || !updateImageMatches(ctx, m.Staged, m.Staged.Program) {
			return agentUpdateResult(applications.OutcomeConflict, "image_changed:"+m.Application)
		}
		allFinal = false
	}
	if !allFinal {
		if !d.lock(ctx) {
			return agentUpdateResult(applications.OutcomeUnavailable, "state")
		}
		current := false
		err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
			marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
			if err != nil {
				return err
			}
			current = d.updateRegistrationsCurrent(legacy, marks, u, true)
			return nil
		})
		d.unlock()
		if err != nil || !current {
			return agentUpdateResult(applications.OutcomeConflict, "registration")
		}
		return result
	}
	if u.Committed {
		for _, m := range u.Members {
			got := d.currentAgentEnrollment(ctx, m.Application)
			if got.Enrollment == nil || got.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
				return agentUpdateResult(applications.OutcomeConflict, "enrollment")
			}
			result.Enrollments = append(result.Enrollments, *got.Enrollment)
		}
		if err := d.restoreAgentUndo(ctx, s, u); err != nil {
			return agentUpdateResult(applications.OutcomeUnavailable, "state")
		}
		return d.readyAgentUpdate(ctx, s, u, result)
	}
	var r asks.Record
	if !u.Maintenance {
		var found bool
		r, found, err = d.agentQuestions.a.book.ApplicationRecord(u.Question)
		if err != nil || !found || r.Pending() || r.Key != asks.AgentUpdateKey || r.About != id || r.Option != "update" || !r.Yes || r.AnsweredBy != asks.AnsweredByOperator {
			return agentUpdateResult(applications.OutcomeConflict, "person_answer")
		}
	}
	if !d.lock(ctx) {
		return agentUpdateResult(applications.OutcomeUnavailable, "state")
	}
	err = d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		anchor := marks[u.Members[0].Application]
		if anchor.Agent == nil || anchor.Agent.Update == nil || anchor.Agent.Update.ID != id || anchor.Agent.Update.Question != u.Question || !d.updateRegistrationsCurrent(legacy, marks, u, true) {
			return errors.New("agent update: registration changed")
		}
		next := maps.Clone(marks)
		for _, c := range u.Members {
			v, binding, _ := d.agentEntryLocked(legacy, c.Application)
			for name, other := range marks {
				if name == c.Application || other.Agent == nil || other.Agent.Account != d.owner || (other.Agent.State != "pending" && other.Agent.State != "enrolled") {
					continue
				}
				if applicationRegistrationPrograms(other.Program, v.Program, other.ProgramKey, c.Staged.Key) {
					return errors.New("agent update: subject already enrolled")
				}
			}
			revision, err := applicationToken()
			if err != nil {
				return err
			}
			m := newSelfMark(v.Program, c.Publisher)
			m.Kind = "agent"
			m.ProgramKey = c.Staged.Key
			m.RegistrationRevision = revision
			m.Question = u.Question
			m.Agent = &applicationAgentMark{Account: d.owner, Binding: binding, State: "enrolled", AnsweredBy: r.AnsweredBy, AnsweredMs: r.Answered.UnixMilli(), UpdateProof: id, EnrollmentIdentity: marks[c.Application].agentEnrollmentIdentity()}
			m.Agent.UpdateHistory = append([]*applicationAgentUpdate(nil), marks[c.Application].Agent.UpdateHistory...)
			// The helper still running the kept image may read this
			// enrollment until the next commit (readMovedConnectorEnrollment).
			m.Agent.MovedKey = c.Old.Key
			m.Agent.Companions = append(json.RawMessage(nil), c.CompanionsAfter...)
			if c.Release.provenFor(c.Staged) {
				kept := c.Maintenance
				if kept != nil {
					copy := kept.Kept
					copy.Current = *cloneAgentRelease(c.Release)
					m.Agent.KeptRelease = &copy
				}
			}
			if u.Maintenance {
				m.Question = c.Maintenance.Question
				m.Agent.AnsweredBy = c.Maintenance.AnsweredBy
				m.Agent.AnsweredMs = c.Maintenance.AnsweredMs
				m.Agent.UpdateProof = c.Maintenance.UpdateProof
			}
			if catalogueMaintenanceSource(u, c) {
				prior := marks[c.Application]
				companion, err := loadCatalogueCompanion(ctx, d.dir, c.Application, prior)
				if err != nil {
					return err
				}
				if companion.matches(c.Application, prior) && prior.ProgramKey == c.Old.Key &&
					prior.agentEnrollmentIdentity() == m.agentEnrollmentIdentity() && prior.Question == m.Question {
					companion.Revision, companion.Image = m.RegistrationRevision, m.ProgramKey
					if err := writeCatalogueCompanion(ctx, d.dir, companion, prior); err != nil {
						return err
					}
				}
			}
			next[c.Application] = m
			result.Enrollments = append(result.Enrollments, agentEnrollmentView(c.Application, v.Program, binding, d.owner, m))
		}
		name := u.Members[0].Application
		a := next[name]
		copy := *a.Agent
		done := *u
		done.Members = append([]agentUpdateCapture(nil), u.Members...)
		for i := range done.Members {
			done.Members[i].CommittedRevision = next[done.Members[i].Application].RegistrationRevision
		}
		done.Committed = true
		done.CommittedMs = d.now().UnixMilli()
		if done.CommittedMs <= 0 {
			return errors.New("agent update: ordinary Commit time unavailable")
		}
		copy.Update = &done
		a.Agent = &copy
		next[name] = a
		d.selfBase = base
		return d.persistAgentUpdate(ctx, next, legacy, map[string]bool{u.ID: true})
	})
	d.unlock()
	if err != nil {
		return agentUpdateResult(applications.OutcomeConflict, "registration")
	}
	result.Reason = "updated"
	phase := applications.AgentUpdatePhaseCommitted
	result.Phase = &phase
	return d.commitAgentUpdate(ctx, s, id)
}

// Every enrollment consumer uses the exact native occurrence semantics. A
// batch proof is written only after the service commits captured identities.
func agentPersonAnswer(m selfMark, r asks.Record) bool {
	if m.Agent == nil || r.Pending() || !r.Yes || r.AnsweredBy != asks.AnsweredByOperator || r.Answered.UnixMilli() != m.Agent.AnsweredMs {
		return false
	}
	if m.Agent.UpdateProof != "" {
		return r.Key == asks.AgentUpdateKey && r.Option == "update" && r.About == m.Agent.UpdateProof
	}
	return r.Key == asks.AgentEnrollmentKey && r.Option == "keep"
}
