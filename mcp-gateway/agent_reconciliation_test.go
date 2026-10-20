package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type reconciliationNative struct {
	nativeMessages
	mu              sync.Mutex
	values          []string
	lost, forbidden bool
	effects         int
}

func (p *reconciliationNative) InboxOpen(instance, contextName, value string) (msg.InboxChange, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.values = append(p.values, value)
	if p.forbidden || instance != "original" || contextName != "original-context" {
		return msg.InboxChange{Outcome: msg.OutcomeForbidden}, nil
	}
	p.effects++
	if p.lost {
		p.lost = false
		return msg.InboxChange{}, errors.New("explicit reply lost after status applied")
	}
	return msg.InboxChange{Outcome: msg.OutcomeOpened}, nil
}
func (p *reconciliationNative) snapshot() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.values...)
}

type reconciliationBackend struct {
	gateway.Backend
	models atomic.Int32
}

// Exercise the admitted connection owner, not just the local admission gate.
// A typed unknown same-status proof must refuse effects without closing MCP.
func TestAgentUnknownUpkeepKeepsConnectionUntilOriginalLeaseBoundary(t *testing.T) {
	for _, mode := range []string{"recover", "expired", "forbidden"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			base := time.Now()
			var elapsed atomic.Int64
			timers := make(chan chan time.Time, 4)
			ticks := make(chan time.Time, 1)
			a := new(agentToolAdmission)
			state := &messagesInboxStatus{admission: a}
			p := new(statusMessagesProbe)
			models := new(reconciliationBackend)
			b := &messagesBackend{Backend: models, instance: "original", contextName: "original-context", status: state, native: func(context.Context) (nativeMessages, error) { return p, nil }}
			f := &agentPresenceFixture{announce: func(int) (facade.ApplicationChange, error) {
				return facade.ApplicationChange{Outcome: facade.ApplicationOutcomeApplied, Instance: "original"}, nil
			}}
			st, ct := mcp.NewInMemoryTransports()
			done := make(chan error, 1)
			presence := make(chan error, 1)
			go func() {
				done <- serveAgentConnection(ctx, func(ctx context.Context) {
					presence <- maintainAgentPresenceWithPolicy(ctx, f, func(ctx context.Context, instance, name string) error {
						out, err := state.openContext(ctx, p, instance, name, nil)
						if err == nil && out.Outcome != msg.OutcomeOpened {
							return &agentInboxOutcomeError{outcome: out.Outcome}
						}
						return err
					}, "fixture-agent", "original-context", nil, ticks, agentPresencePolicy{admission: a, readmit: true, instance: state.setInstance, now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }, after: func(time.Duration) <-chan time.Time { c := make(chan time.Time, 1); timers <- c; return c }})
				}, b, func(ctx context.Context, backend gateway.Backend) error {
					return gateway.NewServer(backend).Run(ctx, st)
				})
			}()
			takeTimer := func() chan time.Time {
				t.Helper()
				select {
				case c := <-timers:
					return c
				case <-time.After(5 * time.Second):
					t.Fatal("missing presence timer")
					return nil
				}
			}
			expiry := takeTimer()
			client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			call := func() *mcp.CallToolResult {
				t.Helper()
				callCtx, stop := context.WithTimeout(ctx, 5*time.Second)
				defer stop()
				out, err := client.CallTool(callCtx, &mcp.CallToolParams{Name: gateway.ToolModels, Arguments: map[string]any{}})
				if err != nil {
					t.Fatal("MCP connection closed", err)
				}
				return out
			}
			// A presence loss ends the attempt, never the MCP connection:
			// the client still gets a typed unavailable answer.
			lostButOpen := func(want string) {
				t.Helper()
				if err := agentMCPResult(t, presence); err == nil || !strings.Contains(err.Error(), want) {
					t.Fatal(err)
				}
				select {
				case err := <-done:
					t.Fatal("presence loss closed the MCP connection", err)
				default:
				}
				out := call()
				if !out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, `"unavailable"`) || models.models.Load() != 0 {
					t.Fatal("lost admission answered", out)
				}
				cancel()
				agentMCPResult(t, done)
			}
			p.mu.Lock()
			p.outcome = msg.OutcomeUnknown
			p.mu.Unlock()
			if !call().IsError || models.models.Load() != 0 {
				t.Fatal("unknown proof admitted effects")
			}
			if !call().IsError || len(p.snapshot()) != 2 {
				t.Fatal("suspended tool replayed native proof")
			}
			select {
			case err := <-done:
				t.Fatal("unknown upkeep closed connection", err)
			default:
			}
			if mode == "expired" {
				elapsed.Store(int64(30 * time.Second))
				expiry <- base.Add(30 * time.Second)
				lostButOpen("lease expired")
				return
			}
			p.mu.Lock()
			p.outcome = msg.OutcomeOpened
			if mode == "forbidden" {
				p.outcome = msg.OutcomeForbidden
			}
			p.mu.Unlock()
			elapsed.Store(int64(10 * time.Second))
			ticks <- base.Add(10 * time.Second)
			if mode == "forbidden" {
				lostButOpen("forbidden")
				return
			}
			takeTimer() // Fresh original-binding confirmation, without replaying Models.
			if models.models.Load() != 0 || call().IsError || models.models.Load() != 1 {
				t.Fatal("fresh proof did not resume exactly one requested dispatch")
			}
			for _, presence := range f.announcements[1:] {
				if presence.Instance != "original" || presence.Contexts[0].Name != "original-context" {
					t.Fatal("rebound original", presence)
				}
			}
			cancel()
			agentMCPResult(t, done)
		})
	}
}

