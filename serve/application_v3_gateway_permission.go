package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"runtime"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// This is runtime composition data, never a wire request or a permission
// selection. The shared permission owner decides the exact admitted effect.
type applicationV3ConsentRequest struct {
	caller                                                                         applicationV3Caller
	application, title, program, revision, instance, tool, key, bodyDigest, method string
	class                                                                          appmodel.CommandClass
	context                                                                        *appmodel.ContextRef
	replay                                                                         bool
	recheck                                                                        func(context.Context) error
	// stays keeps an open question pending when the caller's wait ends
	// (architect ruling 2026-11-22): the result is outcome pending with the
	// question id, and an Allow given later performs the parked open.
	stays bool
	// wait bounds a staying question's wait for the answer within the call.
	wait time.Duration
}
type applicationV3ConsentResult struct {
	outcome, reason string
	// question is the open question's id: on pending, and on an Allow.
	question string
	recheck  func(context.Context) error
	close    func()
}

func applicationV3BodyDigest(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// This occurrence has no PermissionRequest and never calls a policy writer.
// The durable receipt owns dispatch-once; Book owns the person's answer.
func (a *runtimeAsks) applicationV3OneCall(ctx context.Context, r applicationV3ConsentRequest) applicationV3ConsentResult {
	refuse := func(outcome, reason string) applicationV3ConsentResult {
		return applicationV3ConsentResult{outcome: outcome, reason: reason}
	}
	if runtime.GOOS != "windows" || a == nil || a.book == nil || a.rights == nil {
		return refuse("unsupported", "presenter")
	}
	// An agent's start of a stopped application is the person's own answer
	// (coordinator, 2026-11-09): Activate asks this once-occurrence for the
	// classes an agent may open by, which no answering program can answer.
	opening := r.method == "Activate" && applicationV3OpenClass(r.class)
	if !applicationV3SensitiveClass(r.class) && !opening {
		return refuse("unavailable", "rights")
	}
	if r.recheck == nil || r.recheck(ctx) != nil || !r.caller.valid(a.rights.owner) {
		return refuse("unavailable", "rights")
	}
	if movedConnectorCaller(r.caller.subject.Program) {
		return refuse("not_permitted", "rights")
	}
	if _, err := applicationV3ReceiptIssued(r.key); err != nil {
		return refuse("invalid", "key")
	}
	deadline, ok := ctx.Deadline()
	if opening && r.stays {
		// A staying question states its own 24-hour expiry below.
		ok = true
	}
	if !ok || len(r.bodyDigest) != 64 {
		return refuse("unavailable", "rights")
	}
	requestKey, code := applicationV3OnceRequestKey(r)
	operation, valid := grants.ApplicationCommandSentence(r.class.String(), r.title)
	if !valid {
		return refuse("unavailable", "rights")
	}
	until := deadline.UTC().Format(time.RFC3339)
	if opening && r.stays {
		// The question stays until the person answers, for at most its
		// lifetime in the Book (architect ruling 2026-11-22).
		until = time.Now().Add(applicationV3OpenQuestionLife).UTC().Format(time.RFC3339)
	}
	if opening {
		// The question asks for one Open. Its code names the caller,
		// application, verb and the exact request body, so a second open of
		// the identical request while the person decides shows the same
		// question and joins it, and an open with another payload asks its
		// own (security review 2026-11-22: a shared handle always names one
		// request).
		operation = "Open " + r.title
		stable, _ := json.Marshal(struct{ Account, Program, Application, Revision, Tool, Class, Method, Body string }{r.caller.subject.Account, r.caller.subject.Program, r.application, r.revision, r.tool, r.class.String(), r.method, r.bodyDigest})
		code = applicationV3BodyDigest(stable)[:20]
	}
	// Reuse the catalogue's bounded slots without truncating the class sentence
	// or captured title. The accepted 256-byte title fits these two fixed slots.
	words := []rune(operation)
	if len(words) > 400 {
		return refuse("unavailable", "rights")
	}
	operationMore := ""
	if len(words) > 200 {
		operation, operationMore = string(words[:200]), string(words[200:])
	}
	var record asks.Record
	var status string
	var err error
	if r.replay {
		record, status, err = a.book.ObserveApplication(a.scope, requestKey)
	} else {
		if opening {
			// Read the pending open question and ask in one step, so two
			// first opens show the person one question: the second takes
			// the first's expiry, its slots match, and the Book joins it.
			a.openAsk.Lock()
			if pending, ok := a.pendingOnce(r.caller.subject.Program, code); ok {
				until = pending
			}
		}
		record, status, err = a.book.AskApplicationAs(a.scope, r.caller.subject.Program, requestKey, asks.ApplicationCommandOnceKey, map[string]string{"program": selfRegisteredSlot(r.caller.subject.Program), "operation": operation, "operationmore": operationMore, "command": selfRegisteredSlot(r.tool), "code": code, "until": until}, listen.Seen{Why: "native application command bound to exact receipt and live ownership"}, requesterPublisher(r.caller.peer)())
		if opening {
			a.openAsk.Unlock()
		}
	}
	if err != nil {
		return refuse("unavailable", "rights")
	}
	if status == "unknown" || status == "gone" || status == "conflict" || record.ID == "" {
		return refuse("unknown", "consent")
	}
	var waitEnd <-chan time.Time
	if opening && r.stays {
		timer := time.NewTimer(max(r.wait, 0))
		defer timer.Stop()
		waitEnd = timer.C
	}
	for record.Pending() {
		if r.replay {
			return refuse("unknown", "consent")
		}
		decided, stopWaiting := a.book.ObserveQuestionWait(record.ID)
		select {
		case <-waitEnd:
			// The caller's wait for the answer ended; the question stays.
			stopWaiting()
			return applicationV3ConsentResult{outcome: "pending", reason: "consent", question: record.ID}
		case <-ctx.Done():
			stopWaiting()
			if opening && r.stays {
				// The caller stopped waiting; the question stays up and a
				// later Allow performs the parked open.
				return applicationV3ConsentResult{outcome: "pending", reason: "consent", question: record.ID}
			}
			if err := a.book.Forget(record.ID); err != nil && a.report != nil {
				a.report(err)
			}
			return refuse("refused", "")
		case <-decided:
			stopWaiting()
		}
		record, ok, err = a.book.ApplicationRecord(record.ID)
		if err != nil {
			return refuse("unavailable", "rights")
		}
		if !ok {
			if opening {
				// Another open's wait ended and took the question with it;
				// the person did not answer.
				return refuse("refused", "gone")
			}
			return refuse("refused", "")
		}
	}
	quiet := func(ctx context.Context) error {
		if !r.caller.valid(a.rights.owner) || r.recheck(ctx) != nil {
			return errors.New("application command: current authority changed")
		}
		found, err := a.book.WithApplicationRecordContext(ctx, record.ID, func(current asks.Record, scope string, _ []string) error {
			if scope != a.scope || current.Key != asks.ApplicationCommandOnceKey || current.AskerProgram != r.caller.subject.Program || current.Pending() || !current.Yes || current.Option != "once" || current.AnsweredBy != asks.AnsweredByOperator || current.Text != record.Text {
				return errors.New("application command: no exact person answer")
			}
			return r.recheck(ctx)
		})
		if err != nil {
			return err
		}
		if !found {
			return errors.New("application command: answer missing")
		}
		return nil
	}
	if !record.Yes || record.Option != "once" || record.AnsweredBy != asks.AnsweredByOperator {
		return refuse("refused", "")
	}
	if err := quiet(ctx); err != nil {
		return refuse("unavailable", "rights")
	}
	result := applicationV3ConsentResult{recheck: quiet}
	if opening {
		result.question = record.ID
	}
	return result
}

func (g *applicationV3Gateway) authorization(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, v applicationV3Invocation, call appmodel.GatewayCall, key string, body []byte, replay bool) applicationV3ConsentResult {
	if call.Tool == ownedBackendStop && isWorkflowApplication(k.manifest.Name) {
		return g.authorizeOwnedBackendStop(ctx, c, k, v, call.Instance)
	}
	if call.RequireReadPermit != nil && *call.RequireReadPermit {
		check := func(ctx context.Context) error {
			return g.quiet(ctx, c, k, "abstraction.facade/command.read")
		}
		if err := check(ctx); err != nil {
			return applicationV3ConsentResult{outcome: "not_permitted", reason: "rights:not_granted"}
		}
		return applicationV3ConsentResult{recheck: check}
	}
	if !v.binding && keptGraphEdit(k.manifest.Name, call.Tool, v.class) {
		return g.graphEditAuthorization(ctx, c, k, v, call, key, body, replay)
	}
	return g.authorizationFor(ctx, c, k, v, call, key, body, replay, "Call")
}

const ownedBackendStop = "backend.stop"

func (g *applicationV3Gateway) authorizeOwnedBackendStop(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, v applicationV3Invocation, instance string) applicationV3ConsentResult {
	s := g.session(k.manifest.Name, instance)
	if !isWorkflowApplication(k.manifest.Name) || v.class != appmodel.CommandClassCommit ||
		v.mode != appmodel.InvokeModeApply || !s.startedFor(c) {
		return applicationV3ConsentResult{outcome: "not_permitted", reason: "start_receipt"}
	}
	check := func(ctx context.Context) error {
		agent, err := g.agentSubject(ctx, c)
		if err != nil || !agent || !c.valid(g.directory.owner) ||
			g.session(k.manifest.Name, instance) != s || s.guard(ctx, s.bindingValue()) != nil {
			return errors.New("start receipt changed")
		}
		decision := g.policy.decide(ctx, c.subject, "abstraction.facade/command.commit", "app:"+k.manifest.Name)
		if !openClassMayProceed(decision.Outcome) {
			return errors.New("rights")
		}
		return k.recheck(ctx)
	}
	if err := check(ctx); err != nil {
		return applicationV3ConsentResult{outcome: "not_permitted", reason: "start_receipt"}
	}
	return applicationV3ConsentResult{recheck: check}
}

func (g *applicationV3Gateway) authorizationFor(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, v applicationV3Invocation, call appmodel.GatewayCall, key string, body []byte, replay bool, method string) applicationV3ConsentResult {
	action := "abstraction.facade/command." + v.class.String()
	person := g.personOnly(ctx, c, v.class)
	// locked is the recheck the Asks Book runs while it holds its lock (the
	// admission's authorize and the once-answer's record read), so it reads
	// no Book-backed state. The kept-agent check reads the agent's
	// enrollment answer from the Book; it runs beside the locked recheck,
	// never inside it, and a person-only occurrence needs none.
	locked := func(ctx context.Context) error {
		if !c.valid(g.directory.owner) {
			return errors.New("caller")
		}
		if err := k.recheck(ctx); err != nil {
			return err
		}
		decision := g.policy.decide(ctx, c.subject, action, "app:"+k.manifest.Name)
		if decision.Outcome == rights.DecisionOutcomeDenied || decision.Outcome == rights.DecisionOutcomeUnavailable {
			return errors.New("rights")
		}
		return ctx.Err()
	}
	enrolled := func(ctx context.Context) error {
		if person || !applicationV3StandingClass(v.class) {
			return nil
		}
		agent, err := g.agentSubject(ctx, c)
		if err != nil {
			return err
		}
		if !agent {
			return errors.New("agent enrollment changed")
		}
		return nil
	}
	current := func(ctx context.Context) error {
		if err := locked(ctx); err != nil {
			return err
		}
		return enrolled(ctx)
	}
	if err := current(ctx); err != nil {
		return applicationV3ConsentResult{outcome: "unavailable", reason: "rights"}
	}
	r := applicationV3ConsentRequest{caller: c, application: k.manifest.Name, title: k.manifest.Title, program: k.program, revision: k.revision, instance: call.Instance, tool: call.Tool, key: key, bodyDigest: applicationV3BodyDigest(body), method: method, class: v.class, context: v.context, replay: replay, recheck: locked}
	if person {
		return g.asks.applicationV3OneCall(ctx, r)
	}
	if decision := g.policy.decide(ctx, c.subject, action, "app:"+k.manifest.Name); decision.Outcome == rights.DecisionOutcomePermitted {
		return applicationV3ConsentResult{recheck: func(ctx context.Context) error {
			if err := current(ctx); err != nil {
				return err
			}
			return g.quiet(ctx, c, k, action)
		}}
	}
	if g.consent == nil {
		return applicationV3ConsentResult{outcome: "unsupported", reason: "presenter"}
	}
	result := g.consent(ctx, r)
	if result.outcome == "" && result.recheck == nil {
		return applicationV3ConsentResult{outcome: "unavailable", reason: "rights"}
	}
	if answered := result.recheck; result.outcome == "" {
		result.recheck = func(ctx context.Context) error {
			if err := enrolled(ctx); err != nil {
				return err
			}
			return answered(ctx)
		}
	}
	return result
}

// applicationV3OnceRequestKey is the once-occurrence's admission key for one
// exact call and its default question code.
func applicationV3OnceRequestKey(r applicationV3ConsentRequest) (string, string) {
	contextName, epoch := "", ""
	if r.context != nil {
		contextName = r.context.Context
		epoch = r.context.Epoch
	}
	bound, _ := json.Marshal(struct{ Account, Program, Application, Revision, Instance, Context, Epoch, Tool, Class, Method, Key, Body string }{r.caller.subject.Account, r.caller.subject.Program, r.application, r.revision, r.instance, contextName, epoch, r.tool, r.class.String(), r.method, r.key, r.bodyDigest})
	digest := applicationV3BodyDigest(bound)
	return "application-command:" + digest, digest[:20]
}

// applicationV3OpenClass is a verb class an agent may open a kept
// application by (FAC-M32): its start is the person's once-answer.
func applicationV3OpenClass(class appmodel.CommandClass) bool {
	return class == appmodel.CommandClassRead || class == appmodel.CommandClassReveal || class == appmodel.CommandClassPropose
}

// pendingOnce is the expiry of program's pending open question with code,
// so a joining open shows the identical question.
func (a *runtimeAsks) pendingOnce(program, code string) (string, bool) {
	rows, _, err := a.book.ApplicationHistory()
	if err != nil {
		return "", false
	}
	for _, r := range rows {
		if r.Pending() && r.Key == asks.ApplicationCommandOnceKey && r.AskerProgram == program && r.Content != nil && r.Content.Slots["code"] == code {
			return r.Content.Slots["until"], r.Content.Slots["until"] != ""
		}
	}
	return "", false
}

func applicationV3SensitiveClass(class appmodel.CommandClass) bool {
	switch class {
	case appmodel.CommandClassEdit, appmodel.CommandClassCommit, appmodel.CommandClassSpend, appmodel.CommandClassDestroy, appmodel.CommandClassSend:
		return true
	}
	return false
}

func (g *applicationV3Gateway) personOnly(ctx context.Context, c applicationV3Caller, class appmodel.CommandClass) bool {
	if class == appmodel.CommandClassDestroy || class == appmodel.CommandClassSend {
		return true
	}
	if !applicationV3StandingClass(class) {
		return false
	}
	agent, err := g.agentSubject(ctx, c)
	return err != nil || !agent
}

func applicationV3StandingClass(class appmodel.CommandClass) bool {
	return class == appmodel.CommandClassEdit || class == appmodel.CommandClassCommit || class == appmodel.CommandClassSpend
}

// P-J15's directory owner supplies person-Keep classification. Missing evidence
// retains shared-gateway treatment; program names and answerer enrollment never
// replace it. Quiet authorization asks this owner again before use or replay.
func (g *applicationV3Gateway) agentSubject(ctx context.Context, c applicationV3Caller) (bool, error) {
	owner, ok := any(g.directory).(interface {
		applicationV3AgentSubject(context.Context, *identity.Peer) (bool, error)
	})
	if !ok {
		return false, nil
	}
	return owner.applicationV3AgentSubject(ctx, c.peer)
}

func (g *applicationV3Gateway) bundleConsent(ctx context.Context, r applicationV3ConsentRequest) applicationV3ConsentResult {
	if g.asks == nil || r.recheck == nil {
		return applicationV3ConsentResult{outcome: "unsupported", reason: "presenter"}
	}
	owner, ok := any(g.asks).(interface {
		applicationPermissionConsent(context.Context, *identity.Peer, string, string, string, string, bool, func(context.Context) error) (string, string, func(context.Context) error)
	})
	if !ok {
		return applicationV3ConsentResult{outcome: "unsupported", reason: "presenter"}
	}
	key := r.key
	if r.method == "Describe" || r.method == "Commands" {
		if r.replay {
			return applicationV3ConsentResult{outcome: "unavailable", reason: "consent"}
		}
		var err error
		key, err = applicationToken()
		if err != nil {
			return applicationV3ConsentResult{outcome: "unavailable", reason: "consent"}
		}
	} else if r.method != "Call" && r.method != "Activate" {
		return applicationV3ConsentResult{outcome: "unavailable", reason: "consent"}
	}
	if key == "" {
		return applicationV3ConsentResult{outcome: "invalid", reason: "key"}
	}
	scoped := applicationV3ReceiptID(applicationV3ReceiptScope{Account: r.caller.subject.Account, Program: r.caller.subject.Program, Method: r.method}, key)
	outcome, reason, quiet := owner.applicationPermissionConsent(ctx, r.caller.peer, "app:"+r.application, r.title, r.class.String(), scoped, r.replay, func(ctx context.Context) error { return r.recheck(ctx) })
	if outcome == "permitted" {
		outcome = ""
	}
	return applicationV3ConsentResult{outcome: outcome, reason: reason, recheck: quiet}
}
