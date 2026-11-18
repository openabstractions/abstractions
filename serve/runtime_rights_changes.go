package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	"github.com/openabstractions/abstraction-asks/go/catalogue"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsservice "github.com/openabstractions/abstraction-rights/go/authorization"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// Rights changes through operator programs (rights RIGHTS-O17, asks ASK-O17;
// architect ruling A of 2026-11-22). The Panel window and its presenter edit
// directly. Every other operator program, the command line first of all,
// requests a typed change; the runtime holds it in memory, asks the person
// in a person-only popup it words itself, and applies exactly that change,
// once, on Allow. A caller whose own connection-bound token is elevated is
// exempt while the elevated-callers default is on. Every change applied for
// a request, prompted or exempt, writes an Activity line with Undo.

const (
	// changeRequestLifetime is how long a request waits for the person.
	changeRequestLifetime = 10 * time.Minute
	// maxChangeRequests bounds the requests held in memory; the oldest settled
	// request leaves first, and a full list of pending ones refuses another.
	maxChangeRequests = 64
	// maxPendingPerRequester bounds the pending requests one program holds,
	// so one program cannot fill the list or the person's screen.
	maxPendingPerRequester = 4
	// maxPendingPerAccount bounds the pending requests of the whole account,
	// so programs that differ only by name cannot together flood the person.
	maxPendingPerAccount = 8
	// changeApplyAttempts bounds retries of a change made at the current
	// revision while other edits land.
	changeApplyAttempts = 8
	// maxChangeUndoRules bounds the rules a retired action's Undo restores.
	maxChangeUndoRules = 16
	// rightsChangeEvent is the runtime log event the Panel's Activity reads
	// (monitor activity_rights_changes.go).
	rightsChangeEvent = "rights.change.applied"
)

// rightsChanges is the runtime's rightsservice.OperatorChanges.
type rightsChanges struct {
	r    *runtimeRights
	asks *runtimeAsks
	ctx  context.Context
	// lifetime is changeRequestLifetime; tests shorten it.
	lifetime time.Duration
	now      func() time.Time

	mu       sync.Mutex
	requests []*changeRequest
	workers  sync.WaitGroup
}

// changeRequest is one request as the runtime holds it. Every field but the
// settled state is fixed at admission.
type changeRequest struct {
	id       string
	by       rwire.Subject
	change   rwire.PolicyChange
	question string
	expires  time.Time
	exempt   bool
	// key identifies the change, so a repeated identical request joins the
	// pending one.
	key string

	outcome  rwire.ChangeRequestOutcome
	revision string
	done     chan struct{}
	// apply makes a change the rights policy alone cannot, a declaration,
	// on Allow; nil applies change through the policy.
	apply func(context.Context) (rwire.ChangeRequestOutcome, string)
}

func (c *changeRequest) state() rwire.ChangeRequestState {
	change := c.change
	return rwire.ChangeRequestState{Outcome: c.outcome, ID: c.id, Revision: c.revision, Change: &change,
		Expires: c.expires.UTC().Format(time.RFC3339), Exempt: c.exempt}
}

// configureChanges composes the change gate into the rights and asks hosts.
// It needs both; without either, the hosts keep their earlier behaviour,
// which only an isolated runtime without a state directory composes.
func (r *runtimeRights) configureChanges(ctx context.Context, o *host.Options, a *runtimeAsks) *rightsChanges {
	if r == nil || r.policy == nil {
		return nil
	}
	// Without the asks Book no person can be asked: the gate then admits the
	// Panel window and its presenter alone, with no exemption and no request.
	if a != nil && a.book == nil {
		a = nil
	}
	c := &rightsChanges{r: r, asks: a, ctx: ctx, lifetime: changeRequestLifetime, now: time.Now}
	if r.composedChanges != nil {
		r.composedChanges(c)
	}
	r.changes = c
	o.RightsOperatorChanges = c
	o.QuestionOperatorAnswers = c.authorizeAnswer
	return c
}

// wait joins the requests' settling workers.
func (c *rightsChanges) wait() {
	if c != nil {
		c.workers.Wait()
	}
}

// direct reports a peer whose edits are the person's own: the Panel window.
// operatorDirect is a test seam that is nil in the shipped runtime.
func (c *rightsChanges) direct(ctx context.Context, peer *identity.Peer) bool {
	if peer == nil {
		return false
	}
	if c.r.operatorDirect != nil && c.r.operatorDirect(peer) {
		return true
	}
	return c.r.isPanelPeer(ctx, peer)
}

