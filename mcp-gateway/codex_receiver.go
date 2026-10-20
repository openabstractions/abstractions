package main

import (
	"context"
	"errors"
	messageswire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"path/filepath"
	"regexp"
	"time"
	"unicode/utf8"

	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

var codexQueueThread = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

var codexWakeCooldown = 5 * time.Second

// While a thread holds an unconsumed pointer, a waiting wake rechecks at this
// cadence. The held pointer's consumption changes no header revision.
var codexWakeBusyRecheck = 5 * time.Second
var codexObservationRecovery = 30 * time.Second

// A fresh wake that has waited behind an unconsumed pointer queued at least
// this long ago is reported as unconfirmed. A woken Codex turn reads within
// seconds; the pointer's ledger lease is 30 minutes.
var codexWakeUnconfirmedAfter = time.Minute

// Only the connection owner supplies this classification while its original
// confirmed lease remains current. It never represents a queue-driver result.
var errCodexReceivingSuspended = errors.New("Codex native receiving suspended within original lease")

// Headers observation authenticates this native caller independently of its
// presence lease. An affirmative refusal ends this run; the connection owner
// confirms it under current admission before declaring the observation final.
var errCodexObservationForbidden = errors.New("Codex inbox observation forbidden; check enrollment and reconnect")

// This private wrapper marks rejection before the CLI driver was invoked.
type codexQueueNotStarted struct{ cause error }

func (e *codexQueueNotStarted) Error() string { return e.cause.Error() }
func (e *codexQueueNotStarted) Unwrap() error { return e.cause }

type codexReceiverCLI interface {
	Program() string
	Check(context.Context) error
}

type codexReceiverQueueError struct{ cause error }

func (e *codexReceiverQueueError) Error() string {
	return "Codex queue failed; outcome unknown; no automatic retry"
}
func (e *codexReceiverQueueError) Unwrap() error { return e.cause }

// A binding is supplied by the connection owner, never by model tool arguments.
// Native context and vendor thread are distinct. Hook session_id alone does not
// prove a thread binding, especially for forks and subagents.
type codexReceiverBinding struct {
	context, thread, directory string
	cli                        codexReceiverCLI
	current                    func(context.Context) error
	observe                    func(string)
}

func (b codexReceiverBinding) note(event string) {
	if b.observe != nil {
		b.observe(event)
	}
}

func (b codexReceiverBinding) check(ctx context.Context) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if !agentRoutingContext.MatchString(b.context) || !codexQueueThread.MatchString(b.thread) || !filepath.IsAbs(b.directory) || b.cli == nil || b.current == nil {
		return errors.New("Codex receiving requires an owned connection and thread binding")
	}
	if err := b.current(ctx); err != nil {
		return err
	}
	return b.cli.Check(ctx)
}

// Dispatch carries only the OA pointer. Failure has uncertain effect and ends
// this receiver; no fallback, alternate thread, or automatic retry is admitted.
func (b codexReceiverBinding) dispatch(ctx context.Context, run codexSetupRunner) error {
	if err := b.check(ctx); err != nil {
		return err
	}
	call, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	result, err := run(call, b.cli.Program(), b.directory, codexQueueArguments(b.thread, messagesHookPointer))
	var notStarted *codexQueueNotStarted
	if errors.As(err, &notStarted) {
		return notStarted.cause
	}
	if err != nil || result.Code != 0 || len(result.Diagnostic) != 0 {
		return &codexReceiverQueueError{cause: errors.Join(call.Err(), err)}
	}
	err = b.check(ctx)
	if errors.Is(err, errCodexReceivingSuspended) {
		// A positively accepted queue remains consumed across later suspension.
		// The loop records its revision/cooldown before awaiting fresh admission.
		return nil
	}
	if err != nil {
		return &codexQueuedThenLost{cause: err}
	}
	return nil
}

// codexQueuedThenLost is a binding loss observed after an accepted queue. The
// queued revision stays consumed.
type codexQueuedThenLost struct{ cause error }

func (e *codexQueuedThenLost) Error() string { return e.cause.Error() }
func (e *codexQueuedThenLost) Unwrap() error { return e.cause }

// The MCP connection owns this synchronous loop and must cancel and join it
// before withdrawing its inbox or releasing the held CLI. Header revisions are
// observation tokens; this path never reads bodies or acknowledges a cursor.
func runCodexInboxReceiver(ctx context.Context, binding codexReceiverBinding, watch gateway.MessagesWatchBackend, run codexSetupRunner, now func() time.Time, pause func(context.Context, time.Duration) error) error {
	return runCodexInboxReceiverProgress(ctx, binding, watch, run, now, pause, new(codexReceiverProgress))
}

// codexReceiverProgress outlives one receiver run within a connection. A
// restarted run keeps its read position and never queues a revision already
// queued, or one whose queue outcome is unknown, again.
type codexReceiverProgress struct {
	revision, submitted string
	lastWake            time.Time
	load                func(context.Context) (string, error)
}

