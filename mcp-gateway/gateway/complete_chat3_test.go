package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	casapi "github.com/openabstractions/abstraction-cas/go/api"
	messageswire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	wire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	acceptance "github.com/openabstractions/abstraction-job/go/abstraction/job/acceptance"
)

type chat3CompleteFixture struct {
	starts                []wire.RequestV3
	cancels               int
	pages                 []wire.ChatPage
	admission             wire.ChatAdmission
	firstAdmission        *wire.ChatAdmission
	startError, waitError error
	epoch                 string
}

func (f *chat3CompleteFixture) GetHistoryWindow(context.Context) (wire.ChatHistoryRead, error) {
	return wire.ChatHistoryRead{Outcome: wire.ChatHistoryOutcomeAvailable, Window: &acceptance.HistoryWindow{LogicalOwner: "owner", HistoryEpoch: f.epoch, MinimumRetentionMs: 60000}}, nil
}
func (f *chat3CompleteFixture) Start(_ context.Context, request wire.RequestV3) (wire.ChatAdmission, error) {
	f.starts = append(f.starts, request)
	if len(f.starts) == 1 && f.firstAdmission != nil {
		return *f.firstAdmission, nil
	}
	return f.admission, f.startError
}
func (f *chat3CompleteFixture) Observe(context.Context, string, int64, int64, int64, int64) (wire.ChatPage, error) {
	if len(f.pages) == 0 {
		return wire.ChatPage{}, f.waitError
	}
	page := f.pages[0]
	f.pages = f.pages[1:]
	return page, nil
}
func (f *chat3CompleteFixture) Cancel(context.Context, string) (wire.ChatCancellation, error) {
	f.cancels++
	return wire.ChatCancellation{}, nil
}

func chat3CompletionOwner(t *testing.T) *OA {
	t.Helper()
	return &OA{stateDir: t.TempDir(), state: casapi.BoundedFileStore{MaxBytes: maxHandleRecordBytes}}
}
func chat3CompletionInput() CompleteInput {
	return CompleteInput{IncludeReasoning: true, RequestKey: "caller-persisted", Model: "model", Prompt: "prompt", Hosting: "local"}
}
func chat3CompletionPage(cursor int64) wire.ChatPage {
	finish := wire.FinishReasonOther
	return wire.ChatPage{Outcome: wire.PageOutcomePage, Next: cursor + 3, AtEnd: true, Events: []wire.ChatEvent{
		{Sequence: cursor, Kind: wire.ChatEventKindPart, Part: &wire.ChatPart{Block: 0, Kind: wire.ChatPartKindReasoning, Text: "private display reasoning"}},
		{Sequence: cursor + 1, Kind: wire.ChatEventKindPart, Part: &wire.ChatPart{Block: 1, Kind: wire.ChatPartKindText, Text: "answer"}},
		{Sequence: cursor + 2, Kind: wire.ChatEventKindEnd, End: &wire.ChatResult{Outcome: wire.ChatResultOutcomeCompleted, FinishReason: &finish}},
	}}
}

func TestChat3CompleteSeparatesReasoningAndPersistsIdentityBeforeStart(t *testing.T) {
	o := chat3CompletionOwner(t)
	f := &chat3CompleteFixture{epoch: "first", admission: wire.ChatAdmission{Outcome: wire.ChatStartOutcomeAccepted, Request: "operation"}, pages: []wire.ChatPage{chat3CompletionPage(0)}}
	out, err := o.completeWithChat3(context.Background(), f, chat3CompletionInput())
	if err != nil || out.Text != "answer" || len(out.Reasoning) != 1 || out.Reasoning[0].Text != "private display reasoning" || out.Cursor != 3 {
		t.Fatalf("completion %+v %v", out, err)
	}
	if len(f.starts) != 1 || f.starts[0].Identity.Key != "caller-persisted" || f.starts[0].Identity.HistoryEpoch != "first" {
		t.Fatalf("identity %+v", f.starts)
	}
	f.pages = []wire.ChatPage{chat3CompletionPage(0)}
	f.epoch = "changed"
	if _, err := o.completeWithChat3(context.Background(), f, chat3CompletionInput()); err != nil || len(f.starts) != 1 {
		t.Fatalf("accepted duplicate started again: %v starts %d", err, len(f.starts))
	}
	changed := chat3CompletionInput()
	changed.Prompt = "another"
	if _, err := o.completeWithChat3(context.Background(), f, changed); err == nil {
		t.Fatal("key reused for changed body")
	}
}