// answering reports a peer whose answers are the person's own: the Panel
// window, or the Asks presenter that draws the person-only popups for it and
// counts only real input (ASK-O14). The presenter answers and completes the
// answered effect; it makes no other edit.
func (c *rightsChanges) answering(ctx context.Context, peer *identity.Peer) bool {
	return c.direct(ctx, peer) || peer != nil && c.r.isPresenterPeer(ctx, peer)
}

// AdmitCompletion is rightsservice.OperatorChanges.AdmitCompletion: whoever
// may answer completes the answered effect.
func (c *rightsChanges) AdmitCompletion(ctx context.Context, peer *identity.Peer, by rwire.Subject) error {
	if c.answering(ctx, peer) || c.exempt(ctx, peer, by) {
		return nil
	}
	return rightsservice.ErrOperatorForbidden
}

// exempt reports an elevated caller of the runtime's own account while the
// elevated-callers default is on: the same predicate as that default (rights
// RIGHTS-R27), read from the connection-bound token and never claimed by the
// caller. No group, privilege or OA role counts.
func (c *rightsChanges) exempt(ctx context.Context, peer *identity.Peer, by rwire.Subject) bool {
	if c.asks == nil || peer == nil || by.Account != c.r.owner {
		return false
	}
	_, defaults, err := c.r.policy.DefaultsSnapshot(ctx)
	if err != nil {
		return false
	}
	on := false
	for _, d := range defaults {
		if d.Name == rights.DefaultElevatedCallers {
			on = d.On
		}
	}
	if !on {
		return false
	}
	classify := c.r.peerElevated
	if classify == nil {
		classify = (*identity.Peer).Elevated
	}
	return rights.ElevatedCaller(rights.WithBoundPeer(ctx, peer), by, classify)
}

// Admit is rightsservice.OperatorChanges.Admit.
func (c *rightsChanges) Admit(ctx context.Context, peer *identity.Peer, by rwire.Subject) error {
	if c.direct(ctx, peer) {
		return nil
	}
	if c.exempt(ctx, peer, by) {
		return rightsservice.ErrChangeExempt
	}
	return rightsservice.ErrOperatorForbidden
}

// authorizeAnswer is the asks operator answer check (ASK-O17): the Panel
// window, its presenter and an exempt caller answer; every other operator
// program is forbidden. An exempt answer is recorded like any operator answer.
func (c *rightsChanges) authorizeAnswer(ctx context.Context, peer *identity.Peer) error {
	if c.answering(ctx, peer) {
		return nil
	}
	if by, err := rightsSubjectOf(peer); err == nil && c.exempt(ctx, peer, by) {
		return nil
	}
	return asksservice.ErrOperatorForbidden
}

func rightsSubjectOf(peer *identity.Peer) (rwire.Subject, error) {
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil {
		return rwire.Subject{}, err
	}
	return rwire.Subject{Account: subject.Account, Program: subject.Program}, nil
}

// Apply is rightsservice.OperatorChanges.Apply: an exempt caller's direct edit.
func (c *rightsChanges) Apply(ctx context.Context, peer *identity.Peer, by rwire.Subject, change rwire.PolicyChange) rightsservice.ChangeResult {
	if !validChange(change) {
		return rightsservice.ChangeResult{State: rwire.ChangeRequestState{Outcome: rwire.ChangeRequestOutcomeInvalid}}
	}
	if !c.exempt(ctx, peer, by) {
		return rightsservice.ChangeResult{State: rwire.ChangeRequestState{Outcome: rwire.ChangeRequestOutcomeForbidden}}
	}
	return c.applyNow(ctx, by, change, routeExempt)
}

// Request is rightsservice.OperatorChanges.Request.
func (c *rightsChanges) Request(ctx context.Context, peer *identity.Peer, by rwire.Subject, change rwire.PolicyChange, wait time.Duration) rightsservice.ChangeResult {
	if !validChange(change) {
		return rightsservice.ChangeResult{State: rwire.ChangeRequestState{Outcome: rwire.ChangeRequestOutcomeInvalid}}
	}
	if c.direct(ctx, peer) {
		// The person's own window: no question, and its own edits write no
		// Activity line, as its direct edits do not.
		return c.applyNow(ctx, by, change, routeDirect)
	}
	if c.exempt(ctx, peer, by) {
		return c.applyNow(ctx, by, change, routeExempt)
	}
	if outcome := c.prevalidate(ctx, change); outcome != rwire.ChangeRequestOutcomePending {
		return rightsservice.ChangeResult{State: rwire.ChangeRequestState{Outcome: outcome}}
	}
	if c.asks == nil {
		return rightsservice.ChangeResult{State: rwire.ChangeRequestState{Outcome: rwire.ChangeRequestOutcomeUnavailable}}
	}
	req, outcome := c.admitWith(by, change, nil)
	if req == nil {
		return rightsservice.ChangeResult{State: rwire.ChangeRequestState{Outcome: outcome}}
	}
	return rightsservice.ChangeResult{State: c.waitFor(ctx, req, wait)}
}

