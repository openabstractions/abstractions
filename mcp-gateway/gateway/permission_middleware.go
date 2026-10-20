package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	applications "github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	appmodel "github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	messages "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	jobapi "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
	modelwire "github.com/openabstractions/abstraction-model/go/abstraction/model/api"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
)

type permissionFailureKey struct{}
type permissionRetryKey struct{}
type permissionFailure struct {
	reason    string
	questions []string
}

const permissionMetaKey = "org.openabstractions/permission"

// Generated refusal words are structured errors from the service boundary.
// Model output, peer text and ordinary error strings are never parsed here.
func permissionErrorReason(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return ""
	}
	for cause := err; cause != nil; {
		if _, joined := cause.(interface{ Unwrap() []error }); joined {
			return ""
		}
		cause = errors.Unwrap(cause)
	}
	var rights *rightswire.Refusal
	var asks *askswire.Refusal
	var app *applications.Refusal
	var command *appmodel.Refusal
	var message *messages.Refusal
	var job *jobapi.Refusal
	var model *modelwire.Refusal
	var router *routerwire.Refusal
	var routerRefused *routerwire.ServiceError
	word := ""
	switch {
	case errors.As(err, &rights):
		word = rights.Word
	case errors.As(err, &asks):
		word = asks.Word
	case errors.As(err, &app):
		word = app.Word
	case errors.As(err, &command):
		word = command.Word
	case errors.As(err, &message):
		word = message.Word
	case errors.As(err, &job):
		word = job.Word
	case errors.As(err, &model):
		word = model.Word
	case errors.As(err, &router):
		word = router.Word
	case errors.As(err, &routerRefused) && routerRefused.Code == routerwire.ServiceErrorCodeForbidden:
		// The router's policy refusal carries its rights words after its
		// sentence: "router operation not permitted rights:not_granted
		// rights:question_pending:<id>".
		fields := strings.Fields(routerRefused.Message)
		if at := slices.Index(fields, "rights:not_granted"); at >= 0 {
			word = strings.Join(fields[at:], " ")
		}
	}
	// Decoder failures and lost/unknown effects supply no permission to retry.
	if fields := strings.Fields(word); len(fields) > 0 && fields[0] == "rights:not_granted" {
		return word
	}
	return ""
}

func pendingToolQuestions(result *mcp.CallToolResult, failure *permissionFailure) []string {
	if len(failure.questions) != 0 {
		return failure.questions
	}
	if failure.reason != "" {
		return QuestionsPending(failure.reason)
	}
	if result == nil || result.StructuredContent == nil {
		return nil
	}
	raw, err := json.Marshal(result.StructuredContent)
	if err != nil {
		return nil
	}
	var refusal struct{ Outcome, Reason string }
	if json.Unmarshal(raw, &refusal) != nil {
		return nil
	}
	switch refusal.Outcome {
	case "not_permitted", "forbidden":
		return QuestionsPending(refusal.Reason)
	}
	return nil
}

func permissionToolMiddleware(backend Backend, slots chan struct{}) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
			if method == "tools/list" {
				result, err := next(ctx, method, request)
				if err != nil {
					return result, err
				}
				return permissionCatalogueResult(result), nil
			}
			if method != "tools/call" {
				return next(ctx, method, request)
			}
			select {
			case slots <- struct{}{}:
				defer func() { <-slots }()
			default:
				return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: "gateway: concurrent request limit reached"}}}, nil
			}
			ctx = context.WithValue(ctx, nativeToolSlotKey{}, true)
			// A waiting tool's budget follows its wait_ms (wait.go); every
			// other call has two minutes.
			budget := 2 * time.Minute
			if params, ok := request.GetParams().(*mcp.CallToolParamsRaw); ok && params != nil {
				if waiting, ok := waitingBudget(params.Name, params.Arguments); ok {
					budget = waiting
				}
			}
			ctx, cancel := context.WithTimeout(ctx, budget)
			defer cancel()
			// The one permitted retry runs as the same call: it keeps what the
			// first attempt chose, such as its request key.
			ctx = context.WithValue(ctx, callScopeKey{}, &callScope{})
			invoke := func(permissionRetry bool) (mcp.Result, error, []string) {
				if err := ctx.Err(); err != nil {
					return nil, err, nil
				}
				failure := new(permissionFailure)
				call := context.WithValue(ctx, permissionFailureKey{}, failure)
				if permissionRetry {
					call = context.WithValue(call, permissionRetryKey{}, true)
				}
				result, err := next(call, method, request)
				tool, _ := result.(*mcp.CallToolResult)
				return result, err, pendingToolQuestions(tool, failure)
			}
			result, err, ids := invoke(false)
			if err != nil || len(ids) == 0 {
				return result, err
			}
			verdict := "waiting"
			if waiter, ok := backend.(PermissionQuestionBackend); ok {
				verdict, err = waiter.AwaitPermissionQuestions(ctx, ids)
				if err != nil {
					if errors.Is(err, context.DeadlineExceeded) {
						return permissionToolResult("waiting", ids), nil
					}
					return result, err
				}
			}
			if verdict == "allowed" {
				result, err, ids = invoke(true) // identical request, at most one retry
				if err != nil || len(ids) == 0 {
					return result, err
				}
				verdict = "waiting"
			}
			return permissionToolResult(verdict, ids), nil
		}
	}
}
