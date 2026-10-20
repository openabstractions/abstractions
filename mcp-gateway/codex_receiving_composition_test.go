package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// This private test-binary mode is driven by the selected vendor's isolated
// app-server fixture. No model turn is made. Actual SDK request metadata,
// native CLI selection and --version feed the complete production listener;
// only the native Messages peer and final queue effect are inert seams.
func codexReceivingCompositionFixtureMain() bool {
	if len(os.Args) != 3 || os.Args[1] != "--oa-codex-receiving-composition" {
		return false
	}
	os.Exit(runCodexReceivingCompositionFixture(os.Args[2]))
	return true
}

type codexCompositionState struct {
	mu                                 sync.Mutex
	changed                            chan struct{}
	revision                           int
	waiting                            bool
	headerCalls, bodyCalls, preflights int
	queueThreads                       []string
	queueTimes                         []int64
	queueValid                         bool
}

func TestCodexReceivingCompositionFixtureRefusesForeignHomeBeforeEffects(t *testing.T) {
	for _, foreign := range []string{"USERPROFILE", "CODEX_HOME", "CODEX_SQLITE_HOME", "HOME", "APPDATA", "LOCALAPPDATA"} {
		t.Run(foreign, func(t *testing.T) {
			private := t.TempDir()
			for _, key := range []string{"USERPROFILE", "CODEX_HOME", "HOME", "APPDATA", "LOCALAPPDATA"} {
				t.Setenv(key, private)
			}
			t.Setenv("CODEX_SQLITE_HOME", filepath.Join(private, "sqlite"))
			t.Setenv(foreign, t.TempDir())
			directory := filepath.Join(private, "state")
			if runCodexReceivingCompositionFixture(directory) != 2 {
				t.Fatal("foreign home did not refuse the private fixture")
			}
			if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("fixture touched state before home validation", err)
			}
		})
	}
}

func TestCodexReceivingCompositionFixtureRefusesMissingExplicitRoots(t *testing.T) {
	for _, missing := range []string{"CODEX_HOME", "CODEX_SQLITE_HOME"} {
		t.Run(missing, func(t *testing.T) {
			private := t.TempDir()
			for _, key := range []string{"USERPROFILE", "CODEX_HOME", "HOME", "APPDATA", "LOCALAPPDATA"} {
				t.Setenv(key, private)
			}
			t.Setenv("CODEX_SQLITE_HOME", filepath.Join(private, "sqlite"))
			t.Setenv(missing, "")
			if err := os.Unsetenv(missing); err != nil {
				t.Fatal(err)
			}
			directory := filepath.Join(private, "state")
			if runCodexReceivingCompositionFixture(directory) != 2 {
				t.Fatal("missing explicit root admitted the fixture")
			}
			if _, err := os.Stat(directory); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("fixture touched state before explicit root validation", err)
			}
		})
	}
}

type codexCompositionNative struct {
	*messagesProbe
	ctx   context.Context
	state *codexCompositionState
}

func (p *codexCompositionNative) InboxOpen(_, _, _ string) (wire.InboxChange, error) {
	p.state.mu.Lock()
	p.state.preflights++
	p.state.mu.Unlock()
	return wire.InboxChange{Outcome: wire.OutcomeOpened, Revision: "fixture"}, nil
}

func (p *codexCompositionNative) Wait(_, _ string, view wire.View, after, revision string, waitMs int64) (wire.Page, error) {
	s := p.state
	s.mu.Lock()
	if view != wire.ViewHeaders || after != "" {
		s.bodyCalls++
		s.mu.Unlock()
		return wire.Page{}, errors.New("fixture forbids body reads")
	}
	s.headerCalls++
	if revision == fmt.Sprint(s.revision) && waitMs > 0 {
		s.waiting = true
		changed := s.changed
		s.mu.Unlock()
		timer := time.NewTimer(time.Duration(waitMs) * time.Millisecond)
		select {
		case <-p.ctx.Done():
			timer.Stop()
			s.mu.Lock()
			s.waiting = false
			s.mu.Unlock()
			return wire.Page{}, p.ctx.Err()
		case <-changed:
		case <-timer.C:
		}
		timer.Stop()
		s.mu.Lock()
		s.waiting = false
	}
	page := wire.Page{Outcome: wire.OutcomePage, Revision: fmt.Sprint(s.revision)}
	if s.revision > 0 {
		page.Wake = "0001"
		page.PendingUntilUnixMs = time.Now().Add(time.Minute).UnixMilli()
	}
	s.mu.Unlock()
	return page, nil
}

