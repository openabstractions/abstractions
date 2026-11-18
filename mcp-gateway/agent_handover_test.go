package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// chatSendRuntime is the chaos runtime with typed chat sending and native
// Prepare, so a helper bound to its chat can Send in both forms. forbid
// refuses it as a denied image is; uncertain answers that many Sends unknown.
type chatSendRuntime struct {
	*chaosRuntime
	mu        sync.Mutex
	sent      []msg.ChatSendRequest
	uncertain int
}

func (r *chatSendRuntime) SendChat(_, _ string, request msg.ChatSendRequest) (msg.SendResult, error) {
	r.actions.Add(1)
	if err := r.fault(); err != nil {
		return msg.SendResult{}, err
	}
	r.chaosRuntime.mu.Lock()
	forbid := r.forbid
	r.chaosRuntime.mu.Unlock()
	if forbid {
		return msg.SendResult{Outcome: msg.OutcomeForbidden}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent = append(r.sent, request)
	if r.uncertain > 0 {
		r.uncertain--
		return msg.SendResult{Outcome: msg.OutcomeUnknown}, nil
	}
	return msg.SendResult{Outcome: msg.OutcomeSent, MessageID: "handover-message"}, nil
}

func (r *chatSendRuntime) Prepare(_, _ string, request msg.PrepareRequest) (msg.PrepareResult, error) {
	r.actions.Add(1)
	r.mu.Lock()
	n := len(r.sent)
	r.mu.Unlock()
	return msg.PrepareResult{Outcome: msg.OutcomePage, SendRequest: &msg.SendRequest{To: "app:receiver", Context: "target", Kind: msg.KindNote,
		Subject: request.Subject, Body: request.Body, IdempotencyKey: fmt.Sprintf("prepared-%d", n), ExpiresUnixMs: time.Now().Add(time.Minute).UnixMilli()}}, nil
}

func (r *chatSendRuntime) sends() []msg.ChatSendRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]msg.ChatSendRequest(nil), r.sent...)
}

// serveFixtureComposition runs the production helper composition for
// application on transport against runtime until ctx ends or the transport
// closes, with its lifecycle record in dir.
func serveFixtureComposition(ctx context.Context, runtime *chatSendRuntime, application string, index int, dir string, transport mcp.Transport) error {
	program, err := os.Executable()
	if err != nil {
		return err
	}
	diagnostic := newAgentDiagnostic(ctx, dir, program)
	if diagnostic == nil {
		return fmt.Errorf("lifecycle record unavailable")
	}
	defer diagnostic.close()
	ctx = context.WithValue(ctx, agentDiagnosticKey{}, diagnostic)
	contextName := fmt.Sprintf("native-%032x", index+1)
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	native := func(context.Context) (nativeMessages, error) { return runtime, nil }
	backend, err := bindMessagesBackendStatus(new(chaosBackend), application, "", contextName, native, true)
	if err != nil {
		return err
	}
	backend.(*messagesBackend).status = status
	configureInertChatPresence(backend.(*messagesBackend), runtime.chaosRuntime)
	return serveAgentConnection(ctx, func(ctx context.Context) {
		superviseAgentPresence(ctx, func(ctx context.Context, ready func(string), readmit bool, stop func()) error {
			return agentPresenceAttempt(ctx, runtime.chaosRuntime, func(context.Context) nativeMessages { return runtime }, application, contextName, ready, status, readmit, stop)
		}, status, time.After, time.Now)
	}, backend, func(ctx context.Context, b gateway.Backend) error {
		server, err := newGatewayServer(b, false)
		if err != nil {
			return err
		}
		return runNativeMCPServerTransport(ctx, server, b, dir, new(chaosQueue).driver(runtime.chaosRuntime), func(context.Context, func(context.Context, string) ([]byte, error)) (codexReceiverCLI, func() error, error) {
			return chaosCLI{}, func() error { return nil }, nil
		}, transport)
	})
}

