package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

// agentPresenceDirectory is implemented by the existing authenticated
// Applications client. Names are routing metadata; the service checks the
// caller's native account, program, session and kept agent enrollment.
type agentPresenceDirectory interface {
	Announce(context.Context, wire.ApplicationPresence) (wire.ApplicationChange, error)
	Withdraw(context.Context, string, string) (wire.ApplicationChange, error)
}

// agentInboxOpen forwards the retained instance/context to Messages.InboxOpen.
// Its adapter accepts only the generated opened outcome. It grants no rights.
type agentInboxOpen func(context.Context, string, string) error

type agentInboxOutcomeError struct{ outcome messages.Outcome }

func (e *agentInboxOutcomeError) Error() string { return "agent inbox: " + e.outcome.String() }

type agentPresenceTerminalError struct{ cause error }

func (e *agentPresenceTerminalError) Error() string { return e.cause.Error() }
func (e *agentPresenceTerminalError) Unwrap() error { return e.cause }

func establishedChatError(err error) error {
	if err == nil || agentVersionSkew(err) {
		return err
	}
	class := messagesWatchFailure(err, "unavailable")
	if class == "forbidden" || class == "invalid" && agentPresenceCompletion(err, true) != nil {
		return &agentPresenceTerminalError{cause: err}
	}
	return err
}

func transientAgentPresence(err error) bool {
	var inbox *agentInboxOutcomeError
	// Same-status upkeep uncertainty suspends admission within the original
	// confirmed lease. It does not prove revocation or permit fresh enrollment.
	return errors.Is(err, context.DeadlineExceeded) || errors.As(err, &inbox) && (inbox.outcome == messages.OutcomeUnavailable || inbox.outcome == messages.OutcomeUnknown)
}

type agentPresencePolicy struct {
	beforeAnnounce func(context.Context) error
	interfaces     func() []wire.ApplicationInterface
	routingChanged <-chan struct{}
	admission      *agentToolAdmission
	now            func() time.Time
	after          func(time.Duration) <-chan time.Time
	// readmit marks a presence the connection supervisor owns. Every loss
	// suspends tools and returns to the supervisor, which admits the same
	// context again; no loss ends the MCP server. A recoverable loss leaves the
	// old lease to lapse; a refused one, or the end of the connection,
	// withdraws it.
	readmit bool
	// instance receives each admitted instance before tools are confirmed, so
	// no tool call can run against the previous instance.
	instance func(string)
}

// agentAnnounceOutcomeError is a non-applied Announce outcome.
type agentAnnounceOutcomeError struct {
	outcome wire.ApplicationOutcome
	renewal bool
}

func (e *agentAnnounceOutcomeError) Error() string {
	if e.renewal {
		return fmt.Sprintf("agent presence renewal announce: %s; exact lease lost", e.outcome.String())
	}
	return fmt.Sprintf("agent presence: %s", e.outcome.String())
}

// errAgentPresenceLost wraps a loss after which the same process and MCP
// connection may admit the same context again.
type errAgentPresenceLost struct{ cause error }

func (e *errAgentPresenceLost) Error() string { return "agent presence lost: " + e.cause.Error() }
func (e *errAgentPresenceLost) Unwrap() error { return e.cause }

// agentPresenceFinal reports a loss that re-admission must not retry: the
// process is ending, or the person's enrollment no longer admits this program.
func agentPresenceFinal(ctx context.Context, err error) bool {
	if ctx.Err() != nil {
		return true
	}
	var terminal *agentPresenceTerminalError
	if errors.As(err, &terminal) {
		return true
	}
	var inbox *agentInboxOutcomeError
	// held is decided by the caller after one lease length, never here.
	if errors.As(err, &inbox) && (inbox.outcome == messages.OutcomeForbidden || inbox.outcome == messages.OutcomeInvalid || inbox.outcome == messages.OutcomeEnded) {
		return true
	}
	var announce *agentAnnounceOutcomeError
	return errors.As(err, &announce) && (announce.outcome == wire.ApplicationOutcomeForbidden || announce.outcome == wire.ApplicationOutcomeInvalid)
}

// Presence cadences: a 30-second lease renewed every 10 seconds; recovery
// reuses that cadence and its existing RPC budget. They are variables only so
// the chaos tests can run the same composition on a short clock.
var (
	agentPresenceLease      = 30 * time.Second
	agentPresenceRenewal    = 10 * time.Second
	agentPresenceRetryDelay = 10 * time.Second
	agentReadmitFirstDelay  = time.Second
)

