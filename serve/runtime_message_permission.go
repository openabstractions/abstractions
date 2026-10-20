package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

func capturedMessageAgent(entry applications.AgentEnrollment, image applicationAgentImage) applications.CapturedAgentApplication {
	return applications.CapturedAgentApplication{Name: entry.Application, Program: image.Program, FileKey: image.Key, EnrollmentRevision: entry.Revision}
}

// Diagnostics contain only closed labels. Native metadata and captured values
// remain private; the context cause is available without rendering its text.
type messageConsentError struct {
	text    string
	cause   error
	changed bool
}

func (e *messageConsentError) Error() string { return e.text }

// ErrorClass is the closed diagnostic class, without role or field detail.
func (e *messageConsentError) ErrorClass() string {
	if e.changed {
		return "messages: native consent party changed"
	}
	return "messages: native consent party unavailable"
}
func (e *messageConsentError) Unwrap() error { return e.cause }

// messageConsentChanged reports a recorded consent whose captured party is no
// longer the current one, as after an OA connector update. That consent is
// no decision about the current parties; the person is asked afresh.
func messageConsentChanged(err error) bool {
	var consent *messageConsentError
	return errors.As(err, &consent) && consent.changed
}
func messageConsentFailure(ctx context.Context, role, kind string, current applications.AgentEnrollmentResult, fields []string) error {
	outcome := "other"
	switch current.Outcome {
	case applications.OutcomePage:
		outcome = "page"
	case applications.OutcomeUnavailable:
		outcome = "unavailable"
	case applications.OutcomeUnknown:
		outcome = "unknown"
	case applications.OutcomeInvalid:
		outcome = "invalid"
	case applications.OutcomeForbidden:
		outcome = "forbidden"
	}
	field := "other"
	switch current.Reason {
	case "":
		field = "none"
	case "state", "native_identity", "person_answer":
		field = current.Reason
	}
	contextClass := "active"
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		contextClass = "deadline"
	} else if ctx.Err() != nil {
		contextClass = "cancelled"
	}
	return &messageConsentError{fmt.Sprintf("messages: native consent party %s role=%s outcome=%s field=%s context=%s mismatch=%s", kind, role, outcome, field, contextClass, strings.Join(fields, ",")), ctx.Err(), kind == "changed"}
}

// messagePermissionGuard prepares every current native image (the sender and
// each receiver) outside Book and Rights locks. Its quiet guard rechecks the
// owning immutable registration facts and image keys while the existing policy
// owner commits the exact legacy rules.
func (a *runtimeAsks) messagePermissionGuard(ctx context.Context, effect rwire.PermissionEffect) (func(context.Context) error, error) {
	_, captured, ok := grants.AgentMessageLegacyEffect(effect)
	if !ok || a.rights == nil || a.rights.applications == nil {
		return nil, closedError("messages: native consent unavailable")
	}
	d := a.rights.applications
	facts, err := d.currentMessagingFacts(ctx)
	if err != nil {
		return nil, err
	}
	parties := []applications.CapturedAgentApplication{captured[0].Sender}
	for _, pair := range captured {
		parties = append(parties, pair.Receiver)
	}
	for index, expected := range parties {
		role := "sender"
		if index > 0 {
			role = "receiver"
		}
		var image applicationAgentImage
		current := d.currentAgentEnrollmentImage(ctx, expected.Name, &image)
		if current.Outcome != applications.OutcomePage {
			return nil, messageConsentFailure(ctx, role, "unavailable", current, []string{"none"})
		}
		var mismatch []string
		if current.Enrollment == nil {
			mismatch = append(mismatch, "enrollment")
		} else {
			if current.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
				mismatch = append(mismatch, "state")
			}
			actual := capturedMessageAgent(*current.Enrollment, image)
			if actual.Name != expected.Name {
				mismatch = append(mismatch, "name")
			}
			if actual.Program != expected.Program {
				mismatch = append(mismatch, "program")
			}
			if actual.FileKey != expected.FileKey {
				mismatch = append(mismatch, "file_key")
			}
			if actual.EnrollmentRevision != expected.EnrollmentRevision {
				mismatch = append(mismatch, "enrollment_revision")
			}
		}
		if len(mismatch) > 0 {
			return nil, messageConsentFailure(ctx, role, "changed", current, mismatch)
		}
	}
	// The captured registration facts staying current is the condition; an
	// unrelated republication of the roles index does not change the parties.
	return func(ctx context.Context) error {
		if err := facts.current(ctx); err != nil {
			return err
		}
		for _, expected := range parties {
			if identity.ProgramKey(expected.Program) != expected.FileKey {
				return closedError("messages: native consent image changed")
			}
		}
		return ctx.Err()
	}, nil
}