// Observe is rightsservice.OperatorChanges.Observe.
func (c *rightsChanges) Observe(ctx context.Context, by rwire.Subject, id string, wait time.Duration) rwire.ChangeRequestState {
	c.mu.Lock()
	var req *changeRequest
	for _, held := range c.requests {
		if held.id == id && sameRequester(held.by, by) {
			req = held
		}
	}
	c.mu.Unlock()
	if req == nil {
		return rwire.ChangeRequestState{Outcome: rwire.ChangeRequestOutcomeUnknown}
	}
	return c.waitFor(ctx, req, wait)
}

// List is rightsservice.OperatorChanges.List.
func (c *rightsChanges) List(_ context.Context, by rwire.Subject) rwire.ChangeRequestPage {
	c.mu.Lock()
	defer c.mu.Unlock()
	page := rwire.ChangeRequestPage{Outcome: rwire.PolicyPageOutcomePage, Requests: []rwire.ChangeRequestState{}}
	for i := len(c.requests) - 1; i >= 0; i-- {
		if sameRequester(c.requests[i].by, by) {
			page.Requests = append(page.Requests, c.requests[i].state())
		}
	}
	return page
}

// sameRequester compares requesting programs by identity, as subjects
// compare everywhere else.
func sameRequester(a, b rwire.Subject) bool {
	return a.Account == b.Account && identity.SameSubjectProgram(a.Program, b.Program)
}

func (c *rightsChanges) waitFor(ctx context.Context, req *changeRequest, wait time.Duration) rwire.ChangeRequestState {
	if wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-req.done:
		case <-timer.C:
		case <-ctx.Done():
		}
		timer.Stop()
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return req.state()
}

// validChange checks a change's shape: each kind carries its own fields and
// no other.
func validChange(change rwire.PolicyChange) bool {
	text := func(value string, max int) bool { return utf8.ValidString(value) && len(value) <= max }
	if change.Program != "" || change.Address != "" || len(change.Rules) > 0 {
		// A declaration is admitted by the registry or inference service.
		return false
	}
	if !text(change.ExpectedRevision, 128) || change.TTLMs < 0 || change.TTLMs > rights.MaxRuleTTL.Milliseconds() || !text(change.Why, 256) {
		return false
	}
	switch change.Kind {
	case rwire.ChangeKindSetRule:
		return change.Rule != nil && change.Name == "" && !change.On && change.Action == ""
	case rwire.ChangeKindRevokeRule:
		return change.Rule != nil && change.TTLMs == 0 && change.Why == "" && change.Name == "" && !change.On && change.Action == ""
	case rwire.ChangeKindSetDefault:
		return change.Rule == nil && change.TTLMs == 0 && change.Why == "" && change.Name != "" && text(change.Name, 64) && change.Action == ""
	case rwire.ChangeKindRegisterAction, rwire.ChangeKindRetireAction:
		return change.Rule == nil && change.TTLMs == 0 && change.Why == "" && change.Name == "" && !change.On && change.Action != "" && text(change.Action, 128)
	}
	return false
}

