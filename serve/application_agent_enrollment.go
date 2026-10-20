package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"path/filepath"
	"reflect"
	"strings"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	cas "github.com/openabstractions/abstraction-cas/go"
	credservice "github.com/openabstractions/abstraction-credentials/go/service"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstraction-identity/listen"
	rightspolicy "github.com/openabstractions/abstraction-rights/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Agent records live in the existing self-registered metadata. Older writers
// that decode/rewrite only their known fields drop Kind/Agent, revoking class.
// Generic pending registrations keep their original empty kind and wire shape.
type applicationAgentMark struct {
	Account            string                           `json:"account"`
	EnrollmentIdentity string                           `json:"enrollment_identity,omitempty"`
	Binding            string                           `json:"binding"`
	State              string                           `json:"state"`
	AnsweredBy         string                           `json:"answered_by,omitempty"`
	AnsweredMs         int64                            `json:"answered_ms,omitempty"`
	BaseRevision       string                           `json:"base_revision,omitempty"`
	Activation         *applicationActivationTransition `json:"activation,omitempty"`
	Update             *applicationAgentUpdate          `json:"update,omitempty"`
	UpdateHistory      []*applicationAgentUpdate        `json:"update_history,omitempty"`
	UpdateProof        string                           `json:"update_proof,omitempty"`
	UpdateReservation  string                           `json:"update_reservation,omitempty"`
	ReleasePending     *agentNativeRelease              `json:"release_pending,omitempty"`
	KeptRelease        *agentKeptRelease                `json:"kept_release,omitempty"`
	CompanionPending   json.RawMessage                  `json:"companion_pending,omitempty"`
	Companions         json.RawMessage                  `json:"companions,omitempty"`
	// MovedKey is the file key of the image this application ran before its
	// latest committed update: the image the person kept. A helper still
	// running it after Setup moved it aside may read this application's
	// enrollment (CONNECTOR-HANDOVER-AUTH). The next commit replaces it; a
	// Keep, revoke or removal writes a mark without it.
	MovedKey string `json:"moved_key,omitempty"`
}

// agentEnrollmentIdentity is stable across an accepted connection update.
// Existing enrollments already have a unique revision to seed the identity.
func (m selfMark) agentEnrollmentIdentity() string {
	if m.Agent == nil || m.Agent.State != "enrolled" {
		return ""
	}
	if m.Agent.EnrollmentIdentity != "" {
		return m.Agent.EnrollmentIdentity
	}
	return m.RegistrationRevision
}

var errApplicationAgentSharedGateway = errors.New("agent enrollment: shared gateway cannot enroll")

func decodeApplicationSelfMarks(b []byte, target *map[string]selfMark) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return errors.New("applications: trailing self metadata")
	}
	if *target == nil || len(*target) > 64 {
		return errors.New("applications: self metadata capacity")
	}
	for name, m := range *target {
		normalizeAgentCompanions(m.Agent)
		if !providerName.MatchString(name) || !m.valid() {
			return errors.New("applications: invalid self metadata")
		}
		if m.Agent != nil && m.Agent.Activation != nil && m.Agent.Activation.Before.Name != name {
			return errors.New("applications: activation marker name")
		}
	}
	return nil
}

func (m selfMark) genericPending() bool { return m.Kind == "" && m.Agent == nil }
func (m selfMark) pendingOwnership() bool {
	return m.genericPending() || m.Kind == "agent" && m.Agent != nil && m.Agent.State == "pending"
}

func agentHex(v string, size int) bool {
	b, err := hex.DecodeString(v)
	return err == nil && len(b) == size && hex.EncodeToString(b) == v
}

