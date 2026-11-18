package main

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"testing"
	"time"
)

func TestChaosCodexRoutingBoundAdvertisement(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	helpers := make([]*chaosHelper, 6)
	for i := range helpers {
		helpers[i] = startChaosHelper(t, runtime, queue, 910+i)
	}
	// Inert projection of actual Announce calls; native service fixtures also
	// verify the generic instance/context filtering at its owning boundary.
	rows := func() []msg.Inbox {
		runtime.mu.Lock()
		defer runtime.mu.Unlock()
		var found []msg.Inbox
		for context, instance := range runtime.holder {
			if !runtime.live(instance, time.Now()) {
				continue
			}
			if claim, ok := runtime.chatBindings[context]; ok && claim.ChatID != "" {
				found = append(found, msg.Inbox{Program: "app:oa-codex-integration", Context: context})
			}
		}
		return found
	}
	waitCount := func(want int) {
		t.Helper()
		until := time.Now().Add(5 * time.Second)
		for len(rows()) != want {
			if time.Now().After(until) {
				t.Fatalf("receiving destinations=%d want%d", len(rows()), want)
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	until := time.Now().Add(5 * time.Second)
	for {
		runtime.mu.Lock()
		count := len(runtime.holder)
		runtime.mu.Unlock()
		if count == 6 {
			break
		}
		if time.Now().After(until) {
			t.Fatal("source owners did not open")
		}
		time.Sleep(5 * time.Millisecond)
	}
	waitCount(0)
	if out := helpers[4].call(codexSessionStartTool, map[string]any{}); out.IsError {
		t.Fatal(out)
	}
	waitCount(1)
	list := func(msg.InboxQuery) (msg.InboxPage, error) {
		return msg.InboxPage{Outcome: msg.OutcomePage, Inboxes: rows()}, nil
	}
	classify := func(value string) string { return value }
	_, selected, err := discoverMessageReceiver(context.Background(), "oa-codex-integration", "", list, classify)
	expected := fixtureNativeChatContext(helpers[4].thread)
	if err != nil || selected != expected {
		t.Fatal("sole bound root not selected", selected, err)
	}
	// A second occurrence of the same chat keeps its own source owner and
	// cannot advertise a second receiving route or borrow the held body owner.
	helpers[5].thread = helpers[4].thread
	if out := helpers[5].call(codexSessionStartTool, map[string]any{}); out.IsError {
		t.Fatal(out)
	}
	waitCount(1)
	canonical := rows()[0].Context
	helpers[4].close()
	waitCount(1)
	if rows()[0].Context != canonical {
		t.Fatal("reconnect changed canonical chat route", rows())
	}
	other := startChaosHelper(t, runtime, queue, 920)
	if out := other.call(codexSessionStartTool, map[string]any{}); out.IsError {
		t.Fatal(out)
	}
	waitCount(2)
	if _, context, err := discoverMessageReceiver(context.Background(), "oa-codex-integration", "", list, classify); err != nil || context != "" {
		t.Fatal("distinct chats must defer to explicit service choice", context, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// A call without thread metadata runs and leaves the binding and its
	// advertised route in place (architect ruling 2026-11-22).
	out, err := helpers[5].session.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolModels, Arguments: map[string]any{}})
	if err != nil || out.IsError {
		t.Fatal("a call without metadata lost ordinary tool availability", out, err)
	}
	time.Sleep(200 * time.Millisecond)
	waitCount(2)
	other.close()
	waitCount(1)
	helpers[5].close()
	waitCount(0)
	for _, h := range helpers[:4] {
		if r := h.record(); r.Receiving != nil && r.Receiving.Thread != "" {
			t.Fatal("unbound helper acquired chat", r)
		}
	}
}