// prevalidate refuses a change the policy would refuse, so the person is
// never asked about one. pending means ask.
func (c *rightsChanges) prevalidate(ctx context.Context, change rwire.PolicyChange) rwire.ChangeRequestOutcome {
	switch change.Kind {
	case rwire.ChangeKindSetRule, rwire.ChangeKindRevokeRule:
		rule := change.Rule
		if rule.Subject.Account != c.r.owner {
			return rwire.ChangeRequestOutcomeInvalid
		}
		switch c.r.policy.ReadRule(rule.Subject, rule.Action, rule.Resource).Outcome {
		case rwire.RuleReadOutcomeFound, rwire.RuleReadOutcomeExpired, rwire.RuleReadOutcomeUnknown:
		case rwire.RuleReadOutcomeInvalid:
			return rwire.ChangeRequestOutcomeInvalid
		default:
			return rwire.ChangeRequestOutcomeUnavailable
		}
	case rwire.ChangeKindSetDefault:
		_, defaults, err := c.r.policy.DefaultsSnapshot(ctx)
		if err != nil {
			return rwire.ChangeRequestOutcomeUnavailable
		}
		if !slices.ContainsFunc(defaults, func(d rwire.AccountDefault) bool { return d.Name == change.Name }) {
			return rwire.ChangeRequestOutcomeInvalid
		}
	case rwire.ChangeKindRetireAction:
		snapshot, err := c.r.policy.OperatorSnapshot()
		if err != nil {
			return rwire.ChangeRequestOutcomeUnavailable
		}
		if !slices.Contains(snapshot.Catalog, change.Action) || retireTooLarge(snapshot, change.Action) {
			return rwire.ChangeRequestOutcomeInvalid
		}
	}
	return rwire.ChangeRequestOutcomePending
}

// changeKey identifies a requester's exact change.
func changeKey(by rwire.Subject, change rwire.PolicyChange) string {
	data, _ := json.Marshal(struct {
		Account, Program string
		Change           rwire.PolicyChange
	}{by.Account, identity.NormalizeSubjectProgram(by.Program), change})
	return string(data)
}

// admitWith holds a new request and asks the person, or joins the same pending
// request from the same program.
func (c *rightsChanges) admitWith(by rwire.Subject, change rwire.PolicyChange, apply func(context.Context) (rwire.ChangeRequestOutcome, string)) (*changeRequest, rwire.ChangeRequestOutcome) {
	key := changeKey(by, change)
	c.mu.Lock()
	defer c.mu.Unlock()
	pending, total := 0, 0
	for _, held := range c.requests {
		if held.key == key && held.outcome == rwire.ChangeRequestOutcomePending {
			return held, rwire.ChangeRequestOutcomePending
		}
		if held.outcome == rwire.ChangeRequestOutcomePending {
			total++
			if sameRequester(held.by, by) {
				pending++
			}
		}
	}
	if pending >= maxPendingPerRequester || total >= maxPendingPerAccount || c.asks == nil {
		return nil, rwire.ChangeRequestOutcomeUnavailable
	}
	if !c.roomLocked() {
		return nil, rwire.ChangeRequestOutcomeUnavailable
	}
	now := c.now()
	expires := now.Add(c.lifetime)
	code := newChangeCode()
	slots := c.questionSlots(by, change, code, expires)
	record, outcome, err := c.asks.book.AskApplicationAs(c.asks.scope, c.r.self().Program, "rights-change:"+code, asks.RightsChangeKey, slots,
		listen.Seen{Why: "runtime-owned rights change request"}, asks.Publisher{})
	if err != nil || outcome != "pending" {
		if err != nil && c.asks.report != nil {
			c.asks.report(err)
		}
		return nil, rwire.ChangeRequestOutcomeUnavailable
	}
	req := &changeRequest{id: record.ID, by: by, change: change, question: record.ID, expires: expires, key: key,
		outcome: rwire.ChangeRequestOutcomePending, done: make(chan struct{}), apply: apply}
	c.requests = append(c.requests, req)
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		c.settle(req)
	}()
	return req, rwire.ChangeRequestOutcomePending
}

// roomLocked makes room for one more request by dropping the oldest settled
// one. A list of pending requests only has no room.
func (c *rightsChanges) roomLocked() bool {
	if len(c.requests) < maxChangeRequests {
		return true
	}
	for i, held := range c.requests {
		if held.outcome != rwire.ChangeRequestOutcomePending {
			c.requests = slices.Delete(c.requests, i, i+1)
			return true
		}
	}
	return false
}

// remember holds an exempt or direct change's settled state for listing.
func (c *rightsChanges) remember(req *changeRequest) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.roomLocked() {
		c.requests = append(c.requests, req)
	}
}

func newChangeCode() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return hex.EncodeToString([]byte(time.Now().Format(time.RFC3339Nano)))[:16]
	}
	return hex.EncodeToString(b[:])
}

