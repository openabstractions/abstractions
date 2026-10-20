package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestClaudeNativeSessionLifecycleBindsOnlyDocumentedChildIdentity(t *testing.T) {
	for _, id := range []string{"", "invalid\n", strings.Repeat("x", 257), codexAttachThread, "local_a9343ad9"} {
		t.Run(id, func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_SESSION_ID", id)
			_, diagnosticContext, diagnosticDir := diagnosticFixture(t)
			ctx, cancel := context.WithTimeout(diagnosticContext, 5*time.Second)
			defer cancel()
			runtime := newChaosRuntime()
			presence, err := runtime.Announce(ctx, facade.ApplicationPresence{Application: "oa-claude-integration", LeaseMs: 60000, Contexts: []facade.ApplicationContext{{Name: "source"}}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := runtime.InboxOpenChat(presence.Instance, "source", "", msg.ChatBinding{}); err != nil {
				t.Fatal(err)
			}
			b := &messagesBackend{application: "oa-claude-integration", instance: presence.Instance, contextName: "source", native: func(context.Context) (nativeMessages, error) { return runtime, nil }}
			configureInertChatPresence(b, runtime)
			st, ct := mcp.NewInMemoryTransports()
			done := make(chan error, 1)
			go func() { done <- runClaudeChatServer(ctx, gateway.NewServer(b), b, st) }()
			client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			assertDiagnostic := func(stage, result, reason string) {
				t.Helper()
				rows := readAgentDiagnostics(ctx, diagnosticDir)
				want := claudeBindingDiagnostic{Stage: stage, Result: result, Reason: reason}
				if len(rows) != 1 || rows[0].ClaudeBinding == nil || *rows[0].ClaudeBinding != want {
					t.Fatalf("lifecycle diagnostic: want %+v, got %+v", want, rows)
				}
			}
			launchReason := "none"
			if !validClaudeSessionID(id) {
				launchReason = "invalid_child_session"
			}
			assertDiagnostic("awaiting_start", "none", launchReason)
			out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: map[string]any{}})
			if err != nil {
				t.Fatal(err)
			}
			var result map[string]string
			if len(out.Content) != 1 {
				t.Fatal(out)
			}
			if err := json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &result); err != nil {
				t.Fatal(err)
			}
			if id == codexAttachThread || id == "local_a9343ad9" {
				assertDiagnostic("start", "observed", "none")
				if out.IsError || result["outcome"] != "observed" || result["session_id"] != id {
					t.Fatal(out, result)
				}
				b.chatMu.Lock()
				receiver := b.chatReceiver
				b.chatMu.Unlock()
				if receiver == nil {
					t.Fatal("independent identity did not create receiving owner")
				}
				if result["context"] != receiver.contextName || result["context"] == "" {
					t.Fatal("lifecycle did not correlate the receiving context", result)
				}
				receipt := filepath.Join(diagnosticDir, agentDiagnosticName)
				beforeFile, err := fileid.Open(ctx, fileid.Self, receipt, fileid.Options{})
				if err != nil {
					t.Fatal(err)
				}
				beforeID := beforeFile.FileID()
				before, statErr := beforeFile.File().Stat()
				if err := beforeFile.Close(); err != nil {
					t.Fatal(err)
				}
				if statErr != nil {
					t.Fatal(statErr)
				}
				again, err := client.CallTool(ctx, &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: map[string]any{}})
				if err != nil || again.IsError || again.Content[0].(*mcp.TextContent).Text != out.Content[0].(*mcp.TextContent).Text {
					t.Fatal("repeat correlation changed the receiving owner", again, err)
				}
				afterFile, err := fileid.Open(ctx, fileid.Self, receipt, fileid.Options{})
				if err != nil {
					t.Fatal(err)
				}
				afterID := afterFile.FileID()
				after, statErr := afterFile.File().Stat()
				if err := afterFile.Close(); err != nil {
					t.Fatal(err)
				}
				if statErr != nil || !beforeID.Equal(afterID) || !before.ModTime().Equal(after.ModTime()) {
					t.Fatal("identical successful start rewrote the lifecycle receipt", statErr)
				}
				until := time.Now().Add(time.Second)
				for receiver.inboxInstance() == "" && time.Now().Before(until) {
					time.Sleep(time.Millisecond)
				}
				if receiver.inboxInstance() == "" {
					t.Fatal("typed receiver did not admit")
				}
			} else if !out.IsError || result["outcome"] != "unavailable" {
				t.Fatal("missing identity invented a chat", out, result)
			} else {
				assertDiagnostic("start", "unavailable", "invalid_child_session")
			}
			if result["outcome"] == "observed" {
				if _, err := client.CallTool(ctx, &mcp.CallToolParams{Name: claudeSessionEndTool, Arguments: map[string]any{"child_instance": result["child_instance"], "session_id": id}}); err != nil {
					t.Fatal(err)
				}
				assertDiagnostic("end", "ended", "none")
			}
			b.chatMu.Lock()
			receiver := b.chatReceiver
			b.chatMu.Unlock()
			if receiver != nil {
				t.Fatal("end retained canonical owner")
			}
			client.Close()
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("server lifecycle did not join")
			}
		})
	}
}