func (m selfMark) validAgent() bool {
	a := m.Agent
	if a == nil || !agentHex(m.RegistrationRevision, 24) || !agentHex(a.Binding, 32) ||
		!identity.ValidSubjectProgram(m.Program) || m.ProgramKey == "" || len(m.ProgramKey) > 1024 || !utf8.ValidString(m.ProgramKey) ||
		a.Account == "" || len(a.Account) > 4096 || !utf8.ValidString(a.Account) || len(a.BaseRevision) > 256 || !utf8.ValidString(a.BaseRevision) || m.UnboundQuestion != "" ||
		len(m.Question) > 1024 || !utf8.ValidString(m.Question) {
		return false
	}
	if a.EnrollmentIdentity != "" && (!agentHex(a.EnrollmentIdentity, 24) || a.State != "enrolled") {
		return false
	}

	if len(a.MovedKey) > 1024 || !utf8.ValidString(a.MovedKey) || a.MovedKey != "" && a.State != "enrolled" {
		return false
	}
	if a.UpdateProof != "" && !agentHex(a.UpdateProof, 24) || a.UpdateReservation != "" && !agentHex(a.UpdateReservation, 24) || a.Update != nil && !a.Update.valid() {
		return false
	}
	if len(a.UpdateHistory) > agentUpdateHistoryLimit {
		return false
	}
	for _, u := range a.UpdateHistory {
		if u == nil || !u.Committed || !u.valid() {
			return false
		}
	}
	if a.ReleasePending != nil && !a.ReleasePending.valid() || a.KeptRelease != nil && !a.KeptRelease.valid() {
		return false
	}
	if len(a.CompanionPending) > 0 && !companionDeclarationBounded(a.CompanionPending) || len(a.Companions) > 0 && !companionDeclarationBounded(a.Companions) {
		return false
	}
	if a.Activation != nil && (!a.Activation.valid(m.Program) || m.Removing || a.State != "enrolled" || a.Binding != activationDescriptorBinding(a.Activation.Before)) {
		return false
	}
	if m.Removing {
		validState := (a.State == "none" || a.State == "pending") && a.AnsweredBy == "" && a.AnsweredMs == 0 || a.State == "enrolled" && a.AnsweredBy == asks.AnsweredByOperator && a.AnsweredMs > 0
		return validState && strings.HasPrefix(m.Question, messageRemovalQuestion) && agentHex(m.RemovalRevision, 32)
	}
	if m.RemovalRevision != "" {
		return false
	}
	for _, v := range []string{m.Publisher, m.Description, m.OriginalName} {
		if len(v) > 1024 || !utf8.ValidString(v) {
			return false
		}
	}
	switch a.State {
	case "none":
		return m.Question == "" && a.AnsweredBy == "" && a.AnsweredMs == 0
	case "pending":
		return a.AnsweredBy == "" && a.AnsweredMs == 0
	case "enrolled":
		return m.Question != "" && a.AnsweredBy == asks.AnsweredByOperator && a.AnsweredMs > 0
	}
	return false
}

// entry is captured under descriptors CAS. No program or signature I/O occurs
// here; @3 decoding uses retained provenance only. A legacy entry is supported
// directly and is never promoted to a signed-manifest server registration.
func (d *applicationDirectory) agentEntryLocked(legacy map[string]wire.ApplicationDescriptor, name string) (wire.ApplicationDescriptor, string, bool) {
	v, ok := legacy[name]
	revision := ""
	if d.v3 != nil {
		if e, found := d.v3.entries[name]; found {
			f := d.v3.files[name]
			decoded, _, err := d.v3.decode(f.Raw, f.Source.Program, nil)
			if err != nil {
				return wire.ApplicationDescriptor{}, "", false
			}
			v, ok, revision = decoded, true, e.Revision
		}
	}
	if !ok {
		return v, "", false
	}
	b, err := json.Marshal(struct {
		Descriptor wire.ApplicationDescriptor
		Revision   string
	}{v, revision})
	if err != nil {
		return v, "", false
	}
	return v, applicationV3Hash(b), true
}

func agentEnrollmentResult(out applications.Outcome, reason string) applications.AgentEnrollmentResult {
	return applications.AgentEnrollmentResult{Outcome: out, Reason: reason}
}

func (d *applicationDirectory) agentOperator(ctx context.Context, s rights.Subject) wire.ApplicationOutcome {
	if s.Account != d.owner || d.operator == nil || !d.operator(s.Program) {
		return wire.ApplicationOutcomeForbidden
	}
	return d.authorized(ctx, s, ActionApplicationManage, "account")
}

func agentEnrollmentView(name, program, binding, account string, m selfMark) applications.AgentEnrollment {
	v := applications.AgentEnrollment{Application: name, Program: program, State: applications.AgentEnrollmentStateNone}
	if m.Kind != "agent" || m.Agent == nil || m.Removing || m.Agent.Activation != nil {
		return v
	}
	v.Revision = m.RegistrationRevision
	if m.Agent.Binding != binding || m.Agent.Account != account || m.Program != program {
		return v
	}
	switch m.Agent.State {
	case "pending":
		v.State = applications.AgentEnrollmentStatePending
	case "enrolled":
		v.State = applications.AgentEnrollmentStateEnrolled
	}
	return v
}

func (d *applicationDirectory) readAgentEnrollment(ctx context.Context, s rights.Subject, name string) applications.AgentEnrollmentResult {
	if !providerName.MatchString(name) {
		return agentEnrollmentResult(applications.OutcomeInvalid, "application")
	}
	if s.Account != d.owner {
		return agentEnrollmentResult(applications.OutcomeForbidden, "")
	}
	if movedConnectorCaller(s.Program) {
		return d.readMovedConnectorEnrollment(ctx, s, name)
	}
	operator := d.agentOperator(ctx, s) == wire.ApplicationOutcomeApplied
	result := d.currentAgentEnrollment(ctx, name)
	if operator && d.agentOperator(ctx, s) == wire.ApplicationOutcomeApplied {
		return result
	}
	if result.Enrollment != nil && identity.ValidSubjectProgram(s.Program) && samePrograms(s.Program, result.Enrollment.Program) {
		return result
	}
	if out := d.authorized(ctx, s, ActionApplicationRead, "app:"+name); out != wire.ApplicationOutcomeApplied {
		parsed, _ := applications.ParseOutcome(out.String())
		return agentEnrollmentResult(parsed, "")
	}
	if result.Enrollment != nil {
		view := *result.Enrollment
		view.Program = ""
		result.Enrollment = &view
	}
	return result
}

