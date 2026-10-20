package main

import (
	"context"
	"sync/atomic"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// signInRead reads this account's two owned sign-in entries: the runtime's
// and the presenter's. Only the installed runtime binds one
// (startApplicationsLogon); an isolated runtime never changes or reports
// installed sign-in integration.
type signInRead func(context.Context) (runtime, presenter bool, err error)

// signInReader is the installed runtime's read, nil until
// startApplicationsLogon binds it.
type signInReader struct{ read atomic.Pointer[signInRead] }

func (s *signInReader) bind(read signInRead) { s.read.Store(&read) }

// readSignInStart answers applications@3 ReadSignInStart under the same
// operator authority as ReadAgentEnrollment. wanted is the reconciler's own
// input, the account's confirmed registrations; runtime and presenter are
// the Run entries as they stand.
func (d *applicationDirectory) readSignInStart(ctx context.Context, s rights.Subject, reader *signInReader) applications.SignInStartResult {
	if out := d.agentOperator(ctx, s); out != wire.ApplicationOutcomeApplied {
		parsed, _ := applications.ParseOutcome(out.String())
		return applications.SignInStartResult{Outcome: parsed}
	}
	read := reader.read.Load()
	if read == nil {
		return applications.SignInStartResult{Outcome: applications.OutcomeUnavailable, Reason: "sign-in:not_installed"}
	}
	state, err := d.loginState(ctx)
	if err != nil {
		return applications.SignInStartResult{Outcome: applications.OutcomeUnavailable, Reason: "sign-in:directory"}
	}
	runtime, presenter, err := (*read)(ctx)
	if err != nil {
		return applications.SignInStartResult{Outcome: applications.OutcomeUnavailable, Reason: "sign-in:unreadable"}
	}
	start := applications.SignInStart{Wanted: state.enabled, Runtime: runtime, Presenter: presenter}
	return applications.SignInStartResult{Outcome: applications.OutcomePage, State: &start}
}