func TestClaudeOpaqueCorrelationPreservesBoundsAndCodexGrammar(t *testing.T) {
	for _, id := range []string{"local_a9343ad9", "vendor-owned-session", codexAttachThread, strings.Repeat("x", 256)} {
		b := new(messagesBackend)
		if err := b.bindClaudeChat(id); err != nil || b.chatID != id || b.chatContext != "" {
			t.Fatal(id, b.chatID, err)
		}
		if err := b.bindClaudeChat(id + "changed"); err == nil || b.chatID != id {
			t.Fatal("changed child replaced retained correlation", id, err)
		}
	}
	for _, id := range []string{"", "invalid\n", "invalid\x00", strings.Repeat("x", 257), string([]byte{0xff})} {
		b := new(messagesBackend)
		if err := b.bindClaudeChat(id); err == nil || b.chatID != "" {
			t.Fatal("invalid correlation admitted", id, err)
		}
	}
	if err := new(messagesBackend).bindChat("local_a9343ad9"); err == nil {
		t.Fatal("Claude correction widened Codex grammar")
	}
}

func TestClaudeLifecycleDiagnosticRecordsReceiverFailureAndRetirement(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", "opaque-vendor-session")
	_, diagnosticContext, dir := diagnosticFixture(t)
	ctx, cancel := context.WithTimeout(diagnosticContext, 5*time.Second)
	defer cancel()
	// The existing receiver factory refuses an absent presence owner.
	b := &messagesBackend{application: "oa-claude-integration"}
	st, ct := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runClaudeChatServer(ctx, gateway.NewServer(b), b, st) }()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { client.Close(); cancel(); <-done }()
	check := func(name string, input map[string]any, want claudeBindingDiagnostic) map[string]string {
		t.Helper()
		out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]string
		if json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &result) != nil {
			t.Fatal(out)
		}
		rows := readAgentDiagnostics(ctx, dir)
		if len(rows) != 1 || rows[0].ClaudeBinding == nil || *rows[0].ClaudeBinding != want || b.chatReceiver != nil || rows[0].WatchHealth != nil {
			t.Fatalf("want %+v, got %+v; response %v", want, rows, result)
		}
		return result
	}
	start := check(claudeSessionStartTool, map[string]any{}, claudeBindingDiagnostic{"start", "unavailable", "receiver_error"})
	check(claudeSessionEndTool, map[string]any{"child_instance": "other", "session_id": "opaque-vendor-session"}, claudeBindingDiagnostic{"end", "unavailable", "metadata_mismatch"})
	check(claudeSessionEndTool, map[string]any{"child_instance": start["child_instance"], "session_id": start["session_id"], "retire": true}, claudeBindingDiagnostic{"end", "retired", "none"})
	check(claudeSessionStartTool, map[string]any{}, claudeBindingDiagnostic{"start", "unavailable", "retired"})
}

func TestClaudeFolderTitleReachesNativeClaim(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "opaque-vendor-chat")
	runtime := newChaosRuntime()
	_, diagnosticContext, _ := diagnosticFixture(t)
	ctx, cancel := context.WithCancel(diagnosticContext)
	defer cancel()
	presence, err := runtime.Announce(ctx, facade.ApplicationPresence{Application: "oa-claude-integration", LeaseMs: 60000, Contexts: []facade.ApplicationContext{{Name: "source"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.InboxOpenChat(presence.Instance, "source", "", msg.ChatBinding{}); err != nil {
		t.Fatal(err)
	}
	b := &messagesBackend{application: "oa-claude-integration", instance: presence.Instance, contextName: "source", native: func(context.Context) (nativeMessages, error) { return runtime, nil }}
	configureInertChatPresence(b, runtime)
	st, ct := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runClaudeChatServer(ctx, gateway.NewServer(b), b, st) }()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Claude title helper did not join")
		}
	})
	for _, title := range []string{"Pictures", "Review\u202e\n plan"} {
		out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: map[string]any{"chat_title": title}})
		if err != nil || out.IsError {
			t.Fatal(out, err)
		}
		want := hostChatTitle(title)
		until := time.Now().Add(3 * time.Second)
		for {
			runtime.mu.Lock()
			claim := runtime.chatBindings[b.chatContext]
			runtime.mu.Unlock()
			if claim.ChatTitle == want {
				break
			}
			if time.Now().After(until) {
				t.Fatalf("native claim title=%q want=%q", claim.ChatTitle, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	for _, input := range []map[string]any{{"chat_title": strings.Repeat("x", 801)}, {"chat_title": 17}, {"chat_title": "injected", "chat_id": "forged"}} {
		out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: input})
		if err == nil && !out.IsError {
			t.Fatal("invalid display claim admitted", out)
		}
	}
	runtime.mu.Lock()
	claim := runtime.chatBindings[b.chatContext]
	runtime.mu.Unlock()
	if claim.ChatTitle != "Review plan" || claim.ChatID != "opaque-vendor-chat" {
		t.Fatal("display metadata replaced native correlation", claim)
	}
}