// readMovedConnectorEnrollment answers the one call a moved connector caller
// may make (CONNECTOR-HANDOVER-AUTH, amended ruling): its own application's
// enrollment, read as the enrolled program reads it, when the file it runs
// from has the key of the image the person kept before that application's
// latest committed update. The subject stays the moved path (RIGHTS-A3); the
// admission compares a file key with the runtime's own record, consults no
// rule and asks nothing. Every other decision for it stays denied.
func (d *applicationDirectory) readMovedConnectorEnrollment(ctx context.Context, s rights.Subject, name string) applications.AgentEnrollmentResult {
	refused := agentEnrollmentResult(applications.OutcomeForbidden, "")
	// The key is read from the file at the caller's path, opened as
	// readApplicationAgentImage opens an image: a hard link to the kept file
	// is refused.
	file, err := fileid.Open(ctx, fileid.Self, s.Program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return refused
	}
	key := ""
	if !file.FileID().IsDir() && file.FileID().Links() == 1 {
		key = file.FileID().Key()
	}
	file.Close()
	if key == "" || !d.lock(ctx) {
		return refused
	}
	kept := false
	err = d.withRegistrationReadLocked(ctx, func(map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		m, ok := marks[name]
		kept = ok && m.Kind == "agent" && m.Agent != nil && !m.Removing && m.Agent.State == "enrolled" && m.Agent.Account == d.owner && m.Agent.MovedKey != "" && m.Agent.MovedKey == key
		return nil
	})
	d.unlock()
	if err != nil || !kept {
		return refused
	}
	return d.currentAgentEnrollment(ctx, name)
}

// currentAgentEnrollment is receiving composition's private class read. It
// reuses the same registered image and actual person-answer validation while
// readAgentEnrollment owns the public operation's native caller admission.
// Call without holding directory, registration or message-store locks.
func (d *applicationDirectory) currentAgentEnrollment(ctx context.Context, name string) applications.AgentEnrollmentResult {
	return d.currentAgentEnrollmentImage(ctx, name, nil)
}

// capture exposes the image owner's already-validated canonical Program/Key to
// private receiving composition. It performs no consumer normalization and
// grants no authority beyond the unchanged enrollment/image/person checks.
func (d *applicationDirectory) currentAgentEnrollmentImage(ctx context.Context, name string, capture *applicationAgentImage) applications.AgentEnrollmentResult {
	if capture != nil {
		*capture = applicationAgentImage{}
	}
	if !providerName.MatchString(name) {
		return agentEnrollmentResult(applications.OutcomeInvalid, "application")
	}
	if !d.lock(ctx) {
		return agentEnrollmentResult(applications.OutcomeUnavailable, "")
	}
	out := agentEnrollmentResult(applications.OutcomeUnknown, "")
	expectedKey := ""
	registrationBinding := ""
	var captured applicationAgentImage
	var expectedMark selfMark
	err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		v, binding, ok := d.agentEntryLocked(legacy, name)
		if !ok {
			return nil
		}
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		d.unconfirmed, d.selfBase = marks, base
		m := marks[name]
		expectedMark = m
		expectedKey = m.ProgramKey
		registrationBinding = binding
		view := agentEnrollmentView(name, v.Program, binding, d.owner, m)
		out = agentEnrollmentResult(applications.OutcomePage, "")
		out.Enrollment = &view
		return nil
	})
	d.unlock()
	if err != nil {
		return agentEnrollmentResult(applications.OutcomeUnavailable, "state")
	}
	if out.Enrollment != nil && out.Enrollment.State != applications.AgentEnrollmentStateNone {
		// Check the image of the same registration that supplied the enrollment.
		// A second directory read can pair it with a newer registration.
		image, err := d.verifyAgentImage(ctx, out.Enrollment.Program, registrationBinding)
		if err != nil {
			if errors.Is(err, errApplicationAgentSharedGateway) {
				out.Enrollment.State = applications.AgentEnrollmentStateNone
				return out
			}
			return agentEnrollmentResult(applications.OutcomeUnavailable, "native_identity")
		}
		if image.Key != expectedKey {
			out.Enrollment.State = applications.AgentEnrollmentStateNone
		}
		captured = image
	}
	if out.Enrollment != nil && out.Enrollment.State == applications.AgentEnrollmentStateEnrolled {
		if d.agentQuestions == nil {
			return agentEnrollmentResult(applications.OutcomeUnavailable, "person_answer")
		}
		r, found, err := d.agentQuestions.a.book.ApplicationRecord(expectedMark.Question)
		if err != nil {
			return agentEnrollmentResult(applications.OutcomeUnavailable, "person_answer")
		}
		if !found || !agentPersonAnswer(expectedMark, r) {
			out.Enrollment.State = applications.AgentEnrollmentStateNone
		}
	}
	if capture != nil && out.Enrollment != nil && out.Enrollment.State == applications.AgentEnrollmentStateEnrolled {
		*capture = captured
	}
	return out
}

