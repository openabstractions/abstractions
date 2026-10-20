package gateway

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMessagesReplyViewFencesBroaderBackendAndLateHostTools(t *testing.T) {
	b := &receiverCatalogueFixture{messageReceiveFixture: messageReceiveFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}}
	server := NewMessagesReplyServer(b)
	lateCalls := 0
	server.AddTool(&mcp.Tool{Name: "oa_claude_session_start", InputSchema: map[string]any{"type": "object"}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		lateCalls++
		return &mcp.CallToolResult{}, nil
	})
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "broader-global-default", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	list, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{ToolMessagesReply, ToolMessagesWait}) {
		t.Fatal(names)
	}
	for _, name := range []string{ToolMessagesInboxOpen, ToolMessagesInboxesList, ToolMessagesPrepare, ToolMessagesSend, ToolComplete, ToolApplications, "oa_asks_answer", "oa_claude_session_start", "unknown"} {
		out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !out.IsError {
			t.Fatal("excluded tool executed", name)
		}
	}
	if b.admissions != 0 || b.unrelated != 0 || lateCalls != 0 {
		t.Fatal(b.admissions, b.unrelated, lateCalls)
	}
	out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesWait, Arguments: map[string]any{"wait_ms": 0}})
	if err != nil || out.IsError || b.admissions != 1 {
		t.Fatal(out, err, b.admissions)
	}
	out, err = cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesReply, Arguments: map[string]any{"message_id": "question", "body": "answer"}})
	if err != nil || out.IsError || b.admissions != 2 {
		t.Fatal(out, err, b.admissions)
	}
}
