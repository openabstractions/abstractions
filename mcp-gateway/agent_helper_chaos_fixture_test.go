package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// The chaos helper fixture is the real helper composition on the process's
// own stdio, with the in-process fault runtime of the chaos tests in place of
// any OA runtime. A host, real Codex included, starts it as an MCP server.
//
//	OA_AGENT_HELPER_CHAOS_FIXTURE  absolute directory for its trace and state
//	OA_CHAOS_SCHEDULE              faults applied after successive successful
//	                               tool calls: restart, lapse, drop, forget,
//	                               decode, queuefail (comma separated)
//
// A file named control in the directory also applies a fault when its
// content changes. trace.jsonl records each message's structure and timing
// only: method, parameter and _meta key names, whether a thread is present,
// the tool name from this server's own catalogue and each answer's outcome
// word. No values are recorded.
func agentHelperChaosFixtureMain() bool {
	dir := os.Getenv("OA_AGENT_HELPER_CHAOS_FIXTURE")
	if dir == "" {
		return false
	}
	os.Exit(runAgentHelperChaosFixture(dir, os.Getenv("OA_CHAOS_SCHEDULE")))
	return true
}

type chaosTrace struct {
	mu    sync.Mutex
	start time.Time
	file  *os.File
}

func (c *chaosTrace) write(event map[string]any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	event["t_ms"] = time.Since(c.start).Milliseconds()
	event["pid"] = os.Getpid()
	line, _ := json.Marshal(event)
	_, _ = c.file.Write(append(line, '\n'))
}

