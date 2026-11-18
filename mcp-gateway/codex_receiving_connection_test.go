package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	watchwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestCodexReceivingSupervisorConfirmsForbiddenAfterCurrentPresence(t *testing.T) {
	a := new(agentToolAdmission)
	a.confirm(time.Now().Add(time.Minute))
	b := &messagesBackend{application: "oa-codex-integration", status: &messagesInboxStatus{admission: a}}
	runs, waits, resumed := 0, 0, 0
	joined := true
	err := superviseCodexReceiver(context.Background(), b, func(ctx context.Context, observed func()) error {
		if !joined || codexReceivingPresence(ctx, b) != nil {
			t.Fatal("receiver resumed before previous run joined and presence recovered")
		}
		joined = false
		defer func() { joined = true }()
		runs++
		if runs == 1 {
			a.setReady(false)
		}
		if runs == 2 {
			// A valid page inside the restarted run clears the first refusal.
			observed()
		}
		return errCodexObservationForbidden
	}, func(event string) {
		if event != "resuming" {
			t.Fatal(event)
		}
		resumed++
	}, func(context.Context, time.Duration) error {
		if !joined {
			t.Fatal("retry started before run joined")
		}
		waits++
		if waits == 2 {
			a.confirm(time.Now().Add(time.Minute))
		}
		if waits > 3 {
			t.Fatal("confirmed forbidden was retried")
		}
		return nil
	}, time.Now)
	if !errors.Is(err, errCodexObservationForbidden) || runs != 3 || waits != 3 || resumed != 2 || !joined {
		t.Fatalf("runs=%d waits=%d resumed=%d joined=%v err=%v", runs, waits, resumed, joined, err)
	}
}

func TestCodexReceivingSupervisorStopsOnFinalAdmission(t *testing.T) {
	for _, duringRun := range []bool{false, true} {
		t.Run(fmt.Sprint(duringRun), func(t *testing.T) {
			a := new(agentToolAdmission)
			a.confirm(time.Now().Add(time.Minute))
			b := &messagesBackend{application: "oa-codex-integration", status: &messagesInboxStatus{admission: a}}
			if !duringRun {
				a.refuse(errors.New("enrollment revoked"), true)
			}
			runs := 0
			err := superviseCodexReceiver(context.Background(), b, func(context.Context, func()) error {
				runs++
				a.refuse(errors.New("enrollment revoked"), true)
				return errCodexObservationForbidden
			}, func(string) { t.Fatal("final admission resumed") }, func(context.Context, time.Duration) error {
				t.Fatal("final admission entered retry pause")
				return nil
			}, time.Now)
			want := 0
			if duringRun {
				want = 1
			}
			if err == nil || runs != want || !codexReceivingRetired(b) || codexReceivingPresence(context.Background(), b) == nil {
				t.Fatalf("final admission retained receiver: runs=%d err=%v", runs, err)
			}
		})
	}
}

