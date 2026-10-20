package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"net"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go-core/redact"
	"github.com/openabstractions/abstraction-identity/remote"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	logging "github.com/openabstractions/abstraction-logging/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

const remoteServeAction = "abstraction.facade/remote.serve"
const remoteRequestBytes = 1 << 20
const remoteRouterReplyBytes = 8 << 20

// The receiving pairing/configuration owner supplies this private configuration.
// A nil supplier or Listen=false is off. Outgoing registry trust is unrelated.
// A supplier must return an error when its incoming pairing book is unavailable.
type runtimeRemoteConfig struct {
	Listen      bool
	Address     string
	TLS         *tls.Config
	ClientRoots func(context.Context) (*x509.CertPool, error)
	Admit       func(remote.Peer) error
	Minimum     func(context.Context, remote.Peer) (string, error)
	// audit is retained by the private configuration lifecycle owner across
	// listener replacement, preserving the single in-flight remote sink write.
	audit *remoteAudit
	// observeCall returns an epoch-local lifecycle-bound observation closure.
	// The shared offered/profile/remote.serve ingress gate invokes it.
	observeCall func(remote.Peer) func()
}

type incomingRemoteKey struct{}
type incomingRemoteCall struct {
	peer                      remote.Peer
	admit                     func(remote.Peer) error
	minimum, account, program string
}

// matchesSubject keeps the remote route's bound account and program together.
// Capability callers retain their own refusal when this binding does not match.
func (call incomingRemoteCall) matchesSubject(owner, account, program string) bool {
	return owner == call.account && account == call.account && program == call.program
}

type runtimeRemote struct {
	server  remote.Server
	address string
}

// The composed host keeps local IPC available when remote exposure admission
// refuses. Policy/services/configuration failures outside that class stay fatal.
func prepareRuntimeRemoteHost(ctx context.Context, options runtimeFlags, rights *runtimeRights, services *runtimeInference, sink logging.Sink, report func(error)) (*runtimeRemote, error) {
	r, err := prepareRuntimeRemote(ctx, options, rights, services, sink, report)
	if errors.Is(err, errRemoteExposure) {
		if report != nil {
			report(err)
		}
		return nil, nil
	}
	return r, err
}

