package main

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestColdReplyViewKeepsActualClaudeLifecycleToolsPrivateAndMissingBindingClosed(t *testing.T) {
	t.Setenv("CLAUDE_CODE_SESSION_ID", codexAttachThread)
	_, diagnosticContext, _ := diagnosticFixture(t)
	ctx, cancel := context.WithTimeout(diagnosticContext, 5*time.Second)
	defer cancel()
	b := &messagesBackend{application: "oa-claude-integration", contextName: "source"}
	st, ct := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runClaudeChatServer(ctx, gateway.NewMessagesReplyServer(b), b, st) }()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "cold-fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	list, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
	}
	sort.Strings(names)
	if !reflect.DeepEqual(names, []string{gateway.ToolMessagesReply, gateway.ToolMessagesWait}) {
		t.Fatal(names)
	}
	if out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: map[string]any{}}); err == nil && !out.IsError {
		t.Fatal("cold model invoked host lifecycle correlation", out)
	}
	b.chatMu.Lock()
	receiver := b.chatReceiver
	b.chatMu.Unlock()
	if receiver != nil {
		t.Fatal("missing independent host binding was fabricated")
	}
	cs.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cold fixture server did not join")
	}
}