func TestCodexSessionStartProductionCompositionInventory(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	in, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	defer write.Close()
	read, out, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer read.Close()
	defer out.Close()
	oldIn, oldOut := os.Stdin, os.Stdout
	os.Stdin, os.Stdout = in, out
	defer func() { os.Stdin, os.Stdout = oldIn, oldOut }()
	b := &messagesBackend{Backend: new(reconciliationBackend), application: "oa-codex-integration"}
	done := make(chan error, 1)
	go func() {
		done <- runNativeMCPServerWithCodexDriver(ctx, gateway.NewServer(b), b, t.TempDir(), func(context.Context, string, string, []string) (codexCLIResult, error) {
			t.Error("inventory selected a CLI")
			return codexCLIResult{}, errors.New("no effects")
		})
	}()
	reader := bufio.NewReader(read)
	exchange := func(request string) map[string]json.RawMessage {
		t.Helper()
		if _, err := write.WriteString(request + "\n"); err != nil {
			t.Fatal(err)
		}
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		var reply map[string]json.RawMessage
		if json.Unmarshal(line, &reply) != nil {
			t.Fatal(string(line))
		}
		return reply
	}
	exchange(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2026-07-28","capabilities":{},"clientInfo":{"name":"inert-inventory","version":"test"}}}`)
	if _, err := write.WriteString("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/initialized\"}\n"); err != nil {
		t.Fatal(err)
	}
	reply := exchange(`{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	var result struct {
		Tools []struct {
			Name        string
			InputSchema map[string]any
		}
	}
	if json.Unmarshal(reply["result"], &result) != nil {
		t.Fatal(string(reply["result"]))
	}
	found := false
	for _, tool := range result.Tools {
		if tool.Name == codexSessionStartTool {
			found = true
			if tool.InputSchema["type"] != "object" || tool.InputSchema["additionalProperties"] != false {
				t.Fatal(tool.InputSchema)
			}
			properties, ok := tool.InputSchema["properties"].(map[string]any)
			if !ok || len(properties) != 0 {
				t.Fatal(tool.InputSchema)
			}
		}
	}
	if !found {
		t.Error("production native Codex composition omitted internal hook tool")
	}
	write.Close()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("production server did not join")
	}
}

type codexSuspensionWatchPeer struct {
	*statusMessagesProbe
	ctx                         context.Context
	reads                       *atomic.Int32
	entered, cancelled, release chan struct{}
	mode, contextName           string
}

func (p *codexSuspensionWatchPeer) Wait(string, string, wire.View, string, string, int64) (wire.Page, error) {
	if p.reads.Add(1) == 1 {
		close(p.entered)
		<-p.ctx.Done()
		close(p.cancelled)
		<-p.release
		switch p.mode {
		case "trust":
			return wire.Page{}, errors.Join(p.ctx.Err(), listen.ErrServerUntrusted)
		case "forbidden":
			return wire.Page{Outcome: wire.OutcomeForbidden}, nil
		case "malformed":
			return wire.Page{Outcome: wire.OutcomePage, Revision: "same", PendingUntilUnixMs: -1}, nil
		}
		return wire.Page{}, p.ctx.Err()
	}
	return wire.Page{Outcome: wire.OutcomePage, Revision: "same", Wake: "0001", PendingUntilUnixMs: time.Now().Add(time.Minute).UnixMilli()}, nil
}

func TestCodexReceivingActualWatchCancellationRetainsInitiatingSuspension(t *testing.T) {
	for _, mode := range []string{"cancel-then-fresh", "fresh-before-result", "caller-cancel", "expiry", "revoked", "trust", "forbidden", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			a := new(agentToolAdmission)
			a.confirm(time.Now().Add(30 * time.Second))
			p := new(statusMessagesProbe)
			state := &messagesInboxStatus{admission: a}
			var reads, queues atomic.Int32
			entered, cancelled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			defer releaseOnce.Do(func() { close(release) })
			b := &messagesBackend{application: "oa-codex-integration", instance: "original", contextName: "native-11111111111111111111111111111111", status: state, native: func(call context.Context) (nativeMessages, error) {
				return &codexSuspensionWatchPeer{statusMessagesProbe: p, ctx: call, reads: &reads, entered: entered, cancelled: cancelled, release: release, mode: mode}, nil
			}}
			binding := codexReceiverTestBinding(t)
			binding.current = func(ctx context.Context) error { return codexReceivingPresence(ctx, b) }
			run := codexReceivingQueue(ctx, b, binding.cli, binding.thread, func(context.Context, string, string, []string) (codexCLIResult, error) {
				queues.Add(1)
				return codexCLIResult{}, nil
			})
			paused, resume := make(chan struct{}, 1), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- runCodexInboxReceiver(ctx, binding, b, run, time.Now, func(ctx context.Context, _ time.Duration) error {
					if queues.Load() > 0 {
						cancel()
						return ctx.Err()
					}
					paused <- struct{}{}
					select {
					case <-resume:
						return nil
					case <-ctx.Done():
						return ctx.Err()
					}
				})
			}()
			wait := func(c <-chan struct{}) {
				t.Helper()
				select {
				case <-c:
				case <-ctx.Done():
					t.Fatal(ctx.Err())
				}
			}
			wait(entered)
			if mode == "caller-cancel" {
				cancel()
			} else {
				p.mu.Lock()
				p.outcome = wire.OutcomeUnknown
				p.mu.Unlock()
				if _, _, ok := b.BeginNativeTool(ctx, gateway.ToolModels); ok {
					t.Fatal("unknown proof admitted")
				}
			}
			waitCancelled := func() {
				t.Helper()
				select {
				case <-cancelled:
				case <-time.After(time.Second):
					t.Fatal("admission did not cancel actual Watch")
				}
			}
			waitCancelled()
			fresh := func() {
				t.Helper()
				p.mu.Lock()
				p.outcome = wire.OutcomeOpened
				p.mu.Unlock()
				generation := a.version()
				out, err := state.openContext(ctx, p, "original", b.contextName, nil)
				if err != nil || out.Outcome != wire.OutcomeOpened || !a.confirmVersion(time.Now().Add(30*time.Second), generation) {
					t.Fatal(out, err)
				}
			}
			if mode == "fresh-before-result" {
				fresh()
			}
			if mode == "expiry" {
				a.confirm(time.Now().Add(-time.Second))
			}
			if mode == "revoked" {
				a.retire()
			}
			releaseOnce.Do(func() { close(release) })
			if mode != "cancel-then-fresh" && mode != "fresh-before-result" {
				if err := agentMCPResult(t, done); err == nil || reads.Load() != 1 || queues.Load() != 0 {
					t.Fatal("terminal cancellation/read failure recovered", err, reads.Load(), queues.Load())
				}
				return
			}
			select {
			case <-paused:
			case err := <-done:
				t.Fatal("owned read cancellation retired receiver", err)
			case <-ctx.Done():
				t.Fatal(ctx.Err())
			}
			if reads.Load() != 1 || queues.Load() != 0 {
				t.Fatal("suspended read performed work", reads.Load(), queues.Load())
			}
			if mode == "cancel-then-fresh" {
				fresh()
			}
			resume <- struct{}{}
			if err := agentMCPResult(t, done); !errors.Is(err, context.Canceled) || reads.Load() != 2 || queues.Load() != 1 {
				t.Fatal(err, reads.Load(), queues.Load())
			}
		})
	}
}

func TestCodexReceivingInitializeMetadataAttachesBeforeTools(t *testing.T) {
	thread := "11111111-1111-4111-8111-111111111111"
	entered := make(chan string, 1)
	r := newCodexReceivingConnection(context.Background(), func(ctx context.Context, value string) error { entered <- value; <-ctx.Done(); return ctx.Err() }, func(string) {})
	defer r.stop("test_end")
	call := r.middleware(func(context.Context, string, mcp.Request) (mcp.Result, error) { return &mcp.InitializeResult{}, nil })
	_, err := call(context.Background(), "initialize", &mcp.ServerRequest[*mcp.InitializeParams]{Params: &mcp.InitializeParams{Meta: mcp.Meta{"threadId": thread}}})
	if err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-entered:
		if got != thread {
			t.Fatal(got)
		}
	case <-time.After(time.Second):
		t.Fatal("initialize did not attach")
	}
}

func TestCodexReceivingMetadataIgnoresArgumentsAndRetainsForkThread(t *testing.T) {
	thread := "11111111-1111-4111-8111-111111111111"
	session := "22222222-2222-4222-8222-222222222222"
	entered := make(chan string, 1)
	var starts atomic.Int32
	r := newCodexReceivingConnection(context.Background(), func(ctx context.Context, value string) error {
		starts.Add(1)
		entered <- value
		<-ctx.Done()
		return ctx.Err()
	}, func(string) {})
	defer r.stop("test_end")
	ordinary := 0
	call := r.middleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		ordinary++
		return &mcp.CallToolResult{}, nil
	})
	invoke := func(meta mcp.Meta) {
		t.Helper()
		args, _ := json.Marshal(map[string]any{"threadId": session, "session_id": session})
		_, err := call(context.Background(), "tools/call", &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{Meta: meta, Name: "oa_messages_wait", Arguments: args}})
		if err != nil {
			t.Fatal(err)
		}
	}
	invoke(nil)
	invoke(mcp.Meta{"threadId": 12, "sessionId": session})
	invoke(mcp.Meta{"sessionId": session})
	if starts.Load() != 0 {
		t.Fatal("arguments or session tree enrolled receiving")
	}
	invoke(mcp.Meta{"threadId": thread, "sessionId": session})
	select {
	case got := <-entered:
		if got != thread {
			t.Fatal("fork thread replaced by session tree", got)
		}
	case <-time.After(time.Second):
		t.Fatal("host metadata did not bind")
	}
	invoke(mcp.Meta{"threadId": thread, "sessionId": "different-tree"})
	if ordinary != 5 || starts.Load() != 1 {
		t.Fatal("ordinary tools disrupted or receiver duplicated", ordinary, starts.Load())
	}
}

func TestCodexReceivingSuspensionResumesOriginalConnectionWithoutQueueReplay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a := new(agentToolAdmission)
	a.confirm(time.Now().Add(time.Minute))
	p := &statusMessagesProbe{outcome: wire.OutcomeUnknown}
	state := &messagesInboxStatus{admission: a}
	models := new(reconciliationBackend)
	b := &messagesBackend{Backend: models, application: "oa-codex-integration", instance: "original", contextName: "native-11111111111111111111111111111111", status: state, native: func(context.Context) (nativeMessages, error) { return p, nil }}
	if _, _, admitted := b.BeginNativeTool(ctx, gateway.ToolModels); admitted {
		t.Fatal("unknown proof admitted")
	}
	var reads, queues, starts atomic.Int32
	paused := make(chan struct{}, 1)
	resume := make(chan struct{})
	r := newCodexReceivingConnection(ctx, func(ctx context.Context, thread string) error {
		starts.Add(1)
		binding := codexReceiverTestBinding(t)
		binding.context, binding.thread = b.contextName, thread
		binding.current = func(ctx context.Context) error { return codexReceivingPresence(ctx, b) }
		watch := codexReceiverTestWatch(func(context.Context, gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
			reads.Add(1)
			return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: "same-pending", Wake: "0001", PendingUntilUnixMs: time.Now().Add(time.Minute).UnixMilli()}, WatchMetadata: gateway.WatchMetadata{Context: b.contextName}}, nil
		})
		return runCodexInboxReceiver(ctx, binding, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
			queues.Add(1)
			// The queue was accepted before a concurrent uncertainty suspended admission.
			a.setReady(false)
			return codexCLIResult{}, nil
		}, time.Now, func(ctx context.Context, _ time.Duration) error {
			paused <- struct{}{}
			select {
			case <-resume:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		})
	}, func(string) {})
	defer r.stop("test_end")
	server := gateway.NewServer(b)
	server.AddReceivingMiddleware(r.middleware)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "inert-suspension", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func() *mcp.CallToolResult {
		t.Helper()
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: gateway.ToolModels, Meta: mcp.Meta{"threadId": "11111111-1111-4111-8111-111111111111"}, Arguments: map[string]any{}})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if !call().IsError {
		t.Fatal("suspended MCP call admitted effects")
	}
	takePause := func() {
		t.Helper()
		select {
		case <-paused:
		case <-r.done:
			t.Fatal("same-lease suspension permanently retired receiver")
		case <-ctx.Done():
			t.Fatal(ctx.Err())
		}
	}
	takePause()
	if reads.Load() != 0 || queues.Load() != 0 || models.models.Load() != 0 {
		t.Fatal("suspended receiver performed work")
	}
	freshProof := func() {
		t.Helper()
		p.mu.Lock()
		p.outcome = wire.OutcomeOpened
		p.mu.Unlock()
		generation := a.version()
		out, err := state.openContext(ctx, p, "original", b.contextName, nil)
		if err != nil || out.Outcome != wire.OutcomeOpened || !a.confirmVersion(time.Now().Add(time.Minute), generation) {
			t.Fatal("fresh original proof refused", out, err)
		}
	}
	freshProof()
	resume <- struct{}{}
	takePause()
	if reads.Load() != 1 || queues.Load() != 1 {
		t.Fatal("fresh proof did not dispatch once", reads.Load(), queues.Load())
	}
	if !call().IsError {
		t.Fatal("post-acceptance suspension admitted effects")
	}
	freshProof()
	resume <- struct{}{}
	takePause()
	if reads.Load() != 2 || queues.Load() != 1 || starts.Load() != 1 {
		t.Fatal("accepted queue replayed or receiver rebound", reads.Load(), queues.Load(), starts.Load())
	}
	cancel()
}

type codexConcurrentNativeProbe struct {
	*messagesProbe
	ctx         context.Context
	entered     chan struct{}
	contextName string
}

func (p *codexConcurrentNativeProbe) Wait(_, _ string, view wire.View, _, _ string, _ int64) (wire.Page, error) {
	if view == wire.ViewHeaders {
		close(p.entered)
		<-p.ctx.Done()
		return wire.Page{}, p.ctx.Err()
	}
	return wire.Page{Outcome: wire.OutcomePage, Messages: []wire.Message{{Header: wire.Header{MessageID: "question-5", From: "app:sender", FromContext: "source", To: "app:oa-codex-integration", Context: p.contextName, Kind: wire.KindQuestion, ContextID: "thread", ExpiresUnixMs: 2000}, IdempotencyKey: "question-key", Body: "untrusted data"}}}, nil
}

func TestCodexReceivingBackgroundWatchDoesNotBlockMCPReadAndReply(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	watchCtx, stopWatch := context.WithCancel(ctx)
	defer stopWatch()
	entered := make(chan struct{})
	const contextName = "native-11111111111111111111111111111111"
	var calls sync.WaitGroup
	// Reuse the real FrameClient and generated Messages dispatcher. The custom
	// stream is a portable inert transport fixture, not native peer admission.
	transport := listen.FrameClient{Endpoint: "inert-transport", Timeout: 4 * time.Second, Dialer: func(call context.Context, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		calls.Add(1)
		go func() {
			defer calls.Done()
			defer server.Close()
			frame, err := listen.ReadFrameFrom(server, 1<<20)
			if err != nil {
				return
			}
			probe := &codexConcurrentNativeProbe{messagesProbe: &messagesProbe{}, ctx: call, entered: entered, contextName: contextName}
			raw, err := (&wire.MessagesDispatcher{Handler: probe}).ExchangeFrame(frame)
			if err != nil {
				return
			}
			listen.WriteFrameTo(server, raw, 1<<20)
		}()
		return client, nil
	}}
	b := &messagesBackend{application: "oa-codex-integration", instance: "native-instance", contextName: contextName, native: func(call context.Context) (nativeMessages, error) {
		return wire.NewMessagesClient(transport.WithContext(call)), nil
	}}
	watchDone := make(chan struct{})
	go func() { defer close(watchDone); b.MessagesWatch(watchCtx, gateway.MessagesWatchInput{WaitMs: 25000}) }()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("watch did not enter")
	}
	st, ct := mcp.NewInMemoryTransports()
	ss, err := gateway.NewServer(b).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "inert-concurrent-receiving", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	for _, params := range []*mcp.CallToolParams{
		{Name: "oa_messages_wait", Arguments: map[string]any{"wait_ms": 0}},
		{Name: "oa_messages_reply", Arguments: map[string]any{"message_id": "question-5", "body": "reply"}},
	} {
		call, stop := context.WithTimeout(ctx, time.Second)
		result, err := cs.CallTool(call, params)
		stop()
		if err != nil || result.IsError {
			t.Fatal("background wait blocked ordinary MCP", result, err)
		}
		select {
		case <-watchDone:
			t.Fatal("watch was released to serve ordinary call")
		default:
		}
	}
	stopWatch()
	select {
	case <-watchDone:
	case <-time.After(time.Second):
		t.Fatal("cancelled watch did not join")
	}
	calls.Wait()
}

// Architect ruling 2026-11-22 (backlog/AB-1p.md): a bound connection keeps
// its receiver for a call with missing, invalid or another thread's metadata,
// reports that call as foreign, and never retargets.
func TestCodexReceivingForeignOrMissingMetadataKeepsTheBinding(t *testing.T) {
	for _, meta := range []mcp.Meta{nil, {"threadId": "wrong"}, {"threadId": "22222222-2222-4222-8222-222222222222"}} {
		t.Run("foreign", func(t *testing.T) {
			bound := mcp.Meta{"threadId": "11111111-1111-4111-8111-111111111111"}
			entered, finished := make(chan struct{}), make(chan struct{})
			var starts atomic.Int32
			r := newCodexReceivingConnection(context.Background(), func(ctx context.Context, _ string) error {
				starts.Add(1)
				close(entered)
				<-ctx.Done()
				close(finished)
				return ctx.Err()
			}, func(string) {})
			defer r.stop("test_end")
			if r.observe(bound) {
				t.Fatal("first thread reported foreign")
			}
			<-entered
			if !r.observe(meta) {
				t.Fatal("foreign metadata not reported")
			}
			select {
			case <-finished:
				t.Fatal("foreign metadata stopped the receiver")
			default:
			}
			if r.observe(bound) || r.ended() {
				t.Fatal("bound thread lost its binding")
			}
			if !r.observe(mcp.Meta{"threadId": "33333333-3333-4333-8333-333333333333"}) || starts.Load() != 1 {
				t.Fatal("binding retargeted")
			}
			r.stop("test_end")
			if !r.ended() {
				t.Fatal("stopped receiving not reported as ended")
			}
		})
	}
}

// Architect ruling 2026-11-22: once receiving has ended on an open
// connection, the bound thread's bare unavailable chat read names that state.
// Admission answers with their own state pass through unchanged.
func TestCodexReceivingEndedReadNamesTheState(t *testing.T) {
	r := newCodexReceivingConnection(context.Background(), func(ctx context.Context, _ string) error { <-ctx.Done(); return ctx.Err() }, func(string) {})
	bound := mcp.Meta{"threadId": "11111111-1111-4111-8111-111111111111"}
	answer := `{"outcome":"unavailable"}`
	call := r.middleware(func(context.Context, string, mcp.Request) (mcp.Result, error) {
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: answer}}, StructuredContent: json.RawMessage(answer)}, nil
	})
	read := func() string {
		t.Helper()
		out, err := call(context.Background(), "tools/call", &mcp.ServerRequest[*mcp.CallToolParamsRaw]{Params: &mcp.CallToolParamsRaw{Meta: bound, Name: gateway.ToolMessagesWait, Arguments: json.RawMessage(`{}`)}})
		if err != nil {
			t.Fatal(err)
		}
		return out.(*mcp.CallToolResult).Content[0].(*mcp.TextContent).Text
	}
	if got := read(); got != answer {
		t.Fatal("live receiving rewrote the answer", got)
	}
	r.stop("test_end")
	if got := read(); !strings.Contains(got, codexReceivingEndedReason) {
		t.Fatal("ended receiving answered a bare unavailable", got)
	}
	answer = `{"outcome":"unavailable","state":"refused"}`
	if got := read(); got != answer {
		t.Fatal("admission state replaced", got)
	}
}

func TestCodexReceivingRequiresCurrentNativePresence(t *testing.T) {
	b := &messagesBackend{application: "oa-codex-integration", status: &messagesInboxStatus{admission: &agentToolAdmission{}}}
	if codexReceivingPresence(context.Background(), b) == nil {
		t.Fatal("unopened presence admitted receiving")
	}
	b.status.admission.confirm(time.Now().Add(time.Minute))
	if err := codexReceivingPresence(context.Background(), b); err != nil {
		t.Fatal(err)
	}
	b.status.admission.setReady(false)
	if codexReceivingPresence(context.Background(), b) == nil {
		t.Fatal("suspended presence retained receiving")
	}
	b.status.admission.confirm(time.Now().Add(time.Minute))
	b.application = "oa-claude-integration"
	if codexReceivingPresence(context.Background(), b) == nil {
		t.Fatal("foreign application admitted receiving")
	}
}

func TestCodexSessionStartPrivateToolUsesOnlyHostMetadata(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	a := new(agentToolAdmission)
	a.confirm(time.Now().Add(time.Minute))
	p := &statusMessagesProbe{outcome: wire.OutcomeOpened}
	b := &messagesBackend{Backend: new(reconciliationBackend), application: "oa-codex-integration", instance: "original", contextName: "native-11111111111111111111111111111111", status: &messagesInboxStatus{admission: a}, native: func(context.Context) (nativeMessages, error) { return p, nil }}
	started := make(chan string, 1)
	r := newCodexReceivingConnection(ctx, func(ctx context.Context, thread string) error { started <- thread; <-ctx.Done(); return ctx.Err() }, func(string) {})
	defer r.stop("test_end")
	server := gateway.NewServer(b)
	r.addSessionStartTool(server, b)
	server.AddReceivingMiddleware(r.middleware)
	st, ct := mcp.NewInMemoryTransports()
	ss, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "inert-session-start", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(meta mcp.Meta, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		out, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: codexSessionStartTool, Meta: meta, Arguments: args})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	if !call(nil, map[string]any{}).IsError {
		t.Fatal("missing metadata bound receiving")
	}
	if !call(mcp.Meta{"sessionId": "22222222-2222-4222-8222-222222222222"}, map[string]any{}).IsError {
		t.Fatal("session tree bound receiving")
	}
	if !call(nil, map[string]any{"threadId": "11111111-1111-4111-8111-111111111111"}).IsError {
		t.Fatal("hook accepted identity arguments")
	}
	select {
	case <-started:
		t.Fatal("missing metadata started receiver")
	default:
	}
	thread := "11111111-1111-4111-8111-111111111111"
	if call(mcp.Meta{"threadId": thread}, map[string]any{}).IsError {
		t.Fatal("host metadata refused")
	}
	select {
	case got := <-started:
		if got != thread {
			t.Fatal(got)
		}
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if !call(nil, map[string]any{}).IsError {
		t.Fatal("a call without metadata observed the bound chat")
	}
	if !call(mcp.Meta{"threadId": "22222222-2222-4222-8222-222222222222"}, map[string]any{}).IsError {
		t.Fatal("another thread observed the bound chat")
	}
	if call(mcp.Meta{"threadId": thread}, map[string]any{}).IsError {
		t.Fatal("foreign calls ended the bound thread's receiving")
	}
	select {
	case got := <-started:
		t.Fatal("binding restarted", got)
	default:
	}
}

func TestCodexSessionStartToolAbsentFromGenericCatalogue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	st, ct := mcp.NewInMemoryTransports()
	ss, err := gateway.NewServer(new(reconciliationBackend)).Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "inert-generic-catalogue", Version: "test"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	out, err := cs.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range out.Tools {
		if tool.Name == codexSessionStartTool {
			t.Fatal("private hook leaked into generic catalogue")
		}
	}
}