// setAgentEnrollmentAtImage is the inert storage seam. The native caller must
// independently capture the current registered image and its identity first.
func (d *applicationDirectory) setAgentEnrollmentAtImage(ctx context.Context, s rights.Subject, request applications.AgentEnrollmentRequest, image applicationAgentImage) applications.AgentEnrollmentResult {
	if !providerName.MatchString(request.Application) {
		return agentEnrollmentResult(applications.OutcomeInvalid, "application")
	}
	if out := d.agentOperator(ctx, s); out != wire.ApplicationOutcomeApplied {
		parsed, _ := applications.ParseOutcome(out.String())
		return agentEnrollmentResult(parsed, "")
	}
	if request.Enabled && (d.agentQuestions == nil || image.Program == "" || image.Key == "" || image.Binding == "") {
		return agentEnrollmentResult(applications.OutcomeUnavailable, "agent_owner")
	}
	renewRevision, renewQuestion, err := d.expiredAgentQuestion(ctx, request, image)
	if err != nil {
		return agentEnrollmentResult(applications.OutcomeUnavailable, "question")
	}
	if !d.lock(ctx) {
		return agentEnrollmentResult(applications.OutcomeUnavailable, "")
	}
	out := agentEnrollmentResult(applications.OutcomeUnavailable, "state")
	ask := false
	err = d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		v, binding, ok := d.agentEntryLocked(legacy, request.Application)
		if !ok {
			out = agentEnrollmentResult(applications.OutcomeUnknown, "")
			return nil
		}
		if request.Enabled && d.v3 != nil {
			if e, found := d.v3.entries[request.Application]; found && e.State != "enabled" {
				out = agentEnrollmentResult(applications.OutcomeConflict, "registration")
				return nil
			}
		}
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		d.unconfirmed, d.selfBase = marks, base
		m, exists := marks[request.Application]
		if m.Agent != nil && m.Agent.Activation != nil {
			out = agentEnrollmentResult(applications.OutcomeConflict, "activation")
			return nil
		}
		if m.Removing {
			out = agentEnrollmentResult(applications.OutcomeConflict, "registration")
			return nil
		}
		if exists && m.genericPending() {
			out = agentEnrollmentResult(applications.OutcomeConflict, "unconfirmed")
			return nil
		}
		current := ""
		if m.Kind == "agent" {
			current = m.RegistrationRevision
		}
		if image.catalogueHost.Reference != "" {
			if image.checkCatalogueConnector == nil || image.checkCatalogueConnector() != nil {
				out = agentEnrollmentResult(applications.OutcomeConflict, "catalogue_host")
				return nil
			}
		}
		var companion catalogueEnrollmentCompanion
		if image.catalogueHost.Reference != "" {
			companion, err = loadCatalogueCompanion(ctx, d.dir, request.Application, m)
			if err != nil {
				return err
			}
		}
		sameHost := companion.Host.Image == image.catalogueHost.Image
		pendingFactsMatch := m.Agent == nil || m.Agent.State != "pending" || reflect.DeepEqual(m.Agent.ReleasePending, image.Release) && bytes.Equal(m.Agent.CompanionPending, image.Companions) && (image.catalogueHost.Reference == "" || companion.samePending(request.Application, m) && sameHost)
		equalPending := request.Enabled && m.Kind == "agent" && m.Agent.State == "pending" && m.Agent.BaseRevision == request.ExpectedRevision && m.Agent.Binding == binding && m.ProgramKey == image.Key && pendingFactsMatch
		if request.ExpectedRevision != current && !equalPending {
			out = agentEnrollmentResult(applications.OutcomeConflict, "revision")
			return nil
		}
		if request.Enabled && (image.Program != v.Program || image.Binding != binding) {
			out = agentEnrollmentResult(applications.OutcomeConflict, "registration")
			return nil
		}
		state := "none"
		if request.Enabled {
			state = "pending"
		}
		renew := renewRevision != "" && m.RegistrationRevision == renewRevision && m.Question == renewQuestion
		if !renew && m.Kind == "agent" && m.Agent.Binding == binding && m.Agent.Account == d.owner && (!request.Enabled && m.Agent.State == "none" || request.Enabled && m.ProgramKey == image.Key && (m.Agent.State == "pending" && pendingFactsMatch || m.Agent.State == "enrolled" && (image.catalogueHost.Reference == "" || companion.matches(request.Application, m) && sameHost))) {
			// An idempotent Keep preserves its original member declaration.
			// Changed membership needs a new native person-mediated update.
			view := agentEnrollmentView(request.Application, v.Program, binding, d.owner, m)
			out = agentEnrollmentResult(applications.OutcomeApplied, "")
			out.Enrollment = &view
			ask = request.Enabled && m.Agent.State == "pending"
			return nil
		}
		for name, other := range marks {
			if name == request.Application {
				continue
			}
			if request.Enabled && other.Kind == "agent" && other.Agent.Account == d.owner && (other.Agent.State == "pending" || other.Agent.State == "enrolled") && applicationRegistrationPrograms(other.Program, v.Program, other.ProgramKey, image.Key) {
				out = agentEnrollmentResult(applications.OutcomeConflict, "enrollment:"+name)
				return nil
			}
			if request.Enabled && other.pendingOwnership() && applicationRegistrationPrograms(other.Program, v.Program, other.ProgramKey, image.Key) {
				out = agentEnrollmentResult(applications.OutcomeConflict, "unconfirmed")
				return nil
			}
		}
		revision, err := applicationToken()
		if err != nil {
			return err
		}
		key := image.Key
		if !request.Enabled {
			key = m.ProgramKey
			if key == "" {
				key = "revoked"
			}
		}
		m = newSelfMark(v.Program, image.Publisher)
		m.Kind = "agent"
		m.ProgramKey = key
		m.RegistrationRevision = revision
		m.Agent = &applicationAgentMark{Account: d.owner, Binding: binding, State: state, BaseRevision: request.ExpectedRevision}
		if request.Enabled && image.Release.valid() && image.Release.ImageKey == image.Key {
			m.Agent.ReleasePending = cloneAgentRelease(image.Release)
		}
		if request.Enabled && len(image.Companions) > 0 {
			m.Agent.CompanionPending = bytes.Clone(image.Companions)
		}
		next := maps.Clone(marks)
		next[request.Application] = m
		if image.catalogueHost.Reference != "" {
			if err := writeCatalogueCompanion(ctx, d.dir, catalogueEnrollmentCompanion{Version: 1,
				App: request.Application, Revision: revision, Image: image.Key, Host: image.catalogueHost}, marks[request.Application]); err != nil {
				return err
			}
		}
		d.selfBase = base
		if err = d.persistUnconfirmedAtDescriptorLocked(ctx, next, legacy); err != nil {
			return err
		}
		view := agentEnrollmentView(request.Application, v.Program, binding, d.owner, m)
		out = agentEnrollmentResult(applications.OutcomeApplied, "")
		out.Enrollment = &view
		ask = request.Enabled
		return nil
	})
	d.unlock()
	if err != nil {
		return agentEnrollmentResult(applications.OutcomeUnavailable, "state")
	}
	if ask {
		if err := d.agentQuestions.ask(request.Application); err != nil {
			d.agentQuestions.a.report(err)
			return agentEnrollmentResult(applications.OutcomeUnavailable, "question")
		}
	}
	if out.Enrollment != nil {
		d.mu.Lock()
		d.notifyLocked()
		d.unlock()
	}
	return out
}

