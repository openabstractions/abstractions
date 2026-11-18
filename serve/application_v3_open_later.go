package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	client "github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The open question stays until answered (architect ruling 2026-11-22,
// APPLICATIONS.md "The open question stays until answered"). An agent's open
// of a stopped application asks the person "Open <app>?". When the call's
// wait ends first, the question stays up and the call answers not_ready with
// reason consent, the question id and an open handle. The open is parked
// here, bound to the exact request: the requester's enrollment and image,
// the target's Keep identity, manifest and revision, the verb, its class and
// payload. An Allow given later performs that open once, as the requester,
// after every check runs again. Its result is an Activity row and a note to
// the requester naming the handle. An expired, withdrawn or refused question
// performs nothing.

// applicationV3OpenQuestionLife is how long an open question stays pending
// in the Book: the messages default of 24 hours. Tests shorten it.
var applicationV3OpenQuestionLife = 24 * time.Hour

// keepOpenQuestions gives the Book's open questions their 24-hour lifetime.
// The once-occurrence key also carries the person-only command questions;
// those are withdrawn when their caller stops waiting, so the longer lifetime
// keeps only the open questions longer.
func keepOpenQuestions(book *asks.Book) error {
	return book.SetKeyLifetime(asks.ApplicationCommandOnceKey, applicationV3OpenQuestionLife)
}

// openLaterEvent is the runtime log event of a deferred open's result, the
// Activity row the Panel reads (monitor activity_elevated.go).
const openLaterEvent = "applications.open.later"

// applicationV3PendingOpen is one open captured before its wait, bound to
// the exact original request.
type applicationV3PendingOpen struct {
	question, handle, text string
	asked                  time.Time
	// The requester: its proven subject, its captured peer and desktop
	// session, and its agent enrollment with the image it was admitted by.
	subject rights.Subject
	peer    *identity.Peer
	desktop string
	agent   applicationAgentAdmission
	// The target: its kept identity and the exact verb and payload.
	application, title, keepIdentity, revision, digest, program string
	request                                                     applications.ActivateRequest
	class                                                       appmodel.CommandClass
}

// applicationV3OpenOutcome is a deferred open's result for its requester.
type applicationV3OpenOutcome struct {
	Question, Handle, Requester, Application, Title string
	Result                                          applications.ActivationResult
}

// applicationV3OpenLater holds the parked opens, the occurrences already
// used and each open handle's state.
type applicationV3OpenLater struct {
	mu      sync.Mutex
	pending map[string]*applicationV3PendingOpen
	used    map[string]time.Time
	handles map[string]*applicationV3HandleRecord
	stop    chan struct{}
	stopped bool
}

// applicationV3HandleRecord is one open handle's state, readable by the
// caller it was given to (Describe with open_handle).
type applicationV3HandleRecord struct {
	account, program, application, question string
	state                                   appmodel.OpenHandleState
	instance, reason                        string
	at                                      time.Time
}

// openHandleLimit bounds the retained handle states; the oldest leaves first.
const openHandleLimit = 256

// recordLocked keeps a new handle's pending state. Callers hold l.mu.
func (l *applicationV3OpenLater) recordLocked(handle string, p *applicationV3PendingOpen) {
	now := time.Now()
	if l.handles == nil {
		l.handles = map[string]*applicationV3HandleRecord{}
	}
	for id, r := range l.handles {
		if now.Sub(r.at) > applicationV3OpenQuestionLife {
			delete(l.handles, id)
		}
	}
	for len(l.handles) >= openHandleLimit {
		oldest := ""
		for id, r := range l.handles {
			if oldest == "" || r.at.Before(l.handles[oldest].at) {
				oldest = id
			}
		}
		delete(l.handles, oldest)
	}
	l.handles[handle] = &applicationV3HandleRecord{account: p.subject.Account, program: p.subject.Program, application: p.application,
		question: p.question, state: appmodel.OpenHandleStatePending, at: now}
}

