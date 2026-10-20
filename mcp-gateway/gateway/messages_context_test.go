package gateway

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// The coordinator's Claude session kept its first connection's instructions
// after the helper reconnected on 2026-10-30, and its copied context then named
// a closed inbox. Opening this connection's own inbox therefore needs no token.
func TestMessagesInboxOpenContextIsOptional(t *testing.T) {
	_, session := connected(t, &messageFixture{Backend: &fixtureBackend{}}, "client", "2025-11-25")
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != ToolMessagesInboxOpen {
			continue
		}
		raw, _ := json.Marshal(tool.InputSchema)
		var schema struct {
			Required   []string                   `json:"required"`
			Properties map[string]json.RawMessage `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil || schema.Properties["context"] == nil {
			t.Fatal(string(raw), err)
		}
		for _, name := range schema.Required {
			if name == "context" {
				t.Fatal(string(raw))
			}
		}
		out, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolMessagesInboxOpen, Arguments: map[string]any{}})
		if err != nil || out.IsError {
			t.Fatal(out, err)
		}
		return
	}
	t.Fatal("inbox open tool absent")
}
