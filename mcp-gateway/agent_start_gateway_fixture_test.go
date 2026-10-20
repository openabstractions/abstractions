package main

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"sort"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// Compiled only into the native source fixture. It supplies an explicit trusted
// test server and runs the same admitted gateway/presence owners as main.
func TestAgentStartGatewayFixture(t *testing.T) {
	raw := os.Getenv("OA_RECEIVER_START_GATEWAY_FIXTURE")
	if raw == "" {
		return
	}
	var cfg struct {
		Endpoint, State, Application, Context string
		Server                                listen.ServerExpectation
		MessagesOnly                          bool
		ColdBinding, ColdCwdKey, ColdContext  string
	}
	if json.Unmarshal([]byte(raw), &cfg) != nil || cfg.Endpoint == "" || cfg.State == "" {
		t.Fatal("fixture configuration")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cold, err := parseClaudeColdBinding(cfg.ColdBinding, cfg.ColdCwdKey, cfg.ColdContext, cfg.ColdBinding != "")
	if err != nil {
		t.Fatal(err)
	}
	if cold != nil {
		ctx = context.WithValue(ctx, claudeColdBindingKey{}, cold)
		cfg.Context, err = agentContext("")
		if err != nil {
			t.Fatal(err)
		}
	}
	machine := client.NewVerified(cfg.Endpoint, cfg.Server)
	base, err := gateway.NewOA(machine, cfg.State)
	if err != nil {
		t.Fatal(err)
	}
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	messages, err := newMessagesBackendWithStatus(base, machine, cfg.Application, "", cfg.Context, status)
	if err != nil {
		t.Fatal(err)
	}
	err = serveAgentConnection(ctx, func(ctx context.Context) {
		superviseAgentPresence(ctx, func(ctx context.Context, ready func(string), readmit bool, stop func()) error {
			return runNativeAgentPresenceAttempt(ctx, machine, cfg.Application, cfg.Context, ready, status, readmit, stop)
		}, status, time.After, time.Now)
	}, messages, func(ctx context.Context, b gateway.Backend) error {
		server, err := newGatewayServer(b, cfg.MessagesOnly, cold != nil)
		if err != nil {
			return err
		}
		if cfg.MessagesOnly {
			// The actual owned vendor child reaches this admitted native server.
			// Inspect its real MCP catalogue, beyond the vendor argv approval list.
			st, ct := mcp.NewInMemoryTransports()
			ss, err := server.Connect(ctx, st, nil)
			if err != nil {
				return err
			}
			client := mcp.NewClient(&mcp.Implementation{Name: "owned-catalogue-fixture", Version: "1"}, nil)
			cs, err := client.Connect(ctx, ct, nil)
			if err != nil {
				ss.Close()
				return err
			}
			tools, err := cs.ListTools(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			names := []string{}
			for _, tool := range tools.Tools {
				names = append(names, tool.Name)
			}
			want := gateway.MessagesReceiverToolNames()
			sort.Strings(names)
			sort.Strings(want)
			if !reflect.DeepEqual(names, want) {
				t.Fatalf("actual owned child catalogue: %v", names)
			}
			for _, name := range []string{gateway.ToolComplete, gateway.ToolApplications} {
				out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
				if err == nil && !out.IsError {
					t.Fatalf("unrelated child tool available: %s", name)
				}
			}
			cs.Close()
			ss.Close()
		}
		if cold != nil {
			return runNativeMCPServerTransport(ctx, server, b, cfg.State, nil, nil, &mcp.StdioTransport{MaxLineLength: 1 << 20})
		}
		return server.Run(ctx, &mcp.StdioTransport{MaxLineLength: 1 << 20})
	})
	if err != nil && ctx.Err() == nil {
		t.Fatal(err)
	}
}
