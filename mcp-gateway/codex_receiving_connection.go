package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"github.com/openabstractions/abstractions/mcp-gateway/internal/setupio"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

// Host request metadata supplies routing only. Native enrollment and message
// rights retain authority; tool arguments and session-tree IDs never bind it.
type codexReceivingConnection struct {
	mu              sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	thread          string
	started, closed bool
	done            chan struct{}
	run             func(context.Context, string) error
	report          func(string)
	binding         func(bool)
	bindChat        func(string) error
	// topLevel proves a thread top-level before host metadata binds it.
	topLevel func(string) bool
}

func newCodexReceivingConnection(ctx context.Context, run func(context.Context, string) error, report func(string)) *codexReceivingConnection {
	ctx, cancel := context.WithCancel(ctx)
	r := &codexReceivingConnection{ctx: ctx, cancel: cancel, done: make(chan struct{}), run: run, report: report}
	r.topLevel = func(thread string) bool {
		//unchecked: an unknown Codex folder reads as "", and codexProvenTopLevel proves no thread under a home that is not absolute
		home, _ := codexReceivingHome()
		return codexThreadTopLevel(home, thread)
	}
	r.report("unavailable_missing_thread_metadata")
	return r
}

// observe binds the first valid host thread proven top-level and reports
// whether a call comes from another thread. One Codex helper serves a chat
// and its sub-agent threads, so a call with missing, invalid or another
// thread's metadata leaves the binding and its receiver running; the
// middleware refuses only that call's chat-bound messaging (AB-1p ruling
// 2026-11-22). The binding never retargets or restarts. A thread not proven
// top-level (ruling 3, 2026-11-22) leaves the connection unbound and its call
// is handled as another thread's; the main chat binds on its next call.
func (r *codexReceivingConnection) observe(meta map[string]any) (foreign bool) {
	thread, ok := meta["threadId"].(string)
	valid := ok && codexQueueThread.MatchString(thread)
	r.mu.Lock()
	if r.started {
		foreign = !valid || thread != r.thread
		r.mu.Unlock()
		return foreign
	}
	if r.closed || !valid {
		r.mu.Unlock()
		return false
	}
	topLevel := r.topLevel
	r.mu.Unlock()
	if topLevel == nil || !topLevel(thread) {
		return true
	}
	r.mu.Lock()
	if r.started {
		foreign = thread != r.thread
		r.mu.Unlock()
		return foreign
	}
	if r.closed {
		r.mu.Unlock()
		return false
	}
	r.startLocked(thread)
	return false
}

// ended reports receiving that has stopped while the connection is open: its
// receiver returned for good, or the binding was refused or stopped.
func (r *codexReceivingConnection) ended() bool {
	r.mu.Lock()
	closed, started := r.closed, r.started
	r.mu.Unlock()
	if closed {
		return true
	}
	if !started {
		return false
	}
	select {
	case <-r.done:
		return true
	default:
		return false
	}
}

// restore binds a thread the host app has loaded for this connection before
// any request carried its metadata. It never replaces a binding: a connection
// already bound, or closed, ignores it. Later host metadata must name the same
// thread, as for any binding.
func (r *codexReceivingConnection) restore(thread string) bool {
	if !codexQueueThread.MatchString(thread) {
		return false
	}
	r.mu.Lock()
	if r.closed || r.started {
		r.mu.Unlock()
		return false
	}
	r.startLocked(thread)
	r.mu.Lock()
	bound := r.started && r.thread == thread && !r.closed
	r.mu.Unlock()
	return bound
}

// settled reports a connection that is bound or closed; restoring ends then.
func (r *codexReceivingConnection) settled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.started || r.closed
}

// startLocked is entered with r.mu held and releases it.
func (r *codexReceivingConnection) startLocked(thread string) {
	r.thread, r.started = thread, true
	if r.bindChat != nil {
		if err := r.bindChat(thread); err != nil {
			r.started = false
			r.mu.Unlock()
			r.stop("unavailable_chat_metadata")
			return
		}
	}
	if r.binding != nil {
		r.binding(true)
	}
	go func() {
		defer close(r.done)
		if err := r.run(r.ctx, thread); err != nil && r.ctx.Err() == nil {
			r.report("unavailable_receiver_stopped")
		}
	}()
	r.mu.Unlock()
}