// serveClaudeComposition serves one Claude composition with its state under
// base until ctx ends; done closes after it returned.
func serveClaudeComposition(t *testing.T, base string, ctx context.Context, runtime *chatSendRuntime, index int, transport mcp.Transport, finish func()) <-chan struct{} {
	t.Helper()
	dir := filepath.Join(base, fmt.Sprintf("state-%d", index))
	if err := privatedir.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = serveFixtureComposition(ctx, runtime, "oa-claude-integration", index, dir, transport)
		if finish != nil {
			finish()
		}
	}()
	return done
}

// hostRecord keeps every line the host receives, to prove replayed answers
// never reach it.
type hostRecord struct {
	mu    sync.Mutex
	lines bytes.Buffer
	out   io.Writer
}

func (h *hostRecord) Write(p []byte) (int, error) {
	h.mu.Lock()
	h.lines.Write(p)
	h.mu.Unlock()
	return h.out.Write(p)
}

func (h *hostRecord) text() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lines.String()
}

type handoverHost struct {
	t        *testing.T
	session  *mcp.ClientSession
	stdio    *agentStdio
	record   *hostRecord
	moved    atomic.Bool
	mu       sync.Mutex
	launches int
	stopped  int
	served   []<-chan struct{}
}

func (h *handoverHost) launched() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.launches
}

// startHandoverHost connects one host session to a Claude helper on old,
// whose source context is numbered index. A launched successor is a fresh
// composition on fresh.
func startHandoverHost(t *testing.T, old, fresh *chatSendRuntime, index int, protocol string) *handoverHost {
	t.Helper()
	// Every composition's state is under base, which is removed last.
	base := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	fromHost, toHelper := io.Pipe()
	fromHelper, toHost := io.Pipe()
	h := &handoverHost{t: t, record: &hostRecord{out: toHost}}
	launch := func() (*agentSuccessor, error) {
		h.mu.Lock()
		h.launches++
		n := index + 100*h.launches
		h.mu.Unlock()
		successorIn, toSuccessor := io.Pipe()
		fromSuccessor, successorOut := io.Pipe()
		run, stop := context.WithCancel(ctx)
		done := serveClaudeComposition(t, base, run, fresh, n, &mcp.IOTransport{Reader: guardStdioLines(successorIn, nil), Writer: successorOut, MaxLineLength: agentStdioLineLimit + 1}, func() { successorOut.Close() })
		h.mu.Lock()
		h.served = append(h.served, done)
		h.mu.Unlock()
		return &agentSuccessor{in: toSuccessor, out: fromSuccessor, stop: func() {
			h.mu.Lock()
			h.stopped++
			h.mu.Unlock()
			toSuccessor.Close()
			stop()
		}}, nil
	}
	h.stdio = newAgentStdio(fromHost, h.record, nil, launch)
	h.served = append(h.served, serveClaudeComposition(t, base, ctx, old, index, h.stdio.transport(), nil))
	options := new(mcp.ClientSessionOptions)
	options.ProtocolVersion = protocol
	session, err := mcp.NewClient(&mcp.Implementation{Name: "claude-code", Version: "1"}, nil).Connect(ctx, &mcp.IOTransport{Reader: fromHelper, Writer: toHelper}, options)
	if err != nil {
		t.Fatal("initialize", err)
	}
	// The host closes stdio, then every composition ends before base goes.
	t.Cleanup(func() {
		_ = session.Close()
		cancel()
		h.mu.Lock()
		served := append([]<-chan struct{}(nil), h.served...)
		h.mu.Unlock()
		for _, done := range served {
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				t.Error("a composition did not end")
			}
		}
	})
	h.session = session
	return h
}

// adopt makes h this process's helper stdio, whose image moves when h.moved
// is set, as main's composition does.
func (h *handoverHost) adopt() {
	h.stdio.imageMoved = h.moved.Load
	setProcessAgentStdio(h.stdio)
	h.t.Cleanup(func() { setProcessAgentStdio(nil) })
}

