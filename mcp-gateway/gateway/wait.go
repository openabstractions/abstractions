package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-identity/listen"
)

// Long-running tools share one waiting rule. wait_ms is how long one call
// follows the work before it answers working with a handle; with no value the
// call follows defaultCallWait, short so the person sees progress between
// calls. The agent may set any wait. The MCP client's own call timeout is the
// ceiling: Codex 60 s by default, OpenCode 30 min as OA configures it, Claude
// Code its MCP_TOOL_TIMEOUT.
var defaultCallWait = 20 * time.Second

// callStartRoom is added to a call's budget for the person's first-use
// answer and the start; callFinishRoom for reading and saving the result.
// Tests shorten them.
var (
	callStartRoom  = 60 * time.Second
	callFinishRoom = 5 * time.Second
)

// maxCallWait bounds wait_ms only against overflow.
const maxCallWait = 24 * time.Hour

// waitDescription is the few words each waiting tool's description carries.
const waitDescription = "Waits wait_ms (default 20000; Codex ends a call at 60 s), then answers with progress and a handle to call again with."

// callWait is the follow time a call asked for, or def.
func callWait(ms int64, def time.Duration) time.Duration {
	if ms <= 0 {
		return def
	}
	return time.Duration(min(ms, int64(maxCallWait/time.Millisecond))) * time.Millisecond
}

// waitArgument reads wait_ms from raw call arguments; anything unreadable
// leaves def, and the tool's own decoding refuses it.
func waitArgument(raw json.RawMessage, def time.Duration) time.Duration {
	var args struct {
		WaitMS int64 `json:"wait_ms"`
	}
	//unchecked: an unreadable wait keeps the default; the handler refuses it
	_ = json.Unmarshal(raw, &args)
	return callWait(args.WaitMS, def)
}

// waitingBudget is the whole budget of a call to a waiting tool, or false for
// a tool without wait_ms.
func waitingBudget(name string, raw json.RawMessage) (time.Duration, bool) {
	switch name {
	case ToolImageGenerate, ToolComplete:
		return waitArgument(raw, defaultCallWait) + callStartRoom + callFinishRoom, true
	case ToolVideoGenerate:
		return min(waitArgument(raw, defaultCallWait), maxVideoWait) + callStartRoom + videoFinishRoom, true
	case ToolJobStatus, ToolJobSubmit:
		// Without wait_ms the call answers at once and keeps two minutes.
		if wait := waitArgument(raw, 0); wait > 0 {
			return wait + callStartRoom + videoFinishRoom, true
		}
		return 0, false
	case ToolApplicationsCall:
		// An application command is one synchronous call: wait_ms is its
		// whole budget, two minutes unless the agent sets it.
		return waitArgument(raw, appCommandWait) + callFinishRoom, true
	case ToolApplicationsOpen:
		// An open waits for the person's answer (openPersonWait), then
		// follows the start for wait_ms.
		_, _, wait, err := openArguments(raw)
		if err != nil {
			wait = openWaitDefault
		}
		return openPersonWait(wait) + wait + callFinishRoom, true
	}
	return 0, false
}

// appCommandWait is an application command's budget when wait_ms is unset.
const appCommandWait = 2 * time.Minute

// boundedCallWait is the budget of one backend call inside a tool when the
// tool sets none.
const boundedCallWait = 2 * time.Minute

type boundedBudgetKey struct{}

// withBoundedBudget gives the backend calls a tool makes through bounded a
// budget other than boundedCallWait: an open's wait for the person.
func withBoundedBudget(ctx context.Context, budget time.Duration) context.Context {
	return context.WithValue(ctx, boundedBudgetKey{}, budget)
}

// boundedBudget is the budget bounded gives one backend call.
func boundedBudget(ctx context.Context) time.Duration {
	if budget, ok := ctx.Value(boundedBudgetKey{}).(time.Duration); ok && budget > 0 {
		return budget
	}
	return boundedCallWait
}

type callWaitKey struct{}

