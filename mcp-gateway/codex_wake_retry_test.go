package main

import (
	"context"
	"errors"
	watchwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// A note-only page: no pending question, the note alone sets the wake.
func codexNotePage(context, revision, wake string, until time.Time) gateway.MessagesWatchResult {
	unread, questions := int64(1), int64(0)
	return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: revision, Wake: wake, PendingUntilUnixMs: until.UnixMilli(), Unread: &unread, PendingQuestions: &questions}, WatchMetadata: gateway.WatchMetadata{Context: context}}
}

func TestCodexReceiverNoteOnlyWakeQueuesOnce(t *testing.T) {
	b := codexReceiverTestBinding(t)
	at := time.Unix(1000, 0)
	reads, queues := 0, 0
	stop := errors.New("finished")
	watch := codexReceiverTestWatch(func(context.Context, gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
		reads++
		return codexNotePage(b.context, "note", "0001", at.Add(10*time.Minute)), nil
	})
	err := runCodexInboxReceiver(context.Background(), b, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
		queues++
		return codexCLIResult{}, nil
	}, func() time.Time { return at }, func(context.Context, time.Duration) error {
		at = at.Add(time.Minute)
		if reads == 4 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || queues != 1 {
		t.Fatal("note-only wake must queue exactly once", err, queues)
	}
}

func TestCodexReceiverNoteDuringCooldownWakesOnceItClears(t *testing.T) {
	b := codexReceiverTestBinding(t)
	at := time.Unix(1000, 0)
	// The receiver queued wake 0001 a moment ago; a note then raises 0002.
	progress := &codexReceiverProgress{revision: "question", submitted: "0001", lastWake: at}
	reads, queues := 0, 0
	var queuedAt time.Time
	stop := errors.New("finished")
	watch := codexReceiverTestWatch(func(_ context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
		reads++
		if queues == 0 && reads > 1 && in.WaitMs != 0 {
			t.Fatal("a cooled-down note waited on a long poll", reads, in.WaitMs)
		}
		return codexNotePage(b.context, "note", "0002", at.Add(10*time.Minute)), nil
	})
	err := runCodexInboxReceiverProgress(context.Background(), b, watch, func(context.Context, string, string, []string) (codexCLIResult, error) {
		queues++
		queuedAt = at
		return codexCLIResult{}, nil
	}, func() time.Time { return at }, func(context.Context, time.Duration) error {
		at = at.Add(time.Second)
		if reads == 12 {
			return stop
		}
		return nil
	}, progress)
	if !errors.Is(err, stop) || queues != 1 {
		t.Fatal("note during cooldown must wake exactly once", err, queues)
	}
	if waited := queuedAt.Sub(time.Unix(1000, 0)); waited < codexWakeCooldown || waited > codexWakeCooldown+2*time.Second {
		t.Fatal("note wake not delivered as cooldown cleared", waited)
	}
	if progress.submitted != "0002" {
		t.Fatal("queued note not recorded", progress.submitted)
	}
}

func TestCodexReceiverBusyNoteWakesAfterMatchingRead(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	binding.context = "chat-" + strings.Repeat("c", 48)
	state, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	// The earlier question's pointer is outstanding and never confirmed.
	ticket, err := state.Reserve(ctx, "0001")
	if err != nil || ticket == "" {
		t.Fatal(ticket, err)
	}
	if _, err := state.Pointer(ctx, ticket, ""); err != nil {
		t.Fatal(err)
	}
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Wake: "0002"}}
	chat := &messagesBackend{application: "oa-codex-integration", instance: "owned", contextName: binding.context, native: func(context.Context) (nativeMessages, error) { return probe, nil }, wakeConfirm: state.ConfirmChatWake}
	queues := 0
	var pointer string
	run := codexReceivingQueue(ctx, chat, binding.cli, binding.thread, func(_ context.Context, _ string, _ string, args []string) (codexCLIResult, error) {
		queues++
		pointer = args[4]
		return codexCLIResult{}, nil
	}, state)
	progress := &codexReceiverProgress{load: state.Load}
	at := time.Unix(1000, 0)
	reads, busy := 0, 0
	read := false
	binding.observe = func(event string) {
		if event == "wake_busy" {
			busy++
		}
	}
	stop := errors.New("finished")
	watch := codexReceiverTestWatch(func(_ context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
		reads++
		if reads > 1 && queues == 0 && in.WaitMs != codexWakeBusyRecheck.Milliseconds() {
			t.Fatal("busy wake waits beyond its recheck cadence", in.WaitMs)
		}
		return codexNotePage(binding.context, "note", "0002", at.Add(10*time.Minute)), nil
	})
	err = runCodexInboxReceiverProgress(ctx, binding, watch, run, func() time.Time { return at }, func(context.Context, time.Duration) error {
		at = at.Add(time.Second)
		if reads == 3 && !read {
			read = true
			if page, err := chat.MessagesWait(ctx, gateway.MessagesWaitInput{WakeTicket: ticket}); err != nil || page.Outcome != wire.OutcomePage {
				t.Fatal(page, err)
			}
		}
		if reads == 8 {
			return stop
		}
		return nil
	}, progress)
	if !errors.Is(err, stop) || queues != 1 || busy != 1 {
		t.Fatal("busy note must wake once after the held pointer is read", err, queues, busy)
	}
	if ticket := wakeTicketInPrompt(pointer); len(ticket) != 8 {
		t.Fatal("note wake queued without a fresh ticket", pointer)
	}
	if got, err := state.Load(ctx); err != nil || got != "0002" {
		t.Fatal("note wake not recorded", got, err)
	}
}

func TestCodexWakeMatchingReadReleasesOnlyCurrentChatPointer(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	chat, err := codexWakeReceipt(ctx, "oa-codex-integration", "chat-"+strings.Repeat("d", 48), binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := chat.Reserve(ctx, "0001")
	if err != nil || ticket == "" {
		t.Fatal(ticket, err)
	}
	// Another thread's chat holds nothing on this thread and releases nothing.
	foreign, err := codexWakeReceipt(ctx, "oa-codex-integration", "chat-"+strings.Repeat("e", 48), "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := foreign.ConfirmChatWake(ctx, ticket); ok || err != nil {
		t.Fatal("foreign thread released pointer", ok, err)
	}
	// A non-chat owner keeps exact-ticket custody.
	if _, ok, err := chat.ConfirmWake(ctx, ""); ok || err != nil {
		t.Fatal("ticketless non-chat confirm released pointer", ok, err)
	}
	if value, err := chat.Reserve(ctx, "0002"); value != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("pointer released early", value, err)
	}
	if _, ok, err := chat.ConfirmChatWake(ctx, ""); ok || err != nil {
		t.Fatal("ticketless chat read released pointer", ok, err)
	}
	if _, ok, err := chat.ConfirmChatWake(ctx, ticket); !ok || err != nil {
		t.Fatal("matching chat read kept pointer", ok, err)
	}
	if _, ok, err := chat.ConfirmChatWake(ctx, ticket); ok || err != nil {
		t.Fatal("matching read confirmed again", ok, err)
	}
	if value, err := chat.Reserve(ctx, "0001"); value != "" || err != nil {
		t.Fatal("released pointer re-woke the same wake", value, err)
	}
	if value, err := chat.Reserve(ctx, "0002"); value == "" || err != nil {
		t.Fatal("later wake still refused", value, err)
	}
}