func (h *handoverHost) call(name string, arguments map[string]any) (*mcp.CallToolResult, string) {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	out, err := h.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments})
	if err != nil {
		h.t.Fatalf("tool %s: the host connection failed: %v", name, err)
	}
	text := ""
	for _, content := range out.Content {
		text += content.(*mcp.TextContent).Text + "\n"
	}
	return out, text
}

func (h *handoverHost) send(key string) (*mcp.CallToolResult, string) {
	return h.call(gateway.ToolMessagesSend, map[string]any{
		"to": "app:receiver", "context": "target", "kind": "note", "subject": "handover", "body": "inert fixture",
		"idempotency_key": key, "expires_unix_ms": time.Now().Add(time.Minute).UnixMilli(),
	})
}

// sendWithin retries a Send until it is sent, as a model retries.
func (h *handoverHost) sendWithin(key string, within time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(within)
	for {
		out, text := h.send(key)
		var result struct{ Outcome string }
		if !out.IsError && json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &result) == nil && result.Outcome == "sent" {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatalf("no Send within %v; last: %s", within, text)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

// refusedWithin waits until a Send answers refused.
func (h *handoverHost) refusedWithin(key string, within time.Duration) {
	h.t.Helper()
	deadline := time.Now().Add(within)
	for {
		out, text := h.send(key)
		if out.IsError && strings.Contains(text, agentToolRefused) {
			return
		}
		if time.Now().After(deadline) {
			h.t.Fatal("the helper was not refused", text)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (h *handoverHost) bindChat() map[string]string {
	h.t.Helper()
	out, text := h.call(claudeSessionStartTool, map[string]any{"chat_title": "handover"})
	var binding map[string]string
	if out.IsError || json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &binding) != nil || binding["outcome"] != "observed" {
		h.t.Fatal("host session binding failed", text)
	}
	return binding
}

// moveImage is Setup publishing over this helper: its image moves aside and
// the runtime denies the process from then on.
func (h *handoverHost) moveImage(old *chatSendRuntime) {
	h.moved.Store(true)
	old.set(func(r *chaosRuntime) { r.forbid = true })
	old.restart(false)
}

func newChatSendRuntime() *chatSendRuntime { return &chatSendRuntime{chaosRuntime: newChaosRuntime()} }

func allocationsOf(r *chatSendRuntime) []chaosAllocationCall {
	r.chaosRuntime.mu.Lock()
	defer r.chaosRuntime.mu.Unlock()
	return append([]chaosAllocationCall(nil), r.allocations...)
}

// Owner R20, 2026-11-09: a connector update published over the running Claude
// helper; Windows then named it by its moved image, the runtime denied it, and
// the helper's canonical receiver ended (last_loss inbox_ended). Messages
// stayed refused for hours until Claude Code restarted. The helper now hands
// the same host connection to the published connector: a later Send succeeds
// on the same MCP session, with the chat still bound. A host on a protocol
// with initialize has it replayed; a later protocol carries no session.
func TestAgentHandoverMovedHelperKeepsHostSessionAndChat(t *testing.T) {
	for _, protocol := range []string{"2025-06-18", ""} {
		t.Run("protocol="+protocol, func(t *testing.T) {
			shortenAgentTiming(t)
			t.Setenv("CLAUDE_CODE_SESSION_ID", "local_handover_session")
			old, fresh := newChatSendRuntime(), newChatSendRuntime()
			h := startHandoverHost(t, old, fresh, 1, protocol)
			h.adopt()
			h.stdio.recMu.Lock()
			initialized := h.stdio.initialize != nil
			h.stdio.recMu.Unlock()
			if initialized != (protocol != "") {
				t.Fatal("initialize recording does not follow the host protocol", protocol, initialized)
			}
			before := h.bindChat()
			h.sendWithin("before-update", 5*time.Second)
			if len(old.sends()) != 1 || old.sends()[0].ReturnContext != before["context"] {
				t.Fatal("bound helper did not send from its chat", old.sends(), before)
			}
			h.moveImage(old)
			h.refusedWithin("while-moved", 5*time.Second)
			// What the moved-image check and middleware start.
			if !requestAgentHandover(h.stdio) {
				t.Fatal("a moved helper could not hand over")
			}
			h.sendWithin("after-update", 5*time.Second)
			sent := fresh.sends()
			if len(sent) != 1 || sent[0].ReturnContext != before["context"] || sent[0].Request.IdempotencyKey != "after-update" {
				t.Fatal("the successor did not send from the same chat", sent, before)
			}
			if len(old.sends()) != 1 || h.launched() != 1 || !h.stdio.handedOver() {
				t.Fatal("handover reran or the old helper sent", old.sends(), h.launched())
			}
			if allocations := allocationsOf(fresh); len(allocations) == 0 || allocations[0].binding.ChatID != "local_handover_session" {
				t.Fatal("the successor did not bind the host's chat", allocations)
			}
			if strings.Contains(h.record.text(), agentHandoverID) {
				t.Fatal("a replayed answer reached the host")
			}
			// The host lists the successor's tools and schemas again.
			if !strings.Contains(h.record.text(), `"method":"notifications/tools/list_changed"`) {
				t.Fatal("the handover did not tell the host its tools changed")
			}
			// The host's own later binding and reads go to the successor.
			if after := h.bindChat(); after["context"] != before["context"] {
				t.Fatal("the successor changed the chat", before, after)
			}
		})
	}
}

// B2: a newer helper process claimed the chat, so the runtime ended this
// helper's claim. Its image moving later hands nothing over: the newer
// instance keeps the chat.
func TestAgentHandoverAfterNewerChatOwnerIsFenced(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "local_newer_owner")
	shared, fresh := newChatSendRuntime(), newChatSendRuntime()
	older := startHandoverHost(t, shared, fresh, 1, "")
	older.adopt()
	before := older.bindChat()
	older.sendWithin("older", 5*time.Second)
	newer := startHandoverHost(t, shared, newChatSendRuntime(), 2, "")
	if after := newer.bindChat(); after["context"] != before["context"] {
		t.Fatal("the newer helper bound another chat", before, after)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		older.stdio.recMu.Lock()
		fenced := older.stdio.fenced
		older.stdio.recMu.Unlock()
		if fenced {
			break
		}
		older.wait()
		if time.Now().After(deadline) {
			t.Fatal("the replaced helper's ended claim did not fence handover")
		}
		time.Sleep(25 * time.Millisecond)
	}
	older.moveImage(newChatSendRuntime())
	if requestAgentHandover(older.stdio) || handOverNow(older.stdio) != "" || older.launched() != 0 {
		t.Fatal("a replaced helper handed its connection over", older.launched())
	}
	newer.sendWithin("newer", 5*time.Second)
	if sent := shared.sends(); sent[len(sent)-1].ReturnContext != before["context"] {
		t.Fatal("the newer instance lost the chat", sent)
	}
}

// outcomeOf reads a tool result's first text as an outcome record.
func outcomeOf(out *mcp.CallToolResult) string {
	var result struct{ Outcome string }
	if len(out.Content) == 0 || json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &result) != nil {
		return ""
	}
	return result.Outcome
}

func (h *handoverHost) wait() {
	h.call(gateway.ToolMessagesWait, map[string]any{"wait_ms": 0})
}

// B2: the host retired its session binding; a later start is refused by the
// retired helper and nothing is replayed to a successor.
func TestAgentHandoverRetiredBindingIsNotReplayed(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "local_retired")
	old, fresh := newChatSendRuntime(), newChatSendRuntime()
	h := startHandoverHost(t, old, fresh, 1, "")
	h.adopt()
	binding := h.bindChat()
	if out, text := h.call(claudeSessionEndTool, map[string]any{"child_instance": binding["child_instance"], "session_id": binding["session_id"], "retire": true}); out.IsError {
		t.Fatal("retire failed", text)
	}
	if out, _ := h.call(claudeSessionStartTool, map[string]any{"chat_title": "again"}); !out.IsError {
		t.Fatal("a retired helper bound again")
	}
	h.moveImage(old)
	if handOverNow(h.stdio) != agentHandoverSentence || h.launched() != 1 {
		t.Fatal("the moved helper did not hand over", h.launched())
	}
	time.Sleep(300 * time.Millisecond)
	if allocations := allocationsOf(fresh); len(allocations) != 0 {
		t.Fatal("a retired binding was replayed", allocations)
	}
}

// B2: a final refusal while the image is current is the runtime's word on
// this program (the person removed this app's messaging). A later move
// hands nothing over, and Send keeps answering refused.
func TestAgentHandoverFinalRefusalOfCurrentImageFences(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "local_final_session")
	old, fresh := newChatSendRuntime(), newChatSendRuntime()
	h := startHandoverHost(t, old, fresh, 1, "")
	h.adopt()
	h.bindChat()
	h.sendWithin("before-revoke", 5*time.Second)
	old.set(func(r *chaosRuntime) { r.forbid = true })
	old.restart(false)
	h.refusedWithin("after-revoke", 5*time.Second)
	h.moved.Store(true)
	watch, stop := context.WithCancel(context.Background())
	defer stop()
	go watchAgentHandover(watch, h.stdio, h.moved.Load, 10*time.Millisecond)
	h.refusedWithin("after-move", 3*time.Second)
	time.Sleep(200 * time.Millisecond)
	if h.launched() != 0 || h.stdio.handedOver() || len(fresh.sends()) != 0 {
		t.Fatal("a final refusal was handed over", h.launched())
	}
}

