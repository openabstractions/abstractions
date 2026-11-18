package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// 2026-11-22: after a core reinstall the Claude helper answered "reconnecting
// this chat" to every tool call for more than five minutes. A loss that
// outlasts the short reconnect window names its cause in plain words; the
// next admission clears it.
func TestReconnectingNamesItsCauseAfterAShortWindow(t *testing.T) {
	shortenAgentTiming(t)
	saved := agentReconnectPlainAfter
	agentReconnectPlainAfter = 150 * time.Millisecond
	t.Cleanup(func() { agentReconnectPlainAfter = saved })
	runtime := newChaosRuntime()
	h := startChaosHelper(t, runtime, new(chaosQueue), 191)
	h.recovered(3 * time.Second)
	detail := func() (string, string) {
		t.Helper()
		out := h.call(gateway.ToolMessagesInboxesList, map[string]any{})
		var answer map[string]any
		if len(out.Content) == 0 || json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &answer) != nil {
			return "", ""
		}
		state, _ := answer["state"].(string)
		text, _ := answer["detail"].(string)
		return state, text
	}
	runtime.restart(true)
	var state, text string
	for until := time.Now().Add(3 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		state, text = detail()
		if strings.Contains(text, "has not reconnected this chat") || time.Now().After(until) {
			break
		}
	}
	if state != agentToolReconnecting || !strings.Contains(text, "reconnect OpenAbstractions in this app") {
		t.Fatal("a long outage answered without its cause", state, text)
	}
	runtime.set(func(r *chaosRuntime) { r.down = false })
	h.recovered(3 * time.Second)
	h.status.admission.mu.Lock()
	cleared := h.status.admission.cause == nil && h.status.admission.lostSince.IsZero()
	h.status.admission.mu.Unlock()
	if !cleared {
		t.Fatal("re-admission kept the old cause")
	}
}

func TestReconnectCauseNamesRuntimeStates(t *testing.T) {
	for err, want := range map[error]string{
		fmt.Errorf("resolve: %w", listen.ErrServerUntrusted):       "older copy",
		&client.ResolutionError{Status: client.RuntimeUnavailable}: "not running or not answering",
		&client.ResolutionError{Status: client.UpgradeInProgress}:  "being updated",
		fmt.Errorf("presence: %w", context.DeadlineExceeded):       "did not answer in time",
		&agentInboxOutcomeError{outcome: 5}:                        "to this chat's inbox",
	} {
		if got := agentReconnectCause(err); !strings.Contains(got, want) {
			t.Fatal(err, got)
		}
	}
}