func runCodexReceivingCompositionFixture(directory string) int {
	privateHome := filepath.Dir(directory)
	// Windows dirs::home_dir uses KnownFolder, so USERPROFILE cannot confine
	// a vendor CLI whose CODEX_HOME was filtered from the MCP environment.
	for key, expected := range map[string]string{"CODEX_HOME": privateHome, "CODEX_SQLITE_HOME": filepath.Join(privateHome, "sqlite")} {
		value, present := os.LookupEnv(key)
		if !present || value == "" || !strings.EqualFold(filepath.Clean(value), filepath.Clean(expected)) {
			return 2
		}
	}
	if !strings.EqualFold(filepath.Clean(os.Getenv("USERPROFILE")), filepath.Clean(privateHome)) {
		return 2
	}
	for _, key := range []string{"CODEX_HOME", "HOME", "APPDATA", "LOCALAPPDATA"} {
		if value, present := os.LookupEnv(key); present && !strings.EqualFold(filepath.Clean(value), filepath.Clean(privateHome)) {
			return 2
		}
	}
	if !filepath.IsAbs(directory) || os.MkdirAll(directory, 0700) != nil {
		return 2
	}
	self, err := os.Executable()
	if err != nil {
		return 2
	}
	diagnostic := newAgentDiagnostic(directory, self)
	if diagnostic == nil {
		return 2
	}
	defer diagnostic.close()
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), agentDiagnosticKey{}, diagnostic))
	defer cancel()
	const ownContext = "native-11111111111111111111111111111111"
	state := &codexCompositionState{changed: make(chan struct{}), queueValid: true}
	admission := &agentToolAdmission{}
	admission.confirm(time.Now().Add(time.Minute))
	backend := &messagesBackend{application: "oa-codex-integration", instance: "inert-native-fixture", contextName: ownContext, status: &messagesInboxStatus{admission: admission}}
	backend.native = func(call context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: &codexCompositionNative{messagesProbe: &messagesProbe{}, ctx: call, state: state}}), nil
	}
	configureInertChatPresence(backend, newChaosRuntime())
	server := mcp.NewServer(&mcp.Implementation{Name: "oa-inert-receiving-composition", Version: "1"}, &mcp.ServerOptions{SupportedProtocolVersions: []string{"2026-07-28", "2025-11-25", "2025-06-18"}})
	tool := func(name string, handler func()) {
		server.AddTool(&mcp.Tool{Name: name, Description: "Control this disposable, no-effect receiving fixture.", InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}, func(context.Context, *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			if handler != nil {
				handler()
			}
			state.mu.Lock()
			value := map[string]any{"waiting": state.waiting, "header_calls": state.headerCalls, "body_calls": state.bodyCalls, "preflights": state.preflights, "queue_threads": append([]string{}, state.queueThreads...), "queue_times": append([]int64{}, state.queueTimes...), "queue_valid": state.queueValid}
			state.mu.Unlock()
			diagnostic.mu.Lock()
			if diagnostic.record.Receiving != nil {
				copy := *diagnostic.record.Receiving
				value["receiving"] = copy
			}
			diagnostic.mu.Unlock()
			raw, _ := json.Marshal(value)
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}, nil
		})
	}
	tool("fixture_state", nil)
	tool("fixture_stop", cancel)
	tool("fixture_offer", func() {
		state.mu.Lock()
		state.revision++
		close(state.changed)
		state.changed = make(chan struct{})
		state.mu.Unlock()
	})
	driver := func(call context.Context, program, cwd string, args []string) (codexCLIResult, error) {
		if len(args) == 1 && args[0] == "--version" {
			return runCodexSetupCLI(call, program, cwd, args)
		}
		// No vendor queue command is executed by this fixture.
		state.mu.Lock()
		defer state.mu.Unlock()
		valid := len(args) == 6 && args[0] == "queue" && args[1] == "--thread" && codexQueueThread.MatchString(args[2]) && args[3] == "--message" && args[4] == messagesHookPointer && args[5] == "--resume" && cwd == directory && call.Err() == nil
		if deadline, ok := call.Deadline(); !ok || time.Until(deadline) > 10*time.Second {
			valid = false
		}
		state.queueValid = state.queueValid && valid
		if !valid {
			return codexCLIResult{}, errors.New("invalid inert queue dispatch")
		}
		state.queueThreads = append(state.queueThreads, args[2])
		state.queueTimes = append(state.queueTimes, time.Now().UnixMilli())
		return codexCLIResult{}, nil
	}
	err = runNativeMCPServerWithCodexDriver(ctx, server, backend, directory, driver)
	admission.retire()
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		return 2
	}
	// Reaching this marker proves the production owner joined the Watch loop.
	if os.WriteFile(filepath.Join(directory, "joined"), []byte("joined"), 0600) != nil {
		return 2
	}
	return 0
}