// B4: a one-call Send the old helper left uncertain is answered by the relay
// after the handover: the successor cannot reconcile it, so the identical
// retry sends nothing again.
func TestAgentHandoverUncertainSendIsNotSentTwice(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "local_uncertain")
	old, fresh := newChatSendRuntime(), newChatSendRuntime()
	h := startHandoverHost(t, old, fresh, 1, "")
	h.adopt()
	h.bindChat()
	h.sendWithin("warm", 5*time.Second)
	old.mu.Lock()
	old.uncertain = 1
	old.mu.Unlock()
	oneCall := map[string]any{"receiver": "Codex", "body": "only once", "subject": "once"}
	if out, text := h.call(gateway.ToolMessagesSend, oneCall); outcomeOf(out) != "unknown" {
		t.Fatal("the send was not uncertain", text)
	}
	h.moveImage(old)
	if handOverNow(h.stdio) != agentHandoverSentence {
		t.Fatal("no handover")
	}
	before := len(fresh.sends())
	out, text := h.call(gateway.ToolMessagesSend, oneCall)
	if out.IsError || outcomeOf(out) != "unknown" || !strings.Contains(text, agentHandoverSendSentence) {
		t.Fatal("the retry was not answered as unknown", text)
	}
	if len(fresh.sends()) != before || len(old.sends()) != 2 {
		t.Fatal("the uncertain send was sent again", fresh.sends(), old.sends())
	}
	// Another Send goes to the successor.
	if out, text := h.call(gateway.ToolMessagesSend, map[string]any{"receiver": "Codex", "body": "a new one"}); outcomeOf(out) != "sent" || len(fresh.sends()) != before+1 {
		t.Fatal("a new send did not reach the successor", text)
	}
}

