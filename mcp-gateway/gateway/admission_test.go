package gateway

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Nil methods deliberately panic if any registered tool bypasses the common
// gate, including reads, inference, cancellation, permission and message writes.
type suspendedNativeBackend struct {
	Backend
	AppModelBackend
	MessagesBackend
	MessagesAccessBackend
	MessagesWatchBackend
}

func (*suspendedNativeBackend) MessagesContext() string { return "fixture-context" }
func (*suspendedNativeBackend) BeginNativeTool(ctx context.Context, _ string) (context.Context, func(), bool) {
	return ctx, func() {}, false
}

func TestNativeAdmissionSuspendsEveryRegisteredTool(t *testing.T) {
	_, session := connected(t, &suspendedNativeBackend{}, "fixture", "2026-07-28")
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil || len(tools.Tools) != 18 {
		t.Fatal(tools, err)
	}
	for _, tool := range tools.Tools {
		out, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tool.Name, Arguments: map[string]any{}})
		if err != nil || !out.IsError {
			t.Fatalf("%s: %+v %v", tool.Name, out, err)
		}
		raw, err := json.Marshal(out.StructuredContent)
		if err != nil || !strings.Contains(string(raw), `"outcome":"unavailable"`) {
			t.Fatalf("%s: %s %v", tool.Name, raw, err)
		}
	}
}
