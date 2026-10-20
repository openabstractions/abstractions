package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"runtime"
	"strconv"
	"sync/atomic"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go-core/bootstrap"
	"github.com/openabstractions/abstraction-facade/go-core/protected"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	logservice "github.com/openabstractions/abstraction-logging/go/service"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// systemQuestionLifetime is the until the system-access question's text
// names: the Book's default pending lifetime (ASK-A7).
const systemQuestionLifetime = 15 * time.Minute

// runtimeProtected is the runtime's protected path for the Security log
// (logging LOG-N1, facade FAC-H2): it admits the system-access question for
// one frozen request (asks ASK-S1), binds its ID as the broker's ticket and
// returns the helper's result once.
type runtimeProtected struct {
	asks    *runtimeAsks
	broker  *protected.Broker
	helper  string
	serving atomic.Bool
	now     func() time.Time
}

// composeProtected returns the protected path, or nil when this runtime has
// none: another platform, an isolated or explicitly addressed runtime, no
// question book, or no trusted helper beside the runtime's own image
// (FAC-H2, FAC-H4).
func composeProtected(options runtimeFlags, questions *runtimeAsks, image func() (string, error)) *runtimeProtected {
	if runtime.GOOS != "windows" || options.endpoint != "" || options.isolated != "" || options.xpc || questions == nil || questions.book == nil {
		return nil
	}
	own, err := image()
	if err != nil {
		return nil
	}
	helper, err := protected.TrustedHelper(own)
	if err != nil {
		return nil
	}
	self, err := identity.Self()
	if err != nil {
		return nil
	}
	return newRuntimeProtected(questions, helper, protected.HelperVerifier(helper, self))
}

func newRuntimeProtected(questions *runtimeAsks, helper string, verify protected.Verify) *runtimeProtected {
	p := &runtimeProtected{asks: questions, helper: helper, now: time.Now}
	p.broker = protected.NewBroker(p.approve, verify)
	return p
}

// serve listens at the account's system-access endpoint and answers the
// helper until ctx ends. A listen failure leaves the path uncovered.
func (p *runtimeProtected) serve(ctx context.Context, report func(error)) func() {
	if p == nil {
		return func() {}
	}
	endpoint, err := bootstrap.Endpoint(protected.EndpointService)
	if err != nil {
		report(fmt.Errorf("protected operations unavailable: %w", err))
		return func() {}
	}
	l, err := listen.Listen(endpoint)
	if err != nil {
		report(fmt.Errorf("protected operations unavailable: %w", err))
		return func() {}
	}
	return p.serveOn(ctx, l, report)
}

func (p *runtimeProtected) serveOn(ctx context.Context, l listen.Listener, report func(error)) func() {
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	p.serving.Store(true)
	go func() {
		defer close(done)
		if err := p.broker.Serve(ctx, l); err != nil {
			report(fmt.Errorf("protected operations stopped: %w", err))
		}
		p.serving.Store(false)
	}()
	return func() { cancel(); <-done }
}

// Covers is logservice.ProtectedSourceReads: the Security log while the
// broker serves.
func (p *runtimeProtected) Covers(sourceID string) bool {
	return p != nil && sourceID == logservice.ProtectedSourceID && p.serving.Load()
}

// approve is the broker's reading of the person's answer at claim time: the
// runtime's own system-access question, answered once through the operator.
func (p *runtimeProtected) approve(_ context.Context, ticket string) (time.Time, error) {
	record, found, err := p.asks.book.ApplicationRecord(ticket)
	if err != nil || !found {
		return time.Time{}, errors.New("protected: no such question")
	}
	if record.Key != asks.SystemSecurityLogKey || record.Option != "once" || !record.Yes || record.Kept || record.AnsweredBy != asks.AnsweredByOperator || record.Answered.IsZero() {
		return time.Time{}, errors.New("protected: the person has not said once")
	}
	return record.Answered, nil
}

// Read is logservice.ProtectedSourceReads (LOG-N1).
func (p *runtimeProtected) Read(ctx context.Context, peer *identity.Peer, request []byte) ([]byte, error) {
	if p == nil || p.asks == nil || p.asks.rights == nil {
		return nil, logservice.ErrProtectedRefused
	}
	subject, err := rightsclient.SubjectFromPeer(peer)
	if err != nil || subject.Account != p.asks.rights.owner || subject.Program == "" {
		return nil, logservice.ErrProtectedRefused
	}
	return p.readFor(ctx, subject.Program, requesterPublisher(peer), request)
}