func agentPresenceCall(ctx context.Context, now, deadline time.Time, bounded bool) (context.Context, context.CancelFunc) {
	until := now.Add(2 * time.Second)
	if bounded && deadline.Before(until) {
		until = deadline
	}
	return context.WithDeadline(ctx, until)
}

// runNativeAgentPresence uses only the existing authenticated native transport.
// The dedicated child's normal owner selects its application and context labels.
func runNativeAgentPresence(ctx context.Context, machine *client.Machine, application, contextName string, ready func(string), beforeWithdraw ...func()) error {
	return runNativeAgentPresenceWithStatus(ctx, machine, application, contextName, ready, nil, beforeWithdraw...)
}

func runNativeAgentPresenceWithStatus(ctx context.Context, machine *client.Machine, application, contextName string, ready func(string), status *messagesInboxStatus, beforeWithdraw ...func()) error {
	return runNativeAgentPresenceAttempt(ctx, machine, application, contextName, ready, status, false, beforeWithdraw...)
}

// runNativeAgentPresenceAttempt resolves and verifies the native services as
// at helper start, announces a new instance and opens the context.
func runNativeAgentPresenceAttempt(ctx context.Context, machine *client.Machine, application, contextName string, ready func(string), status *messagesInboxStatus, readmit bool, beforeWithdraw ...func()) error {
	if machine == nil {
		return errors.New("agent presence requires a native client")
	}
	directory, err := machine.ResolveApplications(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return fmt.Errorf("agent presence resolve Applications: %w", err)
	}
	binding, err := machine.ResolveService(ctx, "abstraction.facade/messages@1", client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		return fmt.Errorf("agent presence resolve Messages: %w", err)
	}
	return agentPresenceAttempt(ctx, directory, func(ctx context.Context) nativeMessages {
		return messages.NewMessagesClient(binding.Transport().WithContext(ctx))
	}, application, contextName, ready, status, readmit, beforeWithdraw...)
}

// agentPresenceAttempt announces a new instance through directory and opens
// the context through native Messages, then renews both until a loss.
func agentPresenceAttempt(ctx context.Context, directory agentPresenceDirectory, native func(context.Context) nativeMessages, application, contextName string, ready func(string), status *messagesInboxStatus, readmit bool, beforeWithdraw ...func()) error {
	if status != nil && (nativecli.AgentApplicationBase(application) == "oa-codex-integration" || nativecli.AgentApplicationBase(application) == "oa-claude-integration" || nativecli.AgentApplicationBase(application) == "oa-opencode-integration" || status.chatReceiver) {
		status.routingMu.Lock()
		status.chatSource = true
		status.routingMu.Unlock()
	}
	open := func(ctx context.Context, instance, contextName string) error {
		result, err := status.openContext(ctx, native(ctx), instance, contextName, nil)
		if err != nil {
			return err
		}
		if result.Outcome != messages.OutcomeOpened {
			return &agentInboxOutcomeError{outcome: result.Outcome}
		}
		return nil
	}
	ticker := time.NewTicker(agentPresenceRenewal)
	defer ticker.Stop()
	policy := agentPresencePolicy{readmit: readmit}
	if status != nil {
		policy.admission, policy.instance = status.admission, status.setInstance
		if status.chatReceiver && status.chatSourceProof != nil {
			policy.beforeAnnounce = func(ctx context.Context) error {
				_, finish, outcome, err := status.chatSourceProof(ctx)
				defer finish()
				if err != nil {
					return err
				}
				if outcome != messages.OutcomeOpened {
					return &agentInboxOutcomeError{outcome: outcome}
				}
				return nil
			}
		}
		if nativecli.AgentApplicationBase(application) == "oa-codex-integration" {
			_, policy.routingChanged = status.receivingPresence()
			policy.interfaces = func() []wire.ApplicationInterface { value, _ := status.receivingPresence(); return value }
		}
	}
	return maintainAgentPresenceWithPolicy(ctx, directory, open, application, contextName, ready, ticker.C, policy, beforeWithdraw...)
}

var agentReadmitMaxDelay = 30 * time.Second

