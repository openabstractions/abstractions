package main

import (
	"context"

	legacy "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	"time"
)

// The same Program-proof listener serves both closed families. @2 keeps its
// existing generated dispatcher and its original directory operations.
func (h *applicationsHost) ApplicationContracts() []string {
	var contracts []string
	if h.v3 != nil {
		contracts = append(contracts, "abstraction.facade/applications@3", applicationsOperatorContract)
	}
	if h.messages != nil {
		contracts = append(contracts, "abstraction.facade/messages@1")
	}
	return contracts
}

func (r *applicationV3Receiver) PrepareAgentUpdate(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
	return (&applicationsReceiver{host: r.host, call: r.call, ctx: r.ctx}).PrepareAgentUpdate(request)
}
func (r *applicationV3Receiver) FindAgentUpdate(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
	return (&applicationsReceiver{host: r.host, call: r.call, ctx: r.ctx}).FindAgentUpdate(request)
}
func (r *applicationV3Receiver) CommitAgentUpdate(intent string) (applications.AgentUpdateResult, error) {
	return (&applicationsReceiver{host: r.host, call: r.call, ctx: r.ctx}).CommitAgentUpdate(intent)
}

func (h *applicationsHost) exchangeApplicationFrame(ctx context.Context, call *listen.FramedCall) ([]byte, error) {
	// Each decision made for this call reads the peer the listener bound
	// (rights RIGHTS-R27); a call without one decides without it.
	peer, _ := call.Peer()
	ctx = rights.WithBoundPeer(ctx, peer)
	// The call's wait ends when its caller leaves or its budget ends.
	waiting := call.WaitContext()
	if deadline, ok := ctx.Deadline(); ok {
		var end context.CancelFunc
		waiting, end = context.WithDeadline(waiting, deadline)
		defer end()
	}
	wait := rights.WithBoundPeer(waiting, peer)
	if contract, _, err := legacy.ServiceMethod(call.Frame); err == nil && (contract == "abstraction.facade/endpoint@1" || contract == "abstraction.facade/endpoint@2") {
		services := []legacy.DescribedService{&legacy.ApplicationsDispatcher{Handler: &applicationsReceiver{host: h, call: call, ctx: ctx}}}
		if h.v3 != nil {
			services = append(services, &applications.ApplicationsDispatcher{Handler: &applicationV3Receiver{host: h, call: call, ctx: wait}},
				&applications.ApplicationsOperatorDispatcher{Handler: &applicationsOperatorReceiver{host: h, call: call, ctx: ctx}})
		}
		if h.messages != nil {
			services = append(services, &msg.MessagesDispatcher{Handler: &messagesReceiver{host: h, call: call, ctx: wait}})
		}
		return legacy.DescribeEndpoint(call.Frame, "", "", services...)
	}
	if h.messages != nil {
		contract, _, err := legacy.ServiceMethod(call.Frame)
		if err == nil && contract == "abstraction.facade/messages@1" {
			return (&msg.MessagesDispatcher{Handler: &messagesReceiver{host: h, call: call, ctx: wait}}).ExchangeFrame(call.Frame)
		}
	}
	if h.v3 != nil {
		if contract, _, err := legacy.ServiceMethod(call.Frame); err == nil && contract == applicationsOperatorContract {
			return (&applications.ApplicationsOperatorDispatcher{Handler: &applicationsOperatorReceiver{host: h, call: call, ctx: ctx}}).ExchangeFrame(call.Frame)
		}
		contract, method, err := legacy.ServiceMethod(call.Frame)
		if err == nil && contract == "abstraction.facade/applications@3" {
			r := &applicationV3Receiver{host: h, call: call, ctx: wait}
			reply, dispatchErr := (&applications.ApplicationsDispatcher{Handler: r}).ExchangeFrame(call.Frame)
			if method == "Call" && !r.called {
				// The strict shared header identifies this attempt; no unread key,
				// tool, arguments or caller-provided identity is recovered by hand.
				c := r.caller()
				record := appmodel.CallRecord{Subject: c.subject.Program, Outcome: "invalid", Reason: "codec", ResultDigest: "sha256:" + applicationV3BodyDigest(reply)}
				auditCtx, cancel := context.WithTimeout(context.WithoutCancel(r.ctx), time.Second)
				defer cancel()
				if h.v3.audit == nil {
					return nil, context.Canceled
				}
				if auditErr := h.v3.audit(auditCtx, record, ""); auditErr != nil {
					return nil, auditErr
				}
			}
			return reply, dispatchErr
		}
	}
	return (&legacy.ApplicationsDispatcher{Handler: &applicationsReceiver{host: h, call: call, ctx: ctx}}).ExchangeFrame(call.Frame)
}

type applicationV3Receiver struct {
	host   *applicationsHost
	call   *listen.FramedCall
	ctx    context.Context
	called bool
}

func (r *applicationV3Receiver) caller() applicationV3Caller {
	subject := (&applicationsReceiver{host: r.host, call: r.call, ctx: r.ctx}).caller()
	peer, _ := r.call.Peer()
	return applicationV3Caller{subject: subject, peer: peer, recheck: r.call.Recheck, matcher: r.call.MatchStartedProcess}
}

func (r *applicationV3Receiver) ConfirmReceiverStart(recipeDigest string) (legacy.ApplicationChange, error) {
	return r.host.directory.confirmMessageStart(r.ctx, r.caller(), recipeDigest), nil
}

func (r *applicationV3Receiver) SetActivation(request applications.ActivationConfiguration) (legacy.ApplicationChange, error) {
	return r.host.directory.setActivation(r.ctx, r.caller().subject, request, r.call.Recheck), nil
}

