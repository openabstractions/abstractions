package main

import (
	"bytes"
	"context"
	"errors"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"io"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

type agentMCPFixtureBackend struct{ gateway.Backend }

func agentMCPResult(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(time.Second):
		t.Fatal("agent MCP did not join")
		return nil
	}
}

func TestAgentMCPRoutingAndContextAreNotVendorIdentity(t *testing.T) {
	for _, name := range []string{"openabstractions-mcp.exe", "claude.exe", "other-oa-claude-integration.exe"} {
		if dedicatedAgentApplication(name) != "" {
			t.Fatal("generic or vendor image selected", name)
		}
	}
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	if runtime.GOOS == "windows" || runtime.GOOS == "darwin" {
		if dedicatedAgentApplication("oa-claude-integration"+suffix) != "oa-claude-integration" || dedicatedAgentApplication("oa-codex-integration"+suffix) != "oa-codex-integration" {
			t.Fatal("dedicated routing missing")
		}
	}
	for _, wrong := range []string{"oa-claude-integration.bak", "oa-codex-integration.exe.old"} {
		if dedicatedAgentApplication(wrong) != "" {
			t.Fatal("noncanonical routing", wrong)
		}
	}
	contextName, err := freshAgentContext(bytes.NewReader(bytes.Repeat([]byte{1}, 16)))
	if err != nil || len(contextName) != 39 || !strings.HasPrefix(contextName, "native-") {
		t.Fatal(contextName, err)
	}
	if label, err := freshAgentContext(io.LimitReader(strings.NewReader("short"), 5)); err == nil || label != "" {
		t.Fatal("failed entropy produced a label", label, err)
	}
}

// The server serves from the start, before presence has admitted anything,
// and presence ending, for any reason, never ends it.
func TestAgentMCPServesBeforeAndAfterPresence(t *testing.T) {
	presenceEnded := make(chan struct{})
	serving := make(chan struct{})
	stdioEOF := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveAgentConnection(context.Background(), func(context.Context) {
			<-serving
			close(presenceEnded) // presence gave up early; the server must not care
		}, &agentMCPFixtureBackend{}, func(ctx context.Context, _ gateway.Backend) error {
			close(serving)
			<-stdioEOF
			return nil
		})
	}()
	<-presenceEnded
	select {
	case err := <-done:
		t.Fatal("presence ending closed the MCP server", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(stdioEOF)
	if err := agentMCPResult(t, done); err != nil {
		t.Fatal(err)
	}
}

// Stdio EOF ends the server first; presence is then cancelled and joined, so
// the instance is withdrawn after the server stopped answering.
func TestAgentMCPStdioEOFJoinsServerBeforeWithdrawal(t *testing.T) {
	events := make(chan string, 2)
	done := make(chan error, 1)
	go func() {
		done <- serveAgentConnection(context.Background(), func(ctx context.Context) {
			<-ctx.Done()
			events <- "withdraw"
		}, &agentMCPFixtureBackend{}, func(ctx context.Context, _ gateway.Backend) error {
			events <- "server-joined"
			return nil
		})
	}()
	if err := agentMCPResult(t, done); err != nil {
		t.Fatal(err)
	}
	if <-events != "server-joined" || <-events != "withdraw" {
		t.Fatal("withdrawal preceded server join")
	}
}

func TestAgentMCPParentCancellationEndsServerAndPresence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	started := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		done <- serveAgentConnection(ctx, func(ctx context.Context) { <-ctx.Done() }, &agentMCPFixtureBackend{}, func(ctx context.Context, _ gateway.Backend) error {
			close(started)
			<-ctx.Done()
			return nil
		})
	}()
	<-started
	cancel()
	if err := agentMCPResult(t, done); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// The supervised presence withdraws its instance exactly once, when the
// connection ends.
func TestAgentMCPEOFWithdrawsSupervisedInstanceOnce(t *testing.T) {
	fixture := &agentPresenceFixture{announce: func(int) (wire.ApplicationChange, error) {
		return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied, Instance: "original"}, nil
	}, withdrawErr: context.Canceled}
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	admitted := make(chan struct{})
	err := serveAgentConnection(context.Background(), func(ctx context.Context) {
		superviseAgentPresence(ctx, func(ctx context.Context, ready func(string), readmit bool, _ func()) error {
			return maintainAgentPresenceWithPolicy(ctx, fixture, func(context.Context, string, string) error { return nil }, "fixture-agent", "native-child", func(instance string) {
				ready(instance)
				close(admitted)
			}, make(chan time.Time), agentPresencePolicy{admission: status.admission, readmit: readmit, instance: status.setInstance})
		}, status, time.After, time.Now)
	}, &agentMCPFixtureBackend{}, func(context.Context, gateway.Backend) error { <-admitted; return nil })
	if err != nil || len(fixture.withdrawals) != 1 || fixture.withdrawals[0] != "fixture-agent:original" {
		t.Fatalf("withdrawal at end: %v / %v", err, fixture.withdrawals)
	}
}
