package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestOneNativeHelperMultiplexesImageWaitModelsAndSend(t *testing.T) {
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	waiting, release := make(chan struct{}), make(chan struct{})
	image := func(ctx context.Context, _ gateway.ImageInput) (gateway.ImageOutput, error) {
		close(waiting)
		select {
		case <-release:
			return gateway.ImageOutput{Outcome: "working", Message: "fixture image wait ended"}, nil
		case <-ctx.Done():
			return gateway.ImageOutput{}, ctx.Err()
		}
	}
	h := startChaosHelperConfigured(t, runtime, queue, 0, func(ctx context.Context) context.Context {
		return context.WithValue(ctx, chaosImageKey{}, image)
	}, false, "oa-opencode-integration", "ses_multiplex fixture")
	released := false
	defer func() {
		if !released {
			close(release)
		}
	}()
	deadline, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	for {
		out := h.call(openCodeChatStartTool, map[string]any{})
		var bound struct{ Outcome string }
		_ = json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &bound)
		if bound.Outcome == "observed" {
			break
		}
		select {
		case <-deadline.Done():
			t.Fatal("OpenCode chat never bound")
		case <-time.After(50 * time.Millisecond):
		}
	}

	type toolReply struct {
		out *mcp.CallToolResult
		err error
	}
	call := func(ctx context.Context, name string, arguments map[string]any) <-chan toolReply {
		done := make(chan toolReply, 1)
		go func() {
			out, err := h.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
			done <- toolReply{out: out, err: err}
		}()
		return done
	}
	picture := call(deadline, gateway.ToolImageGenerate, map[string]any{"prompt": "an inert test picture", "wait_ms": 10000})
	select {
	case <-waiting:
	case reply := <-picture:
		t.Fatalf("image did not enter its wait: %+v, %v", reply.out, reply.err)
	case <-deadline.Done():
		t.Fatal("image wait never started")
	}
	quick, stop := context.WithTimeout(deadline, 2*time.Second)
	defer stop()
	models := call(quick, gateway.ToolModels, map[string]any{})
	send := call(quick, gateway.ToolMessagesSend, map[string]any{
		"to": "app:oa-claude-integration", "context": "target", "kind": "note", "body": "concurrent send",
		"idempotency_key": "multiplex-send", "expires_unix_ms": time.Now().Add(time.Hour).UnixMilli(),
	})
	if reply := <-models; reply.err != nil || reply.out == nil || reply.out.IsError || h.backend.models.Load() == 0 {
		t.Fatalf("models did not answer during the image wait: %+v, %v", reply.out, reply.err)
	}
	reply := <-send
	if reply.err != nil || reply.out == nil || reply.out.IsError || len(reply.out.Content) == 0 {
		t.Fatalf("send did not answer during the image wait: %+v, %v", reply.out, reply.err)
	}
	var sent struct{ Outcome string }
	if err := json.Unmarshal([]byte(reply.out.Content[0].(*mcp.TextContent).Text), &sent); err != nil || sent.Outcome != "sent" {
		t.Fatalf("concurrent send outcome: %+v, %v", sent, err)
	}
	select {
	case reply := <-picture:
		t.Fatalf("image ended before its release: %+v, %v", reply.out, reply.err)
	default:
	}
	close(release)
	released = true
	reply = <-picture
	if reply.err != nil || reply.out == nil || reply.out.IsError || len(reply.out.Content) == 0 ||
		reply.out.Content[0].(*mcp.TextContent).Text != "fixture image wait ended" {
		t.Fatalf("image reply after release: %+v, %v", reply.out, reply.err)
	}
}