func (d *applicationDirectory) applyAgentAnswer(ctx context.Context, name, revision string, record asks.Record) error {
	if record.Key != asks.AgentEnrollmentKey || record.Pending() || record.Option == "keep" && record.AnsweredBy != asks.AnsweredByOperator {
		return errors.New("agent enrollment: no authenticated person answer")
	}
	image := applicationAgentImage{}
	var host catalogueHostRecord
	var held *heldApplicationManifest
	if record.Option == "keep" {
		var err error
		image, err = d.currentAgentImage(ctx, name)
		if err != nil {
			return err
		}
		image.Companions, _ = d.readAgentCompanions(ctx, image.Program, image.Key)
		host, held, err = d.holdAgentAnswerCatalogueHost(ctx, name, revision, record.ID)
		if err != nil {
			return err
		}
		if held != nil {
			defer held.Close()
		}
	}
	if !d.lock(ctx) {
		return ctx.Err()
	}
	defer d.unlock()
	return d.withRegistrationStorageLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		m := marks[name]
		v, binding, ok := d.agentEntryLocked(legacy, name)
		if !ok || m.Kind != "agent" || m.Agent.State != "pending" || m.RegistrationRevision != revision || m.Question != record.ID || m.Agent.Binding != binding || m.Program != v.Program || record.Option == "keep" && (m.ProgramKey != image.Key || image.Binding != binding) {
			return errors.New("agent enrollment: stale answer")
		}
		a := *m.Agent
		m.Agent = &a
		if record.Yes && record.Option == "keep" {
			companion, err := loadCatalogueCompanion(ctx, d.dir, name, m)
			if err != nil && name == catalogueAgentApplication {
				return err
			}
			if companion.samePending(name, m) != (held != nil) || held != nil && companion.Host != host {
				return errors.New("agent enrollment: catalogue host changed before Keep")
			}
			if held != nil {
				companion.Question = record.ID
				if err := writeCatalogueCompanion(ctx, d.dir, companion, marks[name]); err != nil {
					return err
				}
			}
			if m.Agent.ReleasePending != nil && (!image.Release.valid() || !reflect.DeepEqual(m.Agent.ReleasePending, image.Release)) {
				return errors.New("agent enrollment: signed image changed before Keep")
			}
			if len(m.Agent.CompanionPending) > 0 && !bytes.Equal(m.Agent.CompanionPending, image.Companions) {
				return errors.New("agent enrollment: companion declaration changed before Keep")
			}
			m.Agent.State = "enrolled"
			m.Agent.AnsweredBy = record.AnsweredBy
			m.Agent.AnsweredMs = record.Answered.UnixMilli()
			m.Agent.KeptRelease = &agentKeptRelease{Question: record.ID, Revision: revision, FileKey: image.Key, AnsweredMs: record.Answered.UnixMilli()}
			m.Agent.Companions = bytes.Clone(m.Agent.CompanionPending)
			m.Agent.CompanionPending = nil
			if m.Agent.ReleasePending != nil {
				m.Agent.KeptRelease = &agentKeptRelease{Question: record.ID, Revision: revision, FileKey: image.Key, AnsweredMs: record.Answered.UnixMilli(), Current: *cloneAgentRelease(image.Release)}
				m.Agent.ReleasePending = nil
			}
		} else {
			m.Agent.State = "none"
			m.Agent.ReleasePending = nil
			m.Agent.CompanionPending = nil
			m.Question = ""
		}
		next := maps.Clone(marks)
		next[name] = m
		if held != nil {
			if err := held.Recheck(ctx); err != nil {
				return err
			}
		}
		d.selfBase = base
		if err := d.persistUnconfirmedAtDescriptorLocked(ctx, next, legacy); err != nil {
			return err
		}
		d.notifyLocked()
		return nil
	})
}

