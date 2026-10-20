package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	identity "github.com/openabstractions/abstraction-identity"
	rightspolicy "github.com/openabstractions/abstraction-rights/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The setup request (applications@3 RequestSetup and ReceiveSetupRequest; HP5
// slice 2, architect review 23 option (a)). An app asks for OpenAbstractions'
// setup step; the runtime keeps one pending request; the presenter reads it,
// starts the step and clears it. The step asks the person its one question.
// Nothing here downloads, selects or grants.

const (
	// setupRequestLifetime bounds a pending request no presenter read, so
	// none surfaces later at a presenter's start.
	setupRequestLifetime = time.Minute
	// setupPresenterGrace is how long after its last read a presenter still
	// counts as watching: a read lasts up to setupReadWait.
	setupPresenterGrace = 25 * time.Second
	// setupNoPresenter is the answer when no presenter watches.
	setupNoPresenter = "OpenAbstractions' popup host is not running"
	// setupReadWait is how long ReceiveSetupRequest waits for a request.
	setupReadWait = 20 * time.Second
	// setupNoPerson is the remote or foreign caller's refusal reason.
	setupNoPerson = "no person reachable on this runtime"
)

// setupUses are the uses the setup step sets up, each with the ModelNeed
// modality an app declares for it: code for the chat part, image for the
// picture part and video for the reviewed video model set (architect ruling
// 2026-11-11, Setup use video).
var setupUses = map[string]string{"code": "chat", "image": "image", "video": "video"}

// setupCheck answers a use from the runtime's own reads before any request
// is kept: already_set_up or cannot_run with a sentence, or ok false when
// the person must be asked. account is the caller's account.
type setupCheck func(ctx context.Context, account, use string) (applications.SetupOutcome, string, bool)

// setupRequests is the runtime's one pending setup request.
type setupRequests struct {
	mu         sync.Mutex
	pending    *applications.SetupRequest
	at         time.Time
	changed    chan struct{}
	check      atomic.Pointer[setupCheck]
	factsCheck atomic.Pointer[setupFactsCheck]
	now        func() time.Time
	// reading counts presenter reads waiting now; lastRead is when the
	// latest one ended. Together they say whether a presenter watches.
	reading      int
	lastRead     time.Time
	lastReceived string
	lastRequest  *applications.SetupRequest
	receiptPath  string
	// keepMu guards the followers of answered Setup occurrences
	// (setup_keep.go): the ids followed, and the runtime's context and
	// report they run with.
	keepMu        sync.Mutex
	keepFollowing map[string]bool
	keepContext   context.Context
	keepReport    func(error)
}

// watchedLocked reports a presenter reading now or within the grace.
func (q *setupRequests) watchedLocked() bool {
	return q.reading > 0 || !q.lastRead.IsZero() && q.clock().Sub(q.lastRead) <= setupPresenterGrace
}

func (q *setupRequests) bind(check setupCheck) { q.check.Store(&check) }

func (q *setupRequests) clock() time.Time {
	if q.now != nil {
		return q.now()
	}
	return time.Now()
}

// currentLocked drops a pending request no presenter read in time.
func (q *setupRequests) currentLocked() *applications.SetupRequest {
	if q.pending != nil && q.clock().Sub(q.at) > setupRequestLifetime {
		q.pending = nil
	}
	return q.pending
}

func (q *setupRequests) signalLocked() {
	if q.changed != nil {
		close(q.changed)
	}
	q.changed = make(chan struct{})
}

func setupResult(outcome applications.SetupOutcome, reason string) applications.SetupResult {
	return applications.SetupResult{Outcome: outcome, Reason: reason}
}

