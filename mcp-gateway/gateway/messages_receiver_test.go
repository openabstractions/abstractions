package gateway

import (
	"context"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type receiverCatalogueFixture struct {
	messageReceiveFixture
	AsksBackend
	AppModelBackend
	MessagesAccessBackend
	MessagesWatchBackend
	AsksWatchBackend
	admissions, unrelated int
}

func (b *receiverCatalogueFixture) BeginNativeTool(ctx context.Context, _ string) (context.Context, func(), bool) {
	b.admissions++
	return ctx, func() {}, true
}
func (b *receiverCatalogueFixture) Applications(context.Context, ApplicationsInput) (ApplicationsOutput, error) {
	b.unrelated++
	return ApplicationsOutput{}, nil
}
func (b *receiverCatalogueFixture) Complete(context.Context, CompleteInput) (CompleteOutput, error) {
	b.unrelated++
	return CompleteOutput{}, nil
}

func TestMessagesReceiverCatalogueAndDispatch(t *testing.T) {
	b := &receiverCatalogueFixture{messageReceiveFixture: messageReceiveFixture{messageFixture: messageFixture{Backend: &fixtureBackend{}}}}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := NewMessagesReceiverServer(b).Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "arbitrary-client", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	tools, err := cs.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range tools.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	want := []string{"oa_messages_inbox_open", "oa_messages_inboxes_list", "oa_messages_prepare", "oa_messages_send", "oa_messages_reply", "oa_messages_wait"}
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("catalogue %v", names)
	}
	for _, name := range []string{ToolComplete, ToolApplications, "oa_asks_answer", "oa_messages_request_access", "oa_messages_watch", "unknown"} {
		out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !out.IsError {
			t.Fatalf("unavailable tool accepted: %s", name)
		}
	}
	if b.unrelated != 0 || b.admissions != 0 {
		t.Fatalf("unrelated tool reached backend: %d/%d", b.unrelated, b.admissions)
	}
	out, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolMessagesWait, Arguments: map[string]any{"wait_ms": 0}})
	if err != nil || out.IsError || b.admissions != 1 {
		t.Fatal(out, err, b.admissions)
	}
	instructions := cs.InitializeResult().Instructions
	if strings.Contains(instructions, "trusted-context") || !strings.Contains(instructions, "omit context") || !strings.Contains(instructions, "untrusted") || strings.Contains(instructions, "oa_applications_list") || strings.Contains(instructions, "Asks") {
		t.Fatal(instructions)
	}
	copy := MessagesReceiverToolNames()
	copy[0] = "mutated"
	if MessagesReceiverToolNames()[0] != "oa_messages_inbox_open" {
		t.Fatal("mutable catalogue")
	}
}
