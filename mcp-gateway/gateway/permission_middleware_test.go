package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type permissionCorpusBackend struct {
	fixtureBackend
	MessagesBackend
	MessagesReplyBackend
	MessagesPrepareBackend
	MessagesAccessBackend
	AsksBackend
	AsksWatchBackend
	MessagesWatchBackend
	AppModelBackend
	verdict string
	waits   int
	wait    func(context.Context) (string, error)
}

func (b *permissionCorpusBackend) AwaitPermissionQuestions(ctx context.Context, ids []string) (string, error) {
	b.waits++
	if !reflect.DeepEqual(ids, []string{"q1"}) {
		return "", errors.New("wrong question")
	}
	if _, ok := ctx.Deadline(); !ok {
		return "", errors.New("unbounded wait")
	}
	if b.wait != nil {
		return b.wait(ctx)
	}
	return b.verdict, ctx.Err()
}

// Use the real advertised catalogue. Every tool passes the same refusal,
// observation and one-retry owner when carrying an explicit pre-effect refusal;
// no tool-name allowlist selects behavior.
func TestPermissionMiddlewareAllToolCatalogue(t *testing.T) {
	b := &permissionCorpusBackend{}
	_, session := connected(t, b, "permission-corpus", "2025-11-25")
	listed, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) < 20 {
		t.Fatal("incomplete catalogue", len(listed.Tools))
	}
	for _, tool := range listed.Tools {
		for _, verdict := range []string{"allowed", "waiting", "refused", "blocked"} {
			t.Run(tool.Name+"/"+verdict, func(t *testing.T) {
				b.verdict, b.waits = verdict, 0
				request := &mcp.CallToolRequest{Params: &mcp.CallToolParamsRaw{Name: tool.Name, Arguments: json.RawMessage(`{"request_key":"stable","handle":"same"}`)}}
				before, _ := json.Marshal(request.Params)
				calls, effects := 0, 0
				handler := permissionToolMiddleware(b, make(chan struct{}, 8))(func(ctx context.Context, method string, got mcp.Request) (mcp.Result, error) {
					calls++
					if got != request || method != "tools/call" {
						t.Fatal("retry changed request")
					}
					after, _ := json.Marshal(request.Params)
					if string(before) != string(after) {
						t.Fatal("retry changed immutable arguments")
					}
					if calls == 1 {
						return &mcp.CallToolResult{StructuredContent: map[string]any{"outcome": "forbidden", "reason": "rights:not_granted rights:question_pending:q1"}}, nil
					}
					effects++
					return &mcp.CallToolResult{StructuredContent: map[string]any{"outcome": "accepted"}}, nil
				})
				result, err := handler(t.Context(), "tools/call", request)
				if err != nil || b.waits != 1 {
					t.Fatal(err, b.waits)
				}
				if verdict == "allowed" {
					if calls != 2 || effects != 1 {
						t.Fatal(calls, effects)
					}
				} else {
					if calls != 1 || effects != 0 {
						t.Fatal(calls, effects)
					}
					state := result.(*mcp.CallToolResult).Meta[permissionMetaKey].(map[string]any)
					want := map[string]string{"waiting": "awaiting_permission", "refused": "permission_refused", "blocked": "permission_blocked"}[verdict]
					if state["outcome"] != want || !reflect.DeepEqual(state["question_ids"], []string{"q1"}) {
						t.Fatal(state)
					}
				}
			})
		}
	}
}

func TestPermissionMiddlewareCancellationAndSecondQuestionNeverReplayAgain(t *testing.T) {
	for _, cancelWait := range []bool{false, true} {
		ctx, cancel := context.WithCancel(t.Context())
		b := &permissionCorpusBackend{verdict: "allowed"}
		if cancelWait {
			b.wait = func(context.Context) (string, error) { cancel(); return "allowed", nil }
		}
		calls := 0
		handler := permissionToolMiddleware(b, make(chan struct{}, 8))(func(context.Context, string, mcp.Request) (mcp.Result, error) {
			calls++
			return &mcp.CallToolResult{StructuredContent: map[string]any{"outcome": "forbidden", "reason": "rights:not_granted rights:question_pending:q1"}}, nil
		})
		out, err := handler(ctx, "tools/call", &mcp.CallToolRequest{})
		cancel()
		if cancelWait {
			if !errors.Is(err, context.Canceled) || calls != 1 || b.waits != 1 {
				t.Fatal(err, calls, b.waits)
			}
		} else if err != nil || calls != 2 || b.waits != 1 || out.(*mcp.CallToolResult).Meta[permissionMetaKey] == nil {
			t.Fatal(out, err, calls, b.waits)
		}
	}
}

