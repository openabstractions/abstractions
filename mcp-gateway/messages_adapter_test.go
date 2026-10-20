package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// AB-1s F2: a model calls oa_messages_watch on the same connection as the
// host adapter. A turn signal without the adapter's key leaves the chain
// row in place; so does a guessed key. The adapter's key ends it.
func TestModelWatchTurnLeavesChainInPlace(t *testing.T) {
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage}}
	b := &messagesBackend{application: "oa-claude-integration", instance: "owned", contextName: "chat-" + strings.Repeat("7", 48), native: func(context.Context) (nativeMessages, error) { return probe, nil }, adapter: new(messagesAdapter)}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	server, err := gateway.NewServer(b).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "model", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	watch := func(args map[string]any) gateway.MessagesWatchResult {
		t.Helper()
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "oa_messages_watch", Arguments: args})
		if err != nil || len(out.Content) != 1 {
			t.Fatal(out, err)
		}
		var value gateway.MessagesWatchResult
		raw := []byte(out.Content[0].(*mcp.TextContent).Text)
		if err := json.Unmarshal(raw, &value.WatchMetadata); err != nil {
			t.Fatal(err)
		}
		var summary struct{ Outcome wire.Outcome }
		if err := json.Unmarshal(raw, &summary); err != nil {
			t.Fatal(err)
		}
		value.Outcome = summary.Outcome
		return value
	}
	// Before any adapter registered, and after, a keyless or guessed-key turn
	// is refused and states nothing.
	key, guess := strings.Repeat("ab", 16), strings.Repeat("0", 32)
	for _, register := range []bool{false, true} {
		if register && b.adapter.present(key) != "accepted" {
			t.Fatal("adapter key not registered")
		}
		for _, args := range []map[string]any{{"wait_ms": 0, "turn": "person"}, {"wait_ms": 0, "turn": "ended", "adapter_key": guess}} {
			if out := watch(args); out.Outcome.String() != "invalid" || out.Reason != "adapter_key" {
				t.Fatal("model turn signal admitted", register, args, out)
			}
		}
		if len(probe.turnWakes) != 0 {
			t.Fatal("a model call ended the chain", probe.turnWakes)
		}
	}
	// A later key never replaces the registered one.
	if b.adapter.present(guess) != "refused" || b.adapter.present(key) != "accepted" {
		t.Fatal("adapter key replaced")
	}
	if out := watch(map[string]any{"wait_ms": 0, "turn": "person", "adapter_key": key}); out.Outcome.String() != "page" || len(probe.turnWakes) != 1 || probe.turnWakes[0] != "" {
		t.Fatal("the adapter's turn signal did not end the chain", out, probe.turnWakes)
	}
}

// A wake reservation needs the key once an adapter registered one. A child
// no keyed adapter registered with keeps reserving without it.
func TestWatchReservationNeedsRegisteredAdapterKey(t *testing.T) {
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Wake: "0002"}}
	reserved := 0
	b := &messagesBackend{application: "oa-claude-integration", instance: "owned", contextName: "chat-" + strings.Repeat("1", 48), native: func(context.Context) (nativeMessages, error) { return probe, nil }, adapter: new(messagesAdapter)}
	b.wakeReserve = func(context.Context, string, string) (string, error) { reserved++; return strings.Repeat("a", 8), nil }
	if out, err := b.MessagesWatch(t.Context(), gateway.MessagesWatchInput{ReserveWake: "0002"}); err != nil || out.WakeTicket == "" || reserved != 1 {
		t.Fatal("unkeyed child refused a reservation", out, err)
	}
	key := strings.Repeat("cd", 16)
	b.adapter.present(key)
	for _, in := range []gateway.MessagesWatchInput{{ReserveWake: "0002"}, {ReserveWake: "0002", WakeTicket: strings.Repeat("a", 8)}, {ReserveWake: "0002", AdapterKey: strings.Repeat("0", 32)}} {
		if out, err := b.MessagesWatch(t.Context(), in); err != nil || out.Outcome.String() != "invalid" || out.WakeTicket != "" || reserved != 1 {
			t.Fatal("reservation without the registered key", in, out, err)
		}
	}
	if out, err := b.MessagesWatch(t.Context(), gateway.MessagesWatchInput{ReserveWake: "0002", AdapterKey: key}); err != nil || out.WakeTicket == "" || reserved != 2 {
		t.Fatal("the adapter's reservation refused", out, err)
	}
}

// The Mod registers its key on oa_claude_session_start: a plain start
// advertises the input, the first key registers, a different key is refused.
func TestClaudeSessionStartRegistersOneAdapterKey(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "opaque-vendor-session")
	_, diagnosticContext, _ := diagnosticFixture(t)
	ctx, cancel := context.WithTimeout(diagnosticContext, 5*time.Second)
	defer cancel()
	b := &messagesBackend{application: "oa-claude-integration"}
	st, ct := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runClaudeChatServer(ctx, gateway.NewServer(b), b, st) }()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { client.Close(); cancel(); <-done }()
	start := func(args map[string]any) string {
		t.Helper()
		out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]string
		if json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &result) != nil {
			t.Fatal(out)
		}
		return result["adapter"]
	}
	mod, model := strings.Repeat("12", 16), strings.Repeat("34", 16)
	for _, step := range []struct {
		args map[string]any
		want string
	}{
		{map[string]any{}, "supported"},
		{map[string]any{"adapter_key": mod}, "accepted"},
		{map[string]any{"adapter_key": model}, "refused"},
		{map[string]any{"adapter_key": mod}, "accepted"},
		{map[string]any{}, "supported"},
	} {
		if got := start(step.args); got != step.want {
			t.Fatal(step.args, got, step.want)
		}
	}
}