// settle waits for the person's answer or the expiry, then applies the
// change on Allow, once.
func (c *rightsChanges) settle(req *changeRequest) {
	decided, stop := c.asks.book.ObserveQuestionWait(req.question)
	defer stop()
	timer := time.NewTimer(req.expires.Sub(c.now()))
	defer timer.Stop()
	select {
	case <-c.ctx.Done():
		// A stopping runtime leaves the question to the next start, which
		// withdraws it (RuntimeBound): the change was only in this memory.
		return
	case <-decided:
	case <-timer.C:
		// Withdraw the question unless the person answered it in time.
		//unchecked: the record is read next, whatever the withdrawal did
		_, _, _ = c.asks.book.RetirePendingApplicationAuthorized(req.question, func() error { return nil })
	}
	allowed, answered := c.answer(req)
	switch {
	case allowed && req.apply != nil:
		outcome, revision := req.apply(c.ctx)
		c.finish(req, outcome, revision)
	case allowed:
		result := c.applyNow(c.ctx, req.by, req.change, routeAllowed)
		c.finish(req, result.State.Outcome, result.State.Revision)
	case answered:
		c.finish(req, rwire.ChangeRequestOutcomeRefused, "")
	default:
		c.finish(req, rwire.ChangeRequestOutcomeExpired, "")
	}
}

// answer reads the person's answer: allowed only for an operator Allow
// recorded on this exact runtime question before the request expired.
func (c *rightsChanges) answer(req *changeRequest) (allowed, answered bool) {
	record, found, err := c.asks.book.ApplicationRecord(req.question)
	if err != nil || !found || record.Pending() {
		return false, false
	}
	if record.Answered.After(req.expires) {
		return false, false
	}
	exact := record.Key == asks.RightsChangeKey && record.AskerProgram == c.r.self().Program && record.AnsweredBy == asks.AnsweredByOperator
	return exact && record.Option == "allow" && record.Yes, true
}

func (c *rightsChanges) finish(req *changeRequest, outcome rwire.ChangeRequestOutcome, revision string) {
	c.mu.Lock()
	req.outcome, req.revision = outcome, revision
	c.mu.Unlock()
	close(req.done)
}

// How a change reaches apply: the Panel window's own edit, an exempt
// caller's, or a request the person allowed.
type changeRoute int

const (
	routeDirect changeRoute = iota
	routeExempt
	routeAllowed
)

// applyNow applies change for by. An exempt or allowed change writes the
// Activity line; the Panel window's own edit writes none, as its direct edits
// do not. A direct or exempt change is held under a new id for listing; an
// allowed request keeps its admitted id, which settle records.
func (c *rightsChanges) applyNow(ctx context.Context, by rwire.Subject, change rwire.PolicyChange, route changeRoute) rightsservice.ChangeResult {
	if route != routeDirect && change.Kind == rwire.ChangeKindRetireAction {
		// A retire whose Undo could not restore every rule is the Panel
		// window's alone.
		snapshot, err := c.r.policy.OperatorSnapshot()
		if err != nil || retireTooLarge(snapshot, change.Action) {
			return rightsservice.ChangeResult{State: rwire.ChangeRequestState{Outcome: rwire.ChangeRequestOutcomeInvalid}}
		}
	}
	result, undo := c.apply(ctx, by, change)
	if result.State.Outcome == rwire.ChangeRequestOutcomeApplied && route != routeDirect {
		c.recordActivity(by, change, undo, route == routeExempt)
	}
	if route == routeAllowed {
		return result
	}
	if outcome := result.State.Outcome; outcome == rwire.ChangeRequestOutcomeApplied || outcome == rwire.ChangeRequestOutcomeConflict {
		req := &changeRequest{id: newChangeCode(), by: by, change: change, expires: c.now(), exempt: route == routeExempt,
			outcome: outcome, revision: result.State.Revision, done: make(chan struct{})}
		close(req.done)
		c.remember(req)
		result.State = req.state()
	}
	return result
}

