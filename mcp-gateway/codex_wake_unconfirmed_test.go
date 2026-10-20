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

// A queue with an unknown outcome or a long-held pointer reaches the presence
// status the runtime lists, and a confirmed read clears it.
func TestCodexWakeUnconfirmedReachesPresenceStatus(t *testing.T) {
	p := &watchHealthProbe{messagesProbe: &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Revision: "r"}}}
	status := &messagesInboxStatus{confirmed: "Watching"}
	b := &messagesBackend{application: "oa-codex-integration", instance: "own-instance", contextName: "native-11111111111111111111111111111111", status: status, native: func(context.Context) (nativeMessages, error) { return p, nil }}
	ctx := context.Background()
	b.publishWakeBusy(ctx, true)
	b.publishWakeUnconfirmed(ctx, "queue_unknown")
	if got := p.statuses[len(p.statuses)-1]; got != "Watching | queued; Codex did not confirm the wake | receiver:busy" {
		t.Fatal("unconfirmed wake missing from presence status", got)
	}
	b.publishWakeConfirmed(ctx)
	if got := p.statuses[len(p.statuses)-1]; got != "Watching" {
		t.Fatal("a confirmed read kept the claim", got)
	}
	long := strings.Repeat("x", 300)
	if got := messagesStatusSuffix(long, messagesWakeUnconfirmedText); len([]rune(got)) != 200 || !strings.HasSuffix(got, " | "+messagesWakeUnconfirmedText) {
		t.Fatal("status bound", len([]rune(got)))
	}
}

// A long status keeps the unconfirmed-wake marker when the busy suffix is
// added after it.
func TestCodexWakeUnconfirmedMarkerSurvivesLongStatus(t *testing.T) {
	p := &watchHealthProbe{messagesProbe: &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Revision: "r"}}}
	status := &messagesInboxStatus{confirmed: strings.Repeat("w", 190)}
	b := &messagesBackend{application: "oa-codex-integration", instance: "own-instance", contextName: "native-11111111111111111111111111111111", status: status, native: func(context.Context) (nativeMessages, error) { return p, nil }}
	ctx := context.Background()
	b.publishWakeUnconfirmed(ctx, "queue_unknown")
	b.publishWakeBusy(ctx, true)
	got := p.statuses[len(p.statuses)-1]
	if !strings.HasSuffix(got, " | "+messagesWakeUnconfirmedText+" | receiver:busy") || len([]rune(got)) > 200 {
		t.Fatal("marker truncated", len([]rune(got)), got)
	}
}

// A fresh message that keeps waiting behind an unconsumed pointer is reported
// once, after the pointer has been queued for the bound.
func TestCodexReceiverReportsUnconfirmedWakeAfterBound(t *testing.T) {
	b := codexReceiverTestBinding(t)
	var events []string
	b.observe = func(event string) { events = append(events, event) }
	start := time.Now()
	clock := start
	progress := &codexReceiverProgress{submitted: "0001", lastWake: start}
	watch := codexReceiverTestWatch(func(context.Context, gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
		return gateway.MessagesWatchResult{Page: watchwire.Page{Outcome: watchwire.OutcomePage, Revision: "r2", Wake: "0002", PendingUntilUnixMs: start.Add(time.Hour).UnixMilli()}, WatchMetadata: gateway.WatchMetadata{Context: b.context}}, nil
	})
	busy := func(context.Context, string, string, []string) (codexCLIResult, error) {
		return codexCLIResult{}, &codexQueueNotStarted{cause: errCodexWakeBusy}
	}
	stop := errors.New("stop")
	pauses := 0
	err := runCodexInboxReceiverProgress(context.Background(), b, watch, busy, func() time.Time { clock = clock.Add(20 * time.Second); return clock }, func(context.Context, time.Duration) error {
		if pauses++; pauses > 8 {
			return stop
		}
		return nil
	}, progress)
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
	count := map[string]int{}
	firstUnconfirmed := -1
	for i, event := range events {
		count[event]++
		if event == "wake_unconfirmed" && firstUnconfirmed < 0 {
			firstUnconfirmed = i
		}
	}
	if count["wake_busy"] != 1 || count["wake_unconfirmed"] != 1 {
		t.Fatal("busy and unconfirmed each reported once", events)
	}
	// Two busy rechecks fall within the minute after the pointer was queued.
	observed := 0
	for _, event := range events[:firstUnconfirmed] {
		if event == "observed" {
			observed++
		}
	}
	if observed != 3 {
		t.Fatal("unconfirmed reported before the bound", events)
	}
}

func TestMessagesPrepareTellsSenderCodexDidNotConfirmWake(t *testing.T) {
	p := &messagesProbe{prepareResult: wire.PrepareResult{Outcome: wire.OutcomePage, Reason: "wake_unconfirmed", SendRequest: &wire.SendRequest{To: "app:oa-codex-integration", Context: "chat", Kind: wire.KindQuestion, Body: "body", IdempotencyKey: "key", ExpiresUnixMs: 2000, AnswerByUnixMs: 2000}}}
	prepared, err := newerRuntimeBackend(p, "").MessagesPrepare(context.Background(), gateway.MessagesPrepareInput{Receiver: "oa-codex-integration", Body: "body"})
	if err != nil || prepared.Outcome != "prepared" || prepared.Reach != "queued" || prepared.SendArguments == nil || !strings.Contains(prepared.Guidance, "After oa_messages_send answers sent, tell the person: queued; Codex did not confirm the wake.") || strings.Contains(prepared.Guidance, "closed") || !strings.Contains(prepared.Tell, "did not confirm the wake") {
		t.Fatal(prepared, err)
	}
}

func TestMessagesPrepareWakeUnconfirmedTellSentence(t *testing.T) {
	p := &messagesProbe{prepareResult: wire.PrepareResult{Outcome: wire.OutcomePage, Reason: "wake_unconfirmed", SendRequest: &wire.SendRequest{To: "app:oa-codex-integration", Context: "chat", Kind: wire.KindQuestion, Body: "body", IdempotencyKey: "key", ExpiresUnixMs: 2000, AnswerByUnixMs: 2000}}}
	prepared, err := newerRuntimeBackend(p, "").MessagesPrepare(context.Background(), gateway.MessagesPrepareInput{Receiver: "oa-codex-integration", Body: "body"})
	if err != nil {
		t.Fatal(err)
	}
	const want = "Tell the person: queued; Codex did not confirm the wake."
	if !strings.HasSuffix(prepared.Tell, want) {
		t.Fatalf("tell = %q, want suffix %q", prepared.Tell, want)
	}
}