// AB-1s F5: the wake prompt names an agent only for Setup's connector
// registration. An app titled "Claude", or a look-alike, reads as no sender.
func TestWakeSenderNamesAgentsOnlyForSetupConnectors(t *testing.T) {
	folder := t.TempDir()
	prior := wakeConnectorFolder
	wakeConnectorFolder = func() string { return folder }
	t.Cleanup(func() { wakeConnectorFolder = prior })
	leaf := "oa-claude-integration"
	if runtime.GOOS == "windows" {
		leaf += ".exe"
	}
	connector := filepath.Join(folder, leaf)
	if err := os.WriteFile(connector, []byte("connector"), 0600); err != nil {
		t.Fatal(err)
	}
	// Setup ships native agent connectors on Windows and Darwin.
	wantConnector := ""
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		wantConnector = "Claude"
	}
	for _, tc := range []struct {
		app  gateway.Application
		want string
	}{
		{gateway.Application{Name: "oa-claude-integration", Title: "Anything", Scope: "local", Program: connector}, wantConnector},
		{gateway.Application{Name: "oa-claude-integration", Title: "Claude", Scope: "local", Program: connector, SelfRegistered: true}, ""},
		{gateway.Application{Name: "oa-claude-integration", Title: "Claude", Scope: "local", Program: filepath.Join(t.TempDir(), filepath.Base(connector))}, ""},
		{gateway.Application{Name: "spoof", Title: "Claude", Scope: "local", Program: connector}, ""},
		{gateway.Application{Name: "spoof", Title: "CIaude Helper", Scope: "local"}, ""},
		{gateway.Application{Name: "spoof", Title: "C1 aude", Scope: "local"}, ""},
		{gateway.Application{Name: "spoof", Title: "Сlaude", Scope: "local"}, ""},
		{gateway.Application{Name: "spoof", Title: "open code", Scope: "local"}, ""},
		{gateway.Application{Name: "spoof", Title: "OpenAbstractions", Scope: "local"}, ""},
		{gateway.Application{Name: "lemon", Title: "Pretend Lemonade", Scope: "local"}, "Pretend Lemonade"},
		{gateway.Application{Name: "lemon", Title: "Pretend Lemonade", Scope: "local", SelfRegistered: true}, ""},
	} {
		if got := wakeSenderTitle(tc.app); got != tc.want {
			t.Fatalf("%+v named %q, want %q", tc.app, got, tc.want)
		}
	}

	// Through the wake prompt itself.
	d := &wakeDirectoryFixture{page: gateway.ApplicationsOutput{Outcome: "page", Applications: []gateway.Application{{Name: "spoof", Title: "Claude", Scope: "local"}}}}
	b := &messagesBackend{Backend: d, application: "receiver", contextName: "chat"}
	note := wire.Header{From: "app:spoof", To: "app:receiver", Context: "chat", Kind: wire.KindNote, ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()}
	text, err := messagesTicketPointer("7f3a9c21", b.wakeSender(t.Context(), []wire.Header{note}), false)
	if err != nil || strings.Contains(text, "Claude") {
		t.Fatal("a registration titled Claude reads as Claude", text, err)
	}
}

// Every tool a host lists is one its model can call. No listed input may be
// described as adapter-only; an adapter's field needs the adapter's key.
func TestListedToolInputsClaimNoAdapterOnlyField(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "opaque-vendor-session")
	_, diagnosticContext, _ := diagnosticFixture(t)
	ctx, cancel := context.WithTimeout(diagnosticContext, 5*time.Second)
	defer cancel()
	b := &messagesBackend{application: "oa-claude-integration"}
	st, ct := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runClaudeChatServer(ctx, gateway.NewServer(b), b, st) }()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { client.Close(); cancel(); <-done }()
	listed, err := client.ListTools(ctx, nil)
	if err != nil || len(listed.Tools) == 0 {
		t.Fatal(listed, err)
	}
	seen := map[string]bool{}
	for _, tool := range listed.Tools {
		seen[tool.Name] = true
		raw, _ := json.Marshal(tool.InputSchema)
		if field := adapterOnlyField(raw); field != "" {
			t.Errorf("%s lists %s as adapter-only; a model can set it", tool.Name, field)
		}
	}
	if !seen["oa_messages_watch"] || !seen[claudeSessionStartTool] {
		t.Fatal("roster lacks the adapter tools", seen)
	}
	if adapterOnlyField([]byte(`{"properties":{"turn":{"description":"Adapter-only: x"}}}`)) != "turn" {
		t.Fatal("check misses the claim it guards")
	}
}

// adapterOnlyField names the first schema property whose description claims
// only an adapter sets it.
func adapterOnlyField(schema []byte) string {
	var node struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if json.Unmarshal(schema, &node) != nil {
		return ""
	}
	for name, raw := range node.Properties {
		var property struct {
			Description string `json:"description"`
		}
		_ = json.Unmarshal(raw, &property)
		lower := strings.ToLower(property.Description)
		if strings.Contains(lower, "adapter-only") || strings.Contains(lower, "adapter only") {
			return name
		}
		if nested := adapterOnlyField(raw); nested != "" {
			return name + "." + nested
		}
	}
	return ""
}
