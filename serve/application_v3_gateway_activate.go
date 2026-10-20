package main

import (
	"context"
	"errors"
	"fmt"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-facade/go/manifest"
	"strings"
	"time"
	"unicode"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

func applicationV3Activation(out applications.ActivationOutcome, reason string) applications.ActivationResult {
	return applications.ActivationResult{Outcome: out, Reason: reason}
}

// Selection is over actual current directory leases and runtime-held channels.
// An unowned GUI is never attached, hidden, or duplicated by a new start.
func (g *applicationV3Gateway) activationSession(ctx context.Context, k *applicationV3Kept, contextName string) (*ownedApplicationSession, bool, error) {
	exited := g.retireExited(k.manifest.Name)
	if !g.directory.lock(ctx) {
		return nil, false, ctx.Err()
	}
	g.directory.expireLocked()
	withdrawn := false
	for _, b := range exited {
		if lease, found := g.directory.instances[b.instance]; found && lease.application == b.application && lease.session == b.desktop {
			g.directory.recordPresenceEndLocked(lease, applicationPresenceWithdrawn)
			delete(g.directory.instances, b.instance)
			withdrawn = true
		}
	}
	if withdrawn {
		g.directory.expireNativeInboxesLocked()
		g.directory.notifyLocked()
	}
	instances := []string{}
	for _, lease := range g.directory.instances {
		if lease.application != k.manifest.Name {
			continue
		}
		matches := contextName == ""
		for _, entry := range lease.value.Contexts {
			if entry.Name == contextName {
				matches = true
			}
		}
		if matches {
			instances = append(instances, lease.value.Instance)
		}
	}
	g.directory.unlock()
	var selected *ownedApplicationSession
	for _, instance := range instances {
		s := g.session(k.manifest.Name, instance)
		if s == nil {
			return nil, true, errors.New("owned_host")
		}
		if s.bindingValue().manifestRevision != k.revision {
			return nil, true, errors.New("revision")
		}
		if selected != nil && selected != s {
			return nil, true, errors.New("context")
		}
		selected = s
	}
	if contextName != "" && selected == nil {
		return nil, false, errors.New("context")
	}
	return selected, len(instances) > 0, nil
}

// retireExited removes the owned sessions of application whose process the
// launch owner saw exit and returns their bindings. Such a process left
// nothing running, so its presence lease ends now rather than at its deadline,
// and the next open starts the application at once after a crash.
func (g *applicationV3Gateway) retireExited(application string) []ownedApplicationBinding {
	g.mu.Lock()
	defer g.mu.Unlock()
	var exited []ownedApplicationBinding
	for key, s := range g.owned {
		b := s.bindingValue()
		if b.application != application || b.runtimeEpoch != g.epoch || !s.exited.Load() {
			continue
		}
		select {
		case <-s.done:
			delete(g.owned, key)
			if b.instance != "" {
				exited = append(exited, b)
			}
		default:
		}
	}
	return exited
}

func (g *applicationV3Gateway) beginOwnedApplication(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, starter string) (*ownedApplicationSession, bool, error) {
	for {
		g.mu.Lock()
		for key, s := range g.owned {
			b := s.bindingValue()
			if b.application == k.manifest.Name && b.manifestRevision == k.revision && b.runtimeEpoch == g.epoch {
				select {
				case <-s.done:
					// The person closed the application: its process exited and
					// nothing of it runs, so the session retires and the open
					// starts it again. A channel that ended while its process
					// may still run stays uncertain; a second start could
					// duplicate the person's window.
					if s.exited.Load() {
						delete(g.owned, key)
						continue
					}
					g.mu.Unlock()
					return nil, false, errOwnedApplicationUncertain
				default:
					g.mu.Unlock()
					return s, false, nil
				}
			}
		}
		pending := g.starting[k.manifest.Name]
		if pending == nil {
			pending = make(chan struct{})
			if g.starting == nil {
				g.starting = map[string]chan struct{}{}
			}
			g.starting[k.manifest.Name] = pending
			g.mu.Unlock()
			defer func() { g.mu.Lock(); delete(g.starting, k.manifest.Name); close(pending); g.mu.Unlock() }()
			break
		}
		g.mu.Unlock()
		select {
		case <-pending:
		case <-ctx.Done():
			return nil, false, ctx.Err()
		}
		if s, _, err := g.activationSession(ctx, k, ""); s != nil || err != nil {
			return s, false, err
		}
	}
	// Recheck after winning the start slot: a concurrent authorized request may
	// have published the existing channel while this call waited.
	if s, _, err := g.activationSession(ctx, k, ""); s != nil || err != nil {
		return s, false, err
	}
	desktop, ok := applicationSession(c.peer)
	if !ok {
		return nil, false, errors.New("session")
	}
	if g.directory.unsignedLaunch != nil {
		g.directory.unsignedLaunch(k.manifest.Name, true)
	}
	hold, err := g.directory.revalidateApplicationV3(ctx, fileid.Self, k.manifest.Name, k.revision)
	if err != nil {
		return nil, false, err
	}
	// The start reads the hold as the kept registration, the way k.recheck
	// does: an entry kept under the earlier member spelling starts too.
	fresh, digest, err := g.heldDeclaration(hold, k.program)
	if err != nil {
		hold.Close()
		return nil, false, fmt.Errorf("manifest: %w", err)
	}
	if digest != k.digest {
		hold.Close()
		return nil, false, errors.New("manifest: held image declares another manifest than the kept one")
	}
	image := hold.ownedImage()
	binding := ownedApplicationBinding{application: k.manifest.Name, account: c.subject.Account, desktop: desktop, runtimeEpoch: g.epoch, manifestRevision: k.revision, imageCode: applicationV3HeldKey(hold)}
	// The process lifetime guard uses its own source hold and authoritative
	// registration. It does not retain the activating RPC's temporary peer.
	authority := func(ctx context.Context, b ownedApplicationBinding) error {
		if b.runtimeEpoch != g.epoch || b.application != k.manifest.Name || b.manifestRevision != k.revision {
			return errors.New("application lifetime changed")
		}
		current, err := g.directory.revalidateApplicationV3(ctx, fileid.Self, b.application, b.manifestRevision)
		if err != nil {
			return err
		}
		defer current.Close()
		if applicationV3HeldKey(current) != b.imageCode {
			return errors.New("application image changed")
		}
		if _, now, err := g.heldDeclaration(current, k.program); err != nil || now != k.digest {
			return errors.New("application manifest changed")
		}
		return current.Recheck(ctx)
	}
	if g.start == nil {
		hold.Close()
		return nil, false, errors.New("owned host unavailable")
	}
	s, err := g.start(ctx, image, binding, append([]string(nil), fresh.Host.Arguments...), authority)
	if err != nil {
		return nil, false, err
	}
	if g.recordOwnedStart(k.manifest.Name, starter, s.bindingValue()) {
		s.starter = starter
	}
	g.mu.Lock()
	if g.owned == nil {
		g.owned = map[string]*ownedApplicationSession{}
	}
	g.owned[applicationV3BodyDigest([]byte(k.manifest.Name+"\x00"+k.revision))] = s
	g.mu.Unlock()
	return s, true, nil
}

// Presence is an authenticated directory lease and owned channel association.
// The adopter's exact activation handler owns UI completion; no free-form
// interface name is promoted into a generic GUI-readiness assertion.
func (g *applicationV3Gateway) waitOwnedPresence(ctx context.Context, k *applicationV3Kept, s *ownedApplicationSession) error {
	for {
		if err := s.guard(ctx, s.bindingValue()); err != nil {
			return err
		}
		if !g.directory.lock(ctx) {
			return ctx.Err()
		}
		g.directory.expireLocked()
		b := s.bindingValue()
		lease, found := g.directory.instances[b.instance]
		ready := found && b.instance != "" && lease.application == b.application && lease.session == b.desktop
		changed := g.directory.changed
		g.directory.unlock()
		if ready {
			return nil
		}
		select {
		case <-changed:
		case <-s.done:
			return s.closedError()
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// activationOperator decides who may start or reopen a kept application
// (coordinator dispositions 2026-11-09, FAC-M32 oa_applications_open). A stored
// application.activate permit decides operator programs such as the Panel. A
// kept agent program (agentSubject) with no such rule is decided by the
// declared verb's class (FAC-M19) and its first-use question, which
// authorizationFor asks. Every other caller, and a stored deny, is refused.
func (g *applicationV3Gateway) activationOperator(ctx context.Context, c applicationV3Caller, k *applicationV3Kept) (bool, error) {
	switch g.policy.decide(ctx, c.subject, ActionApplicationActivate, "app:"+k.manifest.Name).Outcome {
	case rights.DecisionOutcomePermitted:
		return true, g.quiet(ctx, c, k, ActionApplicationActivate)
	case rights.DecisionOutcomeNotGranted:
		return false, g.activationByClass(ctx, c, k)
	case rights.DecisionOutcomeDenied:
		return false, errOpenBlocked
	}
	return false, errors.New("rights")
}

// errOpenBlocked is the person's explicit Block for this caller: a stored
// deny on application.activate or on the open verb's class. Its answer reads
// forbidden with reason blocked.
var errOpenBlocked = errors.New("blocked")

// openBlockedByRule reports a stored deny on the open verb's class for c.
func (g *applicationV3Gateway) openBlockedByRule(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, verb *registrationwire.ActivationVerb) bool {
	return g.policy.decide(ctx, c.subject, "abstraction.facade/command."+verb.Class.String(), "app:"+k.manifest.Name).Outcome == rights.DecisionOutcomeDenied
}

// An open class may proceed with a permit or a missing rule, which can
// still need a person's answer. An unknown action and an explicit deny refuse.
func openClassMayProceed(outcome rights.DecisionOutcome) bool {
	if !rightsclient.Evaluated(outcome) {
		return false
	}
	switch outcome {
	case rights.DecisionOutcomeDenied:
		return false
	case rights.DecisionOutcomeUnknownAction:
		return false
	case rights.DecisionOutcomePermitted:
		return true
	case rights.DecisionOutcomeNotGranted:
		return true
	default:
		return false
	}
}

// activationByClass checks and rechecks a class-decided caller: the same
// caller and kept manifest, still a kept agent program, and still no stored
// deny on application.activate.
func (g *applicationV3Gateway) activationByClass(ctx context.Context, c applicationV3Caller, k *applicationV3Kept) error {
	if !c.valid(g.directory.owner) || ctx.Err() != nil {
		return errors.New("caller")
	}
	if err := k.recheck(ctx); err != nil {
		return err
	}
	if !openClassMayProceed(g.policy.decide(ctx, c.subject, ActionApplicationActivate, "app:"+k.manifest.Name).Outcome) {
		return errors.New("rights")
	}
	if agent, err := g.agentSubject(ctx, c); err != nil || !agent {
		return errors.New("rights")
	}
	return c.recheck()
}

// agentRunningInstance answers a class-decided agent's open of an application
// whose owned instance already runs (coordinator, 2026-11-09): the instance
// at once, with no question, no start and no verb delivery, since nothing
// changes on the person's desktop. A stored deny on the verb's class, or a
// verb only the person may answer, still goes through the full path.
func (g *applicationV3Gateway) agentRunningInstance(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, verb *registrationwire.ActivationVerb) (string, bool) {
	if g.personOnly(ctx, c, verb.Class) {
		return "", false
	}
	if !openClassMayProceed(g.policy.decide(ctx, c.subject, "abstraction.facade/command."+verb.Class.String(), "app:"+k.manifest.Name).Outcome) {
		return "", false
	}
	s, _, err := g.activationSession(ctx, k, "")
	if err != nil || s == nil {
		return "", false
	}
	select {
	case <-s.initialized:
	default:
		return "", false
	}
	instance := s.bindingValue().instance
	if instance == "" || g.activationByClass(ctx, c, k) != nil {
		return "", false
	}
	return instance, true
}

// stoppedApplication reports that k has no live instance at all. It names no
// context, so it reveals nothing about one before authorization.
func (g *applicationV3Gateway) stoppedApplication(ctx context.Context, k *applicationV3Kept) bool {
	s, live, err := g.activationSession(ctx, k, "")
	return err == nil && s == nil && !live
}

// openAuthorization is an agent's start of a stopped application: the
// person's own once-answer "Open <app>?" (ASK-O10, FAC-M32). Its record is
// a runtime-only occurrence no answering program lists or answers, it
// writes no policy, and it shares no fingerprint with the routine
// permission question. The current activation right and the verb class's
// stored rule still decide alongside it.
func (g *applicationV3Gateway) openAuthorization(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, v applicationV3Invocation, call appmodel.GatewayCall, key string, body []byte) applicationV3ConsentResult {
	action := "abstraction.facade/command." + v.class.String()
	// classRight runs under the Book's record lock (applicationV3OneCall's
	// recheck), so it reads no Book-backed state; the kept-agent check of
	// activationByClass runs beside it, outside that lock.
	classRight := func(ctx context.Context) error {
		if !c.valid(g.directory.owner) {
			return errors.New("caller")
		}
		if err := k.recheck(ctx); err != nil {
			return err
		}
		for _, a := range []string{ActionApplicationActivate, action} {
			if !openClassMayProceed(g.policy.decide(ctx, c.subject, a, "app:"+k.manifest.Name).Outcome) {
				return errors.New("rights")
			}
		}
		return ctx.Err()
	}
	if g.activationByClass(ctx, c, k) != nil || classRight(ctx) != nil || !applicationV3OpenClass(v.class) {
		return applicationV3ConsentResult{outcome: "unavailable", reason: "rights"}
	}
	if g.asks == nil {
		return applicationV3ConsentResult{outcome: "unsupported", reason: "presenter"}
	}
	result := g.asks.applicationV3OneCall(ctx, applicationV3ConsentRequest{caller: c, application: k.manifest.Name, title: k.manifest.Title, program: k.program, revision: k.revision, instance: call.Instance, tool: call.Tool, key: key, bodyDigest: applicationV3BodyDigest(body), method: "Activate", class: v.class, context: v.context, recheck: classRight})
	if answered := result.recheck; result.outcome == "" && answered != nil {
		result.recheck = func(ctx context.Context) error {
			if err := g.activationByClass(ctx, c, k); err != nil {
				return err
			}
			return answered(ctx)
		}
	}
	return result
}

// applicationV3AnswerWait bounds how long an open waits for the person's
// answer: the wait policy's default of 40 s (backlog/RIGHTS-FLOW.md "Wait
// policy", owner 2026-11-10) until the person's "Wait for my answer" setting
// exists. A wait that runs out reads not_ready/consent; the question stays
// answerable where its owner keeps it, and the next open finds the answer.
// Tests shorten it.
var applicationV3AnswerWait = 40 * time.Second

// applicationV3OpenRefusal is an unauthorized open's answer. A wait that
// ended before the person answered reads not_ready/consent; the person's
// Not now on an open question reads forbidden/consent.
func applicationV3OpenRefusal(ctx context.Context, authorization applicationV3ConsentResult, personStart bool) applications.ActivationResult {
	if ctx.Err() != nil || authorization.reason == "gone" {
		return applicationV3Activation(applications.ActivationOutcomeNotReady, "consent")
	}
	if personStart && authorization.outcome == "refused" {
		return applicationV3Activation(applications.ActivationOutcomeForbidden, "consent")
	}
	return applicationV3Activation(applications.ActivationOutcomeForbidden, authorization.reason)
}

func (g *applicationV3Gateway) activate(ctx context.Context, c applicationV3Caller, key string, request applications.ActivateRequest) applications.ActivationResult {
	refuse := applicationV3Activation
	if !c.valid(g.directory.owner) {
		return refuse(applications.ActivationOutcomeForbidden, "caller")
	}
	k, err := g.load(ctx, c.peer, request.Application)
	if err != nil {
		return refuse(applications.ActivationOutcomeUnavailable, "manifest")
	}
	defer k.close()
	request.Application = k.manifest.Name
	// The kept application's display name rides on every answer about it,
	// so an agent's sentence names the app as the person knows it.
	result := g.activateKept(ctx, c, key, request, k)
	result.Title = k.manifest.Title
	return result
}

func (g *applicationV3Gateway) activateKept(ctx context.Context, c applicationV3Caller, key string, request applications.ActivateRequest, k *applicationV3Kept) applications.ActivationResult {
	refuse := applicationV3Activation
	selectDefault := request.DefaultVerb != nil && *request.DefaultVerb
	if selectDefault && request.Verb != "" {
		return refuse(applications.ActivationOutcomeInvalid, "verb")
	}
	if request.Verb == "" {
		if !selectDefault {
			return refuse(applications.ActivationOutcomeInvalid, "missing_verb")
		}
		agent, _ := g.agentSubject(ctx, c)
		read := g.policy.decide(ctx, c.subject, "abstraction.facade/command.read", "app:"+k.manifest.Name).Outcome
		canRead := read == rights.DecisionOutcomePermitted
		if read == rights.DecisionOutcomeUnavailable || (!canRead && !agent) {
			return refuse(applications.ActivationOutcomeInvalid, "missing_verb")
		}
		verbs := g.openVerbs(ctx, c, k, agent, true)
		if len(verbs) != 1 {
			return refuse(applications.ActivationOutcomeInvalid, "missing_verb")
		}
		request.Verb = verbs[0].Name
		request.DefaultVerb = nil
	}
	if err := manifest.ValidateApplicationActivationPayload(k.manifest, &request); err != nil {
		return refuse(applications.ActivationOutcomeInvalid, applicationV3CodecWord(err))
	}
	operator, err := g.activationOperator(ctx, c, k)
	if errors.Is(err, errOpenBlocked) {
		return refuse(applications.ActivationOutcomeForbidden, "blocked")
	}
	if err != nil {
		return refuse(applications.ActivationOutcomeForbidden, "rights")
	}
	// An operator's stored rule, or for an agent the unchanged absence of one,
	// stays true through the start and the verb's delivery.
	activationRight := func(ctx context.Context) error {
		if operator {
			return g.quiet(ctx, c, k, ActionApplicationActivate)
		}
		return g.activationByClass(ctx, c, k)
	}
	var verb *registrationwire.ActivationVerb
	for i := range k.manifest.Verbs {
		if k.manifest.Verbs[i].Name == request.Verb {
			verb = &k.manifest.Verbs[i]
			break
		}
	}
	if verb == nil {
		return refuse(applications.ActivationOutcomeInvalid, "verb")
	}
	if g.openBlockedByRule(ctx, c, k, verb) {
		return refuse(applications.ActivationOutcomeForbidden, "blocked")
	}
	// The fast path names no context: an agent's context name selects nothing
	// before it is authorized, so the answer reveals no context's existence.
	if !operator && request.Context == "" {
		if instance, running := g.agentRunningInstance(ctx, c, k, verb); running {
			return applications.ActivationResult{Outcome: applications.ActivationOutcomeReady, Instance: instance}
		}
	}
	canonical := applications.EncodeActivateRequest(&request)
	previous, token, err := g.receipts.admit(ctx, applicationV3ReceiptRequest{Scope: applicationV3ReceiptScope{Account: c.subject.Account, Program: c.subject.Program, Method: "Activate"}, Key: key, CanonicalBody: canonical, Binding: applicationV3ReceiptBinding{Application: request.Application, Manifest: k.digest, Revision: k.revision, RuntimeGeneration: g.epoch}})
	if err != nil {
		return refuse(applications.ActivationOutcomeUnavailable, "receipt")
	}
	call := appmodel.GatewayCall{Application: request.Application, Tool: request.Verb, Arguments: appmodel.Raw(request.Payload)}
	v := applicationV3Invocation{class: verb.Class}
	var prior *applications.ActivationResult
	if token == nil {
		if previous.State != "complete" {
			return refuse(applications.ActivationOutcomeUnknown, "receipt")
		}
		prior, err = applications.DecodeActivationResult(previous.Reply)
		if err != nil {
			return refuse(applications.ActivationOutcomeUnavailable, "receipt")
		}
		call.Instance = prior.Instance
		v.context = &appmodel.ContextRef{Instance: call.Instance, Context: request.Context}
	}
	// A sensitive occurrence is asked only after the actual selected instance
	// is known, so only an operator's stored lifecycle rule starts such a verb.
	sensitive := g.personOnly(ctx, c, verb.Class)
	if sensitive && !operator {
		// Its question would follow the start; an agent opens only verbs whose
		// class is decided before anything starts.
		return refuse(applications.ActivationOutcomeForbidden, "rights")
	}
	authorization := applicationV3ConsentResult{recheck: func(ctx context.Context) error {
		if err := activationRight(ctx); err != nil {
			return err
		}
		d := g.policy.decide(ctx, c.subject, "abstraction.facade/command."+verb.Class.String(), "app:"+request.Application)
		switch d.Outcome {
		case rights.DecisionOutcomeDenied:
			return errors.New("rights")
		case rights.DecisionOutcomeUnavailable:
			return errors.New("rights")
		}
		return nil
	}}
	// personStart is true once the person's own once-answer covers this
	// agent's start: an agent never starts a stopped application under a
	// standing or answerer-written permit (coordinator, 2026-11-09).
	personStart := false
	// The wait for the person's answer has its own bound (applicationV3AnswerWait);
	// the start after an Allow keeps the caller's remaining budget.
	answerCtx, endAnswer := context.WithTimeout(ctx, applicationV3AnswerWait)
	defer endAnswer()
	if !sensitive || token == nil {
		switch {
		case operator && !sensitive:
			// FAC-M32 (backlog/APP-LAUNCH-JOB.md ruling 2): a caller holding
			// application.activate, such as the Panel after the person's
			// Open, is decided by that rule alone. A stored deny on the
			// verb's class refuses; a class without a rule asks nothing.
			if authorization.recheck(ctx) != nil {
				return refuse(applications.ActivationOutcomeForbidden, "rights")
			}
		case !operator && token != nil && g.stoppedApplication(ctx, k):
			authorization, personStart = g.openAuthorization(answerCtx, c, k, v, call, key, canonical), true
		default:
			authorization = g.authorizationFor(answerCtx, c, k, v, call, key, canonical, token == nil, "Activate")
		}
	}
	if authorization.outcome != "" {
		return applicationV3OpenRefusal(answerCtx, authorization, personStart)
	}
	quiet := func(ctx context.Context) error {
		if err := activationRight(ctx); err != nil {
			return err
		}
		if authorization.recheck == nil {
			return errors.New("rights")
		}
		return authorization.recheck(ctx)
	}
	if token == nil {
		if previous.State != "complete" || quiet(ctx) != nil {
			return refuse(applications.ActivationOutcomeUnknown, "receipt")
		}
		result := prior
		if result.Outcome == applications.ActivationOutcomeReady {
			s := g.session(request.Application, result.Instance)
			if s == nil || g.waitOwnedPresence(ctx, k, s) != nil {
				return refuse(applications.ActivationOutcomeUnknown, "owned_host")
			}
		}
		return *result
	}
	finish := func(result applications.ActivationResult) applications.ActivationResult {
		if err := g.receipts.finish(ctx, *token, applications.EncodeActivationResult(&result)); err != nil {
			return refuse(applications.ActivationOutcomeUnknown, "receipt")
		}
		if quiet(ctx) != nil {
			return refuse(applications.ActivationOutcomeUnavailable, "authority")
		}
		return result
	}
	budget, cancel := context.WithTimeout(ctx, time.Duration(k.manifest.Host.ReadinessDeadlineMs)*time.Millisecond)
	defer cancel()
	s, _, err := g.activationSession(budget, k, request.Context)
	started := false
	if err != nil {
		return finish(refuse(applications.ActivationOutcomeUnavailable, err.Error()))
	}
	if s == nil {
		if !operator && !personStart {
			// The application stopped after the earlier check: its start
			// still waits for the person's own answer, bounded like the
			// first ask by applicationV3AnswerWait.
			reask, endReask := context.WithTimeout(ctx, applicationV3AnswerWait)
			authorization, personStart = g.openAuthorization(reask, c, k, v, call, key, canonical), true
			if authorization.outcome != "" {
				refusal := applicationV3OpenRefusal(reask, authorization, true)
				endReask()
				return finish(refusal)
			}
			endReask()
		}
		if quiet(budget) != nil {
			return finish(refuse(applications.ActivationOutcomeForbidden, "rights"))
		}
		starter := ""
		if !operator {
			starter = c.subject.Program
		}
		s, started, err = g.beginOwnedApplication(budget, c, k, starter)
		if err != nil {
			if g.report != nil {
				g.report(fmt.Errorf("applications@3: owned start of %q: %.256s", k.manifest.Name, err.Error()))
			}
			if errors.Is(err, errOwnedApplicationUncertain) {
				return finish(refuse(applications.ActivationOutcomeUnknown, "owned_host"))
			}
			return finish(refuse(applications.ActivationOutcomeIdentityRefused, "owned_host"))
		}
		// The current shared SDK advertises this existing MCP revision. There
		// is one initialization attempt, with no protocol retry after sending.
		if started {
			if err = s.initialize(budget, "2026-07-28"); err != nil {
				return finish(refuse(applications.ActivationOutcomeUnknown, "initialize"))
			}
		}
	}
	if err := g.waitOwnedPresence(budget, k, s); err != nil {
		return finish(refuse(applications.ActivationOutcomeNotReady, "owned_host"))
	}
	select {
	case <-s.initialized:
	case <-s.done:
		return finish(refuse(applications.ActivationOutcomeUnknown, "initialize"))
	case <-budget.Done():
		return finish(refuse(applications.ActivationOutcomeNotReady, "initialize"))
	}
	call.Instance = s.bindingValue().instance
	if sensitive {
		v.context = &appmodel.ContextRef{Instance: call.Instance, Context: request.Context}
		authorization = g.authorizationFor(budget, c, k, v, call, key, canonical, false, "Activate")
		if authorization.outcome != "" {
			return finish(refuse(applications.ActivationOutcomeForbidden, authorization.reason))
		}
	}
	text, err := s.call(budget, &call, func(raw []byte) error { _, err := client.ParseHostActivationResult(raw); return err }, func(ctx context.Context, _ ownedApplicationBinding, _ *appmodel.GatewayCall) error { return quiet(ctx) })
	if err != nil {
		return finish(refuse(applications.ActivationOutcomeUnknown, "activation"))
	}
	result, err := client.ParseHostActivationResult(text)
	if err != nil {
		return finish(refuse(applications.ActivationOutcomeUnknown, "activation"))
	}
	switch result.Outcome {
	case applications.HostActivationOutcomeRefused:
		pending := refuse(applications.ActivationOutcomeNotReady, applicationV3HostDetail("activation:refused", result.Reason))
		if strings.HasPrefix(result.Reason, "starting:") {
			pending.Instance = call.Instance
		}
		return finish(pending)
	case applications.HostActivationOutcomeUnavailable:
		return finish(refuse(applications.ActivationOutcomeUnavailable, applicationV3HostDetail("activation:unavailable", result.Reason)))
	case applications.HostActivationOutcomeApplied:
		if err := g.waitOwnedPresence(budget, k, s); err != nil {
			return finish(refuse(applications.ActivationOutcomeNotReady, "owned_host"))
		}
		return finish(applications.ActivationResult{Outcome: applications.ActivationOutcomeReady, Instance: call.Instance, Started: started})
	default:
		return finish(refuse(applications.ActivationOutcomeUnknown, "activation"))
	}
}

// applicationV3HostDetail keeps the application's own detail of a refused or
// unavailable open after the outcome word: one line of at
// most 512 characters, without controls. It is the application's text and
// stays untrusted.
func applicationV3HostDetail(word, detail string) string {
	detail = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return ' '
		}
		return r
	}, detail)
	detail = strings.TrimSpace(detail)
	if runes := []rune(detail); len(runes) > 512 {
		detail = string(runes[:512])
	}
	if detail == "" {
		return word
	}
	return word + ": " + detail
}
