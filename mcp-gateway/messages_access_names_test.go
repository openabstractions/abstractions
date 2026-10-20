package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type recordedAccessIntents struct {
	messagePermissionClient
	intents []askswire.PermissionIntent
	answer  askswire.PermissionObservation
}

func (p *recordedAccessIntents) RequestContext(_ context.Context, intent askswire.PermissionIntent) (askswire.PermissionObservation, error) {
	p.intents = append(p.intents, intent)
	return p.answer, nil
}

func (p *recordedAccessIntents) ObserveContext(ctx context.Context, _ string, _ int64) (askswire.PermissionObservation, error) {
	<-ctx.Done()
	return askswire.PermissionObservation{}, ctx.Err()
}

// Owner R10, 2026-11-08: prepare named "oa-opencode-integration" and the
// agent then asked for access with that exact name. Every spelling the
// guidance or discovery gives must request the one receiver Prepare checks.
func TestMessagesAccessAcceptsTheReceiverPrepareNames(t *testing.T) {
	probe := &messagesProbe{prepareResult: wire.PrepareResult{Outcome: wire.OutcomeForbidden, Reason: "access:oa-opencode-integration"}}
	permission := &recordedAccessIntents{answer: askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeUnavailable}}
	b := &messagesBackend{contextName: "claude-chat", instance: "claude-instance",
		permission: func(context.Context) (messagePermissionClient, error) { return permission, nil },
		native: func(context.Context) (nativeMessages, error) {
			return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: probe}), nil
		}}
	prepared, err := b.MessagesPrepare(t.Context(), gateway.MessagesPrepareInput{Receiver: "OpenCode", Body: "hello"})
	if err != nil || prepared.Outcome != "forbidden" || probe.prepareRequest.Receiver != "app:oa-opencode-integration" {
		t.Fatal(prepared, probe.prepareRequest, err)
	}
	named := strings.TrimPrefix(prepared.Reason, "access:")
	if !strings.Contains(prepared.Guidance, `receiver "`+named+`"`) {
		t.Fatal("guidance does not name the receiver", prepared.Guidance)
	}
	for _, receiver := range []string{named, "app:" + named, "OpenCode", "opencode"} {
		if _, err := b.MessagesRequestAccess(t.Context(), gateway.MessagesAccessInput{Receiver: receiver, IdempotencyKey: "same-explicit-operation"}); err != nil {
			t.Fatal(receiver, err)
		}
	}
	if len(permission.intents) != 4 {
		t.Fatal("expected one native Request per call", len(permission.intents))
	}
	for i, intent := range permission.intents[1:] {
		first := permission.intents[0]
		if intent.RequestKey != first.RequestKey || intent.Selection == nil || !bytes.Equal(intent.Selection.Selector.Target, first.Selection.Selector.Target) {
			t.Fatal("receiver spelling changed the requested permission", i+1, string(intent.Selection.Selector.Target), string(first.Selection.Selector.Target))
		}
	}
	if got := string(permission.intents[0].Selection.Selector.Target); !strings.Contains(got, `"oa-opencode-integration"`) {
		t.Fatal("requested target is not the receiver Prepare checks", got)
	}
}

// A recorded Allow whose permission is not saved reads unavailable, with a
// one-sentence relay for the person and the same-key retry; never allow/yes.
func TestMessagesAccessUnsavedAllowIsNeverReportedAsAllowed(t *testing.T) {
	permission := &recordedAccessIntents{answer: askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeAnswered,
		Answer: &askswire.Answer{ID: "recorded-allow", Option: "allow", Yes: true}}}
	b := &messagesBackend{contextName: "claude-chat",
		permission: func(context.Context) (messagePermissionClient, error) { return permission, nil },
		checkAccess: func(context.Context, string) (askswire.PermissionObservation, error) {
			return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending}, nil
		}}
	ctx := t.Context()
	st, ct := mcp.NewInMemoryTransports()
	server, err := gateway.NewServer(b).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "unsaved-allow", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	reply, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_request_access", Arguments: map[string]any{"receiver": "OpenCode", "idempotency_key": "continue-after-allow"}})
	if err != nil || reply.IsError || len(reply.Content) != 2 {
		t.Fatal(reply, err)
	}
	result := reply.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(result, `"unavailable"`) || strings.Contains(result, "allow") || strings.Contains(result, "yes") {
		t.Fatal("unsaved Allow reported as allowed", result)
	}
	if relay := reply.Content[1].(*mcp.TextContent).Text; !strings.Contains(relay, "has not saved this permission") || !strings.Contains(relay, "same receiver and idempotency_key") {
		t.Fatal(relay)
	}
}

type conflictingAccessKey struct {
	messagePermissionClient
	keys     []string
	conflict string
}

func (p *conflictingAccessKey) RequestContext(_ context.Context, intent askswire.PermissionIntent) (askswire.PermissionObservation, error) {
	p.keys = append(p.keys, intent.RequestKey)
	if intent.RequestKey == p.conflict {
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomeConflict}, nil
	}
	return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending, Answer: &askswire.Answer{ID: "fresh-question", Pending: true}}, nil
}

func (p *conflictingAccessKey) ObserveContext(ctx context.Context, _ string, _ int64) (askswire.PermissionObservation, error) {
	return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending, Answer: &askswire.Answer{ID: "fresh-question", Pending: true}}, nil
}

// Owner R12, 2026-11-08: after an OA connector update the agent's key names
// the old parties. The same idempotency_key continues under one derived key,
// so the person gets a fresh question and a repeat reconciles with it.
func TestMessagesAccessRenewsKeyAfterChangedParties(t *testing.T) {
	first := &conflictingAccessKey{}
	b := &messagesBackend{contextName: "codex-chat", permission: func(context.Context) (messagePermissionClient, error) { return first, nil }}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	in := gateway.MessagesAccessInput{Receiver: "OpenCode", IdempotencyKey: "person-asked"}
	if _, err := b.MessagesRequestAccess(ctx, in); err != nil || len(first.keys) != 1 {
		t.Fatal(first.keys, err)
	}
	updated := &conflictingAccessKey{conflict: first.keys[0]}
	b.permission = func(context.Context) (messagePermissionClient, error) { return updated, nil }
	for range 2 {
		ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
		out, err := b.MessagesRequestAccess(ctx, in)
		cancel()
		if err != nil || out.Outcome != askswire.PermissionOutcomePending {
			t.Fatal("changed parties did not reach a fresh question", out, err)
		}
	}
	if len(updated.keys) != 4 || updated.keys[0] != first.keys[0] || updated.keys[1] == first.keys[0] || updated.keys[3] != updated.keys[1] {
		t.Fatal("renewed key is not derived once and reused", updated.keys)
	}
}