// Native producer and classifier compose only the existing identity owner.
type applicationAgentImage struct {
	catalogueHost           catalogueHostRecord
	checkCatalogueConnector func() error
	Program, Key, Binding   string
	Publisher               asks.Publisher
	Release                 *agentNativeRelease
	Proof                   identity.Proof
	Companions              json.RawMessage
}

func (d *applicationDirectory) agentSnapshot(ctx context.Context, name string) (wire.ApplicationDescriptor, string, error) {
	if !d.lock(ctx) {
		return wire.ApplicationDescriptor{}, "", ctx.Err()
	}
	defer d.unlock()
	var v wire.ApplicationDescriptor
	binding := ""
	found := false
	err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		v, binding, found = d.agentEntryLocked(legacy, name)
		return nil
	})
	if err == nil && !found {
		err = errors.New("agent enrollment: current registration unavailable")
	}
	return v, binding, err
}

func (d *applicationDirectory) currentAgentImage(ctx context.Context, name string) (applicationAgentImage, error) {
	v, binding, err := d.agentSnapshot(ctx, name)
	if err != nil {
		return applicationAgentImage{}, err
	}
	return d.verifyAgentImage(ctx, v.Program, binding)
}

func (d *applicationDirectory) setAgentEnrollment(ctx context.Context, s rights.Subject, request applications.AgentEnrollmentRequest) applications.AgentEnrollmentResult {
	if out := d.agentOperator(ctx, s); out != wire.ApplicationOutcomeApplied {
		parsed, _ := applications.ParseOutcome(out.String())
		return agentEnrollmentResult(parsed, "")
	}
	bindHost := request.BindCatalogueHost != nil && *request.BindCatalogueHost
	if bindHost && (!request.Enabled || request.Application != catalogueAgentApplication ||
		!d.panelForConnect(ctx, rightspolicy.BoundPeer(ctx), s.Program)) {
		return agentEnrollmentResult(applications.OutcomeForbidden, "catalogue_host")
	}
	image := applicationAgentImage{}
	if request.Enabled {
		var err error
		image, err = d.currentAgentImage(ctx, request.Application)
		if err != nil {
			if errors.Is(err, errApplicationAgentSharedGateway) {
				return agentEnrollmentResult(applications.OutcomeForbidden, "shared_gateway")
			}
			return agentEnrollmentResult(applications.OutcomeUnavailable, "native_identity")
		}
		if bindHost {
			host, recheck, close, err := d.prepareCatalogueHost(ctx, image)
			if err != nil {
				return agentEnrollmentResult(applications.OutcomeUnavailable, "catalogue_host")
			}
			defer close()
			image.catalogueHost, image.checkCatalogueConnector = host, recheck
		}
		image.Companions, err = d.readAgentCompanions(ctx, image.Program, image.Key)
		if err != nil && d.agentMaintenance && image.Release.valid() {
			return agentEnrollmentResult(applications.OutcomeUnavailable, "agent_owner")
		}
	}
	return d.setAgentEnrollmentAtImage(ctx, s, request, image)
}

func (d *applicationDirectory) applicationV3AgentSubject(ctx context.Context, peer *identity.Peer) (bool, error) {
	return d.applicationAgentSubject(ctx, peer, "")
}

// applicationAgentSubject optionally restricts the existing enrollment check to
// one registered application. Inbox ownership cannot borrow another name's class.
func (d *applicationDirectory) applicationAgentSubject(ctx context.Context, peer *identity.Peer, application string) (bool, error) {
	return d.withApplicationAgentSubject(ctx, peer, application, nil)
}

// consume runs under the directory and registration-storage locks after the
// final current-enrollment check. Native inbox acquisition joins that owner.
func (d *applicationDirectory) withApplicationAgentSubject(ctx context.Context, peer *identity.Peer, application string, consume func() error) (bool, error) {
	return d.withApplicationAgentSubjectAndRelated(ctx, peer, application, nil, consume)
}

