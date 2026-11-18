package main

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

var chaosCompletes struct {
	sync.Mutex
	labels []string
}

func (b *chaosBackend) Complete(_ context.Context, in gateway.CompleteInput) (gateway.CompleteOutput, error) {
	chaosCompletes.Lock()
	chaosCompletes.labels = append(chaosCompletes.labels, in.Conversation)
	chaosCompletes.Unlock()
	return gateway.CompleteOutput{Outcome: "completed"}, nil
}

// Security review 2026-11-22, Codex follow-up 3: a worker thread's local
// oa_inference_complete borrowed the bound chat's conversation label and its
// engine slot. A worker's call now carries no label; the main chat's keeps
// its own.
func TestChaosCodexWorkerInferenceKeepsNoChatLabel(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	main := mcp.Meta{"threadId": h.thread}
	worker := mcp.Meta{"threadId": "99999999-9999-4999-8999-999999999999"}
	if out := callAs(t, h, main, gateway.ToolMessagesInboxOpen, map[string]any{}); out.IsError {
		t.Fatal("main chat inbox open", out.Content[0].(*mcp.TextContent).Text)
	}
	h.recovered(5 * time.Second)
	chaosCompletes.Lock()
	chaosCompletes.labels = nil
	chaosCompletes.Unlock()
	args := map[string]any{"model": "m", "prompt": "p", "hosting": "local"}
	callAs(t, h, worker, gateway.ToolComplete, args)
	callAs(t, h, main, gateway.ToolComplete, args)
	chaosCompletes.Lock()
	labels := append([]string(nil), chaosCompletes.labels...)
	chaosCompletes.Unlock()
	if len(labels) != 2 || labels[0] != "" || labels[1] == "" {
		t.Fatalf("worker and main labels %q", labels)
	}
}
