package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestAgentAdmissionSuspendsCancelsAndRequiresFreshConfirmation(t *testing.T) {
	a := new(agentToolAdmission)
	if _, _, ok := a.begin(context.Background()); ok {
		t.Fatal("initial unknown admitted")
	}
	if _, _, ok := a.beginOriginal(context.Background(), true); ok {
		t.Fatal("explicit Open borrowed an unconfirmed initial admission")
	}
	a.confirm(time.Now().Add(time.Minute))
	previous := a.version()
	first, doneFirst, ok := a.begin(context.Background())
	if !ok {
		t.Fatal("confirmed refused")
	}
	defer doneFirst()
	second, doneSecond, ok := a.begin(context.Background())
	if !ok {
		t.Fatal("second refused")
	}
	defer doneSecond()
	a.setReady(false)
	if a.confirmVersion(time.Now().Add(time.Minute), previous) {
		t.Fatal("stale heartbeat resumed a suspended generation")
	}
	if first.Err() != context.Canceled || second.Err() != context.Canceled {
		t.Fatal("in-flight native calls survived suspension")
	}
	if _, _, ok := a.begin(context.Background()); ok {
		t.Fatal("unknown admitted")
	}
	a.confirm(time.Now().Add(time.Minute))
	_, done, ok := a.begin(context.Background())
	if !ok {
		t.Fatal("fresh confirmation refused")
	}
	done()
	a.confirm(time.Now().Add(-time.Second))
	if _, _, ok := a.begin(context.Background()); ok {
		t.Fatal("expired confirmation admitted before timer scheduling")
	}
	// A definitive loss suspends tools until presence admits the context
	// again; it never ends the connection.
	a.confirm(time.Now().Add(time.Minute))
	stale := a.version()
	a.lose(errors.New("runtime pipe closed"))
	if _, _, ok := a.begin(context.Background()); ok {
		t.Fatal("lost admission answered tools")
	}
	if state, _ := a.unavailable(); state != agentToolReconnecting {
		t.Fatal("lost admission state", state)
	}
	if a.confirmVersion(time.Now().Add(time.Minute), stale) {
		t.Fatal("a renewal begun before the loss resumed tools")
	}
	a.confirm(time.Now().Add(time.Minute))
	if _, done, ok := a.begin(context.Background()); !ok {
		t.Fatal("re-admission after a loss did not resume tools")
	} else {
		done()
	}
	// Only the end of the stdio connection retires it.
	a.retire()
	a.confirm(time.Now().Add(time.Minute))
	if _, _, ok := a.begin(context.Background()); ok {
		t.Fatal("retired admission resumed")
	}
}

func TestAgentAdmissionNativeRevalidationBlocksModelAndInferenceAfterBindingLoss(t *testing.T) {
	for _, name := range []string{gateway.ToolModels, gateway.ToolComplete, gateway.ToolJobSubmit, gateway.ToolJobsFollow} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()

			a := new(agentToolAdmission)
			a.confirm(time.Now().Add(time.Minute))
			p := &statusMessagesProbe{outcome: msg.OutcomeForbidden}
			// The nil underlying Backend panics if stale Ready allows dispatch.
			b := &messagesBackend{instance: "original", contextName: "original-context", status: &messagesInboxStatus{admission: a}, native: func(context.Context) (nativeMessages, error) { return p, nil }}
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
			out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
			if err != nil || !out.IsError || agentAdmissionLost(a) != 1 || len(p.snapshot()) != 1 {
				t.Fatal(out, err, agentAdmissionLost(a), p.snapshot())
			}
		})
	}
}

func TestAgentAdmissionFreshNativeProofPrecedesEveryDispatch(t *testing.T) {
	for _, outcome := range []msg.Outcome{msg.OutcomeForbidden, msg.OutcomeUnavailable, msg.OutcomeUnknown} {
		t.Run(outcome.String(), func(t *testing.T) {

			a := new(agentToolAdmission)
			a.confirm(time.Now().Add(time.Minute))
			p := &statusMessagesProbe{}
			b := &messagesBackend{instance: "original", contextName: "original-context", status: &messagesInboxStatus{admission: a}, native: func(context.Context) (nativeMessages, error) { return p, nil }}
			_, done, ok := b.BeginNativeTool(context.Background(), gateway.ToolModels)
			if !ok {
				t.Fatal("fresh positive refused")
			}
			done()
			if len(p.snapshot()) != 1 {
				t.Fatal("dispatch skipped native proof")
			}
			// Revocation/re-Keep is observed here before the background heartbeat.
			p.outcome = outcome
			effects := 0
			_, done, ok = b.BeginNativeTool(context.Background(), gateway.ToolModels)
			if ok {
				effects++
				done()
			}
			if ok || effects != 0 || len(p.snapshot()) != 2 {
				t.Fatal("stale ready state dispatched", ok, effects, p.snapshot())
			}
			wantStop := 0
			if outcome == msg.OutcomeForbidden {
				wantStop = 1
			}
			if agentAdmissionLost(a) != wantStop {
				t.Fatal(agentAdmissionLost(a), wantStop)
			}
			if _, _, ok = b.BeginNativeTool(context.Background(), gateway.ToolModels); ok || len(p.snapshot()) != 2 {
				t.Fatal("suspended gate retried a user operation")
			}
		})
	}
}

func TestAgentAdmissionPreservesAtomicExplicitOpenUntilTerminalLoss(t *testing.T) {
	a := new(agentToolAdmission)
	a.confirm(time.Now().Add(time.Minute))
	call, done, ok := a.beginOriginal(context.Background(), true)
	defer done()
	if !ok {
		t.Fatal("original explicit Open refused")
	}
	a.setReady(false)
	if call.Err() != nil {
		t.Fatal("known uncertainty canceled explicit reconciliation", call.Err())
	}
	a.lose(errors.New("inbox refused"))
	if call.Err() != context.Canceled {
		t.Fatal("terminal loss retained explicit operation")
	}
}

func TestAgentAdmissionLifetimeRetirementCancelsExplicitWithoutParentStop(t *testing.T) {

	a := new(agentToolAdmission)
	a.confirm(time.Now().Add(time.Minute))
	call, done, ok := a.beginOriginal(context.Background(), true)
	defer done()
	if !ok {
		t.Fatal("explicit original refused")
	}
	a.retire()
	if call.Err() != context.Canceled {
		t.Fatal("lifetime retirement did not cancel the explicit operation", call.Err())
	}
}

// agentAdmissionLost is 1 after a definitive loss a tool call observed.
func agentAdmissionLost(a *agentToolAdmission) int {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.lost != nil {
		return 1
	}
	return 0
}