// prepareRuntimeRemote is read-only pre-bind admission. The default runtime has no
// receiving supplier until pairing is integrated; it opens no remote socket.
func prepareRuntimeRemote(ctx context.Context, options runtimeFlags, rights *runtimeRights, services *runtimeInference, sink logging.Sink, report func(error)) (*runtimeRemote, error) {
	if options.remoteConfiguration == nil {
		return nil, nil
	}
	config, err := options.remoteConfiguration(ctx)
	if err != nil {
		return nil, err
	}
	if config == nil || !config.Listen {
		return nil, nil
	}
	if rights == nil || rights.policy == nil || rights.owner == "" || services == nil || services.provider == nil || services.router == nil {
		return nil, errors.New("remote: receiving policy and services unavailable")
	}
	if snapshot, err := rights.policy.OperatorSnapshot(); err != nil || snapshot.Revision == "" {
		return nil, errors.New("remote: receiving policy unavailable")
	}
	if _, err := remoteListenAddress(config.Address); err != nil {
		return nil, err
	}
	if sink == nil {
		return nil, errors.New("remote: retained audit sink unavailable")
	}
	audit := config.audit
	if audit == nil {
		audit = &remoteAudit{sink: sink, slot: make(chan struct{}, 1), report: report}
	}
	// Freeze configuration fields; live Admit and Minimum remain owner callbacks.
	selected := *config
	server := remote.Server{TLS: selected.TLS, Admit: selected.Admit, ClientRoots: selected.ClientRoots,
		Timeout: 35 * time.Second, HandshakeTimeout: 5 * time.Second,
		MaxConnections: 64, MaxSourceConnections: 4, MaxPeerCalls: 8,
		PeerRequestsPerSecond: 32, PeerBurst: 64,
		MaxRequestFrame: remoteRequestBytes, MaxFrame: remoteRouterReplyBytes}
	server.Handler = func(ctx context.Context, peer remote.Peer, frame []byte) (reply []byte, resultErr error) {
		attempt := &remoteAuditAttempt{audit: audit, ctx: ctx, observation: "peer-refused", attrs: map[string]string{"peer_key": hex.EncodeToString(peer.Key[:]), "contract": "unknown", "method": "unknown", "profile": "unknown", "claim": "unobserved", "outcome": "attempt"}}
		defer func() {
			if attempt.observation == "not-admitted" {
				if resultErr != nil {
					attempt.observation = "malformed-frame"
				} else {
					attempt.observation = "profile-or-service-refused"
				}
			}
			if !attempt.tried {
				if err := attempt.write(""); err != nil {
					reply = nil
					resultErr = err
				}
			}
			if attempt.written {
				attrs := make(map[string]string, len(attempt.attrs))
				for k, v := range attempt.attrs {
					attrs[k] = v
				}
				attrs["admission"] = attempt.observation
				attrs["outcome"] = "response"
				if resultErr != nil {
					attrs["outcome"] = "return-error"
				}
				if err := audit.write(ctx, logging.Record{Time: logging.At(time.Now()), Level: logging.LevelInfo, Msg: "remote.call.return", Attrs: attrs}); err != nil && report != nil {
					report(errors.New("remote: post-call audit unavailable"))
				}
			}
		}()
		mapped, err := remote.SubjectOf(peer, selected.Admit)
		if err != nil {
			return nil, err
		}
		if selected.observeCall != nil {
			attempt.accepted = selected.observeCall(peer)
		}
		attempt.attrs["account"] = rights.owner
		attempt.attrs["program"] = mapped.Program()
		attempt.observation = "minimum-refused"
		minimum := ""
		if selected.Minimum != nil {
			minimum, err = selected.Minimum(ctx, peer)
			if err != nil {
				return nil, err
			}
		}
		call := incomingRemoteCall{peer: peer, admit: selected.Admit, minimum: minimum, account: rights.owner, program: mapped.Program()}
		ctx = context.WithValue(inference.WithLeafExecution(ctx), incomingRemoteKey{}, call)
		attempt.observation = "services-unavailable"
		selectedServices, err := inferenceservice.RemoteServices(ctx, peer, services.provider, services.router, inferenceservice.RemoteConfig{
			Account: rights.owner, Admit: selected.Admit,
			RouterPolicy: func(ctx context.Context, caller routerservice.RemoteCaller, action, resource string) error {
				if caller.Peer.Program() != call.program {
					attempt.observation = "service-subject-refused"
					return errors.New("remote: subject changed")
				}
				err := remoteDecisionError(rights.decideRemote(ctx, peer, selected.Admit, action, resource, minimum))
				if err != nil {
					attempt.observation = "service-rights-refused"
				}
				return err
			},
		})
		if err != nil {
			return nil, err
		}
		offered := make([]fwire.ServedService, len(selectedServices))
		for i, service := range selectedServices {
			switch d := service.(type) {
			case *iwire.ChatDispatcher:
				d.Handler = &remoteAuditedChat{Chat: d.Handler, attempt: attempt}
			case *iwire.EmbedderDispatcher:
				d.Handler = &remoteAuditedEmbed{Embedder: d.Handler, attempt: attempt}
			}
			offered[i] = service
		}
		if services.registry != nil && services.registry.pairing != nil {
			p := services.registry.pairing
			if p.messageHost != nil {
				offered = append(offered, p.messageHost.remoteMessages(ctx, peer, selected.Admit))
			}
		}
		replyLimit := remoteRequestBytes
		attempt.observation = "not-admitted"
		reply, err = fwire.ServeEndpointRemote(frame, "openabstractions", "", func(contract, method, profile string) error {
			attempt.observation = "validated"
			attempt.attrs["contract"] = contract
			attempt.attrs["method"] = method
			attempt.attrs["profile"] = profile
			if !remoteOfferedMethod(contract, method) {
				attempt.observation = "local-only-refused"
				return &fwire.ServiceError{Code: fwire.ServiceErrorCode("unsupported"), Message: "remote:local_only"}
			}
			if err := remoteDecisionError(rights.decideRemote(ctx, peer, selected.Admit, remoteServeAction, "account", minimum)); err != nil {
				attempt.observation = "rights-refused"
				return err
			}
			attempt.accept()
			if contract == inferenceservice.RouterContract {
				replyLimit = remoteRouterReplyBytes
			}
			// Start and Embed obtain their display claim from the existing typed
			// decoder, then log before invoking the real service. Other methods
			// have no claim field and log at this validated admission boundary.
			if !(contract == "abstraction.inference/chat@1" && method == "Start") && !(contract == "abstraction.inference/embed@1" && method == "Embed") {
				attempt.attrs["claim"] = "not-applicable"
				if err := attempt.write(""); err != nil {
					return err
				}
				attempt.observation = "admitted"
				return nil
			}
			return nil
		}, offered...)
		if err != nil {
			return nil, err
		}
		if len(reply) > replyLimit {
			return nil, errors.New("remote: contract reply bound exceeded")
		}
		return reply, nil
	}
	if err := server.Validate(); err != nil {
		return nil, err
	}
	return &runtimeRemote{server: server, address: selected.Address}, nil
}

// Sink has no cancellation API. One runtime-wide remote slot bounds incomplete
// writes. A timed-out attempt never invokes a handler later; the sole writer
// retains the slot until its real completion. The local runtime owns Sink.Close.
type remoteAudit struct {
	sink   logging.Sink
	slot   chan struct{}
	report func(error)
}

