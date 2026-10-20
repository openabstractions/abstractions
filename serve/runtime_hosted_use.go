package main

import (
	"context"
	"strings"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/remote"
	"github.com/openabstractions/abstraction-inference/adapters/go/gateway"
	inference "github.com/openabstractions/abstraction-inference/go"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// A hosted model server spends the account its declaration names. The
// person's one decision about a program and that account (the first-spend
// question, P-M9 and architect ruling C, 2026-11-08) is
// abstraction.credentials/apply on the declared credential. A program with no
// rule of its own on complete for that server is decided by its use of the
// account: one question, one Allow, both checks pass. An explicit complete
// rule, a deny included, still decides first.

// hostedServerCredential names the credential the hosted model server host
// declares, or "" for a local server, a remote runtime (which applies its own
// credential) or an unknown name.
func (c *runtimeCredentials) hostedServerCredential(host string) string {
	if c == nil || c.hostedCredential == nil {
		return ""
	}
	return c.hostedCredential(host)
}

// hostedUseCredential is the declared credential that decides complete on
// resource for s on this machine, or "" when the ordinary decision applies:
// another action, a local server, a remote peer's call or a remote program.
func (c *runtimeCredentials) hostedUseCredential(ctx context.Context, s inference.Subject, action, resource string) string {
	if action != inference.ActionComplete {
		return ""
	}
	if _, incoming := ctx.Value(incomingRemoteKey{}).(incomingRemoteCall); incoming || remote.ValidSubjectProgram(s.Program) {
		return ""
	}
	host, ok := strings.CutPrefix(resource, "host:")
	if !ok || host == "" {
		return ""
	}
	return c.hostedServerCredential(host)
}

// decideHostedUse decides complete on a hosted server through its declared
// credential. Without a complete rule of its own, a program may use the
// server when it may use the account; when neither is decided yet and ask is
// set, the account's first-use question is admitted (and collected for the
// caller's wait, INF-W10/W11). A Don't allow on the account stands as its
// deny.
func (c *runtimeCredentials) decideHostedUse(ctx context.Context, bound rwire.Subject, resource, credential string, ask bool) rwire.DecisionOutcome {
	own := c.runtimeRights.decide(ctx, bound, inference.ActionComplete, resource).Outcome
	if own != rwire.DecisionOutcomeNotGranted {
		return own
	}
	account := credentials.ResourceFor(credential)
	use := c.useDecision(ctx, bound, account)
	if use == rwire.DecisionOutcomeNotGranted && ask && c.firstUse != nil {
		inference.AskedFrom(ctx).Add(c.firstUse(bound, requesterPublisher(boundPeer(ctx, bound)), credentials.ActionApply, account))
	}
	return use
}

// hostedStanding is the router's HostedStandingReader: the quiet decision of
// complete on hosted server host for the caller ctx carries, read before the
// router picks among several hosted servers. A caller already allowed on one
// account is routed there and asked nothing about another; a server whose
// decision is a deny comes last. A caller ctx binds no peer for, a remote
// peer's call and a remote program keep the ordinary order.
func (c *runtimeCredentials) hostedStanding(ctx context.Context, host string) router.HostedStanding {
	if c == nil || c.runtimeRights == nil {
		return router.HostedUndecided
	}
	peer := inference.PeerFromContext(ctx)
	if peer == nil {
		return router.HostedUndecided
	}
	bindSubject := inferenceservice.SubjectFromPeer
	if peer.Transport == identity.TransportLoopback {
		bindSubject = gateway.LoopbackSubject
	}
	s, err := bindSubject(peer)
	if err != nil || s.Account == "" || s.Program == "" {
		return router.HostedUndecided
	}
	return c.hostedStandingOf(ctx, s, host)
}

// hostedStandingOf is hostedStanding for the bound subject s.
func (c *runtimeCredentials) hostedStandingOf(ctx context.Context, s inference.Subject, host string) router.HostedStanding {
	resource := inference.ResourceHost(host)
	credential := c.hostedUseCredential(ctx, s, inference.ActionComplete, resource)
	if credential == "" {
		return router.HostedUndecided
	}
	switch c.decideHostedUse(ctx, rwire.Subject{Account: s.Account, Program: s.Program}, resource, credential, false) {
	case rwire.DecisionOutcomePermitted:
		return router.HostedPermitted
	case rwire.DecisionOutcomeNotGranted:
		return router.HostedUndecided
	default:
		return router.HostedRefused
	}
}

// decideInference is inference.Config.Decide: complete on a hosted server
// decides through its account (decideHostedUse); everything else is the
// rights decision runtimeRights.decideInference makes.
func (c *runtimeCredentials) decideInference(ctx context.Context, s inference.Subject, action, resource string) (string, error) {
	if credential := c.hostedUseCredential(ctx, s, action, resource); credential != "" {
		bound := rwire.Subject{Account: s.Account, Program: s.Program}
		return c.decideHostedUse(ctx, bound, resource, credential, !inference.PreloadAdmission(ctx)).String(), nil
	}
	return c.runtimeRights.decideInference(ctx, s, action, resource)
}
