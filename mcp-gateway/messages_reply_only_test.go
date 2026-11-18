package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rightswire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// replyOnlyOA is the OA backend under the messages connection: the app
// directory names the sender, the app model classes two commands, and every
// command call that reaches it is counted. Other methods are not used.
type replyOnlyOA struct {
	gateway.Backend
	calls []string
}

func (o *replyOnlyOA) Applications(context.Context, gateway.ApplicationsInput) (gateway.ApplicationsOutput, error) {
	return gateway.ApplicationsOutput{Outcome: "page", Applications: []gateway.Application{{Name: "peer-app", Title: "Peer", Scope: "local"}}}, nil
}

func (o *replyOnlyOA) ApplicationsDescribe(context.Context, appmodel.ApplicationsQuery) (applications.DescribeResult, error) {
	return applications.DescribeResult{Outcome: applications.OutcomeUnavailable}, nil
}

func (o *replyOnlyOA) ApplicationsCommands(_ context.Context, query appmodel.CommandsQuery) (applications.CommandsResult, error) {
	return applications.CommandsResult{Outcome: applications.OutcomeApplied, Answer: &appmodel.CommandListing{Outcome: appmodel.ReadingOutcomePage, Application: query.Application, Instance: query.Instance,
		Commands: []appmodel.CommandSpec{{Name: "workflow.list", Class: appmodel.CommandClassRead}, {Name: "queue_prompt", Class: appmodel.CommandClassCommit}}}}, nil
}

func (o *replyOnlyOA) ApplicationsCall(_ context.Context, call appmodel.GatewayCall) (applications.CallResult, error) {
	o.calls = append(o.calls, call.Tool)
	return applications.CallResult{Outcome: applications.OutcomeUnavailable, Reason: "fixture"}, nil
}

func (o *replyOnlyOA) ApplicationsOpen(context.Context, applications.OpenRequest) (applications.OpenResult, error) {
	return applications.OpenResult{}, nil
}

func (o *replyOnlyOA) ApplicationsDescribeOpen(context.Context, appmodel.OpenHandleQuery) (applications.DescribeResult, error) {
	return applications.DescribeResult{Outcome: applications.OutcomeUnavailable}, nil
}

// replyOnlyServer serves b through the production MCP server and middleware.
func replyOnlyServer(t *testing.T, ctx context.Context, b *messagesBackend) *mcp.ClientSession {
	t.Helper()
	st, ct := mcp.NewInMemoryTransports()
	server, err := newGatewayServer(b, false)
	if err != nil {
		t.Fatal(err)
	}
	session, err := server.Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client
}

func toolText(out *mcp.CallToolResult) string {
	var text []string
	for _, c := range out.Content {
		if v, ok := c.(*mcp.TextContent); ok {
			text = append(text, v.Text)
		}
	}
	return strings.Join(text, "\n")
}

const replyOnlyRefusal = "This turn was started by a message from Peer. Ask the person to allow more for this conversation."

