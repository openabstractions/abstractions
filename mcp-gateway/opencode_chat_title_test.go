package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// Only native response data is inert. The helper's host binding, title
// normalization, receiving owner, MCP list and Prepare projections are real.
type openCodeTitleView struct{ *chaosRuntime }

func (r openCodeTitleView) rows() []wire.Inbox {
	r.mu.Lock()
	defer r.mu.Unlock()
	rows := []wire.Inbox{}
	for route, claim := range r.chatBindings {
		rows = append(rows, wire.Inbox{Program: "app:oa-opencode-integration", Context: route, Revision: "native", ChatTitle: claim.ChatTitle})
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Context < rows[j].Context })
	return rows
}
func (r openCodeTitleView) InboxesList(wire.InboxQuery) (wire.InboxPage, error) {
	return wire.InboxPage{Outcome: wire.OutcomePage, Inboxes: r.rows()}, nil
}
func (r openCodeTitleView) Prepare(string, string, wire.PrepareRequest) (wire.PrepareResult, error) {
	return wire.PrepareResult{Outcome: wire.OutcomeConflict, Reason: "ambiguous_receiver", Choices: r.rows()}, nil
}

func titleChat(t *testing.T, runtime *chaosRuntime, id, title string, index int) *mcp.ClientSession {
	t.Helper()
	return openCodeChatSession(t, t.Context(), runtime, id, title, index)
}

func openCodeChatSession(t *testing.T, base context.Context, runtime *chaosRuntime, id, title string, index int) *mcp.ClientSession {
	t.Helper()
	ctx, cancel := context.WithCancel(base)
	source := fmt.Sprintf("source-%d", index)
	presence, err := runtime.Announce(ctx, facade.ApplicationPresence{Application: "oa-opencode-integration", LeaseMs: 60000, Contexts: []facade.ApplicationContext{{Name: source}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.InboxOpenChat(presence.Instance, source, "", wire.ChatBinding{}); err != nil {
		t.Fatal(err)
	}
	view := openCodeTitleView{runtime}
	bound, err := bindMessagesBackend(new(chaosBackend), "oa-opencode-integration", presence.Instance, source, func(context.Context) (nativeMessages, error) { return view, nil })
	if err != nil {
		t.Fatal(err)
	}
	b := bound.(*messagesBackend)
	configureInertChatPresence(b, runtime)
	st, ct := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runOpenCodeChatServerMetadata(ctx, gateway.NewServer(b), b, st, id, title) }()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "host", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		session.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("title helper did not join")
		}
	})
	for until := time.Now().Add(3 * time.Second); ; {
		out, err := session.CallTool(ctx, &mcp.CallToolParams{Name: openCodeChatStartTool, Arguments: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		if !out.IsError {
			break
		}
		if time.Now().After(until) {
			t.Fatal(out)
		}
		time.Sleep(time.Millisecond)
	}
	return session
}

func TestOpenCodeHostTitlesReachActualMCPInboxAndAmbiguousChoices(t *testing.T) {
	shortenAgentTiming(t)
	runtime := newChaosRuntime()
	a := titleChat(t, runtime, "ses_A", "First chat", 1)
	titleChat(t, runtime, "ses_B", "Second\u202e\n chat", 2)
	call := func(name string, args map[string]any) []wire.Inbox {
		t.Helper()
		out, err := a.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || out.IsError {
			t.Fatal(out, err)
		}
		if len(out.Content) != 1 {
			t.Fatal(out)
		}
		raw := []byte(out.Content[0].(*mcp.TextContent).Text)
		if name == gateway.ToolMessagesPrepare {
			var value gateway.MessagesPrepareOutput
			if err := json.Unmarshal(raw, &value); err != nil {
				t.Fatal(err)
			}
			if value.Outcome != "ambiguous_receiver" {
				t.Fatal(value)
			}
			return value.Choices
		}
		value, err := wire.DecodeInboxPage(raw)
		if err != nil {
			t.Fatal(err)
		}
		return value.Inboxes
	}
	for _, rows := range [][]wire.Inbox{call(gateway.ToolMessagesInboxesList, map[string]any{}), call(gateway.ToolMessagesPrepare, map[string]any{"receiver": "OpenCode", "body": "question"})} {
		if len(rows) != 2 || rows[0].Context == rows[1].Context {
			t.Fatal(rows)
		}
		titles := map[string]bool{}
		for _, row := range rows {
			titles[row.ChatTitle] = true
		}
		if !titles["First chat"] || !titles["Second chat"] {
			t.Fatal("host titles lost or unsafe native title unsanitized", rows)
		}
	}
}

// A renamed or auto-titled OpenCode session sends its new title through the
// same start call; the open chat's listed title follows without a restart.
func TestOpenCodeRenameReachesTheListing(t *testing.T) {
	shortenAgentTiming(t)
	runtime := newChaosRuntime()
	session := titleChat(t, runtime, "ses_A", "New session - 2026-11-09T12:40:00.000Z", 1)
	view := openCodeTitleView{runtime}
	listed := func(want string) {
		t.Helper()
		for until := time.Now().Add(3 * time.Second); ; {
			rows := view.rows()
			if len(rows) == 1 && rows[0].ChatTitle == want {
				return
			}
			if time.Now().After(until) {
				t.Fatalf("listed %v, want %q", rows, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	listed("New session - 2026-11-09T12:40:00.000Z")
	start := func(args map[string]any) map[string]string {
		t.Helper()
		out, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: openCodeChatStartTool, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		var value map[string]string
		if err := json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	before := start(map[string]any{})
	renamed := start(map[string]any{"chat_title": "Fix the\u202e\n inbox titles"})
	if renamed["outcome"] != "observed" || renamed["context"] != before["context"] || renamed["session_id"] != "ses_A" {
		t.Fatal(before, renamed)
	}
	listed("Fix the inbox titles")
	// An empty start call keeps the current title.
	start(map[string]any{})
	listed("Fix the inbox titles")
	for _, args := range []map[string]any{{"chat_title": 17}, {"chat_title": "x", "chat_id": "forged"}, {"title": "x"}} {
		if out := start(args); out["outcome"] != "invalid" {
			t.Fatal(args, out)
		}
	}
	listed("Fix the inbox titles")
}