// readFor is Read for the bound requesting program.
func (p *runtimeProtected) readFor(ctx context.Context, program string, publisher func() asks.Publisher, request []byte) ([]byte, error) {
	sum := sha256.Sum256([]byte(program + "\x00" + protected.OperationSecurityLogPage + "\x00" + string(request)))
	digest := hex.EncodeToString(sum[:])
	key := "system-access:" + digest[:40]
	via := listen.Seen{Why: "admitted by the runtime for a protected source read"}
	for generation := 0; generation < maxFirstUseGenerations; generation++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		requestKey := key
		if generation > 0 {
			requestKey += "." + strconv.Itoa(generation)
		}
		record, outcome, err := p.asks.book.ObserveApplication(p.asks.scope, requestKey)
		if err != nil {
			return nil, err
		}
		switch outcome {
		case "unknown":
			// No question waits on a ticket that cannot be registered: room is
			// checked first, and a registration that still fails withdraws the
			// new question (FAC-H2).
			if err := p.broker.Room(); err != nil {
				return nil, err
			}
			slots := map[string]string{"code": digest[:8], "until": p.now().Add(systemQuestionLifetime).UTC().Format(time.RFC3339)}
			record, outcome, err = p.asks.book.AskApplicationAs(p.asks.scope, program, requestKey, asks.SystemSecurityLogKey, slots, via, publisher())
			if err != nil {
				return nil, err
			}
			if outcome != "pending" {
				return nil, fmt.Errorf("protected: system-access admission %s", outcome)
			}
			if err := p.broker.Register(record.ID, protected.OperationSecurityLogPage, program, request); err != nil {
				if _, _, retireErr := p.asks.book.RetireApplicationAuthorized(record.ID, nil); retireErr != nil {
					return nil, errors.Join(err, retireErr)
				}
				return nil, err
			}
			return nil, logservice.ErrOSAuthorizationRequired
		case "pending":
			// After a restart the pending question has no ticket yet.
			if err := p.broker.Register(record.ID, protected.OperationSecurityLogPage, program, request); err != nil {
				if _, _, retireErr := p.asks.book.RetireApplicationAuthorized(record.ID, nil); retireErr != nil {
					return nil, errors.Join(err, retireErr)
				}
				return nil, err
			}
			return nil, logservice.ErrOSAuthorizationRequired
		case "answered":
			result, keep, err := p.answered(record, program, request)
			if !keep {
				if _, releaseErr := p.asks.book.ReleaseAnsweredApplicationID(p.asks.scope, requestKey, record.ID, record.Option); releaseErr != nil {
					return nil, releaseErr
				}
			}
			if result != nil || err != nil {
				return result, err
			}
		}
		// gone, or a released answer: the next generation asks again.
	}
	return nil, errors.New("protected: no question generation left")
}

// answered decides an answered question (ASK-S1). keep false releases it: a
// once whose execution was delivered, failed or expired, and a refuse past
// its interval. A result or an error ends the read; neither asks again in
// this generation.
func (p *runtimeProtected) answered(record asks.Record, program string, request []byte) (result []byte, keep bool, err error) {
	switch record.Option {
	case "never":
		return nil, true, logservice.ErrProtectedRefused
	case "refuse":
		if p.now().Sub(record.Answered) < firstUseRefuseInterval {
			return nil, true, logservice.ErrProtectedRefused
		}
		return nil, false, nil
	case "once":
	default:
		return nil, false, nil
	}
	if record.AnsweredBy != asks.AnsweredByOperator {
		p.broker.Forget(record.ID)
		return nil, false, nil
	}
	state, result, failure := p.broker.Take(record.ID, protected.OperationSecurityLogPage, program, request)
	switch state {
	case protected.StateDelivered:
		return result, false, nil
	case protected.StateFailed:
		if failure == protected.FailureDenied {
			return nil, false, logservice.ErrProtectedDenied
		}
	case protected.StateAsked, protected.StateClaimed:
		if p.now().Sub(record.Answered) <= protected.ClaimWindow+protected.HelperDeadline+15*time.Second {
			return nil, true, logservice.ErrOSAuthorizationPending
		}
		p.broker.Forget(record.ID)
	}
	return nil, false, nil
}