func sortedKeys(value any) []string {
	m, ok := value.(map[string]any)
	if !ok {
		return nil
	}
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// structure reduces one request to key names, never values.
func chaosStructure(method string, request mcp.Request) map[string]any {
	event := map[string]any{"event": "request", "method": method}
	params := request.GetParams()
	if params == nil {
		return event
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return event
	}
	var decoded map[string]any
	if json.Unmarshal(raw, &decoded) != nil {
		return event
	}
	event["params"] = sortedKeys(decoded)
	if meta, ok := decoded["_meta"].(map[string]any); ok {
		event["meta"] = sortedKeys(meta)
		_, event["thread"] = meta["threadId"]
	}
	if method == "initialize" {
		event["capabilities"] = sortedKeys(decoded["capabilities"])
		event["client_info"] = sortedKeys(decoded["clientInfo"])
	}
	if method == "tools/call" {
		if name, ok := decoded["name"].(string); ok && strings.HasPrefix(name, "oa_") {
			event["tool"] = name
		}
		event["arguments"] = sortedKeys(decoded["arguments"])
	}
	return event
}

func runAgentHelperChaosFixture(dir, schedule string) int {
	if !filepath.IsAbs(dir) {
		return 2
	}
	state := filepath.Join(dir, fmt.Sprintf("state-%d", os.Getpid()))
	if os.Getenv("OA_CHAOS_SHARED_STATE") == "1" {
		// Every helper of one host shares one lifecycle file, as installed.
		state = filepath.Join(dir, "state")
	}
	if err := privatedir.Ensure(state); err != nil {
		return 2
	}
	file, err := os.OpenFile(filepath.Join(dir, "trace.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return 2
	}
	defer file.Close()
	trace := &chaosTrace{start: time.Now(), file: file}
	trace.write(map[string]any{"event": "start"})
	// The chaos tests' short clock, so one Codex turn sees whole recoveries.
	agentPresenceLease, agentPresenceRenewal, agentPresenceRetryDelay = 3*time.Second, time.Second, time.Second
	agentReadmitFirstDelay, agentReadmitMaxDelay, agentPresenceRefusedRetry = 100*time.Millisecond, 2*time.Second, 3*time.Second
	codexWakeCooldown, codexReceiverResumeFirst, codexReceiverResumeMax = time.Second, 100*time.Millisecond, 2*time.Second
	agentToolAdmissionWait = 5 * time.Second

	program, _ := os.Executable()
	diagnostic := newAgentDiagnostic(state, program)
	defer diagnostic.close()
	ctx := context.WithValue(context.Background(), agentDiagnosticKey{}, diagnostic)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	faults := strings.FieldsFunc(schedule, func(r rune) bool { return r == ',' })
	var faultMu sync.Mutex
	applied := 0
	apply := func(fault string) {
		trace.write(map[string]any{"event": "fault", "fault": fault})
		switch fault {
		case "restart":
			runtime.restart(true)
			time.AfterFunc(1500*time.Millisecond, func() { runtime.set(func(r *chaosRuntime) { r.down = false }) })
		case "lapse":
			runtime.set(func(r *chaosRuntime) { r.down = true })
			time.AfterFunc(2*agentPresenceLease, func() { runtime.set(func(r *chaosRuntime) { r.down = false }) })
		case "drop":
			hang := make(chan struct{})
			runtime.set(func(r *chaosRuntime) { r.hang = hang })
			time.AfterFunc(500*time.Millisecond, func() { runtime.set(func(r *chaosRuntime) { r.hang = nil }); close(hang) })
		case "forget":
			runtime.restart(false)
		case "decode":
			runtime.set(func(r *chaosRuntime) { r.decodeFaults = 3 })
		case "queuefail":
			queue.mu.Lock()
			queue.fail = 1
			queue.mu.Unlock()
			runtime.deliver()
		}
	}
	go func() {
		last := ""
		for {
			time.Sleep(50 * time.Millisecond)
			raw, err := os.ReadFile(filepath.Join(dir, "control"))
			if err != nil {
				continue
			}
			if word := strings.TrimSpace(string(raw)); word != last {
				last = word
				if word != "" {
					apply(strings.Fields(word)[0])
				}
			}
		}
	}()
	contextName, err := newAgentContext()
	if err != nil {
		return 2
	}
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	base := new(chaosBackend)
	backend, err := bindMessagesBackendStatus(base, "oa-codex-integration", "", contextName, func(context.Context) (nativeMessages, error) { return runtime, nil }, true)
	if err != nil {
		return 2
	}
	backend.(*messagesBackend).status = status
	configureInertChatPresence(backend.(*messagesBackend), runtime)
	err = serveAgentConnection(ctx, func(ctx context.Context) {
		superviseAgentPresence(ctx, func(ctx context.Context, ready func(string), readmit bool, stop func()) error {
			return agentPresenceAttempt(ctx, runtime, func(context.Context) nativeMessages { return runtime }, "oa-codex-integration", contextName, ready, status, readmit, stop)
		}, status, time.After, time.Now)
	}, backend, func(ctx context.Context, b gateway.Backend) error {
		server, err := newGatewayServer(b, false)
		if err != nil {
			return err
		}
		server.AddReceivingMiddleware(func(next mcp.MethodHandler) mcp.MethodHandler {
			return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
				event := chaosStructure(method, request)
				result, err := next(ctx, method, request)
				if call, ok := result.(*mcp.CallToolResult); ok {
					event["is_error"] = call.IsError
					if call.IsError && len(call.Content) != 0 {
						if text, ok := call.Content[0].(*mcp.TextContent); ok {
							var answer map[string]any
							if json.Unmarshal([]byte(text.Text), &answer) == nil {
								event["outcome"], event["state"] = answer["outcome"], answer["state"]
							}
						}
					}
					if !call.IsError {
						faultMu.Lock()
						if applied < len(faults) {
							fault := faults[applied]
							applied++
							go apply(fault)
						}
						faultMu.Unlock()
					}
				}
				if err != nil {
					event["error"] = true
				}
				trace.write(event)
				return result, err
			}
		})
		return runNativeMCPServerTransport(ctx, server, b, state, queue.driver(runtime), func(context.Context, func(context.Context, string) ([]byte, error)) (codexReceiverCLI, func() error, error) {
			return chaosCLI{}, func() error { return nil }, nil
		}, guardedStdioTransport())
	})
	total, attempts := queue.snapshot()
	record := map[string]any{"event": "end", "stdio_error": err != nil, "wakes": total, "queue_attempts": attempts}
	if rows := readAgentDiagnostics(context.Background(), state); len(rows) == 1 {
		record["phase"], record["cause"], record["losses"], record["readmitted"] = rows[0].Phase, rows[0].Cause, rows[0].Losses, rows[0].Readmitted
	}
	trace.write(record)
	if err != nil {
		return 1
	}
	return 0
}