// messageSameAppEligible is the sender's own current enrolled app as the
// receiver of its other chats (architect ruling 2026-11-09): the same
// registered name and the same image.
func messageSameAppEligible(sender applicationAgentAdmission, target applications.AgentEnrollmentResult, receiver applicationAgentImage) bool {
	return messageSameAppReceivers && target.Outcome == applications.OutcomePage && target.Enrollment != nil && target.Enrollment.State == applications.AgentEnrollmentStateEnrolled &&
		target.Enrollment.Application == sender.Enrollment.Application && receiver.Program != "" && receiver.Key == sender.Image.Key && samePrograms(sender.Image.Program, receiver.Program)
}

// messageReceiverEligible is a current enrolled receiver other than the sender.
func messageReceiverEligible(sender applicationAgentAdmission, target applications.AgentEnrollmentResult, receiver applicationAgentImage) bool {
	return target.Outcome == applications.OutcomePage && target.Enrollment != nil && target.Enrollment.State == applications.AgentEnrollmentStateEnrolled &&
		receiver.Program != "" && receiver.Key != sender.Image.Key && !samePrograms(sender.Image.Program, receiver.Program)
}

// otherMessageReceivers is every current enrolled agent other than the sender
// and the named receiver that the sender is not yet allowed to message. An
// allowed or blocked pair stays out of the question.
func (a *runtimeAsks) otherMessageReceivers(ctx context.Context, subject rwire.Subject, sender applicationAgentAdmission, named string, room int) []applications.CapturedAgentApplication {
	d := a.rights.applications
	facts := d.messagingFactsFor(ctx)
	if facts == nil || room <= 0 {
		return nil
	}
	senderCapture := capturedMessageAgent(sender.Enrollment, sender.Image)
	var out []applications.CapturedAgentApplication
	for _, agent := range facts.agents {
		if len(out) >= room {
			break
		}
		if agent.name == named || agent.name == sender.Enrollment.Application {
			continue
		}
		var receiver applicationAgentImage
		target := d.currentAgentEnrollmentImage(ctx, agent.name, &receiver)
		if !messageReceiverEligible(sender, target, receiver) {
			continue
		}
		capture := capturedMessageAgent(*target.Enrollment, receiver)
		if _, err := grants.AgentMessagePermission(subject, senderCapture, []applications.CapturedAgentApplication{capture}); err != nil {
			continue
		}
		if a.rights.decide(ctx, subject, ActionApplicationMessage, "app:"+agent.name).Outcome != rwire.DecisionOutcomeNotGranted {
			continue
		}
		out = append(out, capture)
	}
	return out
}

// retainedMessageQuestion is this request key's own retained question when it
// names receiver: a same-key retry rereads that question, whose receiver set
// was fixed when it was asked.
func (a *runtimeAsks) retainedMessageQuestion(subject rwire.Subject, key, receiver string) (asks.TypedPermission, bool) {
	record, outcome, err := a.book.ObserveApplication(asksservice.PermissionScope(subject.Account, subject.Program), key)
	if err != nil || outcome == "unknown" || outcome == "gone" || record.TypedPermission == nil || record.TypedPermission.Origin != "application" || record.TypedPermission.Effect.Subject != subject {
		return asks.TypedPermission{}, false
	}
	names, ok := grants.AgentMessageReceivers(record.TypedPermission.Effect)
	if !ok {
		return asks.TypedPermission{}, false
	}
	for _, name := range names {
		if name == receiver {
			return asks.CloneTypedPermission(*record.TypedPermission), true
		}
	}
	return asks.TypedPermission{}, false
}