// A successor that cannot bind the chat is stopped; the old helper keeps the
// host until a later attempt.
func TestAgentHandoverFailedBindingReplayStopsSuccessor(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "local_failed_replay")
	old, fresh := newChatSendRuntime(), newChatSendRuntime()
	fresh.set(func(r *chaosRuntime) { r.forbid = true })
	h := startHandoverHost(t, old, fresh, 1, "")
	h.stdio.budget = time.Second
	h.adopt()
	h.bindChat()
	h.moveImage(old)
	if err := h.stdio.handOver(); err == nil || h.stdio.handedOver() || h.launched() != 1 {
		t.Fatal("a successor that could not bind took the connection", err)
	}
	h.mu.Lock()
	stopped := h.stopped
	h.mu.Unlock()
	if stopped != 1 || h.stdio.canHandOver() {
		t.Fatal("the failed successor was not stopped or is retried at once", stopped)
	}
	if out, text := h.send("still-old"); !out.IsError || strings.Contains(text, agentHandoverSentence) {
		t.Fatal("the old helper did not keep the host", text)
	}
}

// A runtime restart forgets every lease: the same Claude helper re-admits its
// source and its chat with backoff, and a later Send succeeds in the same
// process and MCP session.
func TestAgentClaudeChatReadmitsAfterRuntimeRestart(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", "local_restart_session")
	runtime := newChatSendRuntime()
	h := startHandoverHost(t, runtime, newChatSendRuntime(), 1, "")
	before := h.bindChat()
	h.sendWithin("before-restart", 5*time.Second)
	for round := 1; round <= 5; round++ {
		runtime.restart(true)
		if out, _ := h.send(fmt.Sprintf("down-%d", round)); !out.IsError {
			t.Fatal("a Send succeeded while the runtime was down")
		}
		runtime.set(func(r *chaosRuntime) { r.down = false })
		h.sendWithin(fmt.Sprintf("after-restart-%d", round), 5*time.Second)
	}
	sent := runtime.sends()
	if last := sent[len(sent)-1]; last.ReturnContext != before["context"] || h.launched() != 0 {
		t.Fatal("restart changed the chat or replaced the helper", last, h.launched())
	}
}