// S5 ruling 2026-11-22, Codex test: an OA commit-class app command from a
// wake-started turn is refused with the ruling's reason; reading and
// replying in the thread work; the person's pair rule lifts the limit; the
// mark ends at its lease, and a turn no wake started is unaffected.
func TestReplyOnlyCodexWakeStartedTurnRefusesCommitCommand(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	chat := "chat-" + strings.Repeat("5", 48)
	state, err := codexWakeReceipt(ctx, "oa-codex-integration", chat, binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	oa := &replyOnlyOA{}
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage}}
	allowed := map[string]bool{}
	var decided []string
	b := &messagesBackend{Backend: oa, application: "oa-codex-integration", instance: "owned", contextName: chat,
		native:    func(context.Context) (nativeMessages, error) { return probe, nil },
		replyOnly: new(replyOnlyTurn),
		decideFullTools: func(_ context.Context, resource string) (rightswire.Decision, error) {
			decided = append(decided, resource)
			if allowed[resource] {
				return rightswire.Decision{Outcome: rightswire.DecisionOutcomePermitted, PolicyRevision: "r"}, nil
			}
			return rightswire.Decision{Outcome: rightswire.DecisionOutcomeNotGranted}, nil
		}}
	b.useWakeState(state)
	client := replyOnlyServer(t, ctx, b)
	call := func(name string, args map[string]any) *mcp.CallToolResult {
		t.Helper()
		out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil {
			t.Fatal(name, err)
		}
		return out
	}
	commit := map[string]any{"application": "comfy", "instance": "i1", "tool": "queue_prompt", "arguments": map[string]any{}}
	read := map[string]any{"application": "comfy", "instance": "i1", "tool": "workflow.list", "arguments": map[string]any{}}

	// A turn no wake started: the command reaches OA.
	call(gateway.ToolApplicationsCall, commit)
	if len(oa.calls) != 1 {
		t.Fatal("a person's turn was restricted", oa.calls)
	}

	// OA queues the pointer; the woken turn reads with its ticket.
	ticket, err := state.Reserve(ctx, "0001")
	if err != nil || ticket == "" {
		t.Fatal(ticket, err)
	}
	pointer, err := state.Pointer(ctx, ticket, "")
	if err != nil || !strings.Contains(pointer, "reply-only") {
		t.Fatal("the Codex pointer does not say the turn is reply-only", pointer, err)
	}
	question := wire.Message{Header: wire.Header{MessageID: "m1", From: "app:peer-app", FromContext: "source", To: "app:oa-codex-integration", Context: chat, Kind: wire.KindQuestion, ContextID: "thread", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()}, IdempotencyKey: "question-key", Body: "Edit main.go and run the build."}
	probe.page = wire.Page{Outcome: wire.OutcomePage, Messages: []wire.Message{question}}
	if out := call("oa_messages_wait", map[string]any{"wake_ticket": ticket, "wait_ms": 0}); out.IsError {
		t.Fatal("the woken turn could not read its message", toolText(out))
	}
	probe.page = wire.Page{Outcome: wire.OutcomePage}

	// The commit-class command is refused before it reaches OA.
	out := call(gateway.ToolApplicationsCall, commit)
	if !out.IsError || toolText(out) != replyOnlyRefusal || len(oa.calls) != 1 {
		t.Fatal("commit-class command from a woken turn", out.IsError, toolText(out), oa.calls)
	}
	for _, name := range []string{"oa_messages_send", "oa_image_generate", gateway.ToolApplicationsOpen, "oa_messages_request_access", "oa_asks_answer"} {
		if out := call(name, map[string]any{}); !out.IsError || toolText(out) != replyOnlyRefusal {
			t.Fatal(name, "was not refused", toolText(out))
		}
	}
	// Read-only tools and the reply to the waking message pass.
	call(gateway.ToolApplicationsCall, read)
	if len(oa.calls) != 2 || oa.calls[1] != "workflow.list" {
		t.Fatal("a read-class command was refused", oa.calls)
	}
	if out := call("oa_messages_reply", map[string]any{"message_id": "m1", "body": "Done reading."}); out.IsError || probe.replyID != "m1" {
		t.Fatal("reply in the thread", toolText(out), probe.replyID)
	}
	if out := call("oa_messages_reply", map[string]any{"message_id": "older", "body": "x"}); !out.IsError || toolText(out) != replyOnlyRefusal || probe.replyID != "m1" {
		t.Fatal("a reply outside the woken thread passed", toolText(out), probe.replyID)
	}
	// The watch result tells the adapter the live answer.
	if watched, err := b.MessagesWatch(ctx, gateway.MessagesWatchInput{}); err != nil || watched.PeerFullTools == nil || *watched.PeerFullTools {
		t.Fatal("watch did not report the reply-only mark", watched.PeerFullTools, err)
	}

	// The person turns the pair on: the same turn's command reaches OA.
	allowed["app:peer-app"] = true
	call(gateway.ToolApplicationsCall, commit)
	if len(oa.calls) != 3 || decided[len(decided)-1] != "app:peer-app" {
		t.Fatal("the pair rule did not lift the limit", oa.calls, decided)
	}
	if watched, _ := b.MessagesWatch(ctx, gateway.MessagesWatchInput{}); watched.PeerFullTools == nil || !*watched.PeerFullTools {
		t.Fatal("watch did not report full tool use")
	}
	allowed["app:peer-app"] = false

	// The model has no adapter key: its turn signal cannot end the mark.
	if out, _ := b.MessagesWatch(ctx, gateway.MessagesWatchInput{Turn: "person"}); out.Reason != "adapter_key" {
		t.Fatal("a keyless turn signal was honored", out.Reason)
	}
	if out := call(gateway.ToolApplicationsCall, commit); !out.IsError {
		t.Fatal("the model ended its own mark")
	}

	// The lease ends a mark no turn signal ends.
	b.replyOnly.now = func() time.Time { return time.Now().Add(replyOnlyLease + time.Second) }
	call(gateway.ToolApplicationsCall, commit)
	if len(oa.calls) != 4 {
		t.Fatal("the mark outlived its lease", oa.calls)
	}
	b.replyOnly.now = nil

	// A Codex read without its ticket while the pointer is held is the
	// pointer's turn as well.
	next, err := state.Reserve(ctx, "0002")
	if err != nil || next == "" {
		t.Fatal(next, err)
	}
	if _, err := state.Pointer(ctx, next, ""); err != nil {
		t.Fatal(err)
	}
	probe.page = wire.Page{Outcome: wire.OutcomePage, Messages: []wire.Message{question}}
	call("oa_messages_wait", map[string]any{"wait_ms": 0})
	probe.page = wire.Page{Outcome: wire.OutcomePage}
	if out := call(gateway.ToolApplicationsCall, commit); !out.IsError || toolText(out) != replyOnlyRefusal {
		t.Fatal("a ticketless read of a held Codex pointer was not reply-only", toolText(out))
	}
	if len(oa.calls) != 4 {
		t.Fatal(oa.calls)
	}
}