// prepareMessagePermission asks once per sending agent. The request names one
// receiver; the question names it and every other current enrolled agent the
// sender is not yet allowed to message. One Allow writes one rule per pair.
func (a *runtimeAsks) prepareMessagePermission(ctx context.Context, peer *identity.Peer, intent askswire.PermissionIntent) (asksservice.PreparedPermission, error) {
	refuse := func(out askswire.PermissionOutcome) (asksservice.PreparedPermission, error) {
		return asksservice.PreparedPermission{Outcome: out}, nil
	}
	selection := intent.Selection
	if selection == nil || selection.Permission.Action != ActionApplicationMessage || selection.Permission.ResourceProfile != grants.AgentMessageResourceProfile || selection.Selector.Kind != rwire.ResourceSelectorKindOne || intent.BundleRef != "" || intent.BundleSelector != nil || intent.Purpose != "" {
		return refuse(askswire.PermissionOutcomeInvalid)
	}
	name, err := grants.DecodeNamedApplicationTarget(selection.Selector.Target)
	if err != nil {
		return refuse(askswire.PermissionOutcomeInvalid)
	}
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || a.rights == nil || a.rights.policy == nil || subject.Account != a.rights.owner || a.rights.applications == nil {
		return refuse(askswire.PermissionOutcomeForbidden)
	}
	unavailable := func(step string, err error) (asksservice.PreparedPermission, error) {
		a.reportPermission(fmt.Errorf("message access %s for receiver %s: %v", step, name, err))
		return refuse(askswire.PermissionOutcomeUnavailable)
	}
	ctx, err = a.rights.policy.PrepareMetaHolderContext(ctx)
	if err != nil {
		return unavailable("metadata refresh", err)
	}
	d := a.rights.applications
	facts, err := d.currentMessagingFacts(ctx)
	if err != nil {
		return unavailable("messaging facts", err)
	}
	ctx = withMessagingFacts(ctx, facts)
	sender, allowed, err := d.captureApplicationAgent(ctx, peer, "")
	if err != nil {
		return unavailable("sender capture", err)
	}
	if !allowed {
		return refuse(askswire.PermissionOutcomeForbidden)
	}
	name, err = d.resolveMessageApplication(ctx, name)
	if err != nil {
		return unavailable("receiver lookup", err)
	}
	var receiver applicationAgentImage
	target := d.currentAgentEnrollmentImage(ctx, name, &receiver)
	sameApp := messageSameAppEligible(sender, target, receiver)
	if !sameApp && !messageReceiverEligible(sender, target, receiver) {
		return refuse(askswire.PermissionOutcomeForbidden)
	}
	senderCapture := capturedMessageAgent(sender.Enrollment, sender.Image)
	var p asks.TypedPermission
	var guard func(context.Context) error
	if retained, ok := a.retainedMessageQuestion(subject, intent.RequestKey, name); ok {
		// A retained question whose parties changed is not reused; the fresh
		// question below conflicts with its key as before.
		if g, err := a.messagePermissionGuard(ctx, retained.Effect); err == nil {
			p, guard = retained, g
		}
	}
	if guard == nil {
		receivers := []applications.CapturedAgentApplication{capturedMessageAgent(*target.Enrollment, receiver)}
		// A same-app question asks only that direction.
		if !sameApp && a.rights.decide(ctx, subject, ActionApplicationMessage, "app:"+name).Outcome == rwire.DecisionOutcomeNotGranted {
			receivers = append(receivers, a.otherMessageReceivers(ctx, subject, sender, name, grants.MaxAgentMessageReceivers-1)...)
		}
		p, err = grants.AgentMessagePermission(subject, senderCapture, receivers)
		if err != nil || !a.rights.policy.Definitions.ValidPermissionEffect(p.Effect) {
			return refuse(askswire.PermissionOutcomeUnavailable)
		}
		guard, err = a.messagePermissionGuard(ctx, p.Effect)
		if err != nil {
			return unavailable("party check", err)
		}
	}
	pairs := len(p.Effect.Selections)
	decision := a.permissionDecision(ctx, p.Effect, "")
	if permissionDisposition(decision, pairs) == askswire.PermissionOutcomePending {
		// The person's recorded Allow for this exact effect is the consent. A
		// completion that failed after the answer is retried here, so asking
		// the agent to continue saves the permission instead of asking again.
		switch completed, state := a.completeRetainedMessageAllow(ctx, p); state {
		case retainedAllowFailed:
			// The unsaved Allow could not be released either; it still
			// stands, and a later recheck retries it.
			return refuse(askswire.PermissionOutcomeUnavailable)
		case retainedAllowSaved:
			decision = completed
		}
	}
	if permissionDisposition(decision, pairs) == askswire.PermissionOutcomePending && unsavedReasks.held(unsavedTargetKey(p.Effect)) {
		return refuse(askswire.PermissionOutcomeUnavailable)
	}
	prepared := asksservice.PreparedPermission{Permission: p, Outcome: permissionDisposition(decision, pairs), Revision: decision.Revision}
	prepared.Recheck = func() error {
		if err := guard(ctx); err != nil {
			return err
		}
		if permissionDisposition(a.permissionDecision(ctx, p.Effect, decision.Revision), pairs) != askswire.PermissionOutcomePending {
			return errors.New("messages: consent admission changed")
		}
		return ctx.Err()
	}
	return prepared, nil
}