// settle moves a pending handle to its final state once.
func (l *applicationV3OpenLater) settle(handle string, state appmodel.OpenHandleState, instance, reason string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.handles[handle]
	if r == nil || r.state != appmodel.OpenHandleStatePending {
		return
	}
	r.state, r.instance, r.reason, r.at = state, instance, reason, time.Now()
}

// finish records an open's answer on its handle: opened with the instance,
// or refused with the outcome and reason.
func (l *applicationV3OpenLater) finish(handle string, result applications.ActivationResult) {
	if result.Outcome == applications.ActivationOutcomeReady {
		l.settle(handle, appmodel.OpenHandleStateOpened, result.Instance, "")
		return
	}
	reason := result.Outcome.String()
	if result.Reason != "" {
		reason += ":" + result.Reason
	}
	if runes := []rune(reason); len(runes) > 512 {
		reason = string(runes[:512])
	}
	l.settle(handle, appmodel.OpenHandleStateRefused, "", reason)
}

// state is the handle's state for the caller it was given to.
func (l *applicationV3OpenLater) state(handle string, subject rights.Subject) (appmodel.OpenHandle, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	r := l.handles[handle]
	if r == nil || r.account != subject.Account || r.program != subject.Program {
		return appmodel.OpenHandle{}, false
	}
	return appmodel.OpenHandle{Handle: handle, Application: r.application, State: r.state, Instance: r.instance, Reason: r.reason}, true
}

func (l *applicationV3OpenLater) stopChannel() chan struct{} {
	if l.stop == nil {
		l.stop = make(chan struct{})
	}
	return l.stop
}

// close ends every follower; a parked open performs nothing afterwards.
func (l *applicationV3OpenLater) close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.stopped {
		close(l.stopChannel())
		l.stopped = true
	}
}

// claim marks the occurrence question used and reports whether this caller
// is the first. An empty question is no open occurrence and always claims.
func (l *applicationV3OpenLater) claim(question string) bool {
	if question == "" {
		return true
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	for id, at := range l.used {
		if now.Sub(at) > applicationV3OpenQuestionLife+time.Hour {
			delete(l.used, id)
		}
	}
	if _, done := l.used[question]; done {
		return false
	}
	if l.used == nil {
		l.used = map[string]time.Time{}
	}
	l.used[question] = now
	return true
}

func (l *applicationV3OpenLater) handleOf(question string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	if question == "" {
		return ""
	}
	for handle, r := range l.handles {
		if r.question == question {
			return handle
		}
	}
	return ""
}

func (l *applicationV3OpenLater) drop(question string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.pending, question)
}

// prepareOpenLater captures an agent's open before its wait, or nil when the
// open cannot outlive the call: no Book, no current agent enrollment or no
// verified desktop session. A nil capture keeps the earlier rule, under which
// the caller's leaving withdraws the question.
func (g *applicationV3Gateway) prepareOpenLater(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, request applications.ActivateRequest, class appmodel.CommandClass) *applicationV3PendingOpen {
	if g.asks == nil || g.asks.book == nil || g.directory == nil || k.keepIdentity == "" {
		return nil
	}
	agent, ok, err := g.directory.captureApplicationAgent(ctx, c.peer, "")
	if err != nil || !ok || agent.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
		return nil
	}
	desktop, ok := applicationSession(c.peer)
	if !ok {
		return nil
	}
	return &applicationV3PendingOpen{subject: c.subject, peer: c.peer, desktop: desktop, agent: agent,
		application: k.manifest.Name, title: k.manifest.Title, keepIdentity: k.keepIdentity, revision: k.revision, digest: k.digest, program: k.program,
		request: request, class: class}
}

