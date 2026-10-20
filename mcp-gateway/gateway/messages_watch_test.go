package gateway

import (
	"context"
	"encoding/json"
	watchwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type watchFixture struct {
	messageFixture
	calls int
	input MessagesWatchInput
}

func (b *watchFixture) MessagesWatch(_ context.Context, in MessagesWatchInput) (MessagesWatchResult, error) {
	b.calls++
	b.input = in
	return MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: "r", PendingUntilUnixMs: 2000, AwaitedReplies: new(int64(2)), Wake: "0002", OldestWake: "0001"}, WatchMetadata: WatchMetadata{Context: "native-11111111111111111111111111111111", AsksWake: "0003", OldestAsksWake: "0002", AnswerableAsks: new(int64(4)), WaitingAsks: new(int64(1)), WaitingAsksWake: "0001"}}, nil
}

func TestMessagesNativeWatchSchemaAndDedicatedInventory(t *testing.T) {
	b := &watchFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}
	_, session := connected(t, b, "untrusted-client-name", "2026-07-28")
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 13 {
		t.Fatal("private admitted inventory", tools, err)
	}
	for _, args := range []map[string]any{{"wait_ms": 0, "context": "forged"}, {"wait_ms": 0, "instance": "forged"}, {"wait_ms": 0, "after": "body-cursor"}, {"wait_ms": 25001}, {"wait_ms": -1}, {"wait_ms": 0, "revision": strings.Repeat("x", 257)}, {"wait_ms": 0, "health": "peer text"}, {"wait_ms": 0, "health": map[string]any{"context": "foreign"}}, {"wait_ms": 0, "health": "stopped_protocol", "target": "foreign"}} {
		out, e := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oa_messages_watch", Arguments: args})
		if e == nil && !out.IsError {
			t.Fatal("invalid observation accepted", args)
		}
	}
	if b.calls != 0 {
		t.Fatal("invalid reached native backend")
	}
	out, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oa_messages_watch", Arguments: map[string]any{"wait_ms": 25000, "revision": "prior"}})
	if err != nil || out.IsError || b.calls != 1 || b.input.Revision != "prior" || b.input.WaitMs != 25000 {
		t.Fatal(out, err, b.input)
	}
	var watch map[string]any
	if err := json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &watch); err != nil ||
		len(watch) != 13 || watch["awaited_replies"] != float64(2) || watch["outcome"] != "page" || watch["revision"] != "r" ||
		watch["answerable_asks"] != float64(4) || watch["waiting_asks"] != float64(1) || watch["waiting_asks_wake"] != "0001" || watch["asks_wake"] != "0003" || watch["oldest_asks_wake"] != "0002" ||
		watch["context"] != "native-11111111111111111111111111111111" ||
		watch["pending_until_unix_ms"] != float64(2000) || watch["reply_until_unix_ms"] != float64(0) || watch["wake"] != "0002" || watch["oldest_wake"] != "0001" {
		t.Fatalf("watch projection changed: %v (%v)", watch, err)
	}
	out, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "oa_messages_watch", Arguments: map[string]any{"wait_ms": 0, "health": "stopped_protocol"}})
	if err != nil || out.IsError || b.calls != 2 || b.input.Health != "stopped_protocol" {
		t.Fatal(out, err, b.input)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "oa_messages_watch" {
			raw, err := json.Marshal(tool.OutputSchema)
			if err != nil {
				t.Fatal(err)
			}
			var schema struct {
				AnyOf []struct{ Properties map[string]json.RawMessage }
			}
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatal(err)
			}
			if len(schema.AnyOf) != 2 {
				t.Fatalf("watch tools/list must distinguish success and permission results: %s", raw)
			}
			for _, field := range []string{"unread", "pending_questions", "oldest_unread_unix_ms", "newest_unread_unix_ms", "awaited_replies", "oldest_wake", "answerable_asks", "oldest_asks_wake", "waiting_asks", "waiting_asks_wake"} {
				if _, present := schema.AnyOf[0].Properties[field]; !present {
					t.Fatalf("watch tools/list omits approved summary %s: %s", field, raw)
				}
			}
		}
		if tool.Name == "oa_messages_watch" && (tool.Annotations.ReadOnlyHint || tool.Annotations.DestructiveHint == nil || *tool.Annotations.DestructiveHint || !tool.Annotations.IdempotentHint) {
			t.Fatal("metadata annotation", tool.Annotations)
		}
	}
	_, plain := connected(t, &fixtureBackend{}, "oa-claude-integration", "2026-07-28")
	absent, e := plain.CallTool(context.Background(), &mcp.CallToolParams{Name: "oa_messages_watch", Arguments: map[string]any{"wait_ms": 0}})
	if e == nil && !absent.IsError {
		t.Fatal("client label created native watch authority")
	}
}