func (r *applicationV3Receiver) ReconcileActivation(expected legacy.ApplicationDescriptor) (legacy.ApplicationChange, error) {
	return r.host.directory.reconcileActivation(r.ctx, r.caller().subject, expected, r.call.Recheck), nil
}

func (r *applicationV3Receiver) Register(request applications.RegisterRequest) (applications.Change, error) {
	return r.host.v3.register(r.ctx, r.caller(), request), nil
}
func (r *applicationV3Receiver) ReadAgentEnrollment(application string) (applications.AgentEnrollmentResult, error) {
	return (&applicationsReceiver{host: r.host, call: r.call, ctx: r.ctx}).ReadAgentEnrollment(application)
}
func (r *applicationV3Receiver) RequestSetup(use string) (applications.SetupResult, error) {
	return r.host.setup.request(r.ctx, r.host.directory, r.caller().subject, use), nil
}
func (r *applicationV3Receiver) ReceiveSetupRequest(after string) (applications.SetupRequestRead, error) {
	if r.call == nil {
		return applications.SetupRequestRead{Outcome: applications.OutcomeForbidden}, nil
	}
	return r.host.setup.read(r.ctx, r.host.directory, r.caller().subject, after), nil
}
func (r *applicationV3Receiver) ReportSetupStep(receipt applications.SetupPresenterReceipt) (applications.SetupStepReport, error) {
	if r.call == nil {
		return applications.SetupStepReport{Outcome: applications.OutcomeForbidden}, nil
	}
	return r.host.setup.report(r.ctx, r.host.directory, r.caller().subject, receipt, r.call.Recheck), nil
}
func (r *applicationV3Receiver) ReadSignInStart() (applications.SignInStartResult, error) {
	return r.host.directory.readSignInStart(r.ctx, r.caller().subject, &r.host.signIn), nil
}
func (r *applicationV3Receiver) SetAgentEnrollment(request applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
	return (&applicationsReceiver{host: r.host, call: r.call, ctx: r.ctx}).SetAgentEnrollment(request)
}
func (r *applicationV3Receiver) AskConfirmation(application string) (legacy.ApplicationChange, error) {
	return r.host.directory.askConfirmation(r.ctx, r.caller().subject, application), nil
}
func (r *applicationV3Receiver) Remove(request applications.ChangeRequest) (applications.Change, error) {
	return r.host.v3.change(r.ctx, r.caller(), request.Application, request.ExpectedRevision, "remove"), nil
}
func (r *applicationV3Receiver) Announce(key string, presence legacy.ApplicationPresence) (legacy.ApplicationChange, error) {
	defer r.host.reportSlowPresence("announce", time.Now())
	return r.host.v3.announce(r.ctx, r.caller(), key, presence), nil
}
func (r *applicationV3Receiver) Withdraw(application, instance string) (legacy.ApplicationChange, error) {
	c := r.caller()
	session, ok := applicationSession(c.peer)
	if !ok || !c.valid(r.host.owner) {
		return appChange(legacy.ApplicationOutcomeForbidden, ""), nil
	}
	return r.host.directory.withdrawInSession(r.ctx, c.subject, session, application, instance), nil
}
func (r *applicationV3Receiver) Observe(query applications.Query) (applications.Page, error) {
	return r.host.v3.observe(r.ctx, r.caller(), query), nil
}
func (r *applicationV3Receiver) Activate(key string, request applications.ActivateRequest) (applications.ActivationResult, error) {
	return r.host.v3.activate(r.ctx, r.caller(), key, request), nil
}
func (r *applicationV3Receiver) Describe(query appmodel.ApplicationsQuery) (applications.DescribeResult, error) {
	return r.host.v3.describe(r.ctx, r.caller(), query), nil
}
func (r *applicationV3Receiver) Commands(query appmodel.CommandsQuery) (applications.CommandsResult, error) {
	return r.host.v3.commands(r.ctx, r.caller(), query), nil
}
func (r *applicationV3Receiver) Call(key string, call appmodel.GatewayCall) (applications.CallResult, error) {
	r.called = true
	return r.host.v3.call(r.ctx, r.caller(), key, call), nil
}

// applicationsOperatorContract is the operator-only applications service
// (architect ruling 2026-11-11): the Panel's ExpectConnect. Applications and
// the MCP gateway reach applications@3, which does not carry it.
const applicationsOperatorContract = "abstraction.applications/operator@1"

// applicationsOperatorReceiver serves abstraction.applications/operator@1.
// Admission is the listener's same-account Program proof of the caller and
// the runtime's operator check narrowed to the Panel program
// (expectConnect).
type applicationsOperatorReceiver struct {
	host *applicationsHost
	call *listen.FramedCall
	ctx  context.Context
}

func (r *applicationsOperatorReceiver) ExpectConnect(expectation applications.ConnectExpectation) (legacy.ApplicationChange, error) {
	refused := legacy.ApplicationChange{Outcome: legacy.ApplicationOutcomeForbidden, Reason: "caller"}
	if r.call == nil {
		return refused, nil
	}
	subject := (&applicationsReceiver{host: r.host, call: r.call, ctx: r.ctx}).caller()
	directory := r.host.directory
	peer, err := r.call.Peer()
	if subject.Program == "" || err != nil || directory.connectPeer == nil ||
		!directory.connectPeer(r.ctx, peer) || r.call.Recheck() != nil {
		return refused, nil
	}
	return directory.expectConnect(r.ctx, subject, expectation), nil
}