// agentPresenceRefusedRetry is the slow cadence after a refused enrollment or
// a context another holder keeps or version skew. Announce and InboxOpen
// create no question and no enrollment, so a retry only notices when the other
// holder lets the context go.
var agentPresenceRefusedRetry = 5 * time.Minute

// superviseAgentPresence owns one helper's native presence for the whole life
// of its MCP stdio connection (owner, 2026-11-01: the agent connection is
// stable by construction). It returns only when ctx ends. Every loss, the
// first admission included, suspends tools and is retried: a runtime that is
// down, restarting or asleep, a reset pipe, a lapsed lease, a decode error.
// Retries back off from one second to thirty. A context another holder keeps
// for longer than one lease length, or version skew, is retried every five
// minutes and named in tool refusals; a held context is never taken over
// while it is held. An enrollment refusal (forbidden or invalid) is final for
// this connection: tools answer refused until the host closes stdio, and a
// later enrollment never revives it (architect review 10, B1, option a). A
// panic in an attempt is recovered and retried. Re-admission keeps the same
// application label, context and process; the service derives the program
// and its enrollment from the process.
func superviseAgentPresence(ctx context.Context, attempt func(context.Context, func(string), bool, func()) error, status *messagesInboxStatus, after func(time.Duration) <-chan time.Time, now func() time.Time) {
	var admission *agentToolAdmission
	if status != nil {
		admission = status.admission
	}
	admission.supervise()
	delay := agentReadmitFirstDelay
	admitted := false
	var heldSince time.Time
	// The runtime answers InboxOpen forbidden for an instance it does not
	// know, as after a restart between Announce and InboxOpen. An inbox
	// refusal is therefore final only when a fresh attempt confirms it.
	suspect := false
	var nudge <-chan struct{}
	for ctx.Err() == nil {
		err := recoverAgentAttempt(func() error {
			return attempt(ctx, func(instance string) {
				status.setInstance(instance)
				heldSince, delay, suspect = time.Time{}, agentReadmitFirstDelay, false
				if admitted {
					observeAgentReadmitted(ctx)
				}
				admitted = true
			}, true, nil)
		})
		if ctx.Err() != nil {
			return
		}
		var lost *errAgentPresenceLost
		if errors.As(err, &lost) {
			err = lost.cause
		}
		if err == nil {
			err = errors.New("agent presence ended without a cause")
		}
		admission.setReady(false)
		observeAgentLoss(ctx, err, status != nil && status.chatReceiver)
		wait := delay
		var inbox *agentInboxOutcomeError
		held := errors.As(err, &inbox) && inbox.outcome == messages.OutcomeHeld
		if held && heldSince.IsZero() {
			heldSince = now()
		} else if !held {
			heldSince = time.Time{}
		}
		var announce *agentAnnounceOutcomeError
		var terminal *agentPresenceTerminalError
		inboxRefusal := errors.As(err, &inbox) && !errors.As(err, &announce) && !errors.As(err, &terminal) && inbox.outcome != messages.OutcomeEnded
		switch {
		case agentPresenceFinal(ctx, err) && inboxRefusal && !suspect:
			// Confirm with one fresh Announce and InboxOpen at once.
			suspect = true
			wait = agentReadmitFirstDelay
		case agentPresenceFinal(ctx, err):
			admission.refuse(err, true)
			<-ctx.Done()
			return
		case agentVersionSkew(err) || held && now().Sub(heldSince) > agentPresenceLease:
			admission.refuse(err, false)
			wait = agentPresenceRefusedRetry
		default:
			suspect = false
			if delay *= 2; delay > agentReadmitMaxDelay {
				delay = agentReadmitMaxDelay
			}
			// A tool call waiting for re-admission keeps retries short and
			// starts the next one at once.
			wait = admission.retryDelay(wait)
			nudge = admission.wanted()
		}
		select {
		case <-ctx.Done():
			return
		case <-after(wait):
		case <-nudge:
		}
		nudge = nil
	}
}

// agentVersionSkew reports a runtime answer this helper's generated decoder
// refuses as an unknown field: the runtime is newer than this helper's copy.
// It does not pass with retries; only an updated connection fixes it.
func agentVersionSkew(err error) bool {
	var message *messages.Refusal
	var service *wire.Refusal
	var directory *applications.Refusal
	switch {
	case errors.As(err, &message):
		return message.Word == "unknown_field"
	case errors.As(err, &service):
		return service.Word == "unknown_field"
	case errors.As(err, &directory):
		return directory.Word == "unknown_field"
	}
	return false
}

