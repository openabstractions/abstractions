package main

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestApplicationV3GatewayMCPOutcomePayloads(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	f.description.Kinds[0].Fields = []appmodel.KindField{{Name: "api_key", Role: appmodel.FieldRoleSecret}}
	backend := &applicationV3MCPBackend{c: f.c}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := gateway.NewServer(backend).Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "outcome-fixture", Version: "1"}, nil).Connect(t.Context(), ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	invoke := func(n int, tool string, arguments any) *appmodel.CommandResult {
		t.Helper()
		backend.key = receiptKey(n, time.Now())
		got, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: gateway.ToolApplicationsCall, Arguments: map[string]any{"application": "fixture", "instance": f.instance, "tool": tool, "arguments": arguments}})
		if err != nil {
			t.Fatal(err)
		}
		result, err := appmodel.DecodeCommandResult([]byte(got.Content[0].(*mcp.TextContent).Text))
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	// The command's own input_schema object; the gateway builds the Invocation.
	argumentJSON := []byte(`{}`)
	for outcome := appmodel.CommandOutcomeProposed; outcome <= appmodel.CommandOutcomeFailed; outcome++ {
		t.Run(outcome.String(), func(t *testing.T) {
			want := &appmodel.CommandResult{Outcome: outcome, Reason: "fixture", Revision: "r1"}
			if outcome == appmodel.CommandOutcomeProposed {
				want.Proposal = "p1"
				want.Summary = "Proposed change"
			}
			if outcome == appmodel.CommandOutcomeAccepted {
				want.Job = "j1"
			}
			raw, err := appmodel.ToJSON(want)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.answer = func(tool string, _ json.RawMessage) []byte {
				if tool == "note.read" {
					return raw
				}
				return nil
			}
			f.mu.Unlock()
			got := invoke(200+int(outcome), "note.read", json.RawMessage(argumentJSON))
			if len(got.IdempotencyKey) != 32 {
				t.Fatalf("generated key not echoed: %+v", got)
			}
			got.IdempotencyKey = ""
			encoded, err := appmodel.ToJSON(got)
			if err != nil || !bytes.Equal(encoded, raw) {
				t.Fatalf("outcome payload changed: %s want %s: %v", encoded, raw, err)
			}
		})
	}
	for _, outcome := range []appmodel.CommandOutcome{appmodel.CommandOutcomeAccepted, appmodel.CommandOutcomeFailed} {
		t.Run("job_"+outcome.String(), func(t *testing.T) {
			want := &appmodel.CommandResult{Outcome: outcome, Job: "j1", Reason: "fixture"}
			raw, err := appmodel.ToJSON(want)
			if err != nil {
				t.Fatal(err)
			}
			f.mu.Lock()
			f.answer = func(tool string, _ json.RawMessage) []byte {
				if tool == "app_job" {
					return raw
				}
				return nil
			}
			f.mu.Unlock()
			got := invoke(230+int(outcome), "app_job", map[string]string{"instance": f.instance, "job": "j1"})
			encoded, err := appmodel.ToJSON(got)
			if err != nil || !bytes.Equal(encoded, raw) {
				t.Fatalf("job payload changed: %s want %s: %v", encoded, raw, err)
			}
		})
	}
	got := invoke(250, "note.undeclared", json.RawMessage(argumentJSON))
	if got.Outcome != appmodel.CommandOutcomeForbidden || got.Effects != "" {
		t.Fatalf("gateway refusal carries effects: %+v", got)
	}
	for i, effects := range []string{`{"api_key":"hidden"}`, `[]`, `null`} {
		f.mu.Lock()
		f.answer = func(tool string, _ json.RawMessage) []byte {
			if tool == "note.read" {
				raw, _ := appmodel.ToJSON(&appmodel.CommandResult{Outcome: appmodel.CommandOutcomeApplied, Effects: effects})
				return raw
			}
			return nil
		}
		f.mu.Unlock()
		got := invoke(260+i, "note.read", json.RawMessage(argumentJSON))
		if got.Outcome == appmodel.CommandOutcomeApplied || got.Effects != "" {
			t.Fatalf("unsafe applied effects passed: %+v", got)
		}
	}
}

// Reuse the native owner fixture, including real peer evidence, owned stdio,
// Rights, audit and persisted receipts. Its application effects remain inert.
type applicationV3MCPBackend struct {
	gateway.Backend
	c   *applications.ApplicationsClient
	key string
}