// parkOpen keeps p for its question and gives the waiting call the question
// id and the open handle in parked. A second open of the same question shares
// the first open's handle: the Allow performs the open the question was asked
// for. A question answered between the end of the wait and this park is
// parked too, so the follower acts on that answer. A question that cannot be
// parked is withdrawn: no Allow is left that would perform nothing.
func (g *applicationV3Gateway) parkOpen(p *applicationV3PendingOpen, question string, parked *applicationV3Parked) applications.ActivationResult {
	waiting := applicationV3Activation(applications.ActivationOutcomeNotReady, "consent")
	record, found, err := g.asks.book.ApplicationRecord(question)
	if err != nil || !found || record.Key != asks.ApplicationCommandOnceKey || record.AskerProgram != p.subject.Program {
		return waiting
	}
	withdraw := func() applications.ActivationResult {
		if err := g.asks.book.Forget(question); err != nil && g.report != nil {
			g.report(err)
		}
		return waiting
	}
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return withdraw()
	}
	g.later.mu.Lock()
	if g.later.stopped {
		g.later.mu.Unlock()
		return withdraw()
	}
	if existing := g.later.pending[question]; existing != nil {
		g.later.mu.Unlock()
		parked.question, parked.handle = question, existing.handle
		return waiting
	}
	p.question, p.handle, p.text, p.asked = question, hex.EncodeToString(raw), record.Text, record.Asked
	if g.later.pending == nil {
		g.later.pending = map[string]*applicationV3PendingOpen{}
	}
	g.later.pending[question] = p
	g.later.recordLocked(p.handle, p)
	stop := g.later.stopChannel()
	g.later.mu.Unlock()
	go g.followOpen(p, stop)
	parked.question, parked.handle = question, p.handle
	return waiting
}

// openTaken answers a call whose Allow a parked open of the same question
// already used.
func (g *applicationV3Gateway) openTaken(question string, parked *applicationV3Parked) applications.ActivationResult {
	result := applicationV3Activation(applications.ActivationOutcomeNotReady, "consent")
	if handle := g.later.handleOf(question); handle != "" {
		parked.question, parked.handle = question, handle
	}
	return result
}

// followOpen waits for the person's answer to p's question. It ends with
// nothing performed when the question expires, is withdrawn or refused, when
// the requester's enrollment ends, or when the runtime stops. An Allow
// performs the open once.
func (g *applicationV3Gateway) followOpen(p *applicationV3PendingOpen, stop <-chan struct{}) {
	defer g.later.drop(p.question)
	deadline := p.asked.Add(applicationV3OpenQuestionLife)
	expiry := time.NewTimer(time.Until(deadline))
	defer expiry.Stop()
	withdraw := func(reason string) {
		if err := g.asks.book.Forget(p.question); err != nil && g.report != nil {
			g.report(err)
		}
		g.later.settle(p.handle, appmodel.OpenHandleStateExpired, "", reason)
	}
	for {
		decided := g.asks.book.Decided(p.question)
		changed := g.directoryChanged()
		select {
		case <-stop:
			return
		case <-expiry.C:
			withdraw("expired")
			return
		case <-changed:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			err := g.openRequesterCurrent(ctx, p)
			cancel()
			if err != nil {
				// The requester's enrollment ended: the question goes with it.
				withdraw("enrollment")
				return
			}
			continue
		case <-decided:
		}
		record, found, err := g.asks.book.ApplicationRecord(p.question)
		if err != nil {
			if g.report != nil {
				g.report(err)
			}
			return
		}
		if !found {
			g.later.settle(p.handle, appmodel.OpenHandleStateExpired, "", "withdrawn")
			return
		}
		if record.Pending() {
			continue
		}
		if !time.Now().Before(deadline) || !record.Answered.Before(deadline) {
			g.later.settle(p.handle, appmodel.OpenHandleStateExpired, "", "expired")
			return
		}
		if !record.Yes || record.Option != "once" || record.AnsweredBy != asks.AnsweredByOperator ||
			record.Key != asks.ApplicationCommandOnceKey || record.AskerProgram != p.subject.Program || record.Text != p.text {
			g.later.settle(p.handle, appmodel.OpenHandleStateRefused, "", "consent")
			return
		}
		if !g.later.claim(p.question) {
			// A call still waiting on the question used the Allow; its
			// answer settles the handle.
			return
		}
		result := g.performOpenLater(p)
		g.later.finish(p.handle, result)
		g.deliverOpenLater(p, result)
		return
	}
}