func TestClaudeIndependentOpaqueSessionsAndColdMCPConnection(t *testing.T) {
	runtime := newChaosRuntime()
	_, diagnosticContext, _ := diagnosticFixture(t)
	clients := map[*messagesBackend]*mcp.ClientSession{}
	bindings := map[*messagesBackend]map[string]string{}
	newChild := func(source, id string) *messagesBackend {
		t.Helper()
		t.Setenv("CLAUDE_CODE_SESSION_ID", id)
		ctx, cancel := context.WithCancel(diagnosticContext)
		presence, err := runtime.Announce(ctx, facade.ApplicationPresence{Application: "oa-claude-integration", LeaseMs: 60000, Contexts: []facade.ApplicationContext{{Name: source}}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = runtime.InboxOpenChat(presence.Instance, source, "", msg.ChatBinding{}); err != nil {
			t.Fatal(err)
		}
		b := &messagesBackend{application: "oa-claude-integration", instance: presence.Instance, contextName: source, native: func(context.Context) (nativeMessages, error) { return runtime, nil }}
		configureInertChatPresence(b, runtime)
		st, ct := mcp.NewInMemoryTransports()
		done := make(chan error, 1)
		go func() { done <- runClaudeChatServer(ctx, gateway.NewServer(b), b, st) }()
		client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		t.Cleanup(func() {
			client.Close()
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Error("Claude child did not join")
			}
		})
		if unbound, err := b.MessagesInboxOpen(ctx, gateway.MessagesOpenInput{}); err != nil || unbound.Outcome != msg.OutcomeUnavailable {
			t.Fatal("cold child invented receiving", unbound, err)
		}
		out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: map[string]any{}})
		if err != nil || out.IsError {
			t.Fatal(out, err)
		}
		var binding map[string]string
		if err = json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &binding); err != nil || binding["session_id"] != id || binding["outcome"] != "observed" {
			t.Fatal(binding, err)
		}
		until := time.Now().Add(time.Second)
		for b.chatReceiver.inboxInstance() == "" && time.Now().Before(until) {
			time.Sleep(time.Millisecond)
		}
		if b.chatReceiver.inboxInstance() == "" {
			t.Fatal("correlated child did not admit")
		}
		clients[b], bindings[b] = client, binding
		return b
	}
	a := newChild("claude-source-a", "local_a9343ad9")
	b := newChild("claude-source-b", "local_b9343ad9")
	if a.chatContext == "" || b.chatContext == "" || a.chatContext == b.chatContext {
		t.Fatal("independent sessions collapsed", a.chatContext, b.chatContext)
	}
	cold := newChild("claude-source-a-cold", "local_a9343ad9")
	if cold.chatContext != a.chatContext {
		t.Fatal("cold connection changed canonical chat", a.chatContext, cold.chatContext)
	}
	if old, err := a.MessagesInboxOpen(t.Context(), gateway.MessagesOpenInput{}); err != nil || old.Outcome == msg.OutcomeOpened {
		t.Fatal("old connection retained receiving", old, err)
	}
	if own, err := b.MessagesInboxOpen(t.Context(), gateway.MessagesOpenInput{}); err != nil || own.Outcome != msg.OutcomeOpened {
		t.Fatal("A reconnect displaced B", own, err)
	}
	if own, err := cold.MessagesInboxOpen(t.Context(), gateway.MessagesOpenInput{}); err != nil || own.Outcome != msg.OutcomeOpened {
		t.Fatal("cold connection unavailable", own, err)
	}
	end := func(target *messagesBackend, binding map[string]string, retire bool) *mcp.CallToolResult {
		t.Helper()
		out, err := clients[target].CallTool(t.Context(), &mcp.CallToolParams{Name: claudeSessionEndTool, Arguments: map[string]any{"child_instance": binding["child_instance"], "session_id": binding["session_id"], "retire": retire}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if bindings[a]["child_instance"] == bindings[cold]["child_instance"] || !end(cold, bindings[a], true).IsError {
		t.Fatal("old callback retired replacement child")
	}
	if own, _ := cold.MessagesInboxOpen(t.Context(), gateway.MessagesOpenInput{}); own.Outcome != msg.OutcomeOpened {
		t.Fatal("replacement closed", own)
	}
	_, _ = clients[cold].CallTool(t.Context(), &mcp.CallToolParams{Name: claudeSessionEndTool, Arguments: map[string]any{}})
	if own, _ := cold.MessagesInboxOpen(t.Context(), gateway.MessagesOpenInput{}); own.Outcome != msg.OutcomeOpened {
		t.Fatal("empty old end closed replacement", own)
	}
	wrongSession := map[string]string{"child_instance": bindings[cold]["child_instance"], "session_id": "local_wrong"}
	if !end(cold, wrongSession, true).IsError {
		t.Fatal("mismatched session retired current child")
	}
	if end(b, bindings[b], true).IsError {
		t.Fatal("mismatched child could not retire itself")
	}
	runtime.mu.Lock()
	holder := runtime.holder[b.chatContext]
	runtime.mu.Unlock()
	if holder != "" || b.chatReceiver != nil {
		t.Fatal("mismatch left native receiving advertised", holder)
	}
	if again, err := clients[b].CallTool(t.Context(), &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: map[string]any{}}); err != nil || !again.IsError || b.chatReceiver != nil {
		t.Fatal("retired child rebound", again, err)
	}
	oldBinding := bindings[cold]
	if end(cold, oldBinding, false).IsError {
		t.Fatal("ordinary end refused")
	}
	started, err := clients[cold].CallTool(t.Context(), &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: map[string]any{}})
	if err != nil || started.IsError {
		t.Fatal("ordinary end prevented reopen", started, err)
	}
	var rebound map[string]string
	if err := json.Unmarshal([]byte(started.Content[0].(*mcp.TextContent).Text), &rebound); err != nil || rebound["child_instance"] == oldBinding["child_instance"] {
		t.Fatal("reopen reused teardown generation", rebound, err)
	}
	if !end(cold, oldBinding, true).IsError {
		t.Fatal("old association callback retired reopened binding")
	}
	until := time.Now().Add(time.Second)
	for cold.chatReceiver.inboxInstance() == "" && time.Now().Before(until) {
		time.Sleep(time.Millisecond)
	}
	if own, err := cold.MessagesInboxOpen(t.Context(), gateway.MessagesOpenInput{}); err != nil || own.Outcome != msg.OutcomeOpened {
		t.Fatal("ordinary reopen did not receive", own, err)
	}
}