func runCodexInboxReceiverProgress(ctx context.Context, binding codexReceiverBinding, watch gateway.MessagesWatchBackend, run codexSetupRunner, now func() time.Time, pause func(context.Context, time.Duration) error, progress *codexReceiverProgress) error {
	if watch == nil || run == nil || now == nil || pause == nil || progress == nil {
		return errors.New("Codex receiving dependencies unavailable")
	}
	revision, submitted := progress.revision, progress.submitted
	if progress.load != nil {
		kept, err := progress.load(ctx)
		if err != nil {
			return err
		}
		if kept > submitted {
			submitted = kept
		}
	}
	waitMs := int64(25000)
	lastWake := progress.lastWake
	defer func() {
		progress.revision, progress.submitted = revision, submitted
		progress.lastWake = lastWake
	}()
	var recoveryEnd time.Time
	held := ""           // the last logged reason a fresh wake is waiting
	unconfirmed := false // wake_unconfirmed was noted for the waiting wake
	suspended := func(err error) (bool, error) {
		if !errors.Is(err, errCodexReceivingSuspended) {
			return false, err
		}
		binding.note("recovering_unavailable")
		return true, pause(ctx, time.Second)
	}
	for {
		if err := binding.check(ctx); err != nil {
			if wait, err := suspended(err); wait {
				if err != nil {
					return err
				}
				continue
			}
			binding.note("binding_unavailable")
			return err
		}
		call := ctx
		cancel := func() {}
		if !recoveryEnd.IsZero() {
			remaining := recoveryEnd.Sub(now())
			if remaining <= 0 {
				binding.note("observation_recovery_expired")
				return errors.New("Codex inbox observation recovery expired")
			}
			call, cancel = context.WithTimeout(ctx, remaining)
		} else {
			binding.note("watching")
		}
		page, err := watch.MessagesWatch(call, gateway.MessagesWatchInput{Revision: revision, WaitMs: waitMs})
		callErr := call.Err()
		cancel()
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !recoveryEnd.IsZero() && (callErr != nil || !now().Before(recoveryEnd)) {
			binding.note("observation_recovery_expired")
			return errors.New("Codex inbox observation recovery expired")
		}
		if err != nil {
			binding.note("observation_unavailable")
			return err
		}
		if (page.Outcome == messageswire.OutcomeUnavailable || page.Outcome == messageswire.OutcomeUnknown) && page.Context == "" && page.Revision == "" && page.PendingUntilUnixMs == 0 && page.ReplyUntilUnixMs == 0 {
			// This read has no effect to reconcile. Keep the original position and
			// retry only while the exact native/CLI binding still checks current.
			if err := binding.check(ctx); err != nil {
				if wait, err := suspended(err); wait {
					if err != nil {
						return err
					}
					continue
				}
				binding.note("binding_unavailable")
				return err
			}
			if recoveryEnd.IsZero() {
				recoveryEnd = now().Add(codexObservationRecovery)
			}
			binding.note("recovering_" + page.Outcome.String())
			waitMs = 0
			if err := pause(ctx, time.Second); err != nil {
				return err
			}
			continue
		}
		if page.Outcome != messageswire.OutcomePage || !validMessageWake(page.Wake) || page.Context != binding.context || !utf8.ValidString(page.Revision) || len(page.Revision) > 128 || page.PendingUntilUnixMs < 0 || page.ReplyUntilUnixMs < 0 {
			event := "observation_invalid"
			if page.Outcome == messageswire.OutcomeForbidden {
				event = "observation_forbidden"
			}
			if page.Outcome == messageswire.OutcomePage && page.Context != binding.context {
				event = "context_changed"
			}
			binding.note(event)
			if page.Outcome == messageswire.OutcomeForbidden {
				return errCodexObservationForbidden
			}
			return errors.New("Codex inbox observation unavailable")
		}
		binding.note("observed")
		recoveryEnd = time.Time{}
		revision = page.Revision
		at := now()
		waitMs = 25000
		fresh := page.Wake != "" && page.Wake > submitted
		if fresh && (lastWake.IsZero() || at.Sub(lastWake) >= codexWakeCooldown) {
			if err := binding.dispatch(ctx, run); err != nil {
				if errors.Is(err, errCodexWakeStale) || errors.Is(err, errCodexWakeBusy) {
					// A busy thread still holds an unconsumed pointer; a stale wake
					// was already queued by another owner. The wake stays pending:
					// the loop retries it until it is queued, consumed or expired.
					event := "wake_stale"
					if errors.Is(err, errCodexWakeBusy) {
						event = "wake_busy"
						// Consuming the held pointer changes the ledger, which
						// moves no header revision; recheck on a bounded cadence.
						waitMs = codexWakeBusyRecheck.Milliseconds()
					}
					if held != event {
						binding.note(event)
						held = event
					}
					// A pointer queued longer ago than the bound, or by an
					// earlier connection, is still unread: Codex did not take
					// up that wake, and this message waits behind it.
					if event == "wake_busy" && !unconfirmed && (lastWake.IsZero() || at.Sub(lastWake) >= codexWakeUnconfirmedAfter) {
						binding.note("wake_unconfirmed")
						unconfirmed = true
					}
					if progress.load != nil {
						kept, e := progress.load(ctx)
						if e != nil {
							return e
						}
						if kept > submitted {
							submitted = kept
						}
					}
					if err := pause(ctx, time.Second); err != nil {
						return err
					}
					continue
				}
				if wait, err := suspended(err); wait {
					if err != nil {
						return err
					}
					waitMs = 0
					continue
				}
				var queue *codexReceiverQueueError
				var queued *codexQueuedThenLost
				if errors.As(err, &queue) || errors.As(err, &queued) {
					// The queue may have taken effect: never queue this
					// revision again, in this run or a restarted one.
					submitted, lastWake = page.Wake, at
				} else {
					// The queue never started; the restarted run retries this wake.
					binding.note("queue_not_started")
				}
				return err
			}
			submitted, lastWake = page.Wake, at
			held, unconfirmed = "", false
		} else if fresh {
			// A newly eligible revision during cooldown must not disappear into
			// a fresh 25-second long poll. Recheck it on the bounded local cadence.
			waitMs = 0
		}
		// Preserve a finite cadence even when the host returns immediately.
		if err := pause(ctx, time.Second); err != nil {
			return err
		}
	}
}