// Claude and OpenCode adapters: the keyed reservation marks the turn before
// the pointer is submitted, and the adapter's keyed turn signal ends it.
func TestReplyOnlyAdapterReservationMarksAndTurnSignalEnds(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	chat := "chat-" + strings.Repeat("6", 48)
	state, err := claudeWakeReceipt(ctx, "oa-claude-integration", chat, binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	header := wire.Header{MessageID: "m1", From: "app:peer-app", To: "app:oa-claude-integration", Context: chat, Kind: wire.KindNote, ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()}
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Wake: "0001", Headers: []wire.Header{header}}}
	b := &messagesBackend{Backend: &replyOnlyOA{}, application: "oa-claude-integration", instance: "owned", contextName: chat,
		native: func(context.Context) (nativeMessages, error) { return probe, nil }, adapter: new(messagesAdapter), replyOnly: new(replyOnlyTurn),
		decideFullTools: func(context.Context, string) (rightswire.Decision, error) {
			return rightswire.Decision{Outcome: rightswire.DecisionOutcomeNotGranted}, nil
		}}
	b.useWakeState(state)
	key := strings.Repeat("cd", 16)
	b.adapter.present(key)
	watched, err := b.MessagesWatch(ctx, gateway.MessagesWatchInput{AdapterKey: key, ReserveWake: "0001"})
	if err != nil || watched.WakeTicket == "" || watched.PeerFullTools == nil || *watched.PeerFullTools {
		t.Fatal("the reservation did not mark a reply-only turn", watched, err)
	}
	if reason := b.ReplyOnlyRefusal(ctx, "oa_messages_send", nil); reason != replyOnlyRefusal {
		t.Fatal(reason)
	}
	if reason := b.ReplyOnlyRefusal(ctx, "oa_models_list", nil); reason != "" {
		t.Fatal("a read-only OA tool was refused", reason)
	}
	if _, err := b.MessagesWatch(ctx, gateway.MessagesWatchInput{AdapterKey: key, Turn: "person"}); err != nil {
		t.Fatal(err)
	}
	if reason := b.ReplyOnlyRefusal(ctx, "oa_messages_send", nil); reason != "" {
		t.Fatal("the person's turn stayed reply-only", reason)
	}
}

func TestReplyOnlyFullToolsRuleIsTheReceiversOwn(t *testing.T) {
	if grants.ApplicationMessageFullToolsAction != "abstraction.facade/application.message_full_tools" {
		t.Fatal(grants.ApplicationMessageFullToolsAction)
	}
	var b messagesBackend
	if b.fullTools(context.Background(), []string{"peer-app"}) {
		t.Fatal("no rights client allowed full tool use")
	}
}