// apply makes the change and returns its inverse. An empty expected revision
// applies at the current one, read again after a conflict.
func (c *rightsChanges) apply(ctx context.Context, by rwire.Subject, change rwire.PolicyChange) (rightsservice.ChangeResult, []rwire.PolicyChange) {
	authorize := func() error { return ctx.Err() }
	var result rightsservice.ChangeResult
	var undo []rwire.PolicyChange
	for attempt := 0; attempt < changeApplyAttempts; attempt++ {
		expected := change.ExpectedRevision
		var err error
		switch change.Kind {
		case rwire.ChangeKindSetRule, rwire.ChangeKindRevokeRule:
			rule := change.Rule
			before := c.r.policy.ReadRule(rule.Subject, rule.Action, rule.Resource)
			if expected == "" {
				expected = before.Revision
			}
			edit := rights.RuleEdit{By: by}
			if change.Kind == rwire.ChangeKindSetRule {
				permit := rule.Permit
				edit.Permit = &permit
				if change.TTLMs > 0 || change.Why != "" {
					edit.Exact, edit.TTL, edit.Why = true, time.Duration(change.TTLMs)*time.Millisecond, change.Why
				}
			}
			var policyEdit rwire.PolicyEdit
			policyEdit, err = c.r.policy.ChangeRuleContext(ctx, expected, rule.Subject, rule.Action, rule.Resource, edit, authorize)
			result.Edit = &policyEdit
			result.State.Outcome, result.State.Revision = policyOutcome(policyEdit.Outcome), policyEdit.Revision
			undo = ruleUndo(change, before, c.now())
		case rwire.ChangeKindSetDefault:
			var revision string
			var defaults []rwire.AccountDefault
			revision, defaults, err = c.r.policy.DefaultsSnapshot(ctx)
			if err != nil {
				break
			}
			if expected == "" {
				expected = revision
			}
			var defaultEdit rwire.DefaultEdit
			defaultEdit, err = c.r.policy.SetDefault(expected, change.Name, change.On, by, authorize)
			result.Default = &defaultEdit
			result.State.Outcome, result.State.Revision = policyOutcome(defaultEdit.Outcome), defaultEdit.Revision
			undo = nil
			for _, d := range defaults {
				if d.Name == change.Name && d.On != change.On {
					undo = []rwire.PolicyChange{{Kind: rwire.ChangeKindSetDefault, Name: d.Name, On: d.On}}
				}
			}
		case rwire.ChangeKindRegisterAction, rwire.ChangeKindRetireAction:
			var snapshot rights.PolicySnapshot
			snapshot, err = c.r.policy.OperatorSnapshot()
			if err != nil {
				break
			}
			if expected == "" {
				expected = snapshot.Revision
			}
			register := change.Kind == rwire.ChangeKindRegisterAction
			undo = c.actionUndo(change, snapshot, register)
			var actionEdit rwire.ActionEdit
			actionEdit, err = c.r.policy.EditAction(expected, change.Action, register, by, authorize)
			result.Action = &actionEdit
			result.State.Outcome, result.State.Revision = actionOutcome(actionEdit.Outcome), actionEdit.Revision
		}
		if err != nil {
			result = rightsservice.ChangeResult{State: rwire.ChangeRequestState{Outcome: rwire.ChangeRequestOutcomeUnavailable}}
			if errors.Is(err, rights.ErrUnresolvedShortName) {
				result.State.Outcome = rwire.ChangeRequestOutcomeInvalid
			}
			return result, nil
		}
		if result.State.Outcome != rwire.ChangeRequestOutcomeConflict || change.ExpectedRevision != "" {
			break
		}
	}
	if result.State.Outcome != rwire.ChangeRequestOutcomeApplied && result.State.Outcome != rwire.ChangeRequestOutcomeConflict {
		result.State.Revision = ""
	}
	return result, undo
}

func policyOutcome(outcome rwire.PolicyEditOutcome) rwire.ChangeRequestOutcome {
	switch outcome {
	case rwire.PolicyEditOutcomeApplied:
		return rwire.ChangeRequestOutcomeApplied
	case rwire.PolicyEditOutcomeConflict:
		return rwire.ChangeRequestOutcomeConflict
	case rwire.PolicyEditOutcomeInvalid:
		return rwire.ChangeRequestOutcomeInvalid
	case rwire.PolicyEditOutcomeForbidden:
		return rwire.ChangeRequestOutcomeForbidden
	}
	return rwire.ChangeRequestOutcomeUnavailable
}

func actionOutcome(outcome rwire.ActionEditOutcome) rwire.ChangeRequestOutcome {
	switch outcome {
	case rwire.ActionEditOutcomeApplied:
		return rwire.ChangeRequestOutcomeApplied
	case rwire.ActionEditOutcomeConflict:
		return rwire.ChangeRequestOutcomeConflict
	case rwire.ActionEditOutcomeInvalid, rwire.ActionEditOutcomeUnknown, rwire.ActionEditOutcomeExhausted:
		return rwire.ChangeRequestOutcomeInvalid
	case rwire.ActionEditOutcomeForbidden:
		return rwire.ChangeRequestOutcomeForbidden
	}
	return rwire.ChangeRequestOutcomeUnavailable
}