func rpcLine(id any, method string, params any) agentRPC {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	var message agentRPC
	_ = json.Unmarshal(raw, &message)
	return message
}

func resultLine(id any, result *mcp.CallToolResult) []byte {
	raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": id, "result": result})
	return append(raw, '\n')
}

// The binding a successor receives is the latest start the local helper
// answered observed; an ended association is dropped and a retired one is
// never replayed again. Send answers keep or settle uncertainty.
func TestAgentResultOutcomePreservesMCPDecoding(t *testing.T) {
	for _, test := range []struct {
		name, result string
		outcome      string
		failed       bool
	}{
		{"structured takes precedence", `{"structuredContent":{"outcome":"refused"},"content":[{"text":"{\"outcome\":\"observed\"}"}]}`, "refused", false},
		{"structured error", `{"isError":true,"structuredContent":{"outcome":"observed"}}`, "observed", true},
		{"empty structured falls back", `{"structuredContent":{"outcome":""},"content":[{"text":"{\"outcome\":\"observed\"}"}]}`, "observed", false},
		{"malformed first text", `{"isError":true,"content":[{"text":"{"},{"text":"{\"outcome\":\"observed\"}"}]}`, "", true},
		{"malformed structured rejects envelope", `{"isError":true,"structuredContent":{"outcome":5},"content":[{"text":"{\"outcome\":\"observed\"}"}]}`, "", false},
		{"malformed content rejects envelope", `{"isError":true,"structuredContent":{"outcome":"observed"},"content":[{"text":5}]}`, "", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			outcome, failed := agentResultOutcome(json.RawMessage(test.result))
			if outcome != test.outcome || failed != test.failed {
				t.Fatalf("outcome %q, failed %t; want %q, %t", outcome, failed, test.outcome, test.failed)
			}
		})
	}
}

