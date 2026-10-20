package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// This local gate supplies no authority. Uncertainty suspends ordinary tools;
// explicit Open retains its service-atomic original-binding proof and effect.
type agentToolAdmission struct {
	mu         sync.Mutex
	ready      bool
	closed     bool
	generation uint64
	deadline   time.Time
	active     map[*byte]agentToolCall
	// admitted records a first confirmation; lost and refused name why tools
	// answer unavailable now. None of them ends the MCP connection.
	admitted bool
	lost     error
	refused  error
	// final marks a refusal that lasts for this connection: the person's
	// enrollment no longer admits it (VISION 2026-10-31, the person can revoke
	// the direction). Nothing re-admits it, not even a later enrollment.
	final bool
	// supervised marks an admission a presence supervisor keeps renewing; its
	// tool calls wait briefly for re-admission. confirmed is closed and
	// replaced at each confirmation.
	supervised bool
	confirmed  chan struct{}
	// waiters counts tool calls waiting for re-admission. While any waits,
	// the presence owner retries at its first re-admission delay instead of
	// its slow cadence, and want asks it to try at once.
	waiters int
	want    chan struct{}
}

// agentToolAdmissionWait bounds how long a tool call waits for a supervised
// connection to admit its context again before answering unavailable. A
// model often retries at once, faster than any outage ends; the wait turns a
// short outage into a slower success.
var agentToolAdmissionWait = 10 * time.Second

// agentEffectAdmissionFactor makes the wait of a Send or Reply three times
// the ordinary one, 30 seconds. A runtime that stops answering for longer
// than the presence renewal's budget, as a busy machine does, suspends
// admission; the Send then completes once the runtime answers again rather
// than ask the model to retry (journey J-MSG, 2026-11-10: a 14 s stall
// answered "reconnecting" after 10 s).
const agentEffectAdmissionFactor = 3

type agentAdmissionUntilKey struct{}

// agentAdmissionUntil returns the one deadline every admission wait of this
// tool call shares, the chat receiver's and the connection's own, and the
// context that carries it.
func agentAdmissionUntil(ctx context.Context, name string) (context.Context, time.Time) {
	if until, ok := ctx.Value(agentAdmissionUntilKey{}).(time.Time); ok {
		return ctx, until
	}
	wait := agentToolAdmissionWait
	if name == gateway.ToolMessagesSend || name == gateway.ToolMessagesReply {
		wait *= agentEffectAdmissionFactor
	}
	until := time.Now().Add(wait)
	return context.WithValue(ctx, agentAdmissionUntilKey{}, until), until
}

// wanted signals that a tool call waits for re-admission.
func (a *agentToolAdmission) wanted() <-chan struct{} {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.want == nil {
		a.want = make(chan struct{}, 1)
	}
	return a.want
}

// retryDelay is the presence owner's next retry delay: normal, or the first
// re-admission delay while a tool call waits.
func (a *agentToolAdmission) retryDelay(normal time.Duration) time.Duration {
	if a == nil {
		return normal
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.waiters > 0 {
		return min(normal, agentReadmitFirstDelay)
	}
	return normal
}

// current reports a confirmed admission within its lease.
func (a *agentToolAdmission) current() bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ready && time.Now().Before(a.deadline)
}

func (a *agentToolAdmission) supervise() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.supervised = true
}

// awaitAdmission waits until admission is current, deadline, or ctx. It
// reports false at once when waiting cannot help: an unsupervised gate, the
// end of the connection or a refused admission.
func (a *agentToolAdmission) awaitAdmission(ctx context.Context, deadline time.Time) bool {
	if a == nil {
		return false
	}
	counted := false
	defer func() {
		if counted {
			a.mu.Lock()
			a.waiters--
			a.mu.Unlock()
		}
	}()
	for {
		if ctx.Err() != nil {
			return false
		}
		a.mu.Lock()
		if !a.supervised || a.closed || a.refused != nil {
			a.mu.Unlock()
			return false
		}
		if a.ready && time.Now().Before(a.deadline) {
			a.mu.Unlock()
			return true
		}
		if a.confirmed == nil {
			a.confirmed = make(chan struct{})
		}
		confirmed := a.confirmed
		if !counted {
			// Ask the presence owner to try now, and keep its retries short
			// while this call waits.
			counted = true
			a.waiters++
			if a.want == nil {
				a.want = make(chan struct{}, 1)
			}
			select {
			case a.want <- struct{}{}:
			default:
			}
		}
		a.mu.Unlock()
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return false
		}
		timer := time.NewTimer(remaining)
		select {
		case <-confirmed:
			timer.Stop()
		case <-timer.C:
			return false
		case <-ctx.Done():
			timer.Stop()
			return false
		}
	}
}