func (b *reconciliationBackend) Models(context.Context, gateway.ModelsInput) (gateway.ModelsOutput, error) {
	b.models.Add(1)
	return gateway.ModelsOutput{Models: []gateway.Model{}}, nil
}

func TestAgentAdmissionExplicitStatusReconciliationSurvivesHeartbeat(t *testing.T) {
	for _, mode := range []string{"reconcile", "revoked", "expired"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			var elapsed atomic.Int64
			base := time.Now()
			type timer struct {
				wait time.Duration
				c    chan time.Time
			}
			timers := make(chan timer, 4)
			ticks := make(chan time.Time, 1)

			a := new(agentToolAdmission)
			state := &messagesInboxStatus{admission: a}
			p := new(reconciliationNative)
			backend := new(reconciliationBackend)
			b := &messagesBackend{Backend: backend, instance: "original", contextName: "original-context", status: state, native: func(context.Context) (nativeMessages, error) { return p, nil }}
			f := &agentPresenceFixture{announce: func(int) (facade.ApplicationChange, error) {
				return facade.ApplicationChange{Outcome: facade.ApplicationOutcomeApplied, Instance: "original"}, nil
			}}
			done := make(chan error, 1)
			go func() {
				done <- maintainAgentPresenceWithPolicy(ctx, f, func(ctx context.Context, _, _ string) error {
					_, err := state.openContext(ctx, p, "original", "original-context", nil)
					return err
				}, "fixture-agent", "original-context", nil, ticks, agentPresencePolicy{admission: a, now: func() time.Time { return base.Add(time.Duration(elapsed.Load())) }, after: func(d time.Duration) <-chan time.Time { c := make(chan time.Time, 1); timers <- timer{d, c}; return c }})
			}()
			takeTimer := func() timer {
				t.Helper()
				select {
				case value := <-timers:
					return value
				case <-time.After(5 * time.Second):
					t.Fatal("presence timer was not scheduled")
					return timer{}
				}
			}
			expiry := takeTimer()
			st, ct := mcp.NewInMemoryTransports()
			server, err := gateway.NewServer(b).Connect(ctx, st, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()
			client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
			if err != nil {
				t.Fatal(err)
			}
			defer client.Close()
			call := func(name, status string) *mcp.CallToolResult {
				t.Helper()
				args := map[string]any{}
				if name == "oa_messages_inbox_open" {
					args = map[string]any{"context": "original-context", "status": status}
				}
				callCtx, done := context.WithTimeout(ctx, 5*time.Second)
				defer done()
				out, e := client.CallTool(callCtx, &mcp.CallToolParams{Name: name, Arguments: args})
				if e != nil {
					t.Fatal(e)
				}
				return out
			}
			p.mu.Lock()
			p.lost = true
			p.mu.Unlock()
			lost := call("oa_messages_inbox_open", "busy")
			if !strings.Contains(lost.Content[0].(*mcp.TextContent).Text, `"unknown"`) {
				t.Fatal(lost)
			}
			if !call(gateway.ToolModels, "").IsError || backend.models.Load() != 0 {
				t.Fatal("unknown explicit status admitted models")
			}
			elapsed.Store(int64(10 * time.Second))
			ticks <- base.Add(10 * time.Second)
			retry := takeTimer()
			if retry.wait != agentPresenceRetryDelay || agentAdmissionLost(a) != 0 || !reflect.DeepEqual(p.snapshot(), []string{"", "busy"}) {
				t.Fatal("heartbeat replayed status or ended reconciliation", retry.wait, agentAdmissionLost(a), p.snapshot())
			}
			// Different arguments cannot silently reconcile the unknown operation.
			call("oa_messages_inbox_open", "different")
			if len(p.snapshot()) != 2 {
				t.Fatal("changed request replayed unknown status")
			}
			switch mode {
			case "reconcile":
				out := call("oa_messages_inbox_open", "busy")
				if out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, `"opened"`) {
					t.Fatal(out)
				}
				if call(gateway.ToolModels, "").IsError || backend.models.Load() != 1 || !reflect.DeepEqual(p.snapshot(), []string{"", "busy", "busy", "busy"}) {
					t.Fatal("explicit request did not reconcile once", backend.models.Load(), p.snapshot())
				}
			case "revoked":
				p.mu.Lock()
				p.forbidden = true
				p.mu.Unlock()
				call("oa_messages_inbox_open", "busy")
				if agentAdmissionLost(a) != 1 || !call(gateway.ToolModels, "").IsError || backend.models.Load() != 0 {
					t.Fatal("revoked original resumed effects", agentAdmissionLost(a), backend.models.Load())
				}
				p.mu.Lock()
				effects := p.effects
				p.mu.Unlock()
				if effects != 2 {
					t.Fatal("revoked original committed a status effect", effects)
				}
			case "expired":
				a.mu.Lock()
				a.deadline = time.Now().Add(-time.Second)
				a.mu.Unlock()
				if !call("oa_messages_inbox_open", "busy").IsError || len(p.snapshot()) != 2 {
					t.Fatal("expired original replayed explicit request", p.snapshot())
				}
				elapsed.Store(int64(30 * time.Second))
				expiry.c <- base.Add(30 * time.Second)
				if err := agentMCPResult(t, done); err == nil || !strings.Contains(err.Error(), "lease expired") {
					t.Fatal(err)
				}
				return
			}
			cancel()
			if err := agentMCPResult(t, done); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
		})
	}
}
