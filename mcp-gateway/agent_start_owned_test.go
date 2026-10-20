package main

import (
	"context"
	"errors"
	"net"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

func TestAgentStartOwnedBootstrapSingleFixedCall(t *testing.T) {
	for _, mode := range []string{"single", "target", "payload_refused", "missing_metadata", "malformed_metadata", "malformed_target", "canonical_missing", "cold_malformed", "cold_model_payload"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			serverSide, clientSide := net.Pipe()
			var calls atomic.Int32
			var received atomic.Value
			done := make(chan error, 1)
			go func() {
				done <- serveReceiverStartOwned(ctx, serverSide, serverSide, func(_ context.Context, _ startprofile.CustodyBinding, target string) receiverStartAnswer {
					calls.Add(1)
					received.Store(target)
					answer := receiverStartAnswer{Result: applications.HostActivationResult{Outcome: applications.HostActivationOutcomeApplied}}
					if target != "" {
						answer.Delivery = agentDeliveryDesktopQueue
					}
					return answer
				})
			}()
			client, err := mcp.NewClient(&mcp.Implementation{Name: "owned-runtime-fixture", Version: "1"}, nil).Connect(ctx, &receiverStartTransport{&receiverStartConnection{reader: clientSide, writer: clientSide}}, &mcp.ClientSessionOptions{ProtocolVersion: "2025-11-25"})
			if err != nil {
				t.Fatal(err)
			}
			args := map[string]any{}
			if mode == "payload_refused" {
				args["prompt"] = "PEER_BODY_NOT_AUTHORITY"
			}
			if mode == "cold_model_payload" {
				args["oa_receiver_cold"] = map[string]any{"context": "chat-forged"}
			}
			binding := startprofile.CustodyBinding{Account: "fixture-account", ProgramKey: "fixture-key", EnrollmentRevision: "fixture-enrollment", RecipeDigest: strings.Repeat("a", 64)}
			meta := binding.Metadata()
			if mode == "missing_metadata" {
				meta = nil
			}
			if mode == "target" {
				meta[startprofile.TargetMetaKey] = "attach-0123456789abcdef01234567"
			}
			if mode == "canonical_missing" {
				meta[startprofile.TargetMetaKey] = "chat-forged"
			}
			if mode == "cold_malformed" {
				meta[startprofile.TargetMetaKey] = "chat-forged"
				meta[startprofile.ColdResumeMetaKey] = map[string]any{"context": "chat-forged"}
			}
			if mode == "malformed_target" {
				meta[startprofile.TargetMetaKey] = "../peer chosen"
			}
			if mode == "malformed_metadata" {
				meta = map[string]any{startprofile.CustodyMetaKey: map[string]any{"account": "spoofed"}}
			}
			result, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "receiver_start", Arguments: args, Meta: meta})
			if mode != "single" && mode != "target" {
				if err == nil && !result.IsError {
					t.Fatal("nonempty private payload accepted")
				}
				if calls.Load() != 0 {
					t.Fatal(calls.Load())
				}
			} else {
				if err != nil || result.IsError || len(result.Content) != 1 {
					t.Fatal(result, err)
				}
				out, err := applications.DecodeHostActivationResult([]byte(result.Content[0].(*mcp.TextContent).Text))
				if err != nil || out.Outcome != applications.HostActivationOutcomeApplied {
					t.Fatal(out, err)
				}
				if again, err := client.CallTool(ctx, &mcp.CallToolParams{Name: "receiver_start", Arguments: map[string]any{}}); err == nil && !again.IsError {
					t.Fatal("second dispatch accepted")
				}
				if calls.Load() != 1 {
					t.Fatal(calls.Load())
				}
				if want := map[string]string{"single": "", "target": "attach-0123456789abcdef01234567"}[mode]; received.Load() != want {
					t.Fatal("target not passed exactly", received.Load())
				}
				// The runtime learns how an addressed session received the
				// turn on the same private channel; OA's own conversation
				// reports nothing.
				if want := map[string]any{"single": nil, "target": agentDeliveryDesktopQueue}[mode]; result.Meta[agentDeliveryMetaKey] != want {
					t.Fatal("delivery not reported", result.Meta)
				}
			}
			client.Close()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestAgentStartOwnedConfirmationPrecedesCarrierAndVendor(t *testing.T) {
	folder := t.TempDir()
	p := startprofile.Profile{Name: "claude-v1", Context: "fixed", Folder: folder, StateDir: filepath.Join(folder, "state"), CLIFileID: "registered-file-id", Model: "registered-model", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	confirmed := 0
	if code := ownedReceiverStartCommand(p.Arguments(), nil, nil, func(context.Context, startprofile.Profile) error { confirmed++; return errors.New("unknown_method") }); code != 3 || confirmed != 1 {
		t.Fatal(code, confirmed)
	}
}