func (a *remoteAudit) write(ctx context.Context, record logging.Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case a.slot <- struct{}{}:
	default:
		return errors.New("remote: audit writer busy")
	}
	done := make(chan error, 1)
	go func() {
		var err error
		defer func() {
			if recover() != nil {
				err = errors.New("remote: audit sink panic")
			}
			if err != nil && a.report != nil {
				a.report(errors.New("remote: audit write failed"))
			}
			<-a.slot
			done <- err
		}()
		err = a.sink.Write(record)
	}()
	timer := time.NewTimer(time.Second)
	defer timer.Stop()
	select {
	case err := <-done:
		if err != nil {
			return errors.New("remote: audit write refused")
		}
		return ctx.Err()
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return errors.New("remote: audit write deadline")
	}
}

type remoteAuditAttempt struct {
	audit          *remoteAudit
	ctx            context.Context
	attrs          map[string]string
	tried, written bool
	observation    string
	accepted       func()
}

func (a *remoteAuditAttempt) accept() {
	if a.accepted != nil {
		a.accepted()
	}
}

func (a *remoteAuditAttempt) write(claim string) error {
	if a.tried {
		return errors.New("remote: duplicate audit admission")
	}
	a.tried = true
	if claim != "" {
		a.attrs["claim"] = remoteAuditLabel(claim)
	}
	attrs := make(map[string]string, len(a.attrs))
	for k, v := range a.attrs {
		// Verified identity and generated method metadata are already bounded;
		// preserve the key hash used for attribution. Only the display claim is
		// untrusted application text and uses the shared redactor above.
		attrs[k] = v
	}
	err := a.audit.write(a.ctx, logging.Record{Time: logging.At(time.Now()), Level: logging.LevelInfo, Msg: "remote.call.attempt", Attrs: attrs})
	a.written = err == nil
	return err
}

// Display labels are bounded and cannot inject log lines. Content, arbitrary
// extensions, certificates and headers never enter this projection.
func remoteAuditLabel(value string) string {
	return redact.Line(value, 128)
}

type remoteAuditedChat struct {
	iwire.Chat
	attempt *remoteAuditAttempt
}

func (s *remoteAuditedChat) Start(request iwire.Request) (iwire.Admission, error) {
	s.attempt.attrs["claim"] = "absent"
	if err := s.attempt.write(request.Extensions[inference.ClaimExtension]); err != nil {
		return iwire.Admission{}, err
	}
	s.attempt.observation = "handler-entered"
	result, err := s.Chat.Start(request)
	if err != nil {
		s.attempt.observation = "handler-return-error"
	}
	return result, err
}

type remoteAuditedEmbed struct {
	iwire.Embedder
	attempt *remoteAuditAttempt
}

func (s *remoteAuditedEmbed) Embed(request iwire.EmbedRequest) (iwire.Embeddings, error) {
	s.attempt.attrs["claim"] = "absent"
	if err := s.attempt.write(request.Extensions[inference.ClaimExtension]); err != nil {
		return iwire.Embeddings{}, err
	}
	s.attempt.observation = "handler-entered"
	result, err := s.Embedder.Embed(request)
	if err != nil {
		s.attempt.observation = "handler-return-error"
	}
	return result, err
}

func remoteOfferedMethod(contract, method string) bool {
	switch contract {
	case fwire.EndpointContract:
		return method == "Describe"
	case "abstraction.inference/chat@1":
		return method == "Start" || method == "Observe" || method == "Cancel"
	case "abstraction.inference/embed@1":
		return method == "Embed"
	case inferenceservice.RouterContract:
		return method == "Models" || method == "Hosts" || method == "Modalities"
	case "abstraction.facade/messages@1":
		return method == "Send" || method == "InboxesList" || method == "ObserveDelivery"
	}
	return false
}

func remoteDecisionError(decision rwire.Decision) error {
	if decision.Outcome == rwire.DecisionOutcomePermitted && decision.PolicyRevision != "" {
		return nil
	}
	code := "forbidden"
	if !rightsclient.Evaluated(decision.Outcome) || decision.PolicyRevision == "" {
		code = "unavailable"
	}
	return &fwire.ServiceError{Code: fwire.ServiceErrorCode(code), Message: "remote: receiving policy refused"}
}

// start opens only the admitted listener. stop closes and joins its owned worker.
// An unexpected server failure cancels the composed runtime through fail.
func (r *runtimeRemote) start(ctx context.Context, fail func(error), listen func(string, string) (net.Listener, error)) (func(), error) {
	if r == nil {
		return func() {}, nil
	}
	if err := r.server.Validate(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	listener, err := listen("tcp", r.address)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := r.server.Serve(ctx, listener)
		if closeErr := listener.Close(); closeErr != nil {
			fail(errors.Join(errRemoteExposure, closeErr))
		}
		if err != nil && ctx.Err() == nil {
			fail(err)
		}
	}()
	return func() { cancel(); listener.Close(); <-done }, nil
}
