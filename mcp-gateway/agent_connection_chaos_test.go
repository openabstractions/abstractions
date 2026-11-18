package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	cas "github.com/openabstractions/abstraction-cas/go"
	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// The chaos tests drive the production helper composition: the MCP server on
// a line-delimited stdio transport, the presence supervisor and its attempt,
// tool admission, the Codex wake receiver and its supervisor, the panic
// guard and the lifecycle record. Only the runtime is a fake: an in-process
// stand-in for Applications and Messages that the tests break on purpose.
// Nothing here reaches an installed runtime or a Codex installation.
//
// Every scenario runs at least chaosRounds times. In every round the helper
// keeps its stdio open, every tool call returns a result rather than a
// transport error, and the next tool call succeeds once the runtime is back.

const chaosRounds = 20

var errChaosPipe = errors.New("io: read/write on closed pipe")

// chaosRuntime is the fake Applications and Messages services.
type chaosRuntime struct {
	onAnnounce func(facade.ApplicationPresence, string)
	nativeMessages
	mu                   sync.Mutex
	down                 bool
	listing              *msg.InboxPage
	hang                 chan struct{}
	stalled              chan struct{} // open while the process is suspended (messages_stall_test.go)
	decodeFaults         int
	headerUnavailable    int
	headerForbidden      bool
	headerForbiddenOnce  int
	headerForbiddenReads int
	instances            map[string]time.Time
	holder               map[string]string
	chatBindings         map[string]msg.ChatBinding
	retired              map[string]bool
	next                 int
	revision             int
	pendingUntil         int64
	changed              chan struct{}
	allocationFault      string
	allocationFaults     int
	lostChatStatus       string
	allocations          []chaosAllocationCall
	allocatedContexts    map[string]string
	// epoch is part of every header revision, as serve's directory epoch is:
	// a restart changes the revision of the same pending questions.
	epoch int
	// forbid answers InboxOpen forbidden, as a revoked enrollment does.
	forbid bool
	// actions counts every message call a model's tool could cause: Send,
	// body Wait, InboxesList, Prepare and Reply, faulted or not.
	actions   atomic.Int64
	announces atomic.Int64
	// turnWakes records each TurnWake key the gateway stated, empty for a
	// turn signal.
	turnWakes []string
}

type chaosAllocationCall struct {
	instance, context string
	binding           msg.ChatBinding
}

func newChaosRuntime() *chaosRuntime {
	return &chaosRuntime{instances: map[string]time.Time{}, holder: map[string]string{}, changed: make(chan struct{})}
}

func (r *chaosRuntime) headerRevision() string {
	return strconv.Itoa(r.epoch) + ":" + strconv.Itoa(r.revision)
}

// incoming lists the pending questions addressed to one inbox.
func (r *chaosRuntime) incoming(application, contextName string) []msg.Header {
	var headers []msg.Header
	submitted := msg.TaskStateSubmitted
	for n := max(1, r.revision-49); n <= r.revision; n++ {
		headers = append(headers, msg.Header{MessageID: "chaos-question-" + strconv.Itoa(n), To: "app:" + application, Context: contextName, Kind: msg.KindQuestion, TaskState: &submitted, ExpiresUnixMs: r.pendingUntil, AnswerByUnixMs: r.pendingUntil})
	}
	return headers
}

// fault applies the injected failure to one call.
func (r *chaosRuntime) fault() error {
	if err := r.stall(false); err != nil {
		return err
	}
	r.mu.Lock()
	hang, down := r.hang, r.down
	decode := r.decodeFaults > 0
	if decode {
		r.decodeFaults--
	}
	r.mu.Unlock()
	if hang != nil {
		<-hang
		return errChaosPipe
	}
	if down {
		return errChaosPipe
	}
	if decode {
		// What an older helper's closed decoder answers to a newer field.
		return &msg.Refusal{Word: "unknown_field", Offset: 12}
	}
	return nil
}

func (r *chaosRuntime) set(change func(*chaosRuntime)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(r)
}

// restart forgets every lease and holder, as a new runtime process does.
func (r *chaosRuntime) restart(down bool) {
	r.set(func(r *chaosRuntime) {
		r.instances, r.holder, r.down = map[string]time.Time{}, map[string]string{}, down
		r.epoch++
	})
}

// deliver makes a new question pending for every inbox.
func (r *chaosRuntime) deliver() {
	r.set(func(r *chaosRuntime) {
		r.revision++
		r.pendingUntil = time.Now().Add(time.Minute).UnixMilli()
		close(r.changed)
		r.changed = make(chan struct{})
	})
}

func (r *chaosRuntime) live(instance string, now time.Time) bool {
	expiry, ok := r.instances[instance]
	return ok && now.Before(expiry)
}