// withCallWait keeps the call's follow time for the backend.
func withCallWait(ctx context.Context, wait time.Duration) context.Context {
	return context.WithValue(ctx, callWaitKey{}, wait)
}

// followUntil is when this call stops following an operation, or false when
// the call carries no wait. A following tool sizes each Observe to end by
// then: its last Observe returns its page while the call is still
// connected, and the provider keeps the operation for the agent's resume
// with the handle (INF-Q3). An Observe cut off mid-wait reads to the
// provider as a caller that left.
func followUntil(ctx context.Context) (time.Time, bool) {
	switch wait := ctx.Value(callWaitKey{}).(type) {
	case time.Duration:
		return time.Now().Add(wait), true
	case time.Time:
		return wait, true
	default:
		return time.Time{}, false
	}
}

// Native Start answers before the frame's reply deadline while activation
// continues. Only that definite refusal is retryable: no inference operation
// or chat identity was committed. Keep the same request and remaining wait.
func startCompletionAwaitingHost[A any](ctx context.Context, local bool, start func() (A, error), activating func(A) bool) (context.Context, A, error) {
	until, bounded := followUntil(ctx)
	a, err := start()
	if !local || !bounded || err != nil || !activating(a) {
		return ctx, a, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		if finish := deadline.Add(-callFinishRoom); finish.Before(until) {
			until = finish
		}
	}
	follow := context.WithValue(ctx, callWaitKey{}, until)
	const retryPause = 250 * time.Millisecond
	for time.Until(until) > listen.DefaultFrameTimeout+retryPause {
		timer := time.NewTimer(retryPause)
		select {
		case <-ctx.Done():
			timer.Stop()
			return follow, a, nil
		case <-timer.C:
		}
		if ctx.Err() != nil || time.Until(until) < listen.DefaultFrameTimeout {
			break
		}
		a, err = start()
		if err != nil || !activating(a) {
			return follow, a, err
		}
	}
	return follow, a, nil
}

func activatingCompletionHost(reason string) bool {
	host, ok := strings.CutPrefix(reason, "activating:")
	return ok && host != ""
}

// observeWait is the wait of the next Observe: page, shortened to end at
// until when bounded. False means the follow time is spent.
func observeWait(until time.Time, bounded bool, page time.Duration) (time.Duration, bool) {
	if !bounded {
		return page, true
	}
	left := time.Until(until)
	if left <= 0 {
		return 0, false
	}
	return min(page, left), true
}

type progressKey struct{}

type progressSink func(message string, done, total float64)

// withProgress sends MCP progress notifications for the call when the client
// gave a progress token.
func withProgress(ctx context.Context, request *mcp.CallToolRequest) context.Context {
	if request == nil || request.Params == nil || request.Session == nil {
		return ctx
	}
	token := request.Params.GetProgressToken()
	if token == nil {
		return ctx
	}
	session := request.Session
	return context.WithValue(ctx, progressKey{}, progressSink(func(message string, done, total float64) {
		//unchecked: progress is advisory; the answer carries the same line
		_ = session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{ProgressToken: token, Message: message, Progress: done, Total: total})
	}))
}

// reportProgress sends one progress notification when the call has a sink.
func reportProgress(ctx context.Context, message string, done, total float64) {
	if notify, ok := ctx.Value(progressKey{}).(progressSink); ok {
		notify(message, done, total)
	}
}

// progressLine is one short line of progress: steps when the work reports
// them, and the time this call has followed it.
func progressLine(what string, done, total int64, unit string, since time.Duration) (string, float64, float64) {
	seconds := int64(since.Round(time.Second) / time.Second)
	if total > 0 {
		if unit == "" {
			unit = "step"
			return fmt.Sprintf("%s: %s %d of %d, %d s so far", what, unit, done, total, seconds), float64(done), float64(total)
		}
		return fmt.Sprintf("%s: %d of %d %s, %d s so far", what, done, total, unit, seconds), float64(done), float64(total)
	}
	return fmt.Sprintf("%s: %d s so far", what, seconds), float64(seconds), 0
}