// directoryChanged is the directory's current change signal, or nil.
func (g *applicationV3Gateway) directoryChanged() <-chan struct{} {
	if g.directory == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !g.directory.lock(ctx) {
		return nil
	}
	defer g.directory.unlock()
	return g.directory.changed
}

// openRequesterCurrent checks that the requester is still the enrolled agent
// the open was captured for: the same enrollment revision, program and image.
func (g *applicationV3Gateway) openRequesterCurrent(ctx context.Context, p *applicationV3PendingOpen) error {
	var image applicationAgentImage
	current := g.directory.currentAgentEnrollmentImage(ctx, p.agent.Enrollment.Application, &image)
	if current.Enrollment == nil || *current.Enrollment != p.agent.Enrollment || current.Enrollment.State != applications.AgentEnrollmentStateEnrolled ||
		image.Key != p.agent.Image.Key || image.Program != p.agent.Image.Program {
		return errors.New("open later: requester enrollment changed")
	}
	return nil
}

// openLaterRights checks that no Block or Never stands for the requester on
// the target: no stored deny on application.activate or on the verb's
// class, and the class is still one an agent opens by.
func (g *applicationV3Gateway) openLaterRights(ctx context.Context, p *applicationV3PendingOpen) error {
	if !applicationV3OpenClass(p.class) {
		return errors.New("open later: class")
	}
	for _, action := range []string{ActionApplicationActivate, "abstraction.facade/command." + p.class.String()} {
		if !openClassMayProceed(g.policy.decide(ctx, p.subject, action, "app:"+p.application).Outcome) {
			return errors.New("open later: blocked")
		}
	}
	return nil
}

// openLaterTarget loads the target and checks it is the application the
// question named: its Keep identity, manifest, revision, program and the
// verb's class. A removed or changed target is not opened.
func (g *applicationV3Gateway) openLaterTarget(ctx context.Context, p *applicationV3PendingOpen) (*applicationV3Kept, error) {
	k, err := g.load(ctx, p.peer, p.application)
	if err != nil {
		return nil, errors.New("open later: target removed")
	}
	same := k.manifest.Name == p.application && k.keepIdentity == p.keepIdentity && k.digest == p.digest && k.revision == p.revision && k.program == p.program
	if same {
		same = false
		for _, verb := range k.manifest.Verbs {
			if verb.Name == p.request.Verb && verb.Class == p.class {
				same = true
			}
		}
	}
	if !same {
		k.close()
		return nil, errors.New("open later: target changed")
	}
	if err := k.recheck(ctx); err != nil {
		k.close()
		return nil, errors.New("open later: target changed")
	}
	return k, nil
}

