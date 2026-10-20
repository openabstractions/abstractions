package main

import (
	"context"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"strings"
	"testing"
)

func TestDarwinFriendlyReceiverUsesExactNewPermissionAndPrepareTarget(t *testing.T) {
	name := "oa-claude-integration-" + strings.Repeat("a", 32)
	p := &accessInboxProbe{messagesProbe: &messagesProbe{}, page: wire.InboxPage{Outcome: wire.OutcomePage, Inboxes: []wire.Inbox{{Program: "app:" + name, Context: "current", Revision: "r"}}}}
	permissions := 0
	b := &messagesBackend{instance: "native-instance", contextName: "source", native: func(context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: p}), nil
	}, requestAccess: func(_ context.Context, intent askswire.PermissionIntent) (askswire.PermissionObservation, error) {
		permissions++
		exact, err := grants.DecodeNamedApplicationTarget(intent.Selection.Selector.Target)
		if err != nil || exact != name {
			t.Fatal(exact, err)
		}
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending}, nil
	}}
	if _, err := b.MessagesRequestAccess(context.Background(), gateway.MessagesAccessInput{Receiver: name, IdempotencyKey: "same-key"}); err != nil || permissions != 1 {
		t.Fatal(err, permissions)
	}
	p.prepareResult = wire.PrepareResult{Outcome: wire.OutcomeUnavailable}
	if resolved, conversation, err := b.resolveMessageReceiver(context.Background(), "Claude", ""); err != nil || resolved != name || conversation != "current" {
		t.Fatal(resolved, conversation, err)
	}
	if out, err := b.MessagesPrepare(context.Background(), gateway.MessagesPrepareInput{Receiver: "Claude", Body: "owned fixture"}); err != nil || p.prepareRequest.Receiver != "app:oa-claude-integration" || p.prepareRequest.Context != "" {
		t.Fatal(out, err, p.prepareRequest)
	}
	p.page.Inboxes = nil
	b.requestAccess = func(_ context.Context, intent askswire.PermissionIntent) (askswire.PermissionObservation, error) {
		permissions++
		exact, err := grants.DecodeNamedApplicationTarget(intent.Selection.Selector.Target)
		if err != nil || exact != "oa-claude-integration" {
			t.Fatal("static first-use behavior changed", exact, err)
		}
		return askswire.PermissionObservation{Outcome: askswire.PermissionOutcomePending}, nil
	}
	if out, err := b.MessagesRequestAccess(context.Background(), gateway.MessagesAccessInput{Receiver: "Claude", IdempotencyKey: "first-access"}); err != nil || out.Outcome != askswire.PermissionOutcomePending || permissions != 2 {
		t.Fatal(out, err, permissions)
	}
}