func TestAgentStdioRecordsObservedBindingAndUncertainSends(t *testing.T) {
	s := &agentStdio{starts: map[string]json.RawMessage{}, sends: map[string]string{}, uncertain: map[string]bool{}}
	s.recordHost(rpcLine(1, "initialize", map[string]any{"protocolVersion": "2025-06-18"}))
	s.recordHost(rpcLine(2, "tools/call", map[string]any{"name": claudeSessionStartTool, "arguments": map[string]any{"chat_title": "a"}}))
	if s.binding != nil || !strings.Contains(string(s.initialize), "2025-06-18") {
		t.Fatal("a start was kept before its answer", string(s.binding))
	}
	s.observeLocal(resultLine(2, &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"unavailable"}`}}}))
	if s.binding != nil {
		t.Fatal("an unavailable start became the binding")
	}
	s.recordHost(rpcLine("b", "tools/call", map[string]any{"name": claudeSessionStartTool, "arguments": map[string]any{"chat_title": "b"}}))
	s.observeLocal(resultLine("b", &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"observed"}`}}}))
	if !strings.Contains(string(s.binding), `"chat_title":"b"`) {
		t.Fatal("an observed start was not kept", string(s.binding))
	}
	s.recordHost(rpcLine(3, "tools/call", map[string]any{"name": claudeSessionEndTool, "arguments": map[string]any{}}))
	if s.binding != nil || s.noReplay {
		t.Fatal("an ended association would be replayed")
	}
	s.recordHost(rpcLine(4, "tools/call", map[string]any{"name": claudeSessionEndTool, "arguments": map[string]any{"retire": true}}))
	s.recordHost(rpcLine(5, "tools/call", map[string]any{"name": claudeSessionStartTool, "arguments": map[string]any{}}))
	s.observeLocal(resultLine(5, &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"observed"}`}}}))
	if s.binding != nil || !s.noReplay {
		t.Fatal("a retired association was kept for replay")
	}
	send := map[string]any{"name": gateway.ToolMessagesSend, "arguments": map[string]any{"receiver": "codex", "body": "x"}}
	s.recordHost(rpcLine(6, "tools/call", send))
	s.observeLocal(resultLine(6, &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"unknown"}`}, &mcp.TextContent{Text: gateway.SendUncertainLead + "; call again."}}}))
	params, _ := json.Marshal(map[string]any{"name": gateway.ToolMessagesSend, "arguments": map[string]any{"receiver": "Codex", "body": "x"}})
	if !s.uncertainSend(params) {
		t.Fatal("an uncertain one-call send was not kept under its receiver name")
	}
	s.recordHost(rpcLine(7, "tools/call", send))
	s.observeLocal(resultLine(7, &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"unavailable","next":"Nothing was sent."}`}}}))
	if s.uncertainSend(params) {
		t.Fatal("a Prepare refusal kept the send uncertain")
	}
}

// A Send still unanswered when the switch begins is uncertain after it; a
// late local answer still settles it.
func TestAgentStdioPendingSendIsUncertainAtSwitch(t *testing.T) {
	s := &agentStdio{starts: map[string]json.RawMessage{}, sends: map[string]string{}, uncertain: map[string]bool{}}
	send := map[string]any{"name": gateway.ToolMessagesSend, "arguments": map[string]any{"receiver": "codex", "body": "pending"}}
	s.recordHost(rpcLine(1, "tools/call", send))
	params, _ := json.Marshal(send)
	if s.uncertainSend(params) {
		t.Fatal("a pending send was uncertain before the switch")
	}
	s.recMu.Lock()
	s.keepPendingSendsUncertainLocked()
	s.recMu.Unlock()
	if !s.uncertainSend(params) {
		t.Fatal("a send pending at the switch was not kept uncertain")
	}
	s.observeLocal(resultLine(1, &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"sent"}`}}}))
	if s.uncertainSend(params) {
		t.Fatal("a late sent answer did not settle the send")
	}
}