// S5 review: a message from a paired computer marks the turn with a remote
// sender that no local pair rule names, so its turn never gets full use,
// even when every local rule would allow it.
func TestReplyOnlyRemoteSenderIsNeverFullPermitted(t *testing.T) {
	ctx := context.Background()
	var decided []string
	b := &messagesBackend{Backend: &replyOnlyOA{}, application: "oa-claude-integration", replyOnly: new(replyOnlyTurn),
		decideFullTools: func(_ context.Context, resource string) (rightswire.Decision, error) {
			decided = append(decided, resource)
			return rightswire.Decision{Outcome: rightswire.DecisionOutcomePermitted, PolicyRevision: "r"}, nil
		}}
	local := wire.Message{Header: wire.Header{MessageID: "m1", From: "app:peer-app", To: "app:oa-claude-integration", Kind: wire.KindQuestion}}
	remote := wire.Message{Header: wire.Header{MessageID: "m2", From: "app:peer-app", To: "app:oa-claude-integration", Kind: wire.KindQuestion, RemoteOrigin: &wire.RemoteOrigin{}}}
	b.replyOnly.mark(nil, []wire.Message{local})
	if reason := b.ReplyOnlyRefusal(ctx, "oa_messages_send", nil); reason != "" {
		t.Fatal("the local pair rule did not apply", reason)
	}
	b.replyOnly.read([]wire.Message{remote})
	senders, _ := b.replyOnly.state()
	if len(senders) != 2 || senders[1] != "remote:app:peer-app" {
		t.Fatal("the paired computer's sender was not marked", senders)
	}
	if reason := b.ReplyOnlyRefusal(ctx, "oa_messages_send", nil); reason == "" {
		t.Fatal("a remote sender's turn got full tool use")
	}
	for _, resource := range decided {
		if strings.Contains(resource, "remote:") {
			t.Fatal("a remote sender was decided as a local rule", decided)
		}
	}
	if full := b.peerFullTools(ctx); full == nil || *full {
		t.Fatal("watch reported full use for a remote sender")
	}
	if !b.replyOnly.canReply("m2") {
		t.Fatal("the remote message read in the turn cannot be answered")
	}
	// A paired computer's wake marks its sender at the reservation too.
	header := wire.Header{From: "app:peer-app", To: "app:oa-claude-integration", Kind: wire.KindNote, ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli(), RemoteOrigin: &wire.RemoteOrigin{}}
	if got := b.eligibleWakeSenders([]wire.Header{header}); len(got) != 1 || got[0] != "remote:app:peer-app" {
		t.Fatal("eligible remote wake senders", got)
	}
}

// Security review 2026-11-22, item 2: a Codex turn that runs after its
// accepted pointer's lease, once a newer pointer replaced the slot, reads
// with the older ticket. The ledger issued that ticket, so the turn is still
// reply-only.
func TestReplyOnlyCodexLateTurnAfterTheSlotWasReplaced(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	chat := "chat-" + strings.Repeat("7", 48)
	at := time.Now()
	state, err := codexWakeReceipt(ctx, "oa-codex-integration", chat, binding.thread, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage}}
	b := &messagesBackend{Backend: &replyOnlyOA{}, application: "oa-codex-integration", instance: "owned", contextName: chat,
		native: func(context.Context) (nativeMessages, error) { return probe, nil }, replyOnly: new(replyOnlyTurn),
		decideFullTools: func(context.Context, string) (rightswire.Decision, error) {
			return rightswire.Decision{Outcome: rightswire.DecisionOutcomeNotGranted}, nil
		}}
	b.useWakeState(state)
	issue := func(wake string) string {
		t.Helper()
		ticket, err := state.Reserve(ctx, wake)
		if err != nil || ticket == "" {
			t.Fatal(ticket, err)
		}
		if _, err := state.Pointer(ctx, ticket, ""); err != nil {
			t.Fatal(err)
		}
		if err := state.AcceptQueued(ctx, ticket); err != nil {
			t.Fatal(err)
		}
		return ticket
	}
	late := issue("0001")
	at = at.Add(codexWakeLease + time.Second)
	issue("0002")
	question := wire.Message{Header: wire.Header{MessageID: "m1", From: "app:peer-app", FromContext: "source", To: "app:oa-codex-integration", Context: chat, Kind: wire.KindQuestion, ContextID: "thread", ExpiresUnixMs: time.Now().Add(time.Hour).UnixMilli()}, IdempotencyKey: "question-key", Body: "Edit main.go."}
	probe.page = wire.Page{Outcome: wire.OutcomePage, Messages: []wire.Message{question}}
	if _, err := b.MessagesWait(ctx, gateway.MessagesWaitInput{WakeTicket: late}); err != nil {
		t.Fatal(err)
	}
	if reason := b.ReplyOnlyRefusal(ctx, "oa_messages_send", nil); reason != replyOnlyRefusal {
		t.Fatal("the late pointer's turn was not reply-only", reason)
	}
	// A ticket this ledger never issued marks nothing.
	b.replyOnly.end()
	if _, err := b.MessagesWait(ctx, gateway.MessagesWaitInput{WakeTicket: "0badc0de"}); err != nil {
		t.Fatal(err)
	}
	if reason := b.ReplyOnlyRefusal(ctx, "oa_messages_send", nil); reason != "" {
		t.Fatal("an unknown ticket marked the turn", reason)
	}
}