// ruleUndo is the change that restores the rule as it stood before: the
// earlier rule with its reason and remaining expiry, or no rule.
func ruleUndo(change rwire.PolicyChange, before rwire.RuleRead, now time.Time) []rwire.PolicyChange {
	rule := *change.Rule
	if restore, ok := restoreRule(before.Record, now); ok && before.Outcome == rwire.RuleReadOutcomeFound {
		return []rwire.PolicyChange{restore}
	}
	if change.Kind == rwire.ChangeKindRevokeRule {
		return nil
	}
	rule.Permit = false
	return []rwire.PolicyChange{{Kind: rwire.ChangeKindRevokeRule, Rule: &rule}}
}

// restoreRule is the set_rule change that writes record again.
func restoreRule(record *rwire.RuleRecord, now time.Time) (rwire.PolicyChange, bool) {
	if record == nil {
		return rwire.PolicyChange{}, false
	}
	var ttl int64
	if record.Expires != "" {
		expires, err := time.Parse(time.RFC3339Nano, record.Expires)
		if err != nil || !expires.After(now) {
			return rwire.PolicyChange{}, false
		}
		ttl = max(expires.Sub(now).Milliseconds(), 1)
	}
	rule := record.Rule
	return rwire.PolicyChange{Kind: rwire.ChangeKindSetRule, Rule: &rule, TTLMs: ttl, Why: record.Why}, true
}

// retireTooLarge reports an action more rules name than a retire's Undo
// restores.
func retireTooLarge(snapshot rights.PolicySnapshot, action string) bool {
	n := 0
	for _, rule := range snapshot.Rules {
		if rule.Action == action {
			n++
		}
	}
	return n > maxChangeUndoRules
}

// actionUndo retires an action the change registers anew, or registers a
// retired action again with up to maxChangeUndoRules of its rules.
func (c *rightsChanges) actionUndo(change rwire.PolicyChange, snapshot rights.PolicySnapshot, register bool) []rwire.PolicyChange {
	listed := slices.Contains(snapshot.Catalog, change.Action)
	if register {
		if listed {
			return nil
		}
		return []rwire.PolicyChange{{Kind: rwire.ChangeKindRetireAction, Action: change.Action}}
	}
	if !listed {
		return nil
	}
	undo := []rwire.PolicyChange{{Kind: rwire.ChangeKindRegisterAction, Action: change.Action}}
	now := c.now()
	for _, rule := range snapshot.Rules {
		if rule.Action != change.Action || len(undo) > maxChangeUndoRules {
			continue
		}
		if restore, ok := restoreRule(c.r.policy.ReadRule(rule.Subject, rule.Action, rule.Resource).Record, now); ok {
			undo = append(undo, restore)
		}
	}
	return undo
}

// requesterWords names the requesting operator program. Every operator
// program but the Panel window and its presenter, which edit directly, is an
// image of the runtime itself: the command line. Any other program reads by
// its file name.
func (c *rightsChanges) requesterWords(program string) string {
	if c.r.isOperator(program) {
		return "the command line"
	}
	return asks.AskerDisplay(program)
}

// changeWords is the service's own wording of a change, completing "Let
// <requester> ...?": e.g. "give opencode permission to download files".
func changeWords(change rwire.PolicyChange) string {
	switch change.Kind {
	case rwire.ChangeKindSetRule, rwire.ChangeKindRevokeRule:
		rule := change.Rule
		app := programFileStem(identity.NormalizeSubjectProgram(rule.Subject.Program))
		if app == "" {
			app = "an app"
		}
		verb, object, ok := grants.FirstUseWords(rule.Action, rule.Resource)
		phrase := verb
		if !ok {
			phrase = "use " + rule.Action
		} else if object != "" {
			phrase += " " + object
		}
		switch {
		case change.Kind == rwire.ChangeKindRevokeRule:
			return "remove " + app + "'s rule to " + phrase
		case rule.Permit:
			return "give " + app + " permission to " + phrase
		}
		return "block " + app + " from permission to " + phrase
	case rwire.ChangeKindSetDefault:
		if change.On {
			return "turn on the setting “" + change.Name + "” for all apps"
		}
		return "turn off the setting “" + change.Name + "” for all apps"
	case rwire.ChangeKindRegisterAction:
		return "add the permission " + change.Action
	case rwire.ChangeKindRetireAction:
		return "remove the permission " + change.Action + " and every rule that names it"
	case rwire.ChangeKindDeclaration:
		return declarationWords(change)
	}
	return "change permissions"
}

// slotWords fits value to one question slot: one line of at most
// catalogue.MaxSlot characters, keeping its end, where a path's file
// name is.
func slotWords(value string) string {
	value = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	if value == "" {
		return "-"
	}
	runes := []rune(value)
	if len(runes) > catalogue.MaxSlot {
		return "…" + string(runes[len(runes)-catalogue.MaxSlot+1:])
	}
	return value
}