func (r *codexReceivingConnection) stop(reason string) {
	r.mu.Lock()
	first := !r.closed
	r.closed = true
	if first && r.binding != nil {
		r.binding(false)
	}
	r.cancel()
	started := r.started
	r.mu.Unlock()
	if first {
		r.report(reason)
	}
	if started {
		<-r.done
	}
}

// codexChatBoundTools act as the bound chat: they read its inbox, watch it,
// or send with its context and return route. On a Codex helper every send,
// reply and prepare uses that route once the chat is bound.
var codexChatBoundTools = map[string]bool{
	gateway.ToolMessagesInboxOpen: true,
	gateway.ToolMessagesWait:      true,
	"oa_messages_watch":           true,
	gateway.ToolMessagesSend:      true,
	gateway.ToolMessagesReply:     true,
	gateway.ToolMessagesPrepare:   true,
}

// codexChatReads report a receiving state when receiving has ended.
var codexChatReads = map[string]bool{gateway.ToolMessagesInboxOpen: true, gateway.ToolMessagesWait: true, "oa_messages_watch": true}

const (
	codexForeignThreadReason  = "This thread isn't the Codex chat connected to OpenAbstractions. Ask the main chat to send it."
	codexReceivingEndedReason = "receiving ended for this connection; restart the OA connection in Codex"
)

type codexForeignThreadKey struct{}

func codexForeignThread(ctx context.Context) bool {
	foreign, _ := ctx.Value(codexForeignThreadKey{}).(bool)
	return foreign
}

func codexChatRefusal(outcome, reason string) *mcp.CallToolResult {
	raw, _ := json.Marshal(map[string]string{"outcome": outcome, "reason": reason})
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: string(raw)}}}
}

// codexBareUnavailable reports a tool answer of unavailable with no state or
// reason.
func codexBareUnavailable(result mcp.Result) bool {
	out, ok := result.(*mcp.CallToolResult)
	if !ok || out == nil {
		return false
	}
	var raw []byte
	switch structured := out.StructuredContent.(type) {
	case json.RawMessage:
		raw = structured
	case nil:
		if len(out.Content) != 0 {
			if text, ok := out.Content[0].(*mcp.TextContent); ok {
				raw = []byte(text.Text)
			}
		}
	default:
		raw, _ = json.Marshal(structured)
	}
	var answer map[string]any
	if json.Unmarshal(raw, &answer) != nil {
		return false
	}
	_, state := answer["state"]
	reason, _ := answer["reason"].(string)
	return answer["outcome"] == "unavailable" && !state && reason == ""
}

func (r *codexReceivingConnection) middleware(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, request mcp.Request) (mcp.Result, error) {
		if (method == "tools/call" || method == "initialize") && request.GetParams() != nil {
			foreign := r.observe(request.GetParams().GetMeta())
			if tool, ok := request.(*mcp.CallToolRequest); ok && method == "tools/call" && tool.Params != nil {
				name := tool.Params.Name
				if foreign {
					if codexChatBoundTools[name] {
						//unchecked: bounded routing label; stderr has no fallback destination
						fmt.Fprintf(os.Stderr, "OA_CODEX_RECEIVING foreign_thread_refused tool=%s\n", name)
						return codexChatRefusal("not_permitted", codexForeignThreadReason), nil
					}
					ctx = gateway.WithOtherThread(context.WithValue(ctx, codexForeignThreadKey{}, true))
				} else if codexChatReads[name] {
					// A bare unavailable read after receiving ended names that
					// state; admission and runtime answers keep their own.
					result, err := next(ctx, method, request)
					if err == nil && codexBareUnavailable(result) && r.ended() {
						return codexChatRefusal("unavailable", codexReceivingEndedReason), nil
					}
					return result, err
				}
			}
		}
		return next(ctx, method, request)
	}
}