type agentToolCall struct {
	cancel       context.CancelCauseFunc
	explicitOpen bool
}

var errAgentAdmissionSuspended = errors.New("original agent admission temporarily suspended")

type agentAdmissionCallerKey struct{}

func agentAdmissionCaller(ctx context.Context) context.Context {
	if original, ok := ctx.Value(agentAdmissionCallerKey{}).(context.Context); ok {
		return original
	}
	return ctx
}

func (a *agentToolAdmission) confirm(deadline time.Time) {
	a.confirmVersion(deadline, a.version())
}

func (a *agentToolAdmission) version() uint64 {
	if a == nil {
		return 0
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.generation
}

func (a *agentToolAdmission) confirmVersion(deadline time.Time, generation uint64) bool {
	if a == nil {
		return true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.final || a.generation != generation {
		return false
	}
	a.ready, a.deadline = true, deadline
	a.admitted, a.lost, a.refused = true, nil, nil
	a.wakeWaiters()
	return true
}

func (a *agentToolAdmission) setReady(ready bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ready = ready
	if !ready {
		a.generation++
		for _, call := range a.active {
			// Explicit Open proves its original binding atomically in-service;
			// known uncertainty must leave its requested reconciliation usable.
			if !call.explicitOpen {
				call.cancel(errAgentAdmissionSuspended)
			}
		}
	}
}

// lose records a definitive admission loss seen by a tool call, for example a
// refused InboxOpen or a broken runtime pipe. It cancels running operations and
// suspends tools; the connection stays open and the presence owner admits the
// same context again. Only the host closing stdio ends a helper.
func (a *agentToolAdmission) lose(cause error) {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed {
		return
	}
	a.lost = cause
	a.ready = false
	a.generation++
	for _, call := range a.active {
		call.cancel(nil)
	}
}

// refuse records a loss the presence owner retries only slowly: a refused
// enrollment or a context another holder keeps. Tools name it until a fresh
// admission confirms again.
func (a *agentToolAdmission) refuse(cause error, final bool) {
	if a == nil {
		return
	}
	a.mu.Lock()
	closed := a.closed
	if !closed {
		a.refused = cause
		a.final = a.final || final
		a.ready = false
		a.generation++
		for _, call := range a.active {
			call.cancel(nil)
		}
		a.wakeWaiters()
	}
	a.mu.Unlock()
	if final && !closed {
		// A final refusal of a current image fences any later handover
		// (agent_handover.go).
		observeAgentFinalRefusal()
	}
}

// wakeWaiters releases every awaitAdmission; each rechecks the state. The
// caller holds mu.
func (a *agentToolAdmission) wakeWaiters() {
	if a.confirmed != nil {
		close(a.confirmed)
		a.confirmed = nil
	}
}

// Lifetime retirement cancels every operation when the stdio connection ends.
func (a *agentToolAdmission) retire() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	if a.closed {
		a.mu.Unlock()
		return false
	}
	a.closed = true
	a.ready = false
	a.generation++
	a.wakeWaiters()
	for _, call := range a.active {
		call.cancel(nil)
	}
	a.mu.Unlock()
	return true
}

// Unavailable states. The connection stays open in every state but ended.
const (
	agentToolConnecting   = "connecting"
	agentToolReconnecting = "reconnecting"
	agentToolRefused      = "refused"
	agentToolEnded        = "ended"
)

// unavailable names, in plain words, why a tool call is refused now.
func (a *agentToolAdmission) unavailable() (state, detail string) {
	if a == nil {
		return agentToolReconnecting, ""
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch {
	case a.closed:
		return agentToolEnded, "This MCP connection is closing."
	case a.final:
		return agentToolRefused, "OpenAbstractions no longer admits this chat (" + agentLossWord(a.refused) + "). If the person allows it again in OpenAbstractions, reconnect OpenAbstractions in this app."
	case a.refused != nil && agentVersionSkew(a.refused):
		return agentToolRefused, "OpenAbstractions was updated and this connection is older. Update the connection in OpenAbstractions, then reconnect OpenAbstractions in this app."
	case a.refused != nil:
		return agentToolRefused, "OpenAbstractions does not admit this chat's inbox right now (" + agentLossWord(a.refused) + "). OA tries again every few minutes. If this continues, reconnect OpenAbstractions in this app."
	case !a.admitted:
		return agentToolConnecting, "OpenAbstractions is connecting this chat. Try again in a few seconds; the MCP connection stays open."
	default:
		return agentToolReconnecting, "OpenAbstractions is reconnecting this chat. Try again in a few seconds; the MCP connection stays open."
	}
}

// NativeToolUnavailable supplies the gateway's typed refusal text.
func (b *messagesBackend) NativeToolUnavailable() (string, string) {
	if b.status == nil {
		return agentToolReconnecting, ""
	}
	return b.status.admission.unavailable()
}

func (a *agentToolAdmission) begin(ctx context.Context) (context.Context, func(), bool) {
	return a.beginOriginal(ctx, false)
}

func (a *agentToolAdmission) beginOriginal(ctx context.Context, explicitOpen bool) (context.Context, func(), bool) {
	if a == nil {
		return ctx, func() {}, true
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	// Explicit Open may reconcile an unknown status only within a current
	// admission: never after a definitive loss or a refusal.
	if a.closed || a.deadline.IsZero() || !a.ready && (!explicitOpen || a.lost != nil || a.refused != nil) || ctx.Err() != nil || !time.Now().Before(a.deadline) {
		return ctx, func() {}, false
	}
	// Nested admission (MCP tool plus owned Watch) retains the actual caller,
	// so local suspension cannot hide a later host-request cancellation.
	call, cancel := context.WithCancelCause(context.WithValue(ctx, agentAdmissionCallerKey{}, agentAdmissionCaller(ctx)))
	key := new(byte)
	if a.active == nil {
		a.active = make(map[*byte]agentToolCall)
	}
	a.active[key] = agentToolCall{cancel: cancel, explicitOpen: explicitOpen}
	return call, func() {
		cancel(nil)
		a.mu.Lock()
		delete(a.active, key)
		a.mu.Unlock()
	}, true
}

func (b *messagesBackend) BeginNativeTool(ctx context.Context, name string) (context.Context, func(), bool) {
	if b.status == nil || b.status.admission == nil {
		return ctx, func() {}, true
	}
	explicitOpen := name == "oa_messages_inbox_open"
	// A supervised connection that is re-admitting its context, or that this
	// call's own proof finds lost, gets one bounded wait in total: the call
	// runs after re-admission rather than answer unavailable at once.
	ctx, until := agentAdmissionUntil(ctx, name)
	for {
		call, done, ok := b.beginNativeToolOnce(ctx, explicitOpen)
		if ok || !b.status.admission.awaitAdmission(ctx, until) {
			return call, done, ok
		}
	}
}

type agentNativeFactoryError struct{ cause error }

func (e *agentNativeFactoryError) Error() string { return e.cause.Error() }
func (e *agentNativeFactoryError) Unwrap() error { return e.cause }

func (a *agentToolAdmission) finalError() error {
	if a == nil {
		return nil
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.final && !a.closed {
		return nil
	}
	cause := a.refused
	if cause == nil {
		cause = a.lost
	}
	if cause == nil {
		cause = errors.New("native admission ended")
	}
	return &agentPresenceTerminalError{cause: cause}
}

func (b *messagesBackend) beginNativeToolOnce(ctx context.Context, explicitOpen bool) (context.Context, func(), bool) {
	call, done, ok := b.status.admission.beginOriginal(ctx, explicitOpen)
	if !ok {
		return call, done, false
	}
	// Ordinary tools receive a fresh preflight within their existing budget.
	// Explicit Open combines that proof and its requested effect in the service.
	b.status.admission.mu.Lock()
	deadline := b.status.admission.deadline
	b.status.admission.mu.Unlock()
	if explicitOpen {
		// This sole method performs the original-binding check and its explicit
		// status effect atomically in the service. Do not replay a prior status.
		call, cancel := context.WithDeadline(call, deadline)
		return call, func() { cancel(); done() }, true
	}
	proof, cancel := agentPresenceCall(call, time.Now(), deadline, true)
	c, err := b.native(proof)
	if err != nil {
		err = &agentNativeFactoryError{cause: err}
	}
	if err == nil {
		var out wire.InboxChange
		out, err = b.status.openContext(proof, c, b.inboxInstance(), b.contextName, nil)
		if err == nil && out.Outcome != wire.OutcomeOpened {
			err = &agentInboxOutcomeError{outcome: out.Outcome}
		}
	}
	cancel()
	if err != nil || call.Err() != nil {
		if err != nil && !transientAgentPresence(err) && !errors.Is(err, context.Canceled) && !errors.Is(err, errMessagesInboxStatusUnknown) {
			b.status.admission.lose(fmt.Errorf("agent tool admission inbox proof: %w", err))
		} else {
			b.status.admission.setReady(false)
		}
		done()
		return call, func() {}, false
	}
	return call, done, true
}