func TestChat3CompleteLostStartDoesNotAcquireNewEpochOrSubmitAgain(t *testing.T) {
	o := chat3CompletionOwner(t)
	f := &chat3CompleteFixture{epoch: "first", startError: errors.New("reply lost")}
	first, err := o.completeWithChat3(context.Background(), f, chat3CompletionInput())
	if err != nil || first.Outcome != "unknown" {
		t.Fatalf("first %+v %v", first, err)
	}
	f.epoch = "after-restart"
	f.startError = nil
	second, err := o.completeWithChat3(context.Background(), f, chat3CompletionInput())
	if err != nil || second.Outcome != "unknown" || len(f.starts) != 1 {
		t.Fatalf("uncertain retry %+v %v starts %d", second, err, len(f.starts))
	}
}

func TestChat3CompleteResumeDeadlineAndGapNeverStartOrCancel(t *testing.T) {
	for _, gap := range []bool{false, true} {
		t.Run(map[bool]string{false: "deadline", true: "gap"}[gap], func(t *testing.T) {
			o := chat3CompletionOwner(t)
			f := &chat3CompleteFixture{waitError: context.DeadlineExceeded}
			if gap {
				f.pages = []wire.ChatPage{{Outcome: wire.PageOutcomeGap, Next: 4}}
			}
			cursor := int64(3)
			out, err := o.completeWithChat3(context.Background(), f, CompleteInput{IncludeReasoning: true, OperationID: "existing", Cursor: &cursor})
			if !gap && (err != nil || out.Outcome != "pending" || out.OperationID != "existing" || out.Cursor != 3) {
				t.Fatalf("deadline %+v %v", out, err)
			}
			if gap && err == nil {
				t.Fatal("gap was presented as complete")
			}
			if len(f.starts) != 0 || f.cancels != 0 {
				t.Fatal("resume started or cancelled work")
			}
		})
	}
}

type chat3ToolFixture struct {
	*fixtureBackend
	calls int
}

func (f *chat3ToolFixture) Complete(context.Context, CompleteInput) (CompleteOutput, error) {
	f.calls++
	return CompleteOutput{State: "ended", Outcome: "completed", Text: "answer", Reasoning: []CompleteReasoning{{Block: 0, Kind: "reasoning", Text: "structured-only"}}}, nil
}

func TestChat3MCPReasoningIsOptInStructuredContentOnly(t *testing.T) {
	b := &chat3ToolFixture{fixtureBackend: &fixtureBackend{principal: "a", ledger: &fixtureLedger{owner: map[string]string{}, state: map[string]string{}}}}
	_, session := connected(t, b, "chat3-test", "2025-11-25")
	for _, enabled := range []bool{false, true} {
		args := map[string]any{"model": "model", "prompt": "prompt", "hosting": "local"}
		if enabled {
			args["include_reasoning"] = true
			args["request_key"] = "key"
		}
		result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolComplete, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("MCP %+v %v", result, err)
		}
		content, _ := json.Marshal(result.Content)
		structured, _ := json.Marshal(result.StructuredContent)
		if strings.Contains(string(content), "structured-only") {
			t.Fatal("reasoning leaked into answer content")
		}
		if strings.Contains(string(structured), "structured-only") != enabled {
			t.Fatalf("reasoning opt-in %v: %s", enabled, structured)
		}
	}
	before := b.calls
	result, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: ToolComplete, Arguments: map[string]any{"model": "m", "prompt": "p", "hosting": "local", "include_reasoning": true}})
	if err == nil && !result.IsError {
		t.Fatal("reasoning Start without persisted key reached backend")
	}
	if b.calls != before {
		t.Fatal("invalid identity dispatched backend")
	}
}

type chatContextBackend struct {
	*fixtureBackend
	context string
}

func (b chatContextBackend) MessagesContext() string { return b.context }
func (chatContextBackend) MessagesInboxOpen(context.Context, MessagesOpenInput) (messageswire.InboxChange, error) {
	return messageswire.InboxChange{}, nil
}
func (chatContextBackend) MessagesInboxesList(context.Context, MessagesListInput) (messageswire.InboxPage, error) {
	return messageswire.InboxPage{}, nil
}
func (chatContextBackend) MessagesSend(context.Context, MessagesSendInput) (messageswire.SendResult, error) {
	return messageswire.SendResult{}, nil
}
func (chatContextBackend) MessagesWait(context.Context, MessagesWaitInput) (messageswire.Page, error) {
	return messageswire.Page{}, nil
}