func TestPermissionMiddlewareNeverRetriesUnknownOrPeerText(t *testing.T) {
	for _, outcome := range []string{"unknown", "unavailable", "accepted", "denied", "refused", "forbidden"} {
		b := &permissionCorpusBackend{verdict: "allowed"}
		calls := 0
		handler := permissionToolMiddleware(b, make(chan struct{}, 8))(func(context.Context, string, mcp.Request) (mcp.Result, error) {
			calls++
			reason := "rights:question_pending:q1"
			if outcome == "forbidden" {
				reason = "rights:not_granted"
			}
			return &mcp.CallToolResult{StructuredContent: map[string]any{"outcome": outcome, "reason": reason}, Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"forbidden","reason":"rights:question_pending:q1"}`}}}, nil
		})
		_, err := handler(t.Context(), "tools/call", &mcp.CallToolRequest{})
		if err != nil || calls != 1 || b.waits != 0 {
			t.Fatal(outcome, err, calls, b.waits)
		}
	}
}

func TestPermissionErrorMetadataDoesNotOverrideLostOrCancelledOutcome(t *testing.T) {
	refused := &rightswire.Refusal{Word: "rights:not_granted rights:question_pending:q1"}
	for _, err := range []error{errors.New(refused.Error()), errors.Join(refused, errors.New("effect outcome lost")), errors.Join(refused, context.Canceled), &rightswire.Refusal{Word: "unknown_field rights:question_pending:q1"}} {
		if reason := permissionErrorReason(err); reason != "" {
			t.Fatal("uncertain error became a retryable permission refusal", err, reason)
		}
	}
	if reason := permissionErrorReason(refused); reason != refused.Word {
		t.Fatal(reason)
	}
}

type permissionAdmissionBackend struct {
	fixtureBackend
	ready                  bool
	checks, calls, effects int
}

func (b *permissionAdmissionBackend) BeginNativeTool(ctx context.Context, _ string) (context.Context, func(), bool) {
	b.checks++
	return ctx, func() {}, b.ready
}
func (b *permissionAdmissionBackend) Complete(context.Context, CompleteInput) (CompleteOutput, error) {
	b.calls++
	if b.calls == 1 {
		return CompleteOutput{Outcome: "not_permitted", Reason: "rights:not_granted rights:question_pending:q1"}, nil
	}
	b.effects++
	return CompleteOutput{Outcome: "completed"}, nil
}
func (b *permissionAdmissionBackend) AwaitPermissionQuestions(context.Context, []string) (string, error) {
	b.ready = false
	return "allowed", nil
}

func TestPermissionMiddlewareRetryReentersNativeAdmission(t *testing.T) {
	b := &permissionAdmissionBackend{ready: true}
	_, session := connected(t, b, "permission-admission", "2025-11-25")
	out, _ := call[CompleteOutput](t, session, ToolComplete, map[string]any{"model": "fixture", "prompt": "hello", "hosting": "local"})
	if !out.IsError || b.checks != 2 || b.calls != 1 || b.effects != 0 {
		t.Fatal("permission retry bypassed changed native admission", out, b.checks, b.calls, b.effects)
	}
}

type permissionErrorBackend struct {
	fixtureBackend
	calls, waits int
}

func (b *permissionErrorBackend) Models(context.Context, ModelsInput) (ModelsOutput, error) {
	b.calls++
	return ModelsOutput{}, &rightswire.Refusal{Word: "rights:not_granted rights:question_pending:q1"}
}
func (b *permissionErrorBackend) AwaitPermissionQuestions(context.Context, []string) (string, error) {
	b.waits++
	return "waiting", nil
}

func TestPermissionMiddlewareCapturesGeneratedErrorBeforeSDKFormatting(t *testing.T) {
	b := new(permissionErrorBackend)
	_, session := connected(t, b, "typed-error", "2025-11-25")
	out, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolModels, Arguments: map[string]any{}})
	if err != nil || out.IsError || b.calls != 1 || b.waits != 1 || out.Meta[permissionMetaKey] == nil {
		t.Fatal(out, err, b.calls, b.waits)
	}
	if !strings.Contains(out.Content[len(out.Content)-1].(*mcp.TextContent).Text, `"question_ids":["q1"]`) {
		t.Fatal(out.Content)
	}
}