const codexSessionStartTool = "oa_codex_session_start"

// This private tool is installed only by the dedicated native Codex owner.
// Its empty arguments carry no identity. The ordinary receiving middleware
// observes host-supplied request metadata before this handler is reached.
func (r *codexReceivingConnection) addSessionStartTool(server *mcp.Server, b *messagesBackend) {
	server.AddTool(&mcp.Tool{Name: codexSessionStartTool, Description: "Internal session-start binding; no ordinary model call is needed. Host request metadata supplies routing; this grants no permissions.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, IdempotentHint: true}, InputSchema: map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false}}, func(ctx context.Context, _ *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		r.mu.Lock()
		bound := r.started && !r.closed
		r.mu.Unlock()
		if !bound || codexForeignThread(ctx) || codexReceivingPresence(ctx, b) != nil {
			return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"unavailable"}`}}}, nil
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: `{"outcome":"observed"}`}}}, nil
	})
}

func codexReceivingPresence(ctx context.Context, b *messagesBackend) error {
	if nativecli.AgentApplicationBase(b.application) != "oa-codex-integration" {
		return errors.New("Codex native receiving unavailable")
	}
	return chatReceivingPresence(ctx, b)
}

func chatReceivingPresence(ctx context.Context, b *messagesBackend) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.status == nil || b.status.admission == nil {
		return errors.New("Codex native receiving unavailable")
	}
	a := b.status.admission
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.closed || a.final || !time.Now().Before(a.deadline) {
		return errors.New("Codex native receiving unavailable")
	}
	if !a.ready {
		return errCodexReceivingSuspended
	}
	return nil
}

func codexReceivingQueue(ctx context.Context, b *messagesBackend, cli codexReceiverCLI, thread string, driver codexSetupRunner, states ...*codexWakeState) codexSetupRunner {
	return func(request context.Context, program, directory string, args []string) (codexCLIResult, error) {
		call, done, admitted := b.BeginNativeTool(request, "oa_messages_watch")
		defer done()
		if !admitted {
			err := codexReceivingPresence(request, b)
			if err == nil || errors.Is(err, errCodexReceivingSuspended) {
				err = errCodexReceivingSuspended
			}
			return codexCLIResult{}, &codexQueueNotStarted{cause: err}
		}
		if err := cli.Check(call); err != nil {
			return codexCLIResult{}, &codexQueueNotStarted{cause: err}
		}
		var ticket, wake string
		notStarted := func(cause error) error {
			if ticket != "" && states[0].UndoUnstarted != nil {
				cleanup, cancel := context.WithTimeout(context.WithoutCancel(call), time.Second)
				defer cancel()
				cause = errors.Join(cause, states[0].UndoUnstarted(cleanup, ticket, wake))
			}
			return &codexQueueNotStarted{cause: cause}
		}
		if len(states) != 0 {
			page, err := b.MessagesWatch(call, gateway.MessagesWatchInput{WaitMs: 0})
			if err != nil || page.Outcome.String() != "page" {
				return codexCLIResult{}, &codexQueueNotStarted{cause: errors.Join(err, errors.New("Codex wake recheck unavailable"))}
			}
			if states[0] == nil || states[0].Pointer == nil || states[0].Reserve == nil {
				return codexCLIResult{}, &codexQueueNotStarted{cause: errors.New("Codex wake state unavailable")}
			}
			wake = page.Wake
			ticket, err = states[0].Reserve(call, wake)
			if err != nil {
				if errors.Is(err, errCodexWakeBusy) {
					observeCodexReceiving(ctx, "receiver:busy", thread, b.contextName)
					b.publishWakeBusy(call, true)
				}
				return codexCLIResult{}, &codexQueueNotStarted{cause: err}
			}
			if ticket == "" {
				return codexCLIResult{}, &codexQueueNotStarted{cause: errCodexWakeStale}
			}
			text, promptErr := states[0].Pointer(call, ticket, page.WakeSender)
			if promptErr != nil || text == "" {
				return codexCLIResult{}, notStarted(errors.Join(promptErr, errors.New("Codex wake pointer unavailable")))
			}
			args = codexQueueArguments(thread, text)
		}
		observeCodexReceiving(ctx, "queue_attempt", thread, b.contextName)
		result, err := driver(call, program, directory, args)
		if result.NotStarted {
			return result, notStarted(errors.Join(err, errors.New("Codex queue process did not start")))
		}
		if len(states) != 0 && b.status != nil {
			b.publishWakeBusy(call, true)
		}
		if err != nil || result.Code != 0 || len(result.Diagnostic) != 0 {
			observeCodexReceiving(ctx, "queue_unknown", thread, b.contextName)
			// The call's own deadline may have passed; the connection publishes.
			b.publishWakeUnconfirmed(ctx, "queue_unknown")
			// Only closed facts: vendor output and error text stay out of the log.
			fmt.Fprintf(os.Stderr, "OA_CODEX_RECEIVING queue_unknown thread=%s code=%d diagnostic_bytes=%d driver_error=%t\n", thread, result.Code, len(result.Diagnostic), err != nil)
		} else {
			if ticket != "" && states[0].AcceptQueued != nil {
				reconcile, cancel := context.WithTimeout(context.WithoutCancel(call), time.Second)
				acceptErr := states[0].AcceptQueued(reconcile, ticket)
				cancel()
				if acceptErr != nil {
					observeCodexReceiving(ctx, "queue_acceptance_not_recorded", thread, b.contextName)
				}
			}
			observeCodexReceiving(ctx, "queue_accepted", thread, b.contextName)
			b.publishWakeUnconfirmed(ctx, "")
		}
		return result, err
	}
}

// Run returns only after the listener is cancelled and joined. Its caller's
// admitted-agent owner then withdraws the original native inbox association.
func runNativeMCPServer(ctx context.Context, server *mcp.Server, backend gateway.Backend, directory string) error {
	return runNativeMCPServerWithCodexDriver(ctx, server, backend, directory, runCodexSetupCLI)
}

// The driver seam permits the actual listener composition to be qualified with
// an inert queue sink. Production always supplies the ordinary bounded runner.
func runNativeMCPServerWithCodexDriver(ctx context.Context, server *mcp.Server, backend gateway.Backend, directory string, driver codexSetupRunner) error {
	return runNativeMCPServerTransport(ctx, server, backend, directory, driver, nativeCodexReceiverCLI, guardedStdioTransport())
}

// guardAgentServer makes a handler panic a typed error. It is added last, so it
// is the outermost middleware and covers the Codex receiving middleware too.
func guardAgentServer(server *mcp.Server) {
	gateway.GuardServer(server, func(method string, recovered any, stack []byte) {
		//unchecked: diagnostic on a recovered fault; stderr has no fallback destination
		fmt.Fprintf(os.Stderr, "openabstractions-mcp pid=%d: recovered panic in %s: %v\n%s", os.Getpid(), method, recovered, stack)
	})
}

// The pause before a stopped receiver starts again grows from one second to
// thirty. codexReceiverPauseDivisor is 1 in production; the chaos tests
// shorten every receiver pause with it.
var (
	codexReceiverResumeFirst  = time.Second
	codexReceiverResumeMax    = 30 * time.Second
	codexReceiverPauseDivisor = time.Duration(1)
)

// codexReceiverCLISelection selects the CLI the receiver queues through and
// returns its release. Production uses the native selection.
type codexReceiverCLISelection func(context.Context, func(context.Context, string) ([]byte, error)) (codexReceiverCLI, func() error, error)

func nativeCodexReceiverCLI(ctx context.Context, probe func(context.Context, string) ([]byte, error)) (codexReceiverCLI, func() error, error) {
	cli, err := nativecli.SelectCodexCLI(ctx, probe)
	if err != nil {
		return nil, nil, err
	}
	if err := checkCodexQueueResume(ctx, cli, "", runCodexSetupCLI); err != nil {
		return nil, nil, errors.Join(err, cli.Close())
	}
	return cli, cli.Close, nil
}

// superviseCodexReceiver keeps the wake receiver of one bound thread running
// for the whole connection. A run that stops, after a presence loss, a lost
// read, an expired recovery, a failed CLI selection or a queue whose effect is
// unknown, starts again with backoff once native admission is current. The
// progress it carries prevents another wake for incoming messages already
// queued or whose queue outcome is unknown, with revision fallback for pages
// without incoming message IDs. A first forbidden observation
// ends its run and retries after current admission; a successful observation
// clears that confirmation. A second forbidden before success, final admission
// refusal, connection end or invalidated thread binding ends receiving.
func superviseCodexReceiver(receiving context.Context, b *messagesBackend, once func(context.Context, func()) error, observe func(string), pause func(context.Context, time.Duration) error, now func() time.Time) error {
	delay := codexReceiverResumeFirst
	forbidden := false
	for {
		if codexReceivingRetired(b) {
			return errors.New("Codex native receiving retired")
		}
		started := now()
		err := func() (err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					reportAgentPanic("codex receiver", recovered)
					err = fmt.Errorf("Codex receiver panic: %v", recovered)
				}
			}()
			return once(receiving, func() { forbidden = false })
		}()
		if receiving.Err() != nil {
			return receiving.Err()
		}
		if errors.Is(err, errCodexObservationForbidden) {
			if forbidden {
				return err
			}
			forbidden = true
		}
		if codexReceivingRetired(b) {
			return errors.Join(err, errors.New("Codex native receiving retired"))
		}
		if now().Sub(started) > time.Minute {
			delay = codexReceiverResumeFirst
		}
		if err := pause(receiving, delay); err != nil {
			return err
		}
		if delay *= 2; delay > codexReceiverResumeMax {
			delay = codexReceiverResumeMax
		}
		for {
			current := codexReceivingPresence(receiving, b)
			if current == nil {
				break
			}
			if codexReceivingRetired(b) {
				return errors.Join(err, current)
			}
			if err := pause(receiving, time.Second); err != nil {
				return err
			}
		}
		observe("resuming")
	}
}

func codexReceivingRetired(b *messagesBackend) bool {
	if b.status == nil || b.status.admission == nil {
		return true
	}
	a := b.status.admission
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.closed || a.final
}

// codexReceivingHome names the Codex folder whose locks and session index
// the receiver reads. Tests point it away from the person's own Codex.
var codexReceivingHome = setupio.CodexHome

func runNativeMCPServerTransport(ctx context.Context, server *mcp.Server, backend gateway.Backend, directory string, driver codexSetupRunner, selectCLI codexReceiverCLISelection, transport mcp.Transport) error {
	b, ok := backend.(*messagesBackend)
	if ok && nativecli.AgentApplicationBase(b.application) == "oa-claude-integration" {
		return runClaudeChatServer(ctx, server, b, transport)
	}
	if ok && nativecli.AgentApplicationBase(b.application) == "oa-opencode-integration" {
		return runOpenCodeChatServer(ctx, server, b, transport)
	}
	if !ok || nativecli.AgentApplicationBase(b.application) != "oa-codex-integration" {
		guardAgentServer(server)
		return server.Run(ctx, transport)
	}
	report := func(reason string) {
		observeCodexReceiving(ctx, reason, "", b.contextName)
		fmt.Fprintln(os.Stderr, "OA_CODEX_RECEIVING "+reason)
	}
	pause := func(ctx context.Context, d time.Duration) error {
		timer := time.NewTimer(d / codexReceiverPauseDivisor)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	home, homeErr := codexReceivingHome()
	// The thread lock, title and restore goroutines end with the connection.
	// The transport returns after they have released the thread lock in
	// directory, so a caller can remove or reuse that folder at once.
	var background sync.WaitGroup
	defer background.Wait()
	r := newCodexReceivingConnection(ctx, func(receiving context.Context, thread string) error {
		// A helper bound by host metadata keeps restoring helpers of the same
		// app-server off its thread; a restored binding already holds this lock.
		background.Go(func() { holdCodexThread(receiving, directory, thread, time.After) })
		var wakeState *codexWakeState
		var chat *messagesBackend
		var stop func()
		var err error
		for {
			chat, stop, err = b.newChatReceiver(receiving, func(candidate *messagesBackend) error {
				if homeErr == nil {
					candidate.status.setChatTitle(codexThreadTitle(home, thread))
				}
				var err error
				wakeState, err = codexWakeReceipt(ctx, b.application, candidate.contextName, thread)
				if err != nil {
					return err
				}
				candidate.useWakeState(wakeState)
				return nil
			})
			if !errors.Is(err, errChatSourceAdmissionUnavailable) || receiving.Err() != nil || codexReceivingRetired(b) {
				break
			}
			// Initial host metadata can precede runtime/source admission. Keep
			// this original connection alive under its existing bounded retry.
			if err = pause(receiving, codexReceiverResumeFirst); err != nil {
				break
			}
		}
		if err != nil {
			return err
		}
		defer stop()
		if homeErr == nil {
			background.Go(func() { followCodexThreadTitle(receiving, chat.status, home, thread, time.After) })
		}
		progress := &codexReceiverProgress{load: wakeState.Load}
		once := func(receiving context.Context, observed func()) error {
			observeCodexReceiving(ctx, "selecting_cli", thread, chat.contextName)
			selection, cancel := context.WithTimeout(receiving, 10*time.Second)
			cli, release, err := selectCLI(selection, func(ctx context.Context, program string) ([]byte, error) {
				result, err := driver(ctx, program, directory, []string{"--version"})
				if err == nil && result.Code != 0 {
					err = errors.New("Codex version unavailable")
				}
				return result.Output, err
			})
			cancel()
			if err != nil {
				observeCodexReceiving(ctx, "cli_unavailable", thread, chat.contextName)
				return err
			}
			defer release()
			binding := codexReceiverBinding{context: chat.contextName, thread: thread, directory: directory, cli: cli, current: func(ctx context.Context) error { return codexReceivingPresence(ctx, chat) }, observe: func(event string) {
				switch event {
				case "observed":
					observed()
				case "wake_unconfirmed":
					// A fresh message has waited behind an unconsumed pointer
					// past its bound; senders now read that it is queued.
					chat.publishWakeUnconfirmed(ctx, "wake_busy")
					fmt.Fprintf(os.Stderr, "OA_CODEX_RECEIVING %s thread=%s context=%s\n", event, thread, chat.contextName)
				case "wake_busy", "wake_stale", "queue_not_started":
					// The host keeps this server's stderr; a waiting or unstarted
					// wake is otherwise invisible in the bounded lifecycle row.
					fmt.Fprintf(os.Stderr, "OA_CODEX_RECEIVING %s thread=%s context=%s\n", event, thread, chat.contextName)
				}
				observeCodexReceiving(ctx, event, thread, chat.contextName)
			}}
			run := codexReceivingQueue(ctx, chat, cli, thread, driver, wakeState)

			// Only our bounded routing labels are recorded; never dump request
			// metadata, tool arguments, vendor output, or conversation contents.
			fmt.Fprintf(os.Stderr, "OA_CODEX_RECEIVING binding thread=%s context=%s\n", thread, chat.contextName)
			return runCodexInboxReceiverProgress(receiving, binding, chat, run, time.Now, pause, progress)
		}
		return superviseCodexReceiver(receiving, chat, once, func(event string) {
			observeCodexReceiving(ctx, event, thread, chat.contextName)
			fmt.Fprintln(os.Stderr, "OA_CODEX_RECEIVING "+event)
		}, pause, time.Now)
	}, report)
	b.chatMu.Lock()
	b.requireChat = true
	b.chatMu.Unlock()
	r.bindChat = b.bindChat
	defer r.stop("unavailable_connection_closed")
	r.addSessionStartTool(server, b)
	if homeErr == nil {
		restoring, cancelRestore := context.WithCancel(ctx)
		defer cancelRestore()
		background.Go(func() { restoreCodexThread(restoring, r, home, directory, time.After) })
	}
	server.AddReceivingMiddleware(r.middleware)
	guardAgentServer(server)
	return server.Run(ctx, transport)
}