// A local call that names no conversation runs under this connection's own
// chat; an explicit label, a resume, a hosted call and a reasoning call keep
// what they sent.
func TestCompleteDefaultsConversationToTheConnectionChat(t *testing.T) {
	chat := chatContextBackend{context: "chat-3f2a"}
	local := CompleteInput{Model: "m", Prompt: "p", Hosting: "local"}
	if got := withChatConversation(t.Context(), chat, local); got.Conversation != "chat-3f2a" {
		t.Fatalf("default label %q", got.Conversation)
	}
	cursor := int64(0)
	for _, in := range []CompleteInput{
		{Model: "m", Prompt: "p", Hosting: "local", Conversation: "mine"},
		{OperationID: "op", Cursor: &cursor},
		{Model: "m", Prompt: "p", Hosting: "hosted", Credential: "openrouter"},
		{Model: "m", Prompt: "p", Hosting: "local", IncludeReasoning: true, RequestKey: "k"},
	} {
		if got := withChatConversation(t.Context(), chat, in); got.Conversation != in.Conversation {
			t.Fatalf("%+v became %q", in, got.Conversation)
		}
	}
	if got := withChatConversation(t.Context(), &fixtureBackend{}, local); got.Conversation != "" {
		t.Fatalf("a connection without a chat labelled %q", got.Conversation)
	}
	for _, label := range []string{"", strings.Repeat("a", 129), "line\nbreak", "\x85"} {
		if validConversation(label) {
			t.Fatalf("label %q admitted", label)
		}
	}
}

// The labelled call is one chat@3 Start carrying the label, a fresh request
// key and the local-only route; its resume continues on chat@3.
func TestCompleteLabelledStartsChat3WithItsConversation(t *testing.T) {
	f := &chat3CompleteFixture{epoch: "epoch", admission: wire.ChatAdmission{Outcome: wire.ChatStartOutcomeAccepted, Request: "operation", Model: "qwen"}, pages: []wire.ChatPage{chat3CompletionPage(0)}}
	in := CompleteInput{Model: "m", Prompt: "p", Hosting: "local", MaxOutput: 16, Conversation: "chat-3f2a"}
	out, handled, err := completeLabelledWith(t.Context(), f, in)
	if err != nil || !handled || out.Text != "answer" || out.Model != "qwen" || out.State != "ended" {
		t.Fatalf("labelled completion %+v handled=%v err=%v", out, handled, err)
	}
	f.pages = []wire.ChatPage{chat3CompletionPage(0)}
	if _, _, err := completeLabelledWith(t.Context(), f, in); err != nil {
		t.Fatal(err)
	}
	first, second := f.starts[0], f.starts[1]
	if first.Conversation != "chat-3f2a" || first.Identity.HistoryEpoch != "epoch" || !strings.HasPrefix(first.Identity.Key, "mcp-") ||
		first.Identity.Key == second.Identity.Key || len(first.Content.Guarantees) != 1 || first.Content.Guarantees[0] != wire.ChatGuaranteeLocalOnly ||
		first.Content.Options == nil || first.Content.Options.MaxOutput != 16 {
		t.Fatalf("starts %+v / %+v", first, second)
	}
	f.admission = wire.ChatAdmission{Outcome: wire.ChatStartOutcomeNoHost, Reason: "router:no-host"}
	refused, _, err := completeLabelledWith(t.Context(), f, in)
	if err != nil || refused.Outcome != "no_host" || refused.Message == "" {
		t.Fatalf("refusal %+v %v", refused, err)
	}
	// The first page answers the probe; Observe reads without consuming.
	f.pages = []wire.ChatPage{chat3CompletionPage(3), chat3CompletionPage(3)}
	resumed, ok, err := resumeChat3With(t.Context(), f, "operation", 3)
	if !ok || err != nil || resumed.Cursor != 6 {
		t.Fatalf("resume %+v ok=%v err=%v", resumed, ok, err)
	}
	f.pages = []wire.ChatPage{{Outcome: wire.PageOutcomeUnknown}}
	if _, ok, _ := resumeChat3With(t.Context(), f, "chat2-operation", 0); ok {
		t.Fatal("a chat@2 operation resumed on chat@3")
	}
}

func TestCompleteLabelledWaitsThroughNativeColdActivation(t *testing.T) {
	f := &chat3CompleteFixture{
		epoch:          "epoch",
		firstAdmission: &wire.ChatAdmission{Outcome: wire.ChatStartOutcomeUnavailable, Reason: "activating:fixture"},
		admission:      wire.ChatAdmission{Outcome: wire.ChatStartOutcomeAccepted, Request: "accepted-operation", Model: "qwen"},
		pages:          []wire.ChatPage{chat3CompletionPage(0)},
	}
	ctx := withCallWait(t.Context(), 55*time.Second)
	in := CompleteInput{Model: "m", Prompt: "p", Hosting: "local", MaxOutput: 16, Conversation: "chat-3f2a"}
	out, handled, err := completeLabelledWith(ctx, f, in)
	if err != nil || !handled || out.Outcome != "completed" || out.Text != "answer" || out.OperationID != "accepted-operation" {
		t.Fatalf("completion %+v, handled %v, error %v", out, handled, err)
	}
	if len(f.starts) != 2 || !reflect.DeepEqual(f.starts[0], f.starts[1]) || f.cancels != 0 {
		t.Fatalf("Starts %+v, Cancels %d", f.starts, f.cancels)
	}
}
