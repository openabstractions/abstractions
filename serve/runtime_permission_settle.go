package main

import (
	"context"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// settlePassBudget bounds one settling pass: a decision that does not read in
// time keeps its request pending until the next change.
const settlePassBudget = 30 * time.Second

// permissionSettler withdraws pending typed permission requests the person
// no longer needs to answer (asks ASK-A13): after every committed rights
// change (RIGHTS-R23), each pending occurrence's exact effect is decided
// again, and one a rule or default now permits, or a Block refuses, leaves
// the Book. It runs off the editing goroutine: an operator Allow commits its
// rights effect while the Book is pinned, and the settler takes the Book
// lock. Changes that arrive while a pass runs fold into one more pass.
//
// Its lifetime is the runtime's: followRights starts it, and the stop it
// returns removes the change subscription, cancels the pass in flight and
// waits for it, so no pass edits the Book once the asks host starts closing.
type permissionSettler struct {
	mu sync.Mutex
	// running is a pass in flight; again asks it for one more. stopped
	// refuses new passes once stop began; passes counts the goroutine stop
	// waits for, added under mu while not stopped, so no Add follows the Wait.
	running, again, stopped bool
	passes                  sync.WaitGroup
	// ctx is cancelled by stop; nil until followRights.
	ctx    context.Context
	cancel context.CancelFunc
	// decide is the decision a pass reads; tests replace it.
	decide func(context.Context, rwire.PermissionEffect) rwire.BatchDecision
	// done, when set, is called after each pass; tests wait on it.
	done func()
}

// followRights settles pending typed requests after every committed rights
// change, and once now for a change committed while the runtime was down
// (ASK-A13, RIGHTS-R23). The returned stop removes the subscription,
// cancels the pass in flight and waits for it; the runtime calls it before
// the asks host closes.
func (a *runtimeAsks) followRights(ctx context.Context) (stop func()) {
	if a == nil || a.rights == nil || a.rights.policy == nil {
		return func() {}
	}
	s := &a.settler
	s.mu.Lock()
	s.ctx, s.cancel = context.WithCancel(ctx)
	if s.decide == nil {
		s.decide = func(ctx context.Context, effect rwire.PermissionEffect) rwire.BatchDecision {
			return a.rights.policy.CheckPermissionEffectContext(ctx, effect, "")
		}
	}
	s.mu.Unlock()
	remove := a.rights.policy.OnChange(a.settleSoon)
	a.settleSoon()
	return func() {
		remove()
		s.mu.Lock()
		s.stopped = true
		cancel := s.cancel
		s.mu.Unlock()
		cancel()
		s.passes.Wait()
	}
}

// settleSoon starts a pass, or marks one more pass for the running one.
// Before followRights and after stop it starts nothing.
func (a *runtimeAsks) settleSoon() {
	s := &a.settler
	s.mu.Lock()
	if s.stopped || s.ctx == nil {
		s.mu.Unlock()
		return
	}
	if s.running {
		s.again = true
		s.mu.Unlock()
		return
	}
	s.running = true
	s.passes.Add(1)
	ctx := s.ctx
	s.mu.Unlock()
	go func() {
		defer s.passes.Done()
		for {
			passCtx, cancel := context.WithTimeout(ctx, settlePassBudget)
			a.withdrawSettledPermissions(passCtx)
			cancel()
			s.mu.Lock()
			done := s.done
			more := s.again && ctx.Err() == nil
			s.running, s.again = more, false
			s.mu.Unlock()
			if done != nil {
				done()
			}
			if !more {
				return
			}
		}
	}()
}

// withdrawSettledPermissions decides every pending typed occurrence again and
// withdraws each whose decision is now permitted or denied. A decision that
// cannot be read, that still lacks a grant, or that the pass's context ended,
// keeps the request pending.
func (a *runtimeAsks) withdrawSettledPermissions(ctx context.Context) {
	decide := a.settler.decide
	if decide == nil {
		return
	}
	pending, err := a.book.PendingTypedPermissions()
	if err != nil {
		a.report(err)
		return
	}
	for _, record := range pending {
		if ctx.Err() != nil {
			return
		}
		_, err := a.book.WithdrawSettledPermission(record.ID, func(p asks.TypedPermission) bool {
			decision := decide(ctx, p.Effect)
			if ctx.Err() != nil {
				return false
			}
			switch permissionDisposition(decision, len(p.Effect.Selections)) {
			case askswire.PermissionOutcomePermitted, askswire.PermissionOutcomeDenied:
				return true
			}
			return false
		})
		if err != nil {
			a.report(err)
		}
	}
}