type retainedAllowState int

const (
	retainedAllowNone retainedAllowState = iota
	retainedAllowSaved
	retainedAllowFailed
)

// completeRetainedMessageAllow saves a person's recorded Allow whose rule
// commit never landed (architect ruling A, 2026-11-08). For each pair this
// request names, the person's latest decision on that exact sender→receiver
// pair decides: only an Allow that is still the latest decision for every
// pair it covers, and has no completion receipt, is written. A later Don't
// allow wins, and Undo or Remove keep the receipt, so neither is re-granted.
// The rule records that Allow's ID as its consent through the guarded path.
// An Allow whose save fails here is released and the person asked afresh
// (owner, 2026-11-09).
func (a *runtimeAsks) completeRetainedMessageAllow(ctx context.Context, p asks.TypedPermission) (rwire.BatchDecision, retainedAllowState) {
	wanted, ok := grants.AgentMessageReceivers(p.Effect)
	if !ok {
		return rwire.BatchDecision{}, retainedAllowNone
	}
	history, _, err := a.book.ApplicationHistory()
	if err != nil {
		return rwire.BatchDecision{}, retainedAllowNone
	}
	type decided struct {
		record asks.Record
		pairs  []string
	}
	var decisions []decided
	latest := map[string]int{} // receiver -> index into decisions
	for _, r := range history {
		if r.Key != asks.TypedPermissionKey || r.TypedPermission == nil || r.Pending() || r.AnsweredBy != asks.AnsweredByOperator || r.TypedPermission.Origin != "application" {
			continue
		}
		subject := r.TypedPermission.Effect.Subject
		if subject.Account != p.Effect.Subject.Account || !samePrograms(subject.Program, p.Effect.Subject.Program) {
			continue
		}
		pairs, ok := grants.AgentMessageReceivers(r.TypedPermission.Effect)
		if !ok {
			continue
		}
		decisions = append(decisions, decided{r, pairs})
		i := len(decisions) - 1
		for _, receiver := range pairs {
			if j, seen := latest[receiver]; !seen || r.Answered.After(decisions[j].record.Answered) {
				latest[receiver] = i
			}
		}
	}
	candidates := map[int]bool{}
	for _, receiver := range wanted {
		if i, seen := latest[receiver]; seen && decisions[i].record.Option == "allow" {
			candidates[i] = true
		}
	}
	state := retainedAllowNone
	for i := range candidates {
		c := decisions[i]
		stillLatest := true
		for _, receiver := range c.pairs {
			if latest[receiver] != i {
				stillLatest = false
			}
		}
		if !stillLatest {
			continue
		}
		if applied, err := a.rights.policy.AppliedFirstUseAllow(c.record.ID); err != nil || applied {
			continue
		}
		// An Allow given to parties that have since changed, as after an OA
		// connector update, is no decision about the current parties: the
		// person is asked afresh instead of being told it cannot be saved.
		if _, err := a.messagePermissionGuard(ctx, c.record.TypedPermission.Effect); messageConsentChanged(err) {
			continue
		}
		revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
		if err != nil {
			state = retainedAllowFailed
			continue
		}
		edit, attempted, err := a.savePermissionEffect(ctx, a.rights.self(), revision, c.record.ID, func() error { return ctx.Err() }, false)
		if messageConsentChanged(err) {
			continue
		}
		if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
			a.reportPermission(fmt.Errorf("recorded message Allow %s not saved on recheck: outcome=%s: %v", c.record.ID, edit.Outcome, err))
			// The released Allow no longer stands: this request asks afresh.
			if !attempted || !allowNotSaved(edit.Outcome, err) || !a.releaseUnsavedAllow(c.record.ID, a.rights.self(), edit.Outcome.String(), err, func() error { return nil }) {
				state = retainedAllowFailed
			}
			continue
		}
		if state == retainedAllowNone {
			state = retainedAllowSaved
		}
	}
	if state == retainedAllowNone {
		return rwire.BatchDecision{}, state
	}
	return a.permissionDecision(ctx, p.Effect, ""), state
}
