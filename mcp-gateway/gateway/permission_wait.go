package gateway

import (
	"context"
	"errors"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	facade "github.com/openabstractions/abstraction-facade/go/client"
)

// retryReserve is the part of a tool call's budget kept for the one retry
// after the person allows.
const retryReserve = 20 * time.Second

func permissionObservationQuestionIDs(out askswire.PermissionObservation) []string {
	if out.Answer != nil && out.Answer.ID != "" {
		return []string{out.Answer.ID}
	}
	return nil
}

// Person-facing sentences a tool result carries for a model to relay. A
// popup already asks the person; these say what the call is waiting for.
const (
	questionWaiting = "OpenAbstractions is waiting for your permission. Open Questions in the OpenAbstractions Panel to answer the request, then continue this operation."
	questionRefused = "The person chose not to allow this for now; OpenAbstractions can ask again in 10 minutes."
	questionBlocked = "The person blocked this; they can change it on the Permissions page of the OpenAbstractions Panel."
)

// QuestionsPending returns the ids of the first-use questions a refusal
// reason names as pending, in order (inference INF-W11).
func QuestionsPending(reason string) []string {
	return asks.QuestionsPending(reason)
}

// WaitForAnswer observes a pending permission observation until the person
// answers, the permission settles, or ctx ends; at the deadline it returns
// the last pending observation and no error. observe waits at most its
// wait_ms argument. Messages request_access and inference both wait through
// it (asks ASK-A14).
func WaitForAnswer(ctx context.Context, out askswire.PermissionObservation, observe func(context.Context, int64) (askswire.PermissionObservation, error)) (askswire.PermissionObservation, error) {
	for out.Outcome == askswire.PermissionOutcomePending {
		if ctx.Err() != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				return out, nil
			}
			return out, ctx.Err()
		}
		wait := int64(30000)
		if deadline, ok := ctx.Deadline(); ok {
			left := time.Until(deadline).Milliseconds()
			if left <= 0 {
				return out, nil
			}
			wait = min(wait, left)
		}
		next, err := observe(ctx, wait)
		if err != nil {
			if errors.Is(ctx.Err(), context.DeadlineExceeded) || ctx.Err() == nil && errors.Is(err, context.DeadlineExceeded) {
				return out, nil
			}
			return askswire.PermissionObservation{}, err
		}
		out = next
	}
	return out, nil
}

// questionVerdict is what waiting for a refusal's questions found.
type questionVerdict int

const (
	verdictAllowed questionVerdict = iota
	verdictWaiting
	verdictRefused
	verdictBlocked
)

// permissionObservationVerdict gives the service's current policy result
// precedence over an answer retained from an earlier policy revision.
func permissionObservationVerdict(out askswire.PermissionObservation) questionVerdict {
	switch {
	case out.Outcome == askswire.PermissionOutcomePermitted && out.PolicyRevision != "":
		return verdictAllowed
	case out.Outcome == askswire.PermissionOutcomeDenied && out.PolicyRevision != "":
		return verdictBlocked
	case out.Outcome == askswire.PermissionOutcomePending || out.Answer != nil && out.Answer.Pending:
		return verdictWaiting
	case out.Outcome == askswire.PermissionOutcomeAnswered && out.Answer != nil && out.Answer.Option == "never":
		return verdictBlocked
	case out.Outcome == askswire.PermissionOutcomeAnswered && out.Answer != nil && out.Answer.Option == "refuse":
		return verdictRefused
	default:
		return verdictWaiting
	}
}

type permissionQuestionObserver interface {
	ObserveQuestionContext(context.Context, string, int64) (askswire.PendingQuestion, error)
}

func waitForPermissionQuestion(ctx context.Context, permission permissionQuestionObserver, id string) (askswire.PermissionObservation, error) {
	return WaitForAnswer(ctx, askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending}, func(ctx context.Context, waitMS int64) (askswire.PermissionObservation, error) {
		question, err := permission.ObserveQuestionContext(ctx, id, waitMS)
		return question.Observation, err
	})
}

// awaitQuestions waits within ctx, less retryReserve, for the person's answer
// to each question a refusal named. Every question needs an Allow answer or
// current permitted result. Historical answers retain their meaning unless
// current policy permits. An earlier runtime without ObserveQuestion reads waiting.
func (o *OA) awaitQuestions(ctx context.Context, ids []string) (questionVerdict, error) {
	if len(ids) == 0 {
		return verdictWaiting, nil
	}
	wait := ctx
	if deadline, ok := ctx.Deadline(); ok {
		var cancel context.CancelFunc
		wait, cancel = context.WithDeadline(ctx, deadline.Add(-retryReserve))
		defer cancel()
	}
	permission, err := o.machine.ResolveAsksPermission(wait, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return verdictWaiting, nil
	}
	verdict := verdictAllowed
	for _, id := range ids {
		out, err := waitForPermissionQuestion(wait, permission, id)
		if err != nil {
			if ctx.Err() != nil {
				return verdictWaiting, ctx.Err()
			}
			return verdictWaiting, nil
		}
		decision := permissionObservationVerdict(out)
		if decision == verdictBlocked || decision == verdictRefused {
			return decision, nil
		}
		if decision != verdictAllowed {
			verdict = verdictWaiting
		}
	}
	return verdict, nil
}

// PermissionQuestionBackend is an optional product adapter surface. The
// observer remains bound to this backend's native caller; IDs grant no rights.
// The result is allowed, waiting, refused, or blocked.
type PermissionQuestionBackend interface {
	AwaitPermissionQuestions(context.Context, []string) (string, error)
}

func (o *OA) AwaitPermissionQuestions(ctx context.Context, ids []string) (string, error) {
	v, err := o.awaitQuestions(ctx, ids)
	return []string{"allowed", "waiting", "refused", "blocked"}[v], err
}