// A host call during a switch answers at once; a line longer than the limit
// from the successor is dropped.
func TestAgentStdioSwitchingAnswersAndLineLimit(t *testing.T) {
	var out bytes.Buffer
	_, local := io.Pipe()
	s := &agentStdio{hostOut: &out, local: local, starts: map[string]json.RawMessage{}, sends: map[string]string{}, uncertain: map[string]bool{}}
	s.switching.Store(true)
	done := make(chan struct{})
	go func() {
		defer close(done)
		hostLineWriter{s}.Write([]byte(`{"jsonrpc":"2.0","id":9,"method":"tools/call","params":{"name":"oa_messages_send","arguments":{}}}` + "\n"))
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("a call during the switch waited")
	}
	if !strings.Contains(out.String(), `"id":9`) || !strings.Contains(out.String(), agentHandoverSentence) {
		t.Fatal(out.String())
	}
	long := append(bytes.Repeat([]byte("x"), agentStdioLineLimit+10), '\n')
	reader := bufio.NewReaderSize(io.MultiReader(bytes.NewReader(long), strings.NewReader("{\"ok\":1}\n")), 64<<10)
	if line, err := readAgentLine(reader); line != nil || err != nil {
		t.Fatal("an overlong line passed", len(line), err)
	}
	if line, err := readAgentLine(reader); string(line) != "{\"ok\":1}\n" || err != nil {
		t.Fatal(string(line), err)
	}
}

// handoverFixtureMain runs this test binary as a handover fixture:
//
//	OA_HANDOVER_ECHO_FIXTURE=1      copies stdin to stdout, a started successor
//	OA_HANDOVER_OPENCODE_FIXTURE=1  the OpenCode helper composition on stdio
//	                                with main's handover wiring, against an
//	                                in-process chaos runtime; its lifecycle
//	                                record in OA_HANDOVER_STATE
func handoverFixtureMain() bool {
	if os.Getenv("OA_HANDOVER_ECHO_FIXTURE") == "1" {
		_, _ = io.Copy(os.Stdout, os.Stdin)
		os.Exit(0)
	}
	if os.Getenv("OA_HANDOVER_OPENCODE_FIXTURE") != "1" {
		return false
	}
	// As main: the arguments and environment before the OpenCode owner
	// removes its adapter key.
	args, environment := append([]string(nil), os.Args[1:]...), os.Environ()
	program, err := os.Executable()
	if err != nil {
		os.Exit(2)
	}
	ctx := context.Background()
	stdio := startAgentStdio(ctx, program, args, environment, testEnrollmentOverride())
	if err := serveFixtureComposition(ctx, newChatSendRuntime(), "oa-opencode-integration", os.Getpid(), os.Getenv("OA_HANDOVER_STATE"), stdio.transport()); err != nil {
		os.Exit(1)
	}
	os.Exit(0)
	return true
}

// enrolledAt is a runtime that reads the connector enrolled at program.
func enrolledAt(program string) agentEnrollmentRead {
	return enrollmentAnswer(applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &applications.AgentEnrollment{
		Application: "oa-claude-integration", Program: program, Revision: "kept", State: applications.AgentEnrollmentStateEnrolled}}, nil)
}

func enrollmentAnswer(result applications.AgentEnrollmentResult, err error) agentEnrollmentRead {
	return func(context.Context, string) (applications.AgentEnrollmentResult, error) { return result, err }
}

// testEnrollmentOverride is the fixture's runtime answer: OA_HANDOVER_TEST_ENROLLED
// names the path the runtime keeps the connector at, "published" for the
// path this process started from; unset leaves the real read.
func testEnrollmentOverride() agentEnrollmentRead {
	kept := os.Getenv("OA_HANDOVER_TEST_ENROLLED")
	if kept == "" {
		return nil
	}
	if kept == "published" {
		program, err := os.Executable()
		if err != nil {
			return nil
		}
		kept = program
	}
	return enrolledAt(kept)
}