// programSlot fits a program path to one question slot (programFit).
func programSlot(program string) string {
	return programFit(program, catalogue.MaxSlot)
}

// programFit fits a program path to limit characters on one line: the whole
// path when it fits, else the drive and the start of the folder, an ellipsis
// and the file name, so the person reads where it lives and what it is.
func programFit(program string, limit int) string {
	value := slotWords(program)
	if utf8.RuneCountInString(program) <= limit || value == "-" {
		return value
	}
	cut := strings.LastIndexAny(program, `\/`)
	if cut < 0 {
		return string([]rune(value)[:limit-1]) + "…"
	}
	base := []rune(program[cut:])
	if len(base)+2 > limit {
		return "…" + string(base[len(base)-limit+1:])
	}
	head := []rune(program[:cut])
	keep := limit - len(base) - 1
	return slotWords(string(head[:min(keep, len(head))]) + "…" + string(base))
}

func (c *rightsChanges) questionSlots(by rwire.Subject, change rwire.PolicyChange, code string, expires time.Time) map[string]string {
	program, action, resource := "-", "-", "-"
	switch change.Kind {
	case rwire.ChangeKindSetRule, rwire.ChangeKindRevokeRule:
		program, action, resource = change.Rule.Subject.Program, change.Rule.Action, change.Rule.Resource
	case rwire.ChangeKindSetDefault:
		action = change.Name
	case rwire.ChangeKindRegisterAction, rwire.ChangeKindRetireAction:
		action = change.Action
	case rwire.ChangeKindDeclaration:
		program, action, resource = change.Program, "declare "+change.Name, change.Name
		if change.Address != "" {
			resource = addressFit(change.Address, catalogue.MaxSlot)
		}
		if program == "" {
			program = "-"
		}
	}
	return map[string]string{"requester": slotWords(c.requesterWords(by.Program)), "change": slotWords(changeWords(change)),
		"program": programSlot(program), "action": slotWords(action), "resource": slotWords(resource),
		"code": code, "until": expires.UTC().Format(time.RFC3339)}
}

// recordActivity writes the Activity line of an applied change (monitor
// activity_rights_changes.go): who asked, what changed and its Undo.
func (c *rightsChanges) recordActivity(by rwire.Subject, change rwire.PolicyChange, undo []rwire.PolicyChange, exempt bool) {
	c.r.elevated.mu.Lock()
	sink := c.r.elevated.logs
	c.r.elevated.mu.Unlock()
	if sink == nil {
		return
	}
	attrs := map[string]string{"oa_event": rightsChangeEvent, "program": by.Program, "requester": c.requesterWords(by.Program),
		"change": changeWords(change), "exempt": "false", "line": newChangeCode()}
	if exempt {
		attrs["exempt"] = "true"
	}
	if len(undo) > 0 {
		if data, err := json.Marshal(changeUndo{Changes: undo, After: afterState(change)}); err == nil {
			attrs["undo"] = string(data)
		}
	}
	msg := "Rights changed by " + attrs["requester"]
	if exempt {
		msg += " running as administrator"
	}
	if err := sink.Write(logging.Record{Schema: logging.SchemaVersion, Time: logging.At(c.now()), Level: logging.LevelInfo, Msg: msg, Attrs: attrs}); err != nil {
		writeHostLogLine(os.Stderr, "runtime: rights change log: "+err.Error())
	}
}

// changeUndo is an Activity line's Undo: the changes that reverse it, and the
// rule state the change left, which Undo requires to still stand.
type changeUndo struct {
	Changes []rwire.PolicyChange `json:"changes"`
	After   *changeAfter         `json:"after,omitempty"`
}

// changeAfter is the exact rule a rule change left: present with permit, or
// absent.
type changeAfter struct {
	Subject  rwire.Subject `json:"subject"`
	Action   string        `json:"action"`
	Resource string        `json:"resource"`
	Present  bool          `json:"present"`
	Permit   bool          `json:"permit"`
}

func afterState(change rwire.PolicyChange) *changeAfter {
	if change.Rule == nil {
		return nil
	}
	rule := change.Rule
	return &changeAfter{Subject: rule.Subject, Action: rule.Action, Resource: rule.Resource,
		Present: change.Kind == rwire.ChangeKindSetRule, Permit: change.Kind == rwire.ChangeKindSetRule && rule.Permit}
}