// request answers RequestSetup for caller s.
func (q *setupRequests) request(ctx context.Context, d *applicationDirectory, s rights.Subject, use string) applications.SetupResult {
	if s.Account == "" || s.Program == "" {
		return setupResult(applications.SetupOutcomeForbidden, setupNoPerson)
	}
	modality, known := setupUses[use]
	if !known {
		return setupResult(applications.SetupOutcomeInvalid, "use")
	}
	title, declared := d.programTitle(ctx, s, modality)
	if !declared {
		return setupResult(applications.SetupOutcomeForbidden, "manifest:use:"+use)
	}
	switch d.authorized(ctx, s, rightspolicy.ActionQuestionAsk, rightspolicy.ResourceAccount) {
	case wire.ApplicationOutcomeApplied:
	case wire.ApplicationOutcomeForbidden:
		return setupResult(applications.SetupOutcomeForbidden, "rights:"+rightspolicy.ActionQuestionAsk)
	default:
		return setupResult(applications.SetupOutcomeUnavailable, "rights")
	}
	if check := q.check.Load(); check != nil {
		if outcome, reason, ok := (*check)(ctx, s.Account, use); ok {
			return setupResult(outcome, reason)
		}
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if suppressed, err := q.suppressRequestLocked(ctx, d); err != nil {
		return setupResult(applications.SetupOutcomeUnavailable, "setup state")
	} else if suppressed {
		return setupResult(applications.SetupOutcomeAsked, "")
	}
	// Only a watching presenter can ask the person; nothing is kept for a
	// presenter that starts later.
	if !q.watchedLocked() {
		return setupResult(applications.SetupOutcomeCannotRun, setupNoPresenter)
	}
	if q.currentLocked() != nil {
		return setupResult(applications.SetupOutcomeAsked, "pending")
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return setupResult(applications.SetupOutcomeUnavailable, "id")
	}
	q.pending = &applications.SetupRequest{ID: hex.EncodeToString(id[:]), Use: use, Application: title}
	q.at = q.clock()
	q.signalLocked()
	return setupResult(applications.SetupOutcomeAsked, "")
}

// read answers ReceiveSetupRequest for the presenter s. A read whose after
// names the pending request clears it: the presenter started the step.
func (q *setupRequests) read(ctx context.Context, d *applicationDirectory, s rights.Subject, after string) applications.SetupRequestRead {
	if out := d.agentOperator(ctx, s); out != wire.ApplicationOutcomeApplied {
		parsed, _ := applications.ParseOutcome(out.String())
		return applications.SetupRequestRead{Outcome: parsed}
	}
	q.mu.Lock()
	q.reading++
	q.mu.Unlock()
	defer func() {
		q.mu.Lock()
		q.reading--
		q.lastRead = q.clock()
		q.mu.Unlock()
	}()
	timer := time.NewTimer(setupReadWait)
	defer timer.Stop()
	for {
		q.mu.Lock()
		pending := q.currentLocked()
		if pending != nil && after != "" && pending.ID == after {
			q.lastReceived = after
			copy := *pending
			q.lastRequest = &copy
			q.pending = nil
			pending = nil
		}
		if pending != nil {
			request := *pending
			q.mu.Unlock()
			return applications.SetupRequestRead{Outcome: applications.OutcomePage, Request: &request}
		}
		if q.changed == nil {
			q.changed = make(chan struct{})
		}
		changed := q.changed
		q.mu.Unlock()
		select {
		case <-ctx.Done():
			return applications.SetupRequestRead{Outcome: applications.OutcomeUnavailable, Reason: "cancelled"}
		case <-timer.C:
			return applications.SetupRequestRead{Outcome: applications.OutcomePage}
		case <-changed:
		}
	}
}

type setupAgentCapture struct {
	name, program, binding, key, mark string
}

// programTitle verifies native images outside the directory lock and confirms
// the exact registration/Keep snapshot again before admitting the declaration.
// The declaration supplies no question.ask permission.
func (d *applicationDirectory) programTitle(ctx context.Context, subject rights.Subject, modality string) (string, bool) {
	if !d.lock(ctx) {
		return "", false
	}
	title, declared, captured := d.setupTitleLocked(ctx, subject, modality)
	d.unlock()
	if captured == nil {
		return title, declared
	}
	image, err := d.verifyAgentImage(ctx, captured.program, captured.binding)
	if err != nil || image.Key != captured.key || image.Binding != captured.binding ||
		!identity.SameSubjectProgram(image.Program, captured.program) ||
		image.Release != nil && image.Release.Authenticated {
		return "", false
	}
	if d.lock(ctx) {
		defer d.unlock()
		title, _, current := d.setupTitleLocked(ctx, subject, modality)
		if current != nil && *current == *captured {
			return title, true
		}
	}
	return "", false
}

// setupTitleLocked captures only immutable values while the directory and
// registration owner are held. Native verification belongs to programTitle.
func (d *applicationDirectory) setupTitleLocked(ctx context.Context, subject rights.Subject, modality string) (string, bool, *setupAgentCapture) {
	if subject.Account != d.owner {
		return "", false, nil
	}
	marks := d.unconfirmed
	if d.dir != "" {
		if err := d.withRegistrationReadLocked(ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
			var err error
			marks, _, err = d.readSelfMarksAtDescriptorLocked(ctx)
			if err != nil {
				return err
			}
			return d.projectRegistrationsLocked(legacy)
		}); err != nil {
			return "", false, nil
		}
	}
	for name, descriptor := range d.descriptors {
		if !identity.SameSubjectProgram(descriptor.Program, subject.Program) {
			continue
		}
		mark, marked := marks[name]
		if marked && (mark.pendingOwnership() || mark.Removing) {
			continue
		}
		declared := d.v3 != nil && d.v3.setupModalityLocked(name, subject.Program, modality)
		var captured *setupAgentCapture
		if !declared && marked {
			declared = keptAgentSetupModality(mark, d.owner, descriptor.Program, modality)
			if !declared && mark.Kind == "agent" && mark.validAgent() && mark.Agent.State == "enrolled" &&
				mark.Agent.Account == d.owner && mark.Agent.Activation == nil &&
				identity.SameSubjectProgram(mark.Program, descriptor.Program) &&
				(mark.Agent.KeptRelease == nil || !mark.Agent.KeptRelease.Current.Authenticated) {
				// An unsigned helper has no authenticated release declaration.
				// The person's Keep admits only that exact current native image.
				_, binding, registered := d.agentEntryLocked(d.descriptors, name)
				if registered && binding == mark.Agent.Binding {
					if raw, err := json.Marshal(mark); err == nil {
						captured = &setupAgentCapture{name, descriptor.Program, binding, mark.ProgramKey, string(raw)}
					}
				}
			}
		}
		if !declared && captured == nil {
			continue
		}
		if title := strings.TrimSpace(descriptor.Title); title != "" {
			return truncateTitle(title), declared, captured
		}
		return truncateTitle(name), declared, captured
	}
	return "", false, nil
}

// The common helper's signed release has these exact minimal needs. Its kept
// snapshot retains canonical hashes; matching them reuses that declaration
// owner without adding a parallel modality field or reading its image here.
func keptAgentSetupModality(mark selfMark, account, program, modality string) bool {
	a := mark.Agent
	if a == nil || a.Account != account || a.State != "enrolled" || a.Activation != nil || mark.Removing ||
		!identity.SameSubjectProgram(mark.Program, program) || !a.KeptRelease.valid() {
		return false
	}
	k := a.KeptRelease
	// Revision and FileKey retain the original Keep's provenance across updates.
	if !k.Current.valid() || k.Current.ImageKey != mark.ProgramKey {
		return false
	}
	need := registration.ModelNeed{Modality: modality, RequiredFeatures: []string{}, Placement: registration.PlacementAny}
	hash, err := agentDeclarationHash(registration.EncodeModelNeed(&need))
	if err != nil {
		return false
	}
	for _, kept := range k.Current.Declaration.Models {
		if kept == hash {
			return true
		}
	}
	return false
}

func truncateTitle(title string) string {
	if len(title) <= 128 {
		return title
	}
	return strings.ToValidUTF8(title[:128], "")
}