func (r *chaosRuntime) Announce(_ context.Context, presence facade.ApplicationPresence) (facade.ApplicationChange, error) {
	r.announces.Add(1)
	if err := r.fault(); err != nil {
		return facade.ApplicationChange{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	lease := time.Duration(presence.LeaseMs) * time.Millisecond
	if presence.Instance == "" {
		r.next++
		instance := "chaos-" + strconv.Itoa(r.next)
		r.instances[instance] = now.Add(lease)
		if r.onAnnounce != nil {
			r.onAnnounce(presence, instance)
		}
		return facade.ApplicationChange{Outcome: facade.ApplicationOutcomeApplied, Instance: instance}, nil
	}
	if !r.live(presence.Instance, now) {
		delete(r.instances, presence.Instance)
		return facade.ApplicationChange{Outcome: facade.ApplicationOutcomeStale}, nil
	}
	r.instances[presence.Instance] = now.Add(lease)
	if r.onAnnounce != nil {
		r.onAnnounce(presence, presence.Instance)
	}
	return facade.ApplicationChange{Outcome: facade.ApplicationOutcomeApplied, Instance: presence.Instance}, nil
}

func (r *chaosRuntime) Withdraw(_ context.Context, _ string, instance string) (facade.ApplicationChange, error) {
	if err := r.fault(); err != nil {
		return facade.ApplicationChange{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.instances, instance)
	for name, holder := range r.holder {
		if holder == instance {
			delete(r.holder, name)
		}
	}
	return facade.ApplicationChange{Outcome: facade.ApplicationOutcomeApplied}, nil
}

func (r *chaosRuntime) InboxOpen(instance, contextName, _ string) (msg.InboxChange, error) {
	if err := r.fault(); err != nil {
		return msg.InboxChange{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	// Serve answers forbidden for an instance it does not know, too.
	if r.forbid || !r.live(instance, now) {
		return msg.InboxChange{Outcome: msg.OutcomeForbidden}, nil
	}
	if holder, ok := r.holder[contextName]; ok && holder != instance && r.live(holder, now) {
		return msg.InboxChange{Outcome: msg.OutcomeHeld}, nil
	}
	r.holder[contextName] = instance
	return msg.InboxChange{Outcome: msg.OutcomeOpened, Revision: strconv.Itoa(r.revision)}, nil
}

func (r *chaosRuntime) InboxesList(msg.InboxQuery) (msg.InboxPage, error) {
	r.actions.Add(1)
	if err := r.fault(); err != nil {
		return msg.InboxPage{}, err
	}
	r.mu.Lock()
	listing := r.listing
	r.mu.Unlock()
	if listing != nil {
		return *listing, nil
	}
	return msg.InboxPage{Outcome: msg.OutcomePage}, nil
}

func (r *chaosRuntime) Prepare(string, string, msg.PrepareRequest) (msg.PrepareResult, error) {
	r.actions.Add(1)
	return msg.PrepareResult{}, errChaosPipe
}

func (r *chaosRuntime) Reply(string, string, string, string) (msg.SendResult, error) {
	r.actions.Add(1)
	return msg.SendResult{}, errChaosPipe
}

func (r *chaosRuntime) TurnWake(_, _, wake string) (msg.TurnWakeResult, error) {
	r.mu.Lock()
	r.turnWakes = append(r.turnWakes, wake)
	r.mu.Unlock()
	return msg.TurnWakeResult{Outcome: msg.OutcomePage}, nil
}

func (r *chaosRuntime) Wait(_, contextName string, view msg.View, _, revision string, waitMs int64) (msg.Page, error) {
	if view != msg.ViewHeaders {
		r.actions.Add(1)
	}
	if err := r.fault(); err != nil {
		return msg.Page{}, err
	}
	r.mu.Lock()
	if view == msg.ViewHeaders && (r.headerForbidden || r.headerForbiddenOnce > 0) {
		if r.headerForbiddenOnce > 0 {
			r.headerForbiddenOnce--
		}
		r.headerForbiddenReads++
		r.mu.Unlock()
		return msg.Page{Outcome: msg.OutcomeForbidden}, nil
	}
	if view == msg.ViewHeaders && r.headerUnavailable > 0 {
		r.headerUnavailable--
		r.mu.Unlock()
		return msg.Page{Outcome: msg.OutcomeUnavailable}, nil
	}
	current, changed := r.headerRevision(), r.changed
	r.mu.Unlock()
	if view != msg.ViewHeaders {
		return msg.Page{Outcome: msg.OutcomePage, Revision: current}, nil
	}
	if revision == current && waitMs > 0 {
		select {
		case <-changed:
		case <-time.After(150 * time.Millisecond):
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	// A delivery may have ended the long poll. Use the same snapshot for
	// its wake, revision and pending deadline.
	pending := r.pendingUntil
	wake := ""
	if pending > time.Now().UnixMilli() {
		wake = fmt.Sprintf("%020d", r.revision)
	}
	return msg.Page{Outcome: msg.OutcomePage, Revision: r.headerRevision(), Wake: wake, PendingUntilUnixMs: pending, Headers: r.incoming("oa-codex-integration", contextName)}, nil
}

func TestChaosHeaderWaitReturnsFreshWakeWithNewRevision(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		runtime := newChaosRuntime()
		revision := runtime.headerRevision()
		result := make(chan msg.Page, 1)
		go func() {
			page, _ := runtime.Wait("", "fixture", msg.ViewHeaders, "", revision, 25000)
			result <- page
		}()
		synctest.Wait()
		runtime.deliver()
		page := <-result
		if page.Outcome != msg.OutcomePage || page.Revision == revision || page.Wake == "" ||
			page.PendingUntilUnixMs <= time.Now().UnixMilli() || len(page.Headers) != 1 ||
			page.Headers[0].AnswerByUnixMs != page.PendingUntilUnixMs {
			t.Fatalf("header wait returned an inconsistent delivery: %+v", page)
		}
	})
}

func (r *chaosRuntime) Send(string, string, msg.SendRequest) (msg.SendResult, error) {
	r.actions.Add(1)
	_ = r.stall(true)
	if err := r.fault(); err != nil {
		return msg.SendResult{}, err
	}
	return msg.SendResult{Outcome: msg.OutcomeSent, MessageID: "chaos-message"}, nil
}

// chaosCLI is the selected Codex CLI; the receiver only checks and names it.
type chaosCLI struct{}

func (chaosCLI) Program() string             { return `C:\chaos\codex.exe` }
func (chaosCLI) Check(context.Context) error { return nil }

// chaosQueue records each wake the receiver queues and fails on request.
type chaosQueue struct {
	mu        sync.Mutex
	threads   map[string]int
	tickets   map[string]string
	revisions []string
	fail      int
	attempts  int
}

func (q *chaosQueue) driver(runtime *chaosRuntime) codexSetupRunner {
	return func(_ context.Context, _ string, _ string, args []string) (codexCLIResult, error) {
		if len(args) == 0 || args[0] != "queue" {
			return codexCLIResult{}, nil
		}
		q.mu.Lock()
		defer q.mu.Unlock()
		q.attempts++
		if q.tickets == nil {
			q.tickets = map[string]string{}
		}
		for n, arg := range args {
			if arg == "--message" && n+1 < len(args) {
				q.tickets[args[2]] = wakeTicketInPrompt(args[n+1])
			}
		}
		if q.fail > 0 {
			q.fail--
			return codexCLIResult{}, errors.New("codex queue failed")
		}
		if q.threads == nil {
			q.threads = map[string]int{}
		}
		q.threads[args[2]]++
		runtime.mu.Lock()
		q.revisions = append(q.revisions, strconv.Itoa(runtime.revision))
		runtime.mu.Unlock()
		return codexCLIResult{}, nil
	}
}

func (q *chaosQueue) snapshot() (int, int) {
	q.mu.Lock()
	defer q.mu.Unlock()
	total := 0
	for _, n := range q.threads {
		total += n
	}
	return total, q.attempts
}

// chaosBackend is the rest of the gateway: Models counts, and panics on
// request.
type chaosBackend struct {
	gateway.Backend
	panics atomic.Int32
	models atomic.Int32
	image  func(context.Context, gateway.ImageInput) (gateway.ImageOutput, error)
}

func (b *chaosBackend) Models(context.Context, gateway.ModelsInput) (gateway.ModelsOutput, error) {
	if b.panics.Load() > 0 {
		b.panics.Add(-1)
		panic("synthetic handler fault")
	}
	b.models.Add(1)
	return gateway.ModelsOutput{Models: []gateway.Model{}}, nil
}

func (b *chaosBackend) GenerateImage(ctx context.Context, in gateway.ImageInput) (gateway.ImageOutput, error) {
	if b.image == nil {
		return gateway.ImageOutput{}, errors.New("fixture has no picture backend")
	}
	return b.image(ctx, in)
}

type chaosImageKey struct{}

// chaosHelper is one helper connection and the host's client session.
type chaosHelper struct {
	t        *testing.T
	thread   string
	session  *mcp.ClientSession
	hostIn   io.WriteCloser
	done     chan error
	status   *messagesInboxStatus
	dir      string
	diagCtx  context.Context
	closed   bool
	backend  *chaosBackend
	toServer *io.PipeWriter
}

// shortenAgentTiming runs the production cadences on a short clock.
func shortenAgentTiming(t *testing.T) {
	t.Helper()
	saved := []time.Duration{agentToolAdmissionWait, agentPresenceLease, agentPresenceRenewal, agentPresenceRetryDelay, agentReadmitFirstDelay, agentReadmitMaxDelay, agentPresenceRefusedRetry, codexWakeCooldown, codexObservationRecovery, codexReceiverResumeFirst, codexReceiverResumeMax, codexReceiverPauseDivisor}
	agentToolAdmissionWait = 300 * time.Millisecond
	agentPresenceLease, agentPresenceRenewal, agentPresenceRetryDelay = 600*time.Millisecond, 150*time.Millisecond, 150*time.Millisecond
	agentReadmitFirstDelay, agentReadmitMaxDelay, agentPresenceRefusedRetry = 20*time.Millisecond, 300*time.Millisecond, 600*time.Millisecond
	codexWakeCooldown, codexObservationRecovery = 50*time.Millisecond, 2*time.Second
	codexReceiverResumeFirst, codexReceiverResumeMax, codexReceiverPauseDivisor = 20*time.Millisecond, 300*time.Millisecond, 20
	t.Cleanup(func() {
		agentToolAdmissionWait, saved = saved[0], saved[1:]
		agentPresenceLease, agentPresenceRenewal, agentPresenceRetryDelay = saved[0], saved[1], saved[2]
		agentReadmitFirstDelay, agentReadmitMaxDelay, agentPresenceRefusedRetry = saved[3], saved[4], saved[5]
		codexWakeCooldown, codexObservationRecovery = saved[6], saved[7]
		codexReceiverResumeFirst, codexReceiverResumeMax, codexReceiverPauseDivisor = saved[8], saved[9], saved[10]
	})
}

func startChaosHelper(t *testing.T, runtime *chaosRuntime, queue *chaosQueue, index int, applications ...string) *chaosHelper {
	return startChaosHelperConfigured(t, runtime, queue, index, nil, false, applications...)
}

func startChaosHelperConfigured(t *testing.T, runtime *chaosRuntime, queue *chaosQueue, index int, configure func(context.Context) context.Context, replyOnly bool, applications ...string) *chaosHelper {
	t.Helper()
	application := "oa-codex-integration"
	hostID := ""
	if len(applications) != 0 {
		application = applications[0]
	}
	if len(applications) > 1 {
		hostID = applications[1]
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := privatedir.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	diagnostic := newAgentDiagnostic(testCaller(t), dir, program)
	if diagnostic == nil {
		t.Fatal("lifecycle record unavailable")
	}
	t.Cleanup(diagnostic.close)
	ctx := context.WithValue(context.Background(), agentDiagnosticKey{}, diagnostic)
	if configure != nil {
		ctx = configure(ctx)
	}
	contextName := fmt.Sprintf("native-%032x", index+1)
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	base := new(chaosBackend)
	base.image, _ = ctx.Value(chaosImageKey{}).(func(context.Context, gateway.ImageInput) (gateway.ImageOutput, error))
	backend, err := bindMessagesBackendStatus(base, application, "", contextName, func(context.Context) (nativeMessages, error) { return runtime, nil }, true)
	if err != nil {
		t.Fatal(err)
	}
	backend.(*messagesBackend).status = status
	configureInertChatPresence(backend.(*messagesBackend), runtime)
	fromHost, toServer := io.Pipe()
	fromServer, toHost := io.Pipe()
	h := &chaosHelper{t: t, thread: fmt.Sprintf("%08x-0000-4000-8000-%012x", index+1, index+1), done: make(chan error, 1), status: status, dir: dir, diagCtx: ctx, backend: base, toServer: toServer}
	go func() {
		h.done <- serveAgentConnection(ctx, func(ctx context.Context) {
			superviseAgentPresence(ctx, func(ctx context.Context, ready func(string), readmit bool, stop func()) error {
				return agentPresenceAttempt(ctx, runtime, func(context.Context) nativeMessages { return runtime }, application, contextName, ready, status, readmit, stop)
			}, status, time.After, time.Now)
		}, backend, func(ctx context.Context, b gateway.Backend) error {
			server, err := newGatewayServer(b, false, replyOnly)
			if err != nil {
				return err
			}
			if application == "oa-opencode-integration" {
				return runOpenCodeChatServerID(ctx, server, b.(*messagesBackend), &mcp.IOTransport{Reader: guardStdioLines(fromHost, nil), Writer: toHost, MaxLineLength: agentStdioLineLimit + 1}, hostID)
			}
			return runNativeMCPServerTransport(ctx, server, b, dir, queue.driver(runtime), func(context.Context, func(context.Context, string) ([]byte, error)) (codexReceiverCLI, func() error, error) {
				return chaosCLI{}, func() error { return nil }, nil
			}, &mcp.IOTransport{Reader: guardStdioLines(fromHost, nil), Writer: toHost, MaxLineLength: agentStdioLineLimit + 1})
		})
		_ = toHost.Close()
	}()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "chaos-host", Version: "1"}, nil).Connect(context.Background(), &mcp.IOTransport{Reader: fromServer, Writer: toServer}, nil)
	if err != nil {
		t.Fatal("initialize", err)
	}
	h.session, h.hostIn = session, toServer
	t.Cleanup(h.close)
	return h
}

// call makes one tool call as Codex does, with the thread in its metadata.
// A transport error fails the test: the connection must stay open.
func (h *chaosHelper) call(name string, arguments map[string]any) *mcp.CallToolResult {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	out, err := h.session.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: arguments, Meta: mcp.Meta{"threadId": h.thread}})
	if err != nil {
		h.t.Fatalf("tool %s: the MCP connection failed: %v", name, err)
	}
	select {
	case err := <-h.done:
		h.t.Fatalf("the helper ended while the host kept stdio open: %v", err)
	default:
	}
	return out
}

func (h *chaosHelper) wait() *mcp.CallToolResult {
	return h.call(gateway.ToolMessagesWait, map[string]any{"wait_ms": 0})
}

// The host consumes the exact pointer through the production MCP body Wait.
// Ordinary recovered/manual reads deliberately carry no receipt.
func (h *chaosHelper) consumePointer(queue *chaosQueue) {
	h.t.Helper()
	queue.mu.Lock()
	ticket := queue.tickets[h.thread]
	queue.mu.Unlock()
	if !validCodexWakeTicket(ticket) {
		h.t.Fatal("owned pointer omitted ticket", ticket)
	}
	if out := h.call(gateway.ToolMessagesWait, map[string]any{"wait_ms": 0, "wake_ticket": ticket}); out.IsError {
		h.t.Fatal("owned pointer body consumption refused", out)
	}
}

// consumePointerReceipt proves the exact unknown-queue pointer was read and
// removed durably. The later wake may already own a different ticket.
func (h *chaosHelper) consumePointerReceipt(queue *chaosQueue) time.Time {
	h.t.Helper()
	queue.mu.Lock()
	ticket := queue.tickets[h.thread]
	queue.mu.Unlock()
	outcome, oldHeld := "unread", true
	fail := func(phase string, err error) {
		h.t.Helper()
		total, attempts := queue.snapshot()
		h.t.Fatalf("pointer receipt phase=%s outcome=%s old_held=%t total=%d attempts=%d cause=%.200s", phase, outcome, oldHeld, total, attempts, fmt.Sprint(err))
	}
	if !validCodexWakeTicket(ticket) {
		fail("ticket", errors.New("owned pointer omitted ticket"))
	}
	d := h.diagCtx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	path := filepath.Join(d.root.FileID().Path(), "codex-wake-receipts.json")
	read := func(ctx context.Context) (codexWakeLedger, error) {
		var ledger codexWakeLedger
		raw, err := cas.ReadLimitContext(ctx, path, 64<<10)
		if err == nil {
			err = json.Unmarshal(raw, &ledger)
		}
		return ledger, err
	}
	ctx, cancel := context.WithTimeout(h.t.Context(), 5*time.Second)
	before, err := read(ctx)
	cancel()
	if err != nil {
		fail("before-ledger", err)
	}
	slotKey := fmt.Sprintf("%x", sha256.Sum256([]byte("oa-codex-integration\x00"+h.thread)))
	held, ok := before.Outstanding[slotKey]
	if !ok || held.Ticket != ticket || !held.Codex {
		fail("before-binding", errors.New("queued ticket is not the thread's outstanding Codex pointer"))
	}
	out := h.call(gateway.ToolMessagesWait, map[string]any{"wait_ms": 0, "wake_ticket": ticket})
	deadline := time.Now().Add(5 * time.Second)
	if out.IsError {
		fail("body-call", errors.New("MCP tool error"))
	}
	raw, err := json.Marshal(out.StructuredContent)
	if err != nil {
		fail("body-result", err)
	}
	page, err := msg.DecodePage(raw)
	if err != nil {
		fail("body-result", err)
	}
	outcome = page.Outcome.String()
	if page.Outcome != msg.OutcomePage {
		fail("body-outcome", errors.New("exact-ticket read returned no page"))
	}
	ctx, cancel = context.WithDeadline(h.t.Context(), deadline)
	after, err := read(ctx)
	cancel()
	if err != nil {
		fail("after-ledger", err)
	}
	current, ok := after.Outstanding[slotKey]
	oldHeld = ok && current.Ticket == ticket
	if oldHeld {
		fail("after-binding", errors.New("exact-ticket page left the original pointer outstanding"))
	}
	return deadline
}

// unavailable asserts a typed refusal: the call answered, it did not succeed.
func (h *chaosHelper) refused(out *mcp.CallToolResult) {
	h.t.Helper()
	if !out.IsError {
		return
	}
	text := out.Content[0].(*mcp.TextContent).Text
	if !strings.Contains(text, `"outcome"`) {
		h.t.Fatalf("untyped refusal: %s", text)
	}
}

// recovered polls until a tool call succeeds again and returns how long it
// took.
func (h *chaosHelper) recovered(within time.Duration) time.Duration {
	h.t.Helper()
	start := time.Now()
	for {
		out := h.wait()
		if !out.IsError && len(out.Content) == 1 {
			text := out.Content[0].(*mcp.TextContent).Text
			for _, line := range strings.Split(text, "\n") {
				if strings.HasPrefix(line, "OA_UNTRUSTED_") {
					parts := strings.Split(text, line)
					if len(parts) == 3 {
						page, err := msg.DecodePage([]byte(strings.TrimSpace(parts[1])))
						if err == nil && page.Outcome == msg.OutcomePage {
							return time.Since(start)
						}
					}
					break
				}
			}
		}
		h.refused(out)
		if time.Since(start) > within {
			h.t.Fatalf("no successful tool call within %v; last: %s", within, out.Content[0].(*mcp.TextContent).Text)
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func (h *chaosHelper) close() {
	if h.closed {
		return
	}
	h.closed = true
	_ = h.session.Close()
	select {
	case err := <-h.done:
		if err != nil {
			h.t.Errorf("stdio EOF ended the helper with %v", err)
		}
	case <-time.After(10 * time.Second):
		h.t.Error("the helper did not end after the host closed stdio")
	}
	rows := readAgentDiagnostics(context.Background(), h.dir)
	if len(rows) != 1 || rows[0].Phase != "stdio_end" || rows[0].Cause != "eof" {
		h.t.Errorf("lifecycle end not recorded: %+v", rows)
	}
}

func (h *chaosHelper) record() agentDiagnosticRecord {
	rows := readAgentDiagnostics(context.Background(), h.dir)
	if len(rows) != 1 {
		h.t.Fatalf("lifecycle rows: %+v", rows)
	}
	return rows[0]
}

// woken waits until the receiver has queued want wakes in total.
func woken(t *testing.T, queue *chaosQueue, want int, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		total, _ := queue.snapshot()
		if total == want {
			return
		}
		if total > want {
			t.Fatalf("a wake was queued twice: %d wakes, want %d", total, want)
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued %d wakes within %v, want %d", total, within, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// chaosStart admits one helper and binds its receiver to its thread.
func chaosStart(t *testing.T, runtime *chaosRuntime, queue *chaosQueue, index int) *chaosHelper {
	t.Helper()
	h := startChaosHelper(t, runtime, queue, index)
	h.recovered(5 * time.Second)
	return h
}

func TestChaosRuntimeRestartUnderLiveHelper(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	for round := 1; round <= chaosRounds; round++ {
		runtime.restart(true)
		h.refused(h.wait())
		time.Sleep(time.Duration(round%4) * 200 * time.Millisecond)
		runtime.set(func(r *chaosRuntime) { r.down = false })
		h.recovered(5 * time.Second)
		// The runtime's epoch changed, but it has delivered no new message.
		// Give the receiver several shortened watch/cooldown cadences.
		time.Sleep(200 * time.Millisecond)
		if total, _ := queue.snapshot(); total != round-1 {
			t.Fatalf("runtime restart re-woke pending messages: got %d, want %d", total, round-1)
		}
		runtime.deliver()
		woken(t, queue, round, 5*time.Second)
		h.consumePointer(queue)
	}
	if record := h.record(); record.Losses < chaosRounds || record.Phase != "started" {
		t.Fatalf("losses not recorded as recoverable: %+v", record)
	}
}

func TestChaosSingleForbiddenObservationRecoversAndSuccessResetsConfirmation(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	for round := 1; round <= chaosRounds; round++ {
		runtime.set(func(r *chaosRuntime) { r.headerForbiddenOnce = 1 })
		runtime.deliver()
		woken(t, queue, round, 5*time.Second)
		h.consumePointer(queue)
		runtime.mu.Lock()
		remaining, refusals := runtime.headerForbiddenOnce, runtime.headerForbiddenReads
		runtime.mu.Unlock()
		if remaining != 0 || refusals != round {
			t.Fatalf("round %d: transient forbidden not exercised: remaining=%d refusals=%d", round, remaining, refusals)
		}
		h.recovered(5 * time.Second)
	}
}

func TestChaosTransientHeaderObservationRecoversAndConfirmedForbiddenIsFinal(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	runtime.deliver()
	woken(t, queue, 1, 5*time.Second)
	h.consumePointer(queue)
	runtime.set(func(r *chaosRuntime) { r.headerUnavailable = 1 })
	runtime.deliver()
	woken(t, queue, 2, 5*time.Second)
	h.consumePointer(queue)
	runtime.mu.Lock()
	remaining := runtime.headerUnavailable
	runtime.mu.Unlock()
	if remaining != 0 {
		t.Fatal("transient observation was not exercised")
	}
	runtime.set(func(r *chaosRuntime) { r.headerForbidden = true })
	runtime.deliver()
	until := time.Now().Add(5 * time.Second)
	for {
		record := h.record()
		runtime.mu.Lock()
		confirmed := runtime.headerForbiddenReads >= 2
		runtime.mu.Unlock()
		if confirmed && record.Receiving != nil && record.Receiving.Stage == "stopped" && record.Receiving.Reason == "observation_forbidden" {
			break
		}
		if time.Now().After(until) {
			t.Fatal("affirmative observation refusal did not stop receiver", record)
		}
		time.Sleep(10 * time.Millisecond)
	}
	// The original MCP source remains admitted. The stopped canonical receiver
	// withdraws its presence; a later source tool cannot revive that receiver.
	runtime.set(func(r *chaosRuntime) { r.headerForbidden = false })
	if out := h.call(gateway.ToolModels, map[string]any{}); out.IsError {
		t.Fatal("source connection ended with receiver", out)
	}
	runtime.deliver()
	time.Sleep(300 * time.Millisecond)
	if total, _ := queue.snapshot(); total != 2 {
		t.Fatal("forbidden receiver resumed", total)
	}
}

func TestChaosPipeDropsMidCall(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	for round := 1; round <= chaosRounds; round++ {
		hang := make(chan struct{})
		runtime.set(func(r *chaosRuntime) { r.hang = hang })
		result := make(chan *mcp.CallToolResult, 1)
		go func() { result <- h.wait() }()
		time.Sleep(50 * time.Millisecond)
		runtime.set(func(r *chaosRuntime) { r.hang = nil })
		close(hang) // every call in flight fails as a dropped pipe does
		select {
		case out := <-result:
			h.refused(out)
		case <-time.After(10 * time.Second):
			t.Fatal("the call in flight never answered")
		}
		h.recovered(5 * time.Second)
		runtime.deliver()
		woken(t, queue, round, 5*time.Second)
		h.consumePointer(queue)
	}
}

// The incident of 2026-11-01: renewals fail for longer than the lease, as in
// Modern Standby, so the runtime forgets the lease.
func TestChaosLeaseLapsesWhileAsleep(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	for round := 1; round <= chaosRounds; round++ {
		runtime.set(func(r *chaosRuntime) { r.down = true })
		time.Sleep(agentPresenceLease * 2)
		h.refused(h.wait())
		runtime.set(func(r *chaosRuntime) { r.down = false })
		h.recovered(5 * time.Second)
		runtime.deliver()
		woken(t, queue, round, 5*time.Second)
		h.consumePointer(queue)
	}
	if record := h.record(); record.Readmitted < chaosRounds {
		t.Fatalf("re-admissions not recorded: %+v", record)
	}
}

func TestChaosRuntimeForgetsBinding(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	for round := 1; round <= chaosRounds; round++ {
		runtime.restart(false) // leases and holders gone, runtime up
		h.recovered(5 * time.Second)
		runtime.deliver()
		woken(t, queue, round, 5*time.Second)
		h.consumePointer(queue)
	}
}

func TestChaosCodexQueueFails(t *testing.T) {
	shortenAgentTiming(t)
	for round := 1; round <= chaosRounds; round++ {
		runtime, queue := newChaosRuntime(), new(chaosQueue)
		h := chaosStart(t, runtime, queue, round)
		queue.mu.Lock()
		queue.fail = 1
		queue.mu.Unlock()
		_, before := queue.snapshot()
		runtime.deliver()
		// The failed queue's outcome is unknown: it is never retried.
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, attempts := queue.snapshot(); attempts == before+1 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("the failing queue was never attempted")
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		if _, attempts := queue.snapshot(); attempts != before+1 {
			t.Fatal("a queue with an unknown outcome was retried", attempts, before)
		}
		// A later message waits behind the unknown pointer while ordinary
		// reads recover. If the host delivers that pointer, its exact ticket
		// clears the slot and the higher key wakes once.
		h.recovered(5 * time.Second)
		runtime.deliver()
		time.Sleep(200 * time.Millisecond)
		if _, attempts := queue.snapshot(); attempts != before+1 {
			t.Fatal("ticketless read released the unknown pointer", attempts, before)
		}
		deadline = h.consumePointerReceipt(queue)
		for {
			if _, attempts := queue.snapshot(); attempts == before+2 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("a later message never woke after the held pointer was read")
			}
			time.Sleep(10 * time.Millisecond)
		}
		time.Sleep(200 * time.Millisecond)
		if total, attempts := queue.snapshot(); attempts != before+2 || total != 1 {
			t.Fatal("the later message woke more than once", total, attempts, before)
		}
		h.close()
	}
}

func TestChaosUnknownFieldFromRuntime(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	for round := 1; round <= chaosRounds; round++ {
		runtime.set(func(r *chaosRuntime) { r.decodeFaults = 1 + round%5 })
		for i := 0; i < 3; i++ {
			h.refused(h.wait())
		}
		h.recovered(5 * time.Second)
		runtime.deliver()
		woken(t, queue, round, 5*time.Second)
		h.consumePointer(queue)
	}
}

// Unknown fields and malformed lines from the host never close stdio.
func TestChaosHostSendsUnknownFieldsAndBadLines(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	for round := 1; round <= chaosRounds; round++ {
		// The SDK refuses arguments outside the tool's schema as a tool
		// error; the connection stays open.
		if out := h.call(gateway.ToolMessagesWait, map[string]any{"wait_ms": 0, "unexpected_field": round}); !out.IsError {
			t.Fatal("arguments outside the schema were accepted")
		}
		h.recovered(5 * time.Second)
	}
}

func TestChaosHandlerPanics(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	for round := 1; round <= chaosRounds; round++ {
		h.backend.panics.Store(1)
		out := h.call(gateway.ToolModels, map[string]any{})
		if !out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, `"unknown"`) {
			t.Fatal("a handler panic was not answered as a typed unknown", out)
		}
		if out := h.call(gateway.ToolModels, map[string]any{}); out.IsError {
			t.Fatal("the call after a panic failed", out.Content[0].(*mcp.TextContent).Text)
		}
	}
}

// Seven helpers as Codex spawns them for one desktop session: several
// threads, started at once, all through every restart.
func TestChaosSevenConcurrentHelpers(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	helpers := make([]*chaosHelper, 7)
	var wg sync.WaitGroup
	var mu sync.Mutex
	for i := range helpers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			h := startChaosHelper(t, runtime, queue, i)
			mu.Lock()
			helpers[i] = h
			mu.Unlock()
		}(i)
	}
	wg.Wait()
	// A helper that failed to start ended only its own goroutine; stop here
	// instead of reading its nil entry, which panics the whole package.
	if t.Failed() {
		t.FailNow()
	}
	for _, h := range helpers {
		h.recovered(5 * time.Second)
	}
	for round := 1; round <= chaosRounds; round++ {
		runtime.restart(round%2 == 0)
		time.Sleep(time.Duration(round%3) * 150 * time.Millisecond)
		runtime.set(func(r *chaosRuntime) { r.down = false })
		for _, h := range helpers {
			h.recovered(10 * time.Second)
		}
		runtime.deliver()
		woken(t, queue, round*len(helpers), 10*time.Second)
		for _, h := range helpers {
			h.consumePointer(queue)
		}
	}
}

// A long idle period: the helper renews quietly for many lease lengths and
// answers at once afterwards.
func TestChaosLongIdle(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	for round := 1; round <= chaosRounds; round++ {
		time.Sleep(agentPresenceLease * 5)
		if out := h.wait(); out.IsError {
			t.Fatal("idle connection lost its admission", out.Content[0].(*mcp.TextContent).Text)
		}
		runtime.deliver()
		woken(t, queue, round, 5*time.Second)
		h.consumePointer(queue)
	}
	if record := h.record(); !record.Established || record.Losses != 0 {
		t.Fatalf("idle connection recorded a loss: %+v", record)
	}
}

// A helper started while the runtime is down serves MCP at once and answers
// tools when the runtime arrives.
func TestChaosHelperStartsBeforeRuntime(t *testing.T) {
	shortenAgentTiming(t)
	for round := 1; round <= chaosRounds; round++ {
		runtime, queue := newChaosRuntime(), new(chaosQueue)
		runtime.set(func(r *chaosRuntime) { r.down = true })
		h := startChaosHelper(t, runtime, queue, round)
		out := h.wait()
		if !out.IsError || !strings.Contains(out.Content[0].(*mcp.TextContent).Text, agentToolConnecting) {
			t.Fatal("a helper without a runtime did not say it is connecting", out)
		}
		runtime.set(func(r *chaosRuntime) { r.down = false })
		h.recovered(5 * time.Second)
		h.close()
	}
}

func TestChaosInitialChatAllocationRecoversUnderOriginalCustody(t *testing.T) {
	shortenAgentTiming(t)
	for _, fault := range []string{"unavailable", "unknown", "response-lost", "readmit"} {
		t.Run(fault, func(t *testing.T) {
			runtime, queue := newChaosRuntime(), new(chaosQueue)
			runtime.allocationFault, runtime.allocationFaults = fault, 1
			h := chaosStart(t, runtime, queue, 0)
			runtime.mu.Lock()
			calls := append([]chaosAllocationCall(nil), runtime.allocations...)
			canonical := runtime.allocatedContexts[h.thread]
			runtime.mu.Unlock()
			if len(calls) < 2 || canonical == "" {
				t.Fatal("allocation did not reconcile", calls, canonical)
			}
			for _, call := range calls {
				if call.context != "native-00000000000000000000000000000001" || call.binding.ChatID != h.thread || call.instance != call.binding.SourceInstance {
					t.Fatal("allocation changed source/chat custody or mixed lease instances", calls)
				}
			}
			if fault == "readmit" && calls[0].instance == calls[len(calls)-1].instance {
				t.Fatal("source re-admission was not exercised", calls)
			}
			if total, _ := queue.snapshot(); total != 0 {
				t.Fatal("allocation queued work", total)
			}
			runtime.deliver()
			woken(t, queue, 1, 5*time.Second)
			if row := h.record(); row.Receiving == nil || row.Receiving.Context != canonical {
				t.Fatal("retry changed learned canonical context", row, canonical)
			}
			h.consumePointer(queue)
			h.close()
		})
	}
}

// The raw stdio lines: the server answers a malformed line and a request
// with unknown fields, and keeps reading.
func TestChaosRawStdioLines(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	status := &messagesInboxStatus{admission: new(agentToolAdmission)}
	backend, err := bindMessagesBackendStatus(new(chaosBackend), "oa-codex-integration", "", "native-00000000000000000000000000000077", func(context.Context) (nativeMessages, error) { return runtime, nil }, true)
	if err != nil {
		t.Fatal(err)
	}
	backend.(*messagesBackend).status = status
	fromHost, toServer := net.Pipe()
	t.Cleanup(func() { _ = fromHost.Close(); _ = toServer.Close() })
	fromServer, toHost := io.Pipe()
	done := make(chan error, 1)
	go func() {
		server, _ := newGatewayServer(backend, false)
		done <- runNativeMCPServerTransport(context.Background(), server, backend, t.TempDir(), queue.driver(runtime), func(context.Context, func(context.Context, string) ([]byte, error)) (codexReceiverCLI, func() error, error) {
			return chaosCLI{}, func() error { return nil }, nil
		}, &mcp.IOTransport{Reader: guardStdioLines(fromHost, nil), Writer: toHost, MaxLineLength: agentStdioLineLimit + 1})
		_ = toHost.Close()
	}()
	lines := bufio.NewScanner(fromServer)
	lines.Buffer(make([]byte, 1<<20), 1<<20)
	send := func(line string) {
		t.Helper()
		if err := toServer.SetWriteDeadline(time.Now().Add(5 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(toServer, line+"\n"); err != nil {
			t.Fatal("stdio closed", err)
		}
	}
	answer := func(id int) map[string]any {
		t.Helper()
		for lines.Scan() {
			var reply map[string]any
			if json.Unmarshal(lines.Bytes(), &reply) != nil {
				continue
			}
			if got, ok := reply["id"].(float64); ok && int(got) == id {
				return reply
			}
		}
		t.Fatal("stdio closed before answer", id, lines.Err())
		return nil
	}
	send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-11-25","capabilities":{},"clientInfo":{"name":"raw","version":"1"},"unknownField":true}}`)
	answer(1)
	send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	for round := 1; round <= chaosRounds; round++ {
		send(`{"jsonrpc":"2.0","method":"notifications/unknown"} `)
		send("{\"jsonrpc\":\"2.0\",\"method\":\"notifications/unknown\"}\t")
		send(`{"jsonrpc":"2.0","method":"ping"} {"jsonrpc":"2.0","method":"ping"}`)
		send(`{"jsonrpc":"2.0","method":"ping"}garbage`)
		send(`[{"jsonrpc":"2.0","id":71,"method":"ping"},{"jsonrpc":"2.0","id":72,"method":"ping"}]`)
		send(`this is not json`)
		send(``)
		send(`{"jsonrpc":"2.0","id":"x","method":"tools/list","params":{"unknownField":1}}`)
		send(`{"jsonrpc":"2.0","method":"notifications/unknown","params":{"unknownField":1}}`)
		if round%5 == 0 {
			send(`{"jsonrpc":"2.0","id":"long","method":"tools/list","params":{"pad":"` + strings.Repeat("x", agentStdioLineLimit+10) + `"}}`)
		}
		id := 100 + round
		send(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"oa_models_list","arguments":{},"unknownField":%d,"_meta":{"threadId":"00000000-0000-4000-8000-000000000077"}}}`, id, round))
		if reply := answer(id); reply["result"] == nil && reply["error"] == nil {
			t.Fatal("no answer", reply)
		}
	}
	select {
	case err := <-done:
		t.Fatal("bad lines closed the server", err)
	default:
	}
	// Late answers to the unawaited requests must not block the server's
	// writes while it shuts down: keep reading, as a host does.
	go func() {
		for lines.Scan() {
		}
	}()
	_ = toServer.Close()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not end on EOF")
	}
	// The transport returns after its thread lock is released, so the state
	// folder can be removed at once.
	if _, held := codexHeldThreads.Load("00000000-0000-4000-8000-000000000077"); held {
		t.Fatal("thread lock held after the transport returned")
	}
}

// typedAnswer decodes a tool refusal's outcome and state.
func typedAnswer(t *testing.T, out *mcp.CallToolResult) (string, string) {
	t.Helper()
	if len(out.Content) == 0 {
		t.Fatal("tool answer without content")
	}
	text, _ := out.Content[0].(*mcp.TextContent)
	var answer struct{ Outcome, State string }
	if text == nil || json.Unmarshal([]byte(text.Text), &answer) != nil {
		t.Fatalf("untyped tool answer: %+v", out.Content[0])
	}
	return answer.Outcome, answer.State
}

// Architect review 10, B3. Unadmitted, every tool answers a typed
// unavailable and dispatches nothing. After the person's enrollment refuses
// the inbox, every tool answers refused, nothing is dispatched, presence is
// never retried and a later enrollment does not revive the connection.
func TestChaosUnadmittedAndRefusedConnectionsDispatchNothing(t *testing.T) {
	shortenAgentTiming(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	runtime.set(func(r *chaosRuntime) { r.down = true })
	h := startChaosHelper(t, runtime, queue, 0)
	listed, err := h.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(listed.Tools) != 32 {
		t.Fatalf("the agent composition lists %d tools, want 32 including the private SessionStart tool and oa_image_generate", len(listed.Tools))
	}
	privateSessionStart := false
	for _, tool := range listed.Tools {
		privateSessionStart = privateSessionStart || tool.Name == codexSessionStartTool
	}
	if !privateSessionStart {
		t.Fatal("the dedicated Codex composition omitted its private SessionStart tool")
	}
	callAll := func(wantState string) {
		t.Helper()
		for _, tool := range listed.Tools {
			out := h.call(tool.Name, map[string]any{})
			outcome, state := typedAnswer(t, out)
			if !out.IsError || outcome != "unavailable" || state != wantState {
				t.Fatalf("%s answered %s/%s, want unavailable/%s", tool.Name, outcome, state, wantState)
			}
		}
		if runtime.actions.Load() != 0 || h.backend.models.Load() != 0 {
			t.Fatalf("an unadmitted call dispatched: %d message calls, %d backend calls", runtime.actions.Load(), h.backend.models.Load())
		}
	}
	callAll(agentToolConnecting)

	runtime.set(func(r *chaosRuntime) { r.down = false })
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, _, ok := h.status.admission.begin(context.Background()); ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("never admitted")
		}
		time.Sleep(20 * time.Millisecond)
	}
	runtime.set(func(r *chaosRuntime) { r.forbid = true })
	deadline = time.Now().Add(5 * time.Second)
	for {
		if state, _ := h.status.admission.unavailable(); state == agentToolRefused {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the enrollment refusal was never recorded")
		}
		time.Sleep(20 * time.Millisecond)
	}
	callAll(agentToolRefused)
	announces := runtime.announces.Load()
	time.Sleep(3 * agentPresenceRefusedRetry)
	callAll(agentToolRefused)
	if runtime.announces.Load() != announces {
		t.Fatal("a refused connection kept announcing", runtime.announces.Load()-announces)
	}
	// A later enrollment does not revive this connection.
	runtime.set(func(r *chaosRuntime) { r.forbid = false })
	time.Sleep(3 * agentPresenceRefusedRetry)
	callAll(agentToolRefused)
	if runtime.announces.Load() != announces {
		t.Fatal("a refused connection announced after re-enrollment")
	}
}

// A runtime restart between Announce and InboxOpen makes InboxOpen answer
// forbidden for the unknown instance. That is confirmed by a fresh attempt
// and never becomes a final refusal.
func TestChaosForbiddenUnknownInstanceIsNotARefusal(t *testing.T) {
	shortenAgentTiming(t)
	for round := 1; round <= chaosRounds; round++ {
		status := &messagesInboxStatus{admission: new(agentToolAdmission)}
		ctx, cancel := context.WithCancel(context.Background())
		attempts := 0
		done := make(chan struct{})
		go func() {
			defer close(done)
			superviseAgentPresence(ctx, func(ctx context.Context, ready func(string), _ bool, _ func()) error {
				attempts++
				if attempts == 1 {
					return fmt.Errorf("agent presence initial inbox open: %w", &agentInboxOutcomeError{outcome: msg.OutcomeForbidden})
				}
				ready("fresh")
				status.admission.confirm(time.Now().Add(time.Minute))
				<-ctx.Done()
				return ctx.Err()
			}, status, time.After, time.Now)
		}()
		deadline := time.Now().Add(5 * time.Second)
		for {
			if _, _, ok := status.admission.begin(context.Background()); ok {
				break
			}
			if state, _ := status.admission.unavailable(); state == agentToolRefused || time.Now().After(deadline) {
				t.Fatalf("round %d: one unknown-instance forbidden refused the connection (%s)", round, state)
			}
			time.Sleep(5 * time.Millisecond)
		}
		cancel()
		<-done
	}
}

// A model retries at once (real Codex sent 44 calls within 60 ms of an
// outage). A call made during a short outage waits for re-admission and
// succeeds instead of answering unavailable.
func TestChaosCallDuringShortOutageWaitsAndSucceeds(t *testing.T) {
	shortenAgentTiming(t)
	agentToolAdmissionWait = 3 * time.Second
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := chaosStart(t, runtime, queue, 0)
	for round := 1; round <= chaosRounds; round++ {
		runtime.restart(true)
		time.AfterFunc(200*time.Millisecond, func() { runtime.set(func(r *chaosRuntime) { r.down = false }) })
		if out := h.wait(); out.IsError {
			t.Fatal("a call during a short outage did not wait for re-admission", out.Content[0].(*mcp.TextContent).Text)
		}
	}
}

func (p *chaosRuntime) InboxOpenChat(i, c, s string, claim msg.ChatBinding) (msg.ChatInboxChange, error) {
	if claim.ChatID == "" {
		p.mu.Lock()
		retired := p.retired[i]
		p.mu.Unlock()
		if retired {
			return msg.ChatInboxChange{Outcome: msg.OutcomeEnded}, nil
		}
		out, err := p.InboxOpen(i, c, s)
		return msg.ChatInboxChange{Outcome: out.Outcome, Revision: out.Revision, Holder: out.Holder, OpenedUnixMs: out.OpenedUnixMs}, err
	}
	if err := p.fault(); err != nil {
		return msg.ChatInboxChange{}, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.retired[i] || p.retired[claim.SourceInstance] {
		return msg.ChatInboxChange{Outcome: msg.OutcomeEnded}, nil
	}
	if p.forbid || !p.live(i, time.Now()) || !p.live(claim.SourceInstance, time.Now()) || p.holder[c] != claim.SourceInstance {
		return msg.ChatInboxChange{Outcome: msg.OutcomeForbidden}, nil
	}
	canonical := fixtureNativeChatContext(claim.ChatID)
	if i == claim.SourceInstance {
		if p.allocatedContexts == nil {
			p.allocatedContexts = map[string]string{}
		}
		if prior := p.allocatedContexts[claim.ChatID]; prior != "" {
			canonical = prior
		} else {
			p.allocatedContexts[claim.ChatID] = canonical
		}
		p.allocations = append(p.allocations, chaosAllocationCall{i, c, claim})
		if p.allocationFaults > 0 {
			p.allocationFaults--
			switch p.allocationFault {
			case "unavailable":
				return msg.ChatInboxChange{Outcome: msg.OutcomeUnavailable}, nil
			case "unknown":
				return msg.ChatInboxChange{Outcome: msg.OutcomeUnknown}, nil
			case "response-lost":
				return msg.ChatInboxChange{}, io.EOF
			case "readmit":
				p.instances = map[string]time.Time{}
				p.holder = map[string]string{}
				p.epoch++
				return msg.ChatInboxChange{Outcome: msg.OutcomeUnknown}, nil
			}
		}
		return msg.ChatInboxChange{Outcome: msg.OutcomePage, Context: canonical}, nil
	}
	if p.chatBindings == nil {
		p.chatBindings = map[string]msg.ChatBinding{}
	}
	if p.retired == nil {
		p.retired = map[string]bool{}
	}
	if prior, ok := p.chatBindings[canonical]; ok && p.holder[canonical] != i {
		p.retired[p.holder[canonical]] = true
		if prior.SourceInstance != claim.SourceInstance {
			p.retired[prior.SourceInstance] = true
			for context, holder := range p.holder {
				if holder == prior.SourceInstance {
					delete(p.holder, context)
				}
			}
		}
	}
	p.chatBindings[canonical] = claim
	p.holder[canonical] = i
	if p.lostChatStatus != "" && s == p.lostChatStatus {
		p.lostChatStatus = ""
		return msg.ChatInboxChange{}, io.EOF
	}
	return msg.ChatInboxChange{Outcome: msg.OutcomeOpened, Context: canonical, Revision: strconv.Itoa(p.revision)}, nil
}