// related contains currentAgentEnrollment snapshots captured outside admission
// locks. The same final registration snapshot fences their revocation/rebinding
// without reentering image, Asks or enrollment readers under those locks.
func (d *applicationDirectory) withApplicationAgentSubjectAndRelated(ctx context.Context, peer *identity.Peer, application string, related []applications.AgentEnrollment, consume func() error) (bool, error) {
	return d.withApplicationAgentCapture(ctx, peer, application, related, nil, consume)
}

type applicationAgentAdmission struct {
	Enrollment applications.AgentEnrollment
	Image      applicationAgentImage
}

func (d *applicationDirectory) captureApplicationAgent(ctx context.Context, peer *identity.Peer, application string) (applicationAgentAdmission, bool, error) {
	var result applicationAgentAdmission
	ok, err := d.withApplicationAgentCapture(ctx, peer, application, nil, &result, nil)
	return result, ok, err
}

func (d *applicationDirectory) withApplicationAgentCapture(ctx context.Context, peer *identity.Peer, application string, related []applications.AgentEnrollment, capture *applicationAgentAdmission, consume func() error) (bool, error) {
	return d.withApplicationAgentAdmission(ctx, peer, application, related, capture, consume, false)
}

func (d *applicationDirectory) withApplicationAgentAdmission(ctx context.Context, peer *identity.Peer, application string, related []applications.AgentEnrollment, capture *applicationAgentAdmission, consume func() error, sharedPublication bool) (bool, error) {
	return d.withApplicationAgentAdmissionLock(ctx, peer, application, related, capture, consume, sharedPublication, d.lockAgentAdmission)
}

func (d *applicationDirectory) lockAgentAdmission(ctx context.Context) error {
	if !d.lock(ctx) {
		return ctx.Err()
	}
	return nil
}

func (d *applicationDirectory) withApplicationAgentAdmissionLock(ctx context.Context, peer *identity.Peer, application string, related []applications.AgentEnrollment, capture *applicationAgentAdmission, consume func() error, sharedPublication bool, lock func(context.Context) error) (bool, error) {
	if len(related) > 64 {
		return false, errors.New("agent enrollment: related snapshot bound")
	}
	if peer == nil {
		return false, errors.New("agent enrollment: native peer unavailable")
	}
	if err := peer.Check(listen.Program); err != nil {
		return false, err
	}
	subject, err := credservice.SubjectFromPeer(peer)
	if err != nil {
		return false, err
	}
	if subject.Account != d.owner {
		return false, nil
	}
	if d.agentQuestions == nil {
		return false, errors.New("agent enrollment: person-answer owner unavailable")
	}
	// RIGHTS-A3/A5 names the proven native Program subject. This fresh path
	// key is used by that existing subject comparator; it is not a retained
	// FileID of the caller's mapped image. Windows identity documents that
	// ceiling in the identity owner's Authenticode contract. An older image at the
	// same proven path may match when the current registered/kept image agrees.
	peerKey := identity.ProgramKey(subject.Program)
	if err := lock(ctx); err != nil {
		return false, err
	}
	type candidate struct {
		name string
		m    selfMark
	}
	candidates := []candidate{}
	err = d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, _, err := d.readSelfMarksAtDescriptorLocked(ctx)
		if err != nil {
			return err
		}
		for name, m := range marks {
			if application != "" && name != application {
				continue
			}
			if m.Kind != "agent" || m.Agent.State != "enrolled" || m.Agent.Activation != nil || m.Agent.Account != subject.Account {
				continue
			}
			v, binding, ok := d.agentEntryLocked(legacy, name)
			if ok && m.Agent.Binding == binding && m.Program == v.Program && applicationRegistrationPrograms(m.Program, subject.Program, m.ProgramKey, peerKey) {
				candidates = append(candidates, candidate{name, m})
			}
		}
		return nil
	})
	d.unlock()
	if err != nil {
		return false, err
	}
	for _, c := range candidates {
		// The candidate's mark matched its registered program and binding under
		// the read above; the final read below fences both before admission.
		image, err := d.admittedAgentImage(ctx, c.m.Program, c.m.Agent.Binding)
		if err != nil {
			if errors.Is(err, errApplicationAgentSharedGateway) {
				return false, nil
			}
			return false, err
		}
		if image.Key != c.m.ProgramKey {
			return false, nil
		}
		record, found, err := d.agentQuestions.a.book.ApplicationRecord(c.m.Question)
		if err != nil {
			return false, err
		}
		if !found || !agentPersonAnswer(c.m, record) {
			return false, nil
		}
		if err := lock(ctx); err != nil {
			return false, err
		}
		current := false
		var publication messagePublicationFacts
		err = d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
			marks, markBytes, err := d.readSelfMarksAtDescriptorLocked(ctx)
			if err != nil {
				return err
			}
			m := marks[c.name]
			entry, binding, ok := d.agentEntryLocked(legacy, c.name)
			current = ok && m.Kind == "agent" && m.Agent.State == "enrolled" && m.Agent.Activation == nil && m.RegistrationRevision == c.m.RegistrationRevision && m.Agent.Binding == binding && binding == image.Binding && m.ProgramKey == image.Key && m.Question == record.ID && m.Program == c.m.Program && entry.Program == c.m.Program
			if current {
				for _, expected := range related {
					entry, entryBinding, exists := d.agentEntryLocked(legacy, expected.Application)
					if !exists {
						current = false
						break
					}
					actual := agentEnrollmentView(expected.Application, entry.Program, entryBinding, d.owner, marks[expected.Application])
					if expected.State != applications.AgentEnrollmentStateEnrolled || actual.State != expected.State || actual.Revision != expected.Revision || actual.Program != expected.Program {
						current = false
						break
					}
				}
			}
			if current && sharedPublication {
				publication.admission.Enrollment = agentEnrollmentView(c.name, entry.Program, binding, d.owner, m)
				if err := d.captureMessagePublication(ctx, c.name, related, markBytes.Data, &publication); err != nil {
					return err
				}
				d.prepareMessageRegistrationSnapshot(legacy, marks, c.name, related, publication)
				return nil
			}
			if current && consume != nil {
				if capture != nil {
					*capture = applicationAgentAdmission{Enrollment: agentEnrollmentView(c.name, entry.Program, binding, d.owner, m), Image: image}
				}
				return consume()
			}
			if current && capture != nil {
				*capture = applicationAgentAdmission{Enrollment: agentEnrollmentView(c.name, entry.Program, binding, d.owner, m), Image: image}
			}
			return nil
		})
		if err == nil && current && sharedPublication {
			d.unlock()
			current, err = d.publishMessageWithReadCustody(ctx, &publication, func() error {
				if capture != nil {
					*capture = publication.admission
					capture.Image = image
				}
				return consume()
			})
		} else {
			d.unlock()
		}
		if err != nil {
			return false, err
		}
		if current {
			return true, nil
		}
	}
	return false, nil
}