// performOpenLater performs p's open as the requester after every check runs
// again. An instance already running answers ready with no verb delivery, as
// an agent's open of a running instance does.
func (g *applicationV3Gateway) performOpenLater(p *applicationV3PendingOpen) applications.ActivationResult {
	refuse := applicationV3Activation
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := g.openRequesterCurrent(ctx, p); err != nil {
		return refuse(applications.ActivationOutcomeForbidden, "rights")
	}
	if err := g.openLaterRights(ctx, p); err != nil {
		return refuse(applications.ActivationOutcomeForbidden, "blocked")
	}
	k, err := g.openLaterTarget(ctx, p)
	if err != nil {
		return refuse(applications.ActivationOutcomeUnavailable, "manifest")
	}
	defer k.close()
	guard := func(ctx context.Context) error {
		return errors.Join(g.openRequesterCurrent(ctx, p), g.openLaterRights(ctx, p), k.recheck(ctx))
	}
	budget, end := context.WithTimeout(ctx, time.Duration(k.manifest.Host.ReadinessDeadlineMs)*time.Millisecond)
	defer end()
	s, _, err := g.activationSession(budget, k, p.request.Context)
	if err != nil {
		return refuse(applications.ActivationOutcomeUnavailable, err.Error())
	}
	if s != nil {
		select {
		case <-s.initialized:
			if instance := s.bindingValue().instance; instance != "" && guard(budget) == nil {
				return applications.ActivationResult{Outcome: applications.ActivationOutcomeReady, Instance: instance}
			}
		default:
		}
	}
	started := false
	if s == nil {
		if guard(budget) != nil {
			return refuse(applications.ActivationOutcomeForbidden, "rights")
		}
		s, started, err = g.beginOwnedApplicationAs(budget, p.subject.Account, func() (string, bool) { return p.desktop, p.desktop != "" }, k, p.subject.Program)
		if err != nil {
			if g.report != nil {
				g.report(errors.New("applications@3: deferred open of " + k.manifest.Name + ": " + err.Error()))
			}
			if errors.Is(err, errOwnedApplicationUncertain) {
				return refuse(applications.ActivationOutcomeUnknown, "owned_host")
			}
			return refuse(applications.ActivationOutcomeIdentityRefused, "owned_host")
		}
		if started {
			if err := s.initialize(budget, "2026-07-28"); err != nil {
				return refuse(applications.ActivationOutcomeUnknown, "initialize")
			}
		}
	}
	if err := g.waitOwnedPresence(budget, k, s); err != nil {
		return refuse(applications.ActivationOutcomeNotReady, "owned_host")
	}
	select {
	case <-s.initialized:
	case <-s.done:
		return refuse(applications.ActivationOutcomeUnknown, "initialize")
	case <-budget.Done():
		return refuse(applications.ActivationOutcomeNotReady, "initialize")
	}
	call := appmodel.GatewayCall{Application: p.application, Tool: p.request.Verb, Arguments: appmodel.Raw(p.request.Payload), Instance: s.bindingValue().instance}
	text, err := s.call(budget, &call, func(raw []byte) error { _, err := client.ParseHostActivationResult(raw); return err },
		func(ctx context.Context, _ ownedApplicationBinding, _ *appmodel.GatewayCall) error { return guard(ctx) })
	if err != nil {
		return refuse(applications.ActivationOutcomeUnknown, "activation")
	}
	result, err := client.ParseHostActivationResult(text)
	if err != nil {
		return refuse(applications.ActivationOutcomeUnknown, "activation")
	}
	switch result.Outcome {
	case applications.HostActivationOutcomeApplied:
		return applications.ActivationResult{Outcome: applications.ActivationOutcomeReady, Instance: call.Instance, Started: started}
	case applications.HostActivationOutcomeRefused:
		return refuse(applications.ActivationOutcomeNotReady, applicationV3HostDetail("activation:refused", result.Reason))
	case applications.HostActivationOutcomeUnavailable:
		return refuse(applications.ActivationOutcomeUnavailable, applicationV3HostDetail("activation:unavailable", result.Reason))
	}
	return refuse(applications.ActivationOutcomeUnknown, "activation")
}

// deliverOpenLater records the deferred open's result: one Activity row in
// the runtime's own log, attributed to the requester, and the note to the
// requester naming the handle.
func (g *applicationV3Gateway) deliverOpenLater(p *applicationV3PendingOpen, result applications.ActivationResult) {
	result.Title = p.title
	outcome := applicationV3OpenOutcome{Question: p.question, Handle: p.handle, Requester: p.subject.Program, Application: p.application, Title: p.title, Result: result}
	if g.policy != nil {
		g.policy.elevated.mu.Lock()
		sink := g.policy.elevated.logs
		g.policy.elevated.mu.Unlock()
		if sink != nil {
			if err := sink.Write(openLaterRecord(time.Now(), outcome)); err != nil && g.report != nil {
				g.report(err)
			}
		}
	}
	if g.noteOpen != nil {
		g.noteOpen(outcome)
	}
}

// openLaterRecord is the Activity row of one deferred open.
func openLaterRecord(at time.Time, o applicationV3OpenOutcome) logging.Record {
	return logging.Record{Schema: logging.SchemaVersion, Time: logging.At(at), Level: logging.LevelInfo, Msg: "Opened an app after the person's Allow",
		Attrs: map[string]string{"oa_event": openLaterEvent, "program": o.Requester, "application": o.Application, "title": o.Title,
			"outcome": o.Result.Outcome.String(), "reason": o.Result.Reason, "question": o.Question, "handle": o.Handle}}
}