func TestClaudeOpaqueWakeCustodyKeepsIndependentSessionsAcrossColdConnection(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	open := func(session, suffix string) *codexWakeState {
		t.Helper()
		state, err := claudeWakeReceipt(ctx, "oa-claude-integration", "chat-"+strings.Repeat(suffix, 48), session)
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	a, b := open("local_a9343ad9", "1"), open("local_b9343ad9", "2")
	ta, err := a.Reserve(ctx, "0001")
	if err != nil || ta == "" {
		t.Fatal(ta, err)
	}
	tb, err := b.Reserve(ctx, "0001")
	if err != nil || tb == "" || ta == tb {
		t.Fatal(ta, tb, err)
	}
	fresh := open("local_a9343ad9", "3")
	if value, err := fresh.Reserve(ctx, "0002"); value != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("cold child replayed uncertain pointer", value, err)
	}
	if _, ok, err := b.ConfirmChatWake(ctx, ta); ok || err != nil {
		t.Fatal("another chat consumed A pointer", ok, err)
	}
	probe := &messagesProbe{page: msg.Page{Outcome: msg.OutcomeUnknown}}
	backend := &messagesBackend{instance: "current-owner", contextName: "chat-" + strings.Repeat("3", 48), native: func(context.Context) (nativeMessages, error) { return probe, nil }, wakeConfirm: fresh.ConfirmChatWake}
	if _, err := backend.MessagesWait(ctx, gateway.MessagesWaitInput{WakeTicket: ta}); err != nil {
		t.Fatal(err)
	}
	if value, err := fresh.Reserve(ctx, "0002"); value != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("unknown read settled pointer", value, err)
	}
	probe.page.Outcome = msg.OutcomePage
	if _, err := backend.MessagesWait(ctx, gateway.MessagesWaitInput{WakeTicket: ta}); err != nil {
		t.Fatal(err)
	}
	if value, err := fresh.Reserve(ctx, "0002"); value == "" || err != nil {
		t.Fatal("confirmed body read blocked fresh owner", value, err)
	}
	if value, err := b.Load(ctx); value != "0001" || err != nil {
		t.Fatal("A reconnect changed B custody", value, err)
	}
	if _, err := codexWakeReceipt(ctx, "oa-codex-integration", "chat-"+strings.Repeat("1", 48), "local_a9343ad9"); err == nil {
		t.Fatal("Claude wake correction widened Codex grammar")
	}
}