func (d *applicationDirectory) publishMessageWithReadCustody(ctx context.Context, facts *messagePublicationFacts, consume func() error) (bool, error) {
	if !d.lockRead(ctx) {
		return false, ctx.Err()
	}
	defer d.mu.RUnlock()
	return facts.publish(ctx, consume)
}

type messagePublicationFacts struct {
	descriptor string
	admission  applicationAgentAdmission
	raw        []messagePartyInput
	manifests  []struct{ path, hash string }
}

// Capture inside the existing exclusive read while its validated v3
// projection and the exact registration bytes still describe the same state.
func (d *applicationDirectory) captureMessagePublication(ctx context.Context, sender string, related []applications.AgentEnrollment, marks []byte, facts *messagePublicationFacts) error {
	facts.descriptor = filepath.Join(d.dir, "descriptors.json")
	descriptors, err := cas.ReadLimitContext(ctx, facts.descriptor, d.store.MaxBytes)
	if err != nil {
		return err
	}
	index := filepath.Join(filepath.Dir(d.dir), "applications-v3", "registrations", "index.json")
	facts.raw = []messagePartyInput{
		{path: facts.descriptor, limit: d.store.MaxBytes, raw: descriptors},
		{path: filepath.Join(d.dir, selfRegisteredFile), limit: 600 << 10, raw: marks},
		{path: index, limit: applicationV3IndexLimit},
	}
	if d.v3 == nil {
		return nil // The exclusive read already proved that this index is absent.
	}
	facts.raw[2].path = filepath.Join(d.v3.dir, "index.json")
	facts.raw[2].raw = d.v3.indexRaw
	names := []string{sender}
	for _, enrollment := range related {
		names = append(names, enrollment.Application)
	}
	for _, name := range names {
		if entry, ok := d.v3.entries[name]; ok {
			facts.manifests = append(facts.manifests, struct{ path, hash string }{d.v3.filePath(entry.FileRevision), entry.FileHash})
		}
	}
	return nil
}

func (f *messagePublicationFacts) publish(ctx context.Context, consume func() error) (bool, error) {
	release, err := cas.LockContext(ctx, f.descriptor, true)
	if err != nil {
		return false, err
	}
	defer release()
	current, err := f.current(ctx)
	if err != nil || !current {
		return false, err
	}
	return true, consume()
}

func (f *messagePublicationFacts) current(ctx context.Context) (bool, error) {
	for _, input := range f.raw {
		raw, err := cas.ReadLimitContext(ctx, input.path, input.limit)
		if err != nil {
			return false, err
		}
		if (raw == nil) != (input.raw == nil) || !bytes.Equal(raw, input.raw) {
			return false, nil
		}
	}
	for _, manifest := range f.manifests {
		raw, err := cas.ReadLimitContext(ctx, manifest.path, applicationV3ManifestFileLimit)
		if err != nil {
			return false, err
		}
		if raw == nil || applicationV3Hash(raw) != manifest.hash {
			return false, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return false, err
	}
	return true, nil
}
