package main

import (
	"context"
	"errors"
	"fmt"
	registrationwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"strings"
	"sync"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstraction-facade/go/appmodelcall"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// A kept declaration copy comes from the existing registration owner. No caller
// can supply a manifest, process binding, or these composition functions.
type applicationV3Kept struct {
	manifest                  *registrationwire.Manifest
	program, revision, digest string
	keepIdentity              string
	state                     string
	live                      map[string]bool
	recheck                   func(context.Context) error
	close                     func() error
}

type applicationV3Gateway struct {
	mu         sync.Mutex
	directory  *applicationDirectory
	receipts   *applicationV3Receipts
	policy     *runtimeRights
	asks       *runtimeAsks
	epoch      string
	owned      map[string]*ownedApplicationSession
	starting   map[string]chan struct{}
	start      func(context.Context, ownedApplicationImage, ownedApplicationBinding, []string, func(context.Context, ownedApplicationBinding) error) (*ownedApplicationSession, error)
	load       func(context.Context, *identity.Peer, string) (*applicationV3Kept, error)
	list       func(context.Context, *identity.Peer) ([]*applicationV3Kept, error)
	consent    func(context.Context, applicationV3ConsentRequest) applicationV3ConsentResult
	audit      func(context.Context, appmodel.CallRecord, string) error
	auditCheck func(appmodel.CallRecord, string) error
	// report logs one bounded line for a failed owned start: the
	// application name and the error, never arguments or payloads.
	report func(error)
	// later holds agents' opens whose question outlived the call
	// (application_v3_open_later.go).
	later applicationV3OpenLater
	// noteOpen receives a deferred open's result for its requester.
	noteOpen func(applicationV3OpenOutcome)
}

type applicationV3Caller struct {
	subject rights.Subject
	peer    *identity.Peer
	recheck func() error
	matcher ownedApplicationRequestMatcher
}

func (c applicationV3Caller) valid(owner string) bool {
	return c.subject.Account == owner && c.subject.Program != "" && c.peer != nil && c.recheck != nil && c.recheck() == nil
}

func (g *applicationV3Gateway) session(application, instance string) *ownedApplicationSession {
	g.mu.Lock()
	defer g.mu.Unlock()
	for _, s := range g.owned {
		b := s.bindingValue()
		if b.instance == instance && b.application == application && b.runtimeEpoch == g.epoch {
			select {
			case <-s.done:
				continue
			default:
				return s
			}
		}
	}
	return nil
}

func (g *applicationV3Gateway) invalidate(application, revision string) {
	var retired []*ownedApplicationSession
	g.mu.Lock()
	for key, s := range g.owned {
		b := s.bindingValue()
		if b.application == application && b.manifestRevision == revision {
			delete(g.owned, key)
			retired = append(retired, s)
		}
	}
	g.mu.Unlock()
	for _, s := range retired {
		s.Close()
	}
}

// Called only after this listener's workers joined. Closing mediation never
// terminates an application process or independently accepted application job.
func (g *applicationV3Gateway) close() error {
	g.later.close()
	g.mu.Lock()
	sessions := g.owned
	g.owned = map[string]*ownedApplicationSession{}
	g.mu.Unlock()
	var err error
	for _, s := range sessions {
		err = errors.Join(err, s.Close())
	}
	if g.receipts != nil {
		err = errors.Join(err, g.receipts.ownerClosed(context.Background(), g.receipts.owner))
	}
	return err
}

func (g *applicationV3Gateway) quiet(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, action string) error {
	if !c.valid(g.directory.owner) || ctx.Err() != nil {
		return errors.New("caller")
	}
	if err := k.recheck(ctx); err != nil {
		return err
	}
	d := g.policy.decide(ctx, c.subject, action, "app:"+k.manifest.Name)
	if d.Outcome != rights.DecisionOutcomePermitted {
		return errors.New("rights")
	}
	return c.recheck()
}

func (g *applicationV3Gateway) permission(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, class appmodel.CommandClass, ask bool, method string) (string, string) {
	action := "abstraction.facade/command." + class.String()
	d := g.policy.decide(ctx, c.subject, action, "app:"+k.manifest.Name)
	switch d.Outcome {
	case rights.DecisionOutcomeDenied:
		return "not_permitted", ""
	case rights.DecisionOutcomeUnavailable:
		return "unavailable", "rights"
	}
	// FAC-M20 requires a fresh person-only occurrence even under a permit.
	// Persistent first-use permission is never used to substitute for it.
	if class == appmodel.CommandClassDestroy || class == appmodel.CommandClassSend {
		return "unsupported", "presenter"
	}
	if d.Outcome == rights.DecisionOutcomePermitted {
		return "", ""
	}
	if !ask {
		return "not_permitted", ""
	}
	if g.consent == nil {
		return "unsupported", "presenter"
	}
	result := g.consent(ctx, applicationV3ConsentRequest{caller: c, application: k.manifest.Name, title: k.manifest.Title, program: k.program, revision: k.revision, class: class, method: method, recheck: func(ctx context.Context) error {
		if !c.valid(g.directory.owner) {
			return errors.New("caller")
		}
		if err := k.recheck(ctx); err != nil {
			return err
		}
		d := g.policy.decide(ctx, c.subject, action, "app:"+k.manifest.Name)
		if d.Outcome == rights.DecisionOutcomeDenied || d.Outcome == rights.DecisionOutcomeUnavailable {
			return errors.New("rights")
		}
		return ctx.Err()
	}})
	if result.outcome != "" {
		return result.outcome, result.reason
	}
	if result.recheck == nil || result.recheck(ctx) != nil {
		return "unavailable", "rights"
	}
	if err := g.quiet(ctx, c, k, action); err != nil {
		return "unavailable", "rights"
	}
	return "", ""
}

func (g *applicationV3Gateway) call(ctx context.Context, c applicationV3Caller, key string, call appmodel.GatewayCall) (returned applications.CallResult) {
	record := appmodel.CallRecord{Subject: c.subject.Program, Application: call.Application, Instance: call.Instance, Command: call.Tool, ArgumentsDigest: "sha256:" + applicationV3BodyDigest([]byte(call.Arguments))}
	regarding, gatewayReason := "", ""
	var appliedGraph *applications.CallResult
	defer func() {
		if appliedGraph != nil {
			applicationV3AuditAnswer(&record, *appliedGraph)
		} else {
			applicationV3AuditAnswer(&record, returned)
			record.Reason = gatewayReason
			if record.Reason == "" && returned.Outcome != applications.OutcomeApplied {
				record.Reason = returned.Reason
			}
		}
		if g.audit != nil {
			auditCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
			defer cancel()
			if err := g.audit(auditCtx, record, regarding); err != nil {
				returned = applications.CallResult{Outcome: applications.OutcomeUnknown, Reason: "audit"}
			}
		}
	}()
	if g.audit == nil {
		return applications.CallResult{Outcome: applications.OutcomeUnavailable, Reason: "audit"}
	}
	persist := func(result applications.CallResult) applications.CallResult { return result }
	refuse := func(outcome, reason string, v applicationV3Invocation) applications.CallResult {
		gatewayReason = reason
		answer := applicationV3Refusal(call.Tool, outcome, reason, v.context)
		if answer.Result != nil && !v.binding {
			answer.Result.IdempotencyKey = v.key
		}
		return persist(applications.CallResult{Outcome: applications.OutcomeApplied, Answer: answer})
	}
	v, err := decodeApplicationV3Invocation(call)
	if v.binding {
		class := v.class
		record.Class = &class
	}
	if err != nil {
		return refuse("invalid", applicationV3CodecWord(err), v)
	}
	if call.RequireReadPermit != nil && call.Tool != "app_read" {
		return refuse("invalid", "require_read_permit", v)
	}
	record.Context = v.context
	record.IdempotencyKey = v.key
	record.RevisionBefore = v.revisionBefore
	if !c.valid(g.directory.owner) {
		return applications.CallResult{Outcome: applications.OutcomeForbidden, Reason: "caller"}
	}
	k, err := g.load(ctx, c.peer, call.Application)
	if err != nil {
		if errors.Is(err, errApplicationV3NotFound) {
			return refuse("not_found", "application:"+call.Application, v)
		}
		return refuse("unavailable", "manifest", v)
	}
	defer k.close()
	call.Application = k.manifest.Name
	record.Application = call.Application
	regarding = k.program
	if !v.binding {
		found := false
		for _, decl := range k.manifest.Offers.Commands {
			if decl.Name == call.Tool {
				v.class = decl.Class
				found = true
				break
			}
		}
		if !found {
			return refuse("forbidden", "manifest:"+call.Tool, v)
		}
		v.mode = appmodelcall.Mode(&call, v.class)
		if v.mode == appmodel.InvokeModePropose {
			v.class = appmodel.CommandClassPropose
		}
		class := v.class
		record.Class = &class
	}
	// Stored denial precedes liveness and all application round trips.
	action := "abstraction.facade/command." + v.class.String()
	decision := g.policy.decide(ctx, c.subject, action, "app:"+call.Application)
	if decision.Outcome == rights.DecisionOutcomeDenied {
		return refuse("not_permitted", "", v)
	}
	if decision.Outcome == rights.DecisionOutcomeUnavailable {
		return refuse("unavailable", "rights", v)
	}
	if !k.live[call.Instance] {
		return refuse("stale", "instance:"+call.Instance, v)
	}
	canonical, err := appmodel.ToJSON(&call)
	if err != nil {
		return refuse("invalid", applicationV3CodecWord(err), v)
	}
	binding := applicationV3ReceiptBinding{Application: call.Application, Manifest: k.digest, Revision: k.revision, RuntimeGeneration: g.epoch, Instance: call.Instance, InnerKey: v.key}
	if v.context != nil {
		binding.InstanceEpoch = v.context.Epoch
	}
	previous, token, err := g.receipts.admit(ctx, applicationV3ReceiptRequest{Scope: applicationV3ReceiptScope{Account: c.subject.Account, Program: c.subject.Program, Method: "Call"}, Key: key, Binding: binding, CanonicalBody: canonical})
	if err != nil {
		if errors.Is(err, errApplicationV3ReceiptConflict) {
			return applications.CallResult{Outcome: applications.OutcomeConflict, Reason: "key"}
		}
		return applications.CallResult{Outcome: applications.OutcomeUnavailable, Reason: "receipt"}
	}
	if token == nil && previous.State != "complete" {
		return applications.CallResult{Outcome: applications.OutcomeUnknown, Reason: "receipt"}
	}
	if token != nil && !v.binding && v.key == "" {
		// A call without a key gets a fresh one, echoed in its answer; the
		// receipt replays that answer for the same outer key (FAC-M32).
		if v.key, err = appmodelcall.NewKey(); err != nil {
			return applications.CallResult{Outcome: applications.OutcomeUnavailable, Reason: "key"}
		}
		record.IdempotencyKey = v.key
	}
	if token != nil {
		persist = func(result applications.CallResult) applications.CallResult {
			result = boundedApplicationV3CallReply(result)
			if err := g.receipts.finish(ctx, *token, applications.EncodeCallResult(&result)); err != nil {
				return applications.CallResult{Outcome: applications.OutcomeUnknown, Reason: "receipt"}
			}
			return result
		}
	}
	authorization := g.authorization(ctx, c, k, v, call, key, canonical, token == nil)
	if authorization.close != nil {
		defer authorization.close()
	}
	if authorization.outcome != "" {
		return refuse(authorization.outcome, authorization.reason, v)
	}
	if authorization.recheck == nil || authorization.recheck(ctx) != nil {
		return applications.CallResult{Outcome: applications.OutcomeForbidden, Reason: "rights"}
	}
	if g.auditCheck != nil {
		prospective := record
		prospective.Outcome = "unavailable"
		prospective.ResultDigest = "sha256:" + strings.Repeat("0", 64)
		if !v.binding && prospective.Context == nil {
			prospective.Context = &appmodel.ContextRef{Instance: call.Instance, Context: strings.Repeat("c", 64), Epoch: strings.Repeat("e", 64)}
		}
		if g.auditCheck(prospective, regarding) != nil {
			return refuse("unavailable", "audit:bound", v)
		}
	}
	s := g.session(call.Application, call.Instance)
	if s == nil || s.bindingValue().manifestRevision != k.revision || s.guard(ctx, s.bindingValue()) != nil {
		return refuse("unavailable", "owned_host", v)
	}
	if token == nil {
		answer, err := applications.DecodeCallResult(previous.Reply)
		if err != nil {
			return applications.CallResult{Outcome: applications.OutcomeUnavailable, Reason: "receipt"}
		}
		if !v.binding && answer.Answer != nil && answer.Answer.Result != nil {
			record.IdempotencyKey = answer.Answer.Result.IdempotencyKey
		}
		return *answer
	}
	// The description is fetched and kept only over the exact owned channel.
	description, catalogue, err := g.description(ctx, c, k, s, authorization.recheck)
	if err != nil {
		out, reason := applicationV3DescriptionFailure(err)
		return refuse(out, reason, v)
	}
	var command appmodel.CommandSpec
	if !v.binding {
		served := false
		for _, spec := range description.Commands {
			if spec.Name == call.Tool {
				served = true
				command = spec
				var declared appmodel.CommandClass
				for _, decl := range k.manifest.Offers.Commands {
					if decl.Name == call.Tool {
						declared = decl.Class
					}
				}
				if spec.Class != declared {
					return refuse("forbidden", "class:"+call.Tool, v)
				}
				if v.mode == appmodel.InvokeModePropose && !spec.Proposable && spec.Class != appmodel.CommandClassPropose {
					return refuse("unsupported", "mode", v)
				}
				if reason := appmodelcall.Check(spec.InputSchema, &call); reason != "" {
					return refuse("invalid", reason, v)
				}
			}
		}
		if !served {
			return refuse("unsupported", "served:"+call.Tool, v)
		}
	}
	if v.context != nil {
		if err := g.context(ctx, c, k, s, *v.context, authorization.recheck); err != nil {
			return refuse("stale", "context", v)
		}
	}
	// A command call is sent as the Invocation the gateway builds in the
	// context the call names, or the instance's current one, read from the
	// application's own app_contexts (FAC-M32).
	sent := call
	build := func() (string, string) {
		entries, err := g.contexts(ctx, k, s, authorization.recheck)
		if err != nil {
			return "unavailable", "context"
		}
		entry, ok := appmodelcall.Context(entries, v.contextName)
		if !ok {
			if v.contextName == "" {
				return "not_found", "context"
			}
			return "not_found", "context:" + v.contextName
		}
		invocation := appmodelcall.Build(&call, v.mode, entry, v.key)
		v.invocation, v.context = &invocation, &invocation.Context
		record.Context, record.RevisionBefore = v.context, invocation.ExpectedRevision
		if err := validateApplicationV3Command(v, command); err != nil {
			return "invalid", err.Error()
		}
		if sent, err = appmodelcall.Encode(&call, invocation); err != nil {
			return "invalid", applicationV3CodecWord(err)
		}
		return "", ""
	}
	if !v.binding {
		if outcome, reason := build(); outcome != "" {
			return refuse(outcome, reason, v)
		}
	}
	var env *appmodel.Envelope
	send := func() {
		raw, dispatchErr := s.callAt(ctx, &catalogue, &sent, func(raw []byte) error {
			var err error
			env, err = applicationV3Answer(call.Tool, raw)
			if err == nil {
				err = applicationV3SecretAnswer(description, env)
			}
			return err
		}, func(ctx context.Context, b ownedApplicationBinding, _ *appmodel.GatewayCall) error {
			if b != s.bindingValue() {
				return errors.New("owned_host")
			}
			return authorization.recheck(ctx)
		})
		_ = raw
		if dispatchErr != nil {
			out := "unknown"
			if applicationV3ReadingTool(call.Tool) {
				out = "unavailable"
			}
			why := "answer:unavailable"
			if errors.Is(dispatchErr, errOwnedApplicationAbandoned) {
				why = "answer:wait_ended"
			}
			if errors.Is(dispatchErr, errOwnedApplicationCatalogue) {
				why = "catalogue"
			}
			var rpc *ownedApplicationRPCError
			if errors.As(dispatchErr, &rpc) {
				why = fmt.Sprintf("answer:%d", rpc.code)
			}
			var invalid *ownedApplicationInvalidAnswer
			if errors.As(dispatchErr, &invalid) {
				why = "answer:" + applicationV3AnswerWord(invalid.cause)
				if applicationV3ReadingTool(call.Tool) {
					out = "invalid"
				}
			}
			var secret *applicationV3SecretRefusal
			if errors.As(dispatchErr, &secret) {
				out = "unknown"
				if call.Tool == "app_read" || call.Tool == "app_changes" {
					out = "invalid"
				}
				why = secret.Error()
			}
			env = applicationV3Refusal(call.Tool, out, why, v.context)
			gatewayReason = why
		}
	}
	if call.Tool == "app_describe" {
		env = &appmodel.Envelope{Description: &appmodel.DescriptionResult{Outcome: appmodel.ReadingOutcomePage, Description: description}}
	} else {
		send()
	}
	// A stale answer applied nothing: the gateway reads the context again and
	// sends the call once more with the same key.
	if !v.binding && env.Result != nil && env.Result.Outcome == appmodel.CommandOutcomeStale && gatewayReason == "" {
		if outcome, _ := build(); outcome == "" {
			send()
		}
	}
	if env.Result != nil {
		env.Result.IdempotencyKey = ""
		if !v.binding {
			env.Result.IdempotencyKey = v.key
		}
	}
	result := applications.CallResult{Outcome: applications.OutcomeApplied, Answer: env}
	if keptGraphEdit(call.Application, call.Tool, v.class) && gatewayReason == "" && env.Result != nil && env.Result.Outcome == appmodel.CommandOutcomeApplied {
		// The effect reached the application even if a later receipt or
		// protected reply check fails. Activity follows the confirmed effect.
		confirmed := result
		appliedGraph = &confirmed
	}
	result = persist(result)
	if err := authorization.recheck(ctx); err != nil {
		return applications.CallResult{Outcome: applications.OutcomeForbidden, Reason: "rights"}
	}
	return result
}

// description returns the instance's kept description and the catalogue
// count it was read at. It reads the description again once the application
// sent notifications/tools/list_changed (FAC-M17). A failure says its cause
// (applicationV3DescriptionFailure) and writes one log line.
func (g *applicationV3Gateway) description(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, s *ownedApplicationSession, authorize func(context.Context) error) (*appmodel.Description, uint64, error) {
	s.descriptionMu.Lock()
	defer s.descriptionMu.Unlock()
	if err := authorize(ctx); err != nil {
		return nil, 0, &applicationV3DescriptionError{outcome: "unavailable", reason: "rights"}
	}
	if err := s.guard(ctx, s.bindingValue()); err != nil {
		return nil, 0, &applicationV3DescriptionError{outcome: "unavailable", reason: "owned_host"}
	}
	at := s.catalogue.Load()
	if s.description != nil && s.descriptionAt == at {
		return s.description, at, nil
	}
	d, err := g.readDescription(ctx, k, s, at, authorize)
	if err != nil {
		if g.report != nil {
			outcome, reason := applicationV3DescriptionFailure(err)
			g.report(fmt.Errorf("applications@3: %q has no description (%s, %s): %.256s", k.manifest.Name, outcome, reason, err.Error()))
		}
		return nil, 0, err
	}
	s.description, s.descriptionAt = d, at
	return d, at, nil
}

func (g *applicationV3Gateway) readDescription(ctx context.Context, k *applicationV3Kept, s *ownedApplicationSession, at uint64, authorize func(context.Context) error) (*appmodel.Description, error) {
	var answer *appmodel.Envelope
	call := &appmodel.GatewayCall{Application: k.manifest.Name, Instance: s.bindingValue().instance, Tool: "app_describe", Arguments: appmodel.Raw("{}")}
	_, err := s.callAt(ctx, &at, call, func(raw []byte) error {
		var err error
		answer, err = applicationV3Answer("app_describe", raw)
		return err
	}, func(ctx context.Context, _ ownedApplicationBinding, _ *appmodel.GatewayCall) error {
		return authorize(ctx)
	})
	if err != nil {
		return nil, err
	}
	if answer.Description.Outcome != appmodel.ReadingOutcomePage || answer.Description.Description == nil {
		return nil, &applicationV3DescriptionError{outcome: "unavailable", reason: "describe:" + answer.Description.Outcome.String()}
	}
	d := answer.Description.Description
	if d.Version != k.manifest.Offers.Version {
		return nil, &applicationV3DescriptionError{outcome: "unsupported", reason: "version:" + d.Version}
	}
	fitApplicationV3Description(d)
	if err := validateApplicationV3Description(d); err != nil {
		return nil, &applicationV3DescriptionError{outcome: "invalid", reason: err.Error()}
	}
	return d, nil
}

func (g *applicationV3Gateway) context(ctx context.Context, c applicationV3Caller, k *applicationV3Kept, s *ownedApplicationSession, ref appmodel.ContextRef, authorize func(context.Context) error) error {
	entries, err := g.contexts(ctx, k, s, authorize)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.Context == ref {
			return nil
		}
	}
	return errors.New("context")
}

// contexts reads the instance's contexts with their epochs and current
// revisions over the exact owned channel. The answer is read on every call:
// the revision it carries is the default expected_revision and moves with
// every change (FAC-M32).
func (g *applicationV3Gateway) contexts(ctx context.Context, k *applicationV3Kept, s *ownedApplicationSession, authorize func(context.Context) error) ([]appmodel.ContextEntry, error) {
	call := &appmodel.GatewayCall{Application: k.manifest.Name, Instance: s.bindingValue().instance, Tool: "app_contexts", Arguments: appmodel.Raw("{}")}
	var env *appmodel.Envelope
	_, err := s.call(ctx, call, func(raw []byte) error { var err error; env, err = applicationV3Answer("app_contexts", raw); return err }, func(ctx context.Context, _ ownedApplicationBinding, _ *appmodel.GatewayCall) error {
		return authorize(ctx)
	})
	if err != nil {
		return nil, err
	}
	if env.Contexts.Outcome != appmodel.ReadingOutcomePage {
		return nil, errors.New("context")
	}
	for _, entry := range env.Contexts.Contexts {
		if entry.Context.Instance != s.bindingValue().instance {
			return nil, errors.New("context")
		}
	}
	return env.Contexts.Contexts, nil
}