func (b *applicationV3MCPBackend) ApplicationsDescribe(_ context.Context, q appmodel.ApplicationsQuery) (applications.DescribeResult, error) {
	return b.c.Describe(q)
}
func (b *applicationV3MCPBackend) ApplicationsCommands(_ context.Context, q appmodel.CommandsQuery) (applications.CommandsResult, error) {
	return b.c.Commands(q)
}
func (b *applicationV3MCPBackend) ApplicationsCall(_ context.Context, q appmodel.GatewayCall) (applications.CallResult, error) {
	return b.c.Call(b.key, q)
}
func (b *applicationV3MCPBackend) ApplicationsOpen(_ context.Context, q applications.ActivateRequest) (applications.ActivationResult, error) {
	key, err := applications.NewRequestKey()
	if err != nil {
		return applications.ActivationResult{}, err
	}
	return b.c.Activate(key, q)
}

func TestApplicationV3GatewayMCPNativeComposition(t *testing.T) {
	f := newApplicationV3GatewayFixture(t)
	backend := &applicationV3MCPBackend{c: f.c, key: receiptKey(100, time.Now())}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := gateway.NewServer(backend).Connect(t.Context(), st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "forged-operator", Version: "forged"}, nil).Connect(t.Context(), ct, nil)
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
	for _, name := range appmodel.GatewayTools {
		if !slices.Contains(names, name) {
			t.Fatalf("missing %s", name)
		}
	}
	invoke := func(name string, args any) *mcp.CallToolResult {
		t.Helper()
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	for _, name := range []string{gateway.ToolApplicationsDescribe, gateway.ToolApplicationsCommands} {
		args := map[string]any{"application": "fixture"}
		if name == gateway.ToolApplicationsCommands {
			args["instance"] = f.instance
		}
		result := invoke(name, args)
		if result.IsError || result.StructuredContent.(map[string]any)["outcome"] != "page" {
			t.Fatalf("discovery %s: %+v", name, result)
		}
	}
	args := map[string]any{"application": "fixture", "instance": f.instance, "tool": "note.read", "arguments": map[string]any{}, "expected_revision": "r1", "idempotency_key": "inner-key"}
	result := invoke(gateway.ToolApplicationsCall, args)
	if result.IsError || result.StructuredContent.(map[string]any)["outcome"] != "applied" || f.effects.Load() != 1 {
		t.Fatalf("native call: %+v effects=%d", result, f.effects.Load())
	}
	result = invoke(gateway.ToolApplicationsCall, args)
	if result.IsError || result.StructuredContent.(map[string]any)["outcome"] != "applied" || f.effects.Load() != 1 {
		t.Fatalf("native replay: %+v effects=%d", result, f.effects.Load())
	}
	args["arguments"] = map[string]any{"changed": true}
	result = invoke(gateway.ToolApplicationsCall, args)
	if !result.IsError || result.StructuredContent.(map[string]any)["outcome"] != "conflict" || f.effects.Load() != 1 {
		t.Fatalf("changed operation: %+v effects=%d", result, f.effects.Load())
	}
	backend.key = receiptKey(103, time.UnixMilli(1))
	result = invoke(gateway.ToolApplicationsCall, args)
	if !result.IsError || result.StructuredContent.(map[string]any)["outcome"] != "unknown" || f.effects.Load() != 1 {
		t.Fatalf("expired operation: %+v effects=%d", result, f.effects.Load())
	}
	backend.key = receiptKey(101, time.Now())
	args["tool"] = "note.undeclared"
	result = invoke(gateway.ToolApplicationsCall, args)
	if !result.IsError || result.StructuredContent.(map[string]any)["outcome"] != "forbidden" || f.effects.Load() != 1 {
		t.Fatalf("manifest refusal: %+v", result)
	}
	args["tool"] = "note.read"
	backend.key = receiptKey(102, time.Now())
	if err := f.g.policy.policy.Set(f.subject, "abstraction.facade/command.read", "app:fixture", false); err != nil {
		t.Fatal(err)
	}
	result = invoke(gateway.ToolApplicationsCall, args)
	if !result.IsError || result.StructuredContent.(map[string]any)["outcome"] != "not_permitted" || f.effects.Load() != 1 {
		t.Fatalf("rights refusal: %+v", result)
	}
	// The text answer stays a generated AppModel record, independently decodable.
	if _, err := appmodel.DecodeCommandResult([]byte(result.Content[0].(*mcp.TextContent).Text)); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(result.StructuredContent)
	if !json.Valid(raw) {
		t.Fatal("invalid structured result")
	}
}