// recoverAgentAttempt turns a panic inside one presence attempt into an
// ordinary loss, so the supervisor retries it.
func recoverAgentAttempt(attempt func() error) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			reportAgentPanic("presence", recovered)
			err = fmt.Errorf("agent presence panic: %v", recovered)
		}
	}()
	return attempt()
}

// runAgentPresence owns one child presence until cancellation or first loss of
// receiving. It never registers an application, retries enrollment or starts a
// vendor process. The caller joins it before releasing the child lifetime.
func runAgentPresence(ctx context.Context, directory agentPresenceDirectory, open agentInboxOpen, application, contextName string, ready func(string), beforeWithdraw ...func()) error {
	ticker := time.NewTicker(agentPresenceRenewal)
	defer ticker.Stop()
	return maintainAgentPresence(ctx, directory, open, application, contextName, ready, ticker.C, beforeWithdraw...)
}

func maintainAgentPresence(ctx context.Context, directory agentPresenceDirectory, open agentInboxOpen, application, contextName string, ready func(string), ticks <-chan time.Time, beforeWithdraw ...func()) (result error) {
	return maintainAgentPresenceWithPolicy(ctx, directory, open, application, contextName, ready, ticks, agentPresencePolicy{}, beforeWithdraw...)
}

func maintainAgentPresenceWithPolicy(ctx context.Context, directory agentPresenceDirectory, open agentInboxOpen, application, contextName string, ready func(string), ticks <-chan time.Time, policy agentPresencePolicy, beforeWithdraw ...func()) (result error) {
	if directory == nil || open == nil || application == "" || contextName == "" {
		return errors.New("agent presence requires an application, context and native receiving")
	}
	presence := wire.ApplicationPresence{Application: application, LeaseMs: agentPresenceLease.Milliseconds(),
		Contexts: []wire.ApplicationContext{{Name: contextName}}}
	if policy.interfaces != nil {
		presence.Interfaces = policy.interfaces()
	}
	if policy.now == nil {
		policy.now = time.Now
	}
	if policy.after == nil {
		policy.after = time.After
	}
	submitted := policy.now()
	call, cancel := context.WithTimeout(ctx, 2*time.Second)
	if err := policy.admission.finalError(); err != nil {
		cancel()
		return err
	}
	if policy.beforeAnnounce != nil {
		if err := policy.beforeAnnounce(call); err != nil {
			cancel()
			return err
		}
	}
	change, err := directory.Announce(call, presence)
	cancel()
	if err != nil {
		return fmt.Errorf("agent presence initial announce: %w", err)
	}
	if change.Outcome != wire.ApplicationOutcomeApplied || change.Instance == "" {
		return &agentAnnounceOutcomeError{outcome: change.Outcome}
	}
	presence.Instance = change.Instance
	defer func() {
		if policy.readmit && !agentPresenceFinal(ctx, result) {
			// Keep the server and admission; tools answer unavailable until
			// the caller opens this context again under a new instance.
			policy.admission.setReady(false)
			result = &errAgentPresenceLost{cause: result}
			// The old lease is left to lapse rather than withdrawn: a
			// withdraw reads to senders as receiver_withdrawn, the chat
			// closing, while this connection is re-admitting. The runtime
			// records the lapse, and the reopened inbox replaces it.
			return
		}
		if policy.readmit {
			// A refused admission or the end of the connection withdraws
			// this instance. The supervisor owns the connection: it keeps
			// the server, and the stdio owner records the process end.
			policy.admission.setReady(false)
		} else {
			// Record the initiating presence loss before local cancellation
			// closes stdio. Normal server completion has already recorded
			// its own cause.
			observeAgentDiagnostic(ctx, "presence_end", result)
			policy.admission.retire()
		}
		// The dedicated MCP caller cancels and joins its server before this
		// exact receiving association is withdrawn. Existing callers omit it.
		for _, stop := range beforeWithdraw {
			if stop != nil {
				stop()
			}
		}
		// Cleanup has its own finite budget after the child context is cancelled.
		cleanup, done := context.WithTimeout(context.Background(), 2*time.Second)
		defer done()
		withdrawn, err := directory.Withdraw(cleanup, application, presence.Instance)
		if err == nil && withdrawn.Outcome != wire.ApplicationOutcomeApplied && withdrawn.Outcome != wire.ApplicationOutcomeStale {
			err = fmt.Errorf("agent withdrawal: %s", withdrawn.Outcome.String())
		}
		if err != nil {
			result = errors.Join(result, fmt.Errorf("agent presence cleanup: %w", err))
		}
	}()
	call, cancel = context.WithTimeout(ctx, 2*time.Second)
	err = open(call, presence.Instance, contextName)
	cancel()
	if err != nil {
		return fmt.Errorf("agent presence initial inbox open: %w", err)
	}
	deadline := submitted.Add(time.Duration(presence.LeaseMs) * time.Millisecond)
	if !policy.now().Before(deadline) {
		return errors.New("agent presence initial admission exceeded lease")
	}
	if policy.instance != nil {
		policy.instance(presence.Instance)
	}
	policy.admission.confirm(deadline)
	if ready != nil {
		ready(presence.Instance)
	}
	var retry <-chan time.Time
	var expiry <-chan time.Time
	if policy.admission != nil {
		expiry = policy.after(deadline.Sub(policy.now()))
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-policy.routingChanged:
		case <-expiry:
			return errors.New("agent presence original confirmed lease expired")
		case <-retry:
			retry = nil
		case <-policy.admission.wanted():
			// A tool call waits for re-admission: renew now, unless this
			// admission is current and renews on its usual tick.
			if retry == nil && policy.admission.current() {
				continue
			}
			retry = nil
		case _, ok := <-ticks:
			if !ok {
				return errors.New("agent presence renewal ended")
			}
			if retry != nil {
				continue
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if policy.admission != nil && !policy.now().Before(deadline) {
			return errors.New("agent presence original confirmed lease expired")
		}
		generation := policy.admission.version()
		if policy.interfaces != nil {
			presence.Interfaces = policy.interfaces()
		}
		submitted = policy.now()
		call, cancel := agentPresenceCall(ctx, policy.now(), deadline, policy.admission != nil)
		if err := policy.admission.finalError(); err != nil {
			cancel()
			return err
		}
		if policy.beforeAnnounce != nil {
			if err := policy.beforeAnnounce(call); err != nil {
				cancel()
				return err
			}
		}
		change, err := directory.Announce(call, presence)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if ctx.Err() == nil && policy.admission != nil && transientAgentPresence(err) {
				policy.admission.setReady(false)
				retry = policy.after(policy.admission.retryDelay(agentPresenceRetryDelay))
				continue
			}
			return fmt.Errorf("agent presence renewal announce: %w", err)
		}
		if change.Outcome == wire.ApplicationOutcomeUnavailable && policy.admission != nil && ctx.Err() == nil {
			policy.admission.setReady(false)
			retry = policy.after(policy.admission.retryDelay(agentPresenceRetryDelay))
			continue
		}
		if change.Outcome != wire.ApplicationOutcomeApplied || change.Instance != presence.Instance {
			return &agentAnnounceOutcomeError{outcome: change.Outcome, renewal: true}
		}
		if policy.admission != nil && !policy.now().Before(deadline) {
			return errors.New("agent presence original confirmed lease expired")
		}
		call, cancel = agentPresenceCall(ctx, policy.now(), deadline, policy.admission != nil)
		err = open(call, presence.Instance, contextName)
		cancel()
		if err != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if ctx.Err() == nil && policy.admission != nil && (transientAgentPresence(err) || errors.Is(err, errMessagesInboxStatusUnknown)) {
				policy.admission.setReady(false)
				retry = policy.after(policy.admission.retryDelay(agentPresenceRetryDelay))
				continue
			}
			return fmt.Errorf("agent presence renewal inbox open: %w", err)
		}
		// An accepted Announce cannot extend the recovery window until fresh
		// InboxOpen proves the original service-pinned enrollment again.
		if policy.admission != nil && !policy.now().Before(deadline) {
			return errors.New("agent presence original confirmed lease expired")
		}
		nextDeadline := submitted.Add(time.Duration(presence.LeaseMs) * time.Millisecond)
		if !policy.admission.confirmVersion(nextDeadline, generation) {
			retry = policy.after(policy.admission.retryDelay(agentPresenceRetryDelay))
			continue
		}
		deadline = nextDeadline
		if policy.admission != nil {
			expiry = policy.after(deadline.Sub(policy.now()))
		}
		observeAgentEstablished(ctx)
	}
}
