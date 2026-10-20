package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestCodexWakePromptPreservesStoredLegacyNotices(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	state, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	d := ctx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	path := filepath.Join(d.root.FileID().Path(), "codex-wake-receipts.json")
	for i, wake := range []string{"0001", "0002"} {
		ticket, err := state.Reserve(ctx, wake)
		if err != nil || ticket == "" {
			t.Fatal(ticket, err)
		}
		prompt, err := state.Pointer(ctx, ticket, "private sender")
		if want := "Check your OA inbox with oa_messages_wait (wake_ticket: " + ticket + ")."; err != nil || prompt != want {
			t.Fatal(prompt, err)
		}
		legacy, err := messagesTicketPointer(ticket, "", i > 0)
		if err != nil {
			t.Fatal(err)
		}
		if err := cas.ChangeLimitContext(ctx, path, 64<<10, func(raw []byte) ([]byte, error) {
			var ledger codexWakeLedger
			if err := json.Unmarshal(raw, &ledger); err != nil {
				return nil, err
			}
			for key, slot := range ledger.Outstanding {
				slot.Prompt = legacy
				slot.UnknownUntil = 0
				ledger.Outstanding[key] = slot
			}
			return json.Marshal(ledger)
		}); err != nil {
			t.Fatal(err)
		}
		if prompt, err := state.Pointer(ctx, ticket, ""); err != nil || prompt != legacy {
			t.Fatal("stored prompt was changed or rejected", prompt, err)
		}
		if _, ok, err := state.ConfirmWake(ctx, ticket); err != nil || !ok {
			t.Fatal("stored prompt could not be confirmed", ok, err)
		}
	}
}

func TestCodexLegacyIssuedWakeExpiresAtStoredDeadline(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	at := time.Unix(1000000, 0)
	state, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := state.Reserve(ctx, "0001")
	if err != nil || ticket == "" {
		t.Fatal(ticket, err)
	}
	legacy, err := messagesTicketPointer(ticket, "", false)
	if err != nil {
		t.Fatal(err)
	}
	d := ctx.Value(agentDiagnosticKey{}).(*agentDiagnostic)
	path := filepath.Join(d.root.FileID().Path(), "codex-wake-receipts.json")
	if err := cas.ChangeLimitContext(ctx, path, 64<<10, func(raw []byte) ([]byte, error) {
		var ledger codexWakeLedger
		if err := json.Unmarshal(raw, &ledger); err != nil {
			return nil, err
		}
		for key, slot := range ledger.Outstanding {
			slot.Prompt, slot.Codex = legacy, true
			ledger.Outstanding[key] = slot
		}
		return json.Marshal(ledger)
	}); err != nil {
		t.Fatal(err)
	}
	at = at.Add(29 * time.Minute)
	if prompt, err := state.Pointer(ctx, ticket, ""); err != nil || prompt != legacy {
		t.Fatal("legacy prompt changed before expiry", prompt, err)
	}
	if next, err := state.Reserve(ctx, "0002"); next != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("legacy pointer released early", next, err)
	}
	at = at.Add(time.Minute)
	if next, err := state.Reserve(ctx, "0001"); next != "" || err != nil {
		t.Fatal("legacy wake replayed", next, err)
	}
	if next, err := state.Reserve(ctx, "0002"); next == "" || next == ticket || err != nil {
		t.Fatal("newer wake did not replace expired legacy pointer", next, err)
	}
	if _, ok, err := state.ConfirmWake(ctx, ticket); err != nil || ok {
		t.Fatal("expired legacy ticket consumed replacement", ok, err)
	}
}

func TestCodexWakeReceiptCapacityPreservesLiveSlotsAndAdmitsLaterChats(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	protected, err := codexWakeReceipt(ctx, "oa-codex-integration", "native-"+strings.Repeat("f", 32), "ffffffff-ffff-4fff-8fff-ffffffffffff")
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := protected.Reserve(ctx, "0001")
	if err != nil || ticket == "" {
		t.Fatal(ticket, err)
	}
	for n := 1; n <= 270; n++ {
		state, err := codexWakeReceipt(ctx, "oa-codex-integration", fmt.Sprintf("native-%032x", n), fmt.Sprintf("%08x-0000-4000-8000-%012x", n, n))
		if err != nil {
			t.Fatal(n, err)
		}
		value, err := state.Reserve(ctx, strings.Repeat("w", 128))
		if err != nil || value == "" {
			t.Fatal("new chat permanently lost wake at capacity", n, value, err)
		}
		if _, confirmed, err := state.ConfirmWake(ctx, value); err != nil || !confirmed {
			t.Fatal(n, confirmed, err)
		}
	}
	if got, err := protected.Load(ctx); err != nil || got != "0001" {
		t.Fatal("live unknown receipt evicted", got, err)
	}
	if value, err := protected.Reserve(ctx, "0002"); value != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("capacity released live slot", value, err)
	}
	if _, confirmed, err := protected.ConfirmWake(ctx, ticket); err != nil || !confirmed {
		t.Fatal("capacity replaced exact live ticket", confirmed, err)
	}
}

func TestCodexWakeReceiptSurvivesReopenAndIsScoped(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	state, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	var wins atomic.Int32
	var joined sync.WaitGroup
	var ticket string
	var mu sync.Mutex
	for n := 0; n < 2; n++ {
		joined.Add(1)
		go func() {
			defer joined.Done()
			value, err := state.Reserve(ctx, "0002")
			if err != nil {
				t.Error(err)
			}
			if value != "" {
				wins.Add(1)
				mu.Lock()
				ticket = value
				mu.Unlock()
			}
		}()
	}
	joined.Wait()
	if wins.Load() != 1 || !validCodexWakeTicket(ticket) {
		t.Fatal("same binding claimed twice", wins.Load(), ticket)
	}
	reopened, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := reopened.Load(ctx); err != nil || got != "0002" {
		t.Fatal("reopen reset high-water", got, err)
	}
	for _, wake := range []string{"", "0001", "0002"} {
		if value, err := reopened.Reserve(ctx, wake); err != nil || value != "" {
			t.Fatal("older key re-woke", wake, value, err)
		}
	}
	if value, err := reopened.Reserve(ctx, "0003"); value != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("greater key released unconsumed pointer", value, err)
	}
	other, err := codexWakeReceipt(ctx, "oa-codex-integration", "native-22222222222222222222222222222222", binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := other.Load(ctx); err != nil || got != "" {
		t.Fatal("native contexts shared high-water", got, err)
	}
	if _, ok, err := other.ConfirmWake(ctx, ticket); err != nil || ok {
		t.Fatal("foreign context cleared slot", ok, err)
	}
	if value, err := other.Reserve(ctx, "0004"); value != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("new context bypassed bound-thread slot", value, err)
	}
	if _, ok, err := reopened.ConfirmWake(ctx, ticket); err != nil || !ok {
		t.Fatal("owned ticket lost across reopen", ok, err)
	}
	if _, ok, err := reopened.ConfirmWake(ctx, ticket); err != nil || ok {
		t.Fatal("ticket replay cleared another slot", ok, err)
	}
	if value, err := other.Reserve(ctx, "0001"); err != nil || value == "" {
		t.Fatal("old binding high-water suppressed another context", value, err)
	}
}

func TestNativeChatReconnectConsumesOldTicketOnlyAfterMatchingBodyRead(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	old, err := codexWakeReceipt(ctx, "oa-codex-integration", "chat-"+strings.Repeat("1", 48), binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := old.Reserve(ctx, "0009")
	if err != nil || ticket == "" {
		t.Fatal(ticket, err)
	}
	fresh, err := codexWakeReceipt(ctx, "oa-codex-integration", "chat-"+strings.Repeat("2", 48), binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := fresh.Reserve(ctx, "0001"); got != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("unknown old submission was cleared", got, err)
	}
	foreign, err := codexWakeReceipt(ctx, "oa-codex-integration", "chat-"+strings.Repeat("3", 48), "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := foreign.ConfirmChatWake(ctx, ticket); ok || err != nil {
		t.Fatal("foreign chat consumed pointer", ok, err)
	}
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomeUnknown}}
	b := &messagesBackend{instance: "current-owner", contextName: "chat-" + strings.Repeat("2", 48), native: func(context.Context) (nativeMessages, error) { return probe, nil }, wakeConfirm: fresh.ConfirmChatWake}
	if _, err := b.MessagesWait(ctx, gateway.MessagesWaitInput{WaitMs: 0, WakeTicket: ticket}); err != nil {
		t.Fatal(err)
	}
	if got, err := fresh.Reserve(ctx, "0001"); got != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("unknown read cleared custody", got, err)
	}
	probe.page.Outcome = wire.OutcomePage
	if _, err := b.MessagesWait(ctx, gateway.MessagesWaitInput{WaitMs: 0, WakeTicket: ticket}); err != nil {
		t.Fatal(err)
	}
	if got, err := fresh.Reserve(ctx, "0001"); got == "" || err != nil {
		t.Fatal("confirmed old pointer blocked fresh owner wake", got, err)
	}
	if _, ok, err := fresh.ConfirmChatWake(ctx, ticket); ok || err != nil {
		t.Fatal("old ticket replay cleared fresh pointer", ok, err)
	}
}

func TestCodexQueueRechecksWakeBeforeHostSubmissionAndKeepsUnknownTicket(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	state, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Wake: "0001"}}
	b := &messagesBackend{application: "oa-codex-integration", instance: "source", contextName: binding.context, native: func(context.Context) (nativeMessages, error) { return probe, nil }}
	queues := 0
	var sent []string
	run := codexReceivingQueue(ctx, b, binding.cli, binding.thread, func(_ context.Context, _ string, _ string, args []string) (codexCLIResult, error) {
		queues++
		sent = append([]string{}, args...)
		return codexCLIResult{}, nil
	}, state)
	if _, err := run(ctx, "inert", "", nil); err != nil || queues != 1 {
		t.Fatal(queues, err)
	}
	if len(sent) != 6 || sent[0] != "queue" || sent[2] != binding.thread || len(wakeTicketInPrompt(sent[4])) != 8 || sent[4] != codexQueuedWakePrompt(wakeTicketInPrompt(sent[4])) {
		t.Fatal("owned fixed pointer/ticket arguments", sent)
	}
	for _, wake := range []string{"", "0001", "0000"} {
		probe.page.Wake = wake
		if _, err := run(ctx, "inert", "", nil); !errors.Is(err, errCodexWakeStale) || queues != 1 {
			t.Fatal("stale pointer reached host", wake, queues, err)
		}
	}
	for n := 2; n <= 10; n++ {
		probe.page.Wake = fmt.Sprintf("%04d", n)
		if _, err := run(ctx, "inert", "", nil); !errors.Is(err, errCodexWakeBusy) || queues != 1 {
			t.Fatal("higher key queued a second outstanding pointer", queues, err)
		}
	}
	unknown, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	run = codexReceivingQueue(ctx, b, binding.cli, binding.thread, func(context.Context, string, string, []string) (codexCLIResult, error) {
		queues++
		return codexCLIResult{}, errors.New("lost acceptance reply")
	}, unknown)
	if _, err := run(ctx, "inert", "", nil); err == nil || queues != 2 {
		t.Fatal(queues, err)
	}
	if got, err := unknown.Load(ctx); err != nil || got != probe.page.Wake {
		t.Fatal("unknown acceptance not retained", got, err)
	}
	if _, err := run(ctx, "inert", "", nil); !errors.Is(err, errCodexWakeStale) || queues != 2 {
		t.Fatal("unknown pointer replayed", queues, err)
	}
	reopened, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, "22222222-2222-4222-8222-222222222222")
	if err != nil {
		t.Fatal(err)
	}
	run = codexReceivingQueue(ctx, b, binding.cli, binding.thread, func(context.Context, string, string, []string) (codexCLIResult, error) {
		queues++
		return codexCLIResult{}, nil
	}, reopened)
	if _, err := run(ctx, "inert", "", nil); !errors.Is(err, errCodexWakeStale) || queues != 2 {
		t.Fatal("helper restart replayed unknown submission", queues, err)
	}
	empty := &codexWakeState{Reserve: func(context.Context, string) (string, error) { return "7f3a9c21", nil }, Pointer: func(context.Context, string, string) (string, error) { return "", nil }}
	run = codexReceivingQueue(ctx, b, binding.cli, binding.thread, func(context.Context, string, string, []string) (codexCLIResult, error) {
		queues++
		return codexCLIResult{}, nil
	}, empty)
	if _, err := run(ctx, "inert", "", nil); err == nil || err.Error() == "" || queues != 2 {
		t.Fatal("empty pointer reached queue", queues, err)
	}
}

func TestCodexQueueRetriesProvenPrelaunchFailure(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	state, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	previous, err := state.Reserve(ctx, "0001")
	if err != nil || previous == "" {
		t.Fatal(previous, err)
	}
	if _, ok, err := state.ConfirmWake(ctx, previous); err != nil || !ok {
		t.Fatal(ok, err)
	}
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Wake: "0002"}}
	b := &messagesBackend{application: "oa-codex-integration", instance: "source", contextName: binding.context, native: func(context.Context) (nativeMessages, error) { return probe, nil }}
	attempts := 0
	var ticket string
	queue := codexReceivingQueue(ctx, b, binding.cli, binding.thread, func(_ context.Context, _ string, _ string, args []string) (codexCLIResult, error) {
		attempts++
		ticket = wakeTicketInPrompt(args[4])
		if attempts == 1 {
			return codexCLIResult{NotStarted: true}, context.DeadlineExceeded
		}
		return codexCLIResult{}, nil
	}, state)
	var notStarted *codexQueueNotStarted
	if _, err := queue(ctx, "inert", "", nil); !errors.As(err, &notStarted) {
		t.Fatal("prelaunch failure was classified as uncertain", err)
	}
	failedTicket := ticket
	if wake, err := state.Load(ctx); err != nil || wake != "0001" {
		t.Fatal("prelaunch failure changed the prior high-water", wake, err)
	}
	if _, err := queue(ctx, "inert", "", nil); err != nil || attempts != 2 || ticket == failedTicket {
		t.Fatal("same wake did not retry with a new ticket", attempts, ticket, err)
	}
	if err := state.UndoUnstarted(ctx, failedTicket, "0002"); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := state.ConfirmWake(ctx, ticket); err != nil || !ok {
		t.Fatal("old rollback disturbed the accepted ticket", ok, err)
	}
}

func TestCodexWakeTicketManualReadAndNativeRefusalDoNotConfirmConsumption(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	state, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	ticket, err := state.Reserve(ctx, "0001")
	if err != nil {
		t.Fatal(err)
	}
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage}}
	b := &messagesBackend{instance: "owned", contextName: binding.context, native: func(context.Context) (nativeMessages, error) { return probe, nil }, wakeConfirm: state.ConfirmWake}
	if page, err := b.MessagesWait(ctx, gateway.MessagesWaitInput{}); err != nil || page.Outcome != wire.OutcomePage {
		t.Fatal(page, err)
	}
	if value, err := state.Reserve(ctx, "0002"); value != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("manual read cleared slot", value, err)
	}
	probe.page.Outcome = wire.OutcomeForbidden
	if page, err := b.MessagesWait(ctx, gateway.MessagesWaitInput{WakeTicket: ticket}); err != nil || page.Outcome != wire.OutcomeForbidden {
		t.Fatal(page, err)
	}
	if value, err := state.Reserve(ctx, "0002"); value != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("ticket granted native access or cleared refusal", value, err)
	}
	probe.page.Outcome = wire.OutcomePage
	if page, err := b.MessagesWait(ctx, gateway.MessagesWaitInput{WakeTicket: strings.Repeat("f", 64)}); err != nil || page.Outcome != wire.OutcomePage {
		t.Fatal(page, err)
	}
	if value, err := state.Reserve(ctx, "0002"); value != "" || !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("foreign ticket cleared slot", value, err)
	}
	if page, err := b.MessagesWait(ctx, gateway.MessagesWaitInput{WakeTicket: ticket, After: "received-cursor"}); err != nil || page.Outcome != wire.OutcomePage {
		t.Fatal(page, err)
	}
	if probe.after != "received-cursor" || probe.view != wire.ViewBody || probe.contextName != binding.context {
		t.Fatal("adapter changed service Wait", probe)
	}
	if value, err := state.Reserve(ctx, "0002"); err != nil || value == "" {
		t.Fatal("exact ticket did not clear slot", value, err)
	}
}

func TestCodexIssuedWakeCoalescesUntilRead(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	at := time.Unix(1000000, 0)
	state, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread, func() time.Time { return at })
	if err != nil {
		t.Fatal(err)
	}
	first, err := state.Reserve(ctx, "0001")
	if err != nil || first == "" {
		t.Fatal(first, err)
	}
	if text, err := state.Pointer(ctx, first, "private sender"); err != nil || text == "" || strings.Contains(text, "private sender") {
		t.Fatal("issued pointer exposes sender or is absent", err)
	}
	if err := state.AcceptQueued(ctx, first); err != nil {
		t.Fatal(err)
	}
	at = at.Add(31 * time.Minute)
	for n := 2; n <= 10; n++ {
		if value, err := state.Reserve(ctx, fmt.Sprintf("%04d", n)); value != "" || !errors.Is(err, errCodexWakeBusy) {
			t.Fatal("busy inbox admitted another pointer", value, err)
		}
	}
	if _, ok, err := state.ConfirmWake(ctx, first); err != nil || !ok {
		t.Fatal("issued pointer could not be consumed", ok, err)
	}
	if value, err := state.Reserve(ctx, "0001"); err != nil || value != "" {
		t.Fatal("consumption repeated stale work", value, err)
	}
	if value, err := state.Reserve(ctx, "0011"); err != nil || value == "" {
		t.Fatal("fresh delivery did not wake after consumption", value, err)
	}
}

func TestCodexUnknownQueueLeaseAllowsOnlyNewerWakeAfterReopen(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	at := time.Unix(1000000, 0)
	reopen := func() *codexWakeState {
		t.Helper()
		state, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread, func() time.Time { return at })
		if err != nil {
			t.Fatal(err)
		}
		return state
	}
	state := reopen()
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Wake: "0001"}}
	b := &messagesBackend{application: "oa-codex-integration", instance: "source", contextName: binding.context, native: func(context.Context) (nativeMessages, error) { return probe, nil }}
	var tickets []string
	driver := func(_ context.Context, _ string, _ string, args []string) (codexCLIResult, error) {
		tickets = append(tickets, wakeTicketInPrompt(args[4]))
		if len(tickets) == 1 {
			return codexCLIResult{Code: 1}, nil
		}
		return codexCLIResult{}, nil
	}
	queue := codexReceivingQueue(ctx, b, binding.cli, binding.thread, driver, state)
	if result, err := queue(ctx, "inert", "", nil); err != nil || result.Code != 1 || len(tickets) != 1 {
		t.Fatal(result, err, tickets)
	}
	state = reopen()
	queue = codexReceivingQueue(ctx, b, binding.cli, binding.thread, driver, state)
	at = at.Add(10*time.Minute - time.Second)
	probe.page.Wake = "0002"
	if _, err := queue(ctx, "inert", "", nil); !errors.Is(err, errCodexWakeBusy) {
		t.Fatal("unknown acceptance released early", err)
	}
	at = at.Add(time.Second)
	probe.page.Wake = "0001"
	if _, err := queue(ctx, "inert", "", nil); !errors.Is(err, errCodexWakeStale) {
		t.Fatal("unknown wake replayed after expiry", err)
	}
	probe.page.Wake = "0002"
	if _, err := queue(ctx, "inert", "", nil); err != nil || len(tickets) != 2 || tickets[0] == tickets[1] {
		t.Fatal("newer wake did not replace expired unknown item", err, tickets)
	}
	if _, ok, err := state.ConfirmWake(ctx, tickets[0]); err != nil || ok {
		t.Fatal("expired ticket consumed replacement", ok, err)
	}
	at = at.Add(time.Hour)
	state = reopen()
	queue = codexReceivingQueue(ctx, b, binding.cli, binding.thread, driver, state)
	probe.page.Wake = "0003"
	if _, err := queue(ctx, "inert", "", nil); !errors.Is(err, errCodexWakeBusy) || len(tickets) != 2 {
		t.Fatal("accepted replacement lost its outstanding slot", err, tickets)
	}
}

type delayedPointerMessages struct {
	*messagesProbe
	message         wire.Message
	wake, cursor    string
	received, acked bool
}

func (p *delayedPointerMessages) Wait(instance, contextName string, view wire.View, after, revision string, waitMs int64) (wire.Page, error) {
	p.messagesProbe.Wait(instance, contextName, view, after, revision, waitMs)
	empty := wire.Page{Outcome: wire.OutcomePage, Messages: []wire.Message{}, Headers: []wire.Header{}}
	if view == wire.ViewHeaders {
		empty.Revision = "observed-" + p.wake
		if !p.acked {
			empty.Wake = p.wake
			empty.ReplyUntilUnixMs = p.message.Header.ExpiresUnixMs
			empty.Headers = []wire.Header{p.message.Header}
		}
		return empty, nil
	}
	if after != "" {
		if after != p.cursor || !p.received {
			empty.Outcome = wire.OutcomeGap
			return empty, nil
		}
		p.acked = true
	}
	if p.acked {
		return empty, nil
	}
	p.received = true
	empty.Cursor = p.cursor
	empty.Messages = []wire.Message{p.message}
	return empty, nil
}

func TestCodexDelayedPointersAfterBodyAckDoNotResubmit(t *testing.T) {
	for _, tc := range []struct {
		name                   string
		submissions, uncertain int
	}{
		{name: "accepted", submissions: 1},
		{name: "uncertain and read before expiry", submissions: 1, uncertain: 1},
		{name: "older uncertain pointers retained by host", submissions: 3, uncertain: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, ctx, _ := diagnosticFixture(t)
			binding := codexReceiverTestBinding(t)
			binding.context = "chat-" + strings.Repeat("3", 48)
			at := time.Unix(1000000, 0)
			state, err := codexWakeReceiptWithRandom(ctx, "oa-codex-integration", binding.context, binding.thread, bytes.NewReader([]byte{0, 0, 0, 1, 0, 0, 0, 2, 0, 0, 0, 3}), func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			probe := &delayedPointerMessages{messagesProbe: new(messagesProbe)}
			backend := &messagesBackend{application: "oa-codex-integration", instance: "owned-source", contextName: binding.context, native: func(context.Context) (nativeMessages, error) {
				return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: probe}), nil
			}}
			backend.useWakeState(state)
			var retained []string
			queue := codexReceivingQueue(ctx, backend, binding.cli, binding.thread, func(_ context.Context, _ string, _ string, args []string) (codexCLIResult, error) {
				if len(args) != 6 || args[0] != "queue" || args[2] != binding.thread {
					t.Fatal("unexpected inert host queue request", args)
				}
				retained = append(retained, args[4])
				if len(retained) <= tc.uncertain {
					// Desktop kept the item, but the caller cannot infer that
					// from a queue command that returned a warning.
					return codexCLIResult{Diagnostic: []byte("inert queue warning")}, nil
				}
				return codexCLIResult{}, nil
			}, state)
			assertQuiet := func() {
				t.Helper()
				before := len(retained)
				if _, err := queue(ctx, "inert", "", nil); !errors.Is(err, errCodexWakeStale) || len(retained) != before {
					t.Fatal("empty or previously submitted work queued another host pointer", len(retained), before, err)
				}
			}
			assertBusy := func() {
				t.Helper()
				before := len(retained)
				if _, err := queue(ctx, "inert", "", nil); !errors.Is(err, errCodexWakeBusy) || len(retained) != before {
					t.Fatal("newer work queued while the prior slot was retained", len(retained), before, err)
				}
			}
			assertNewerBusy := func() {
				t.Helper()
				wake, acked := probe.wake, probe.acked
				probe.wake, probe.acked = "9999", false
				assertBusy()
				probe.wake, probe.acked = wake, acked
			}
			for n := 1; n <= tc.submissions; n++ {
				prior := probe.wake
				next := fmt.Sprintf("%04d", n)
				probe.wake, probe.cursor = next, fmt.Sprintf("received-page-%d", n)
				probe.received, probe.acked = false, false
				probe.message = wire.Message{Header: wire.Header{MessageID: fmt.Sprintf("delayed-message-%d", n), From: "app:peer", FromContext: "peer", To: "app:oa-codex-integration", Context: binding.context, ContextID: "inert-thread", Kind: wire.KindReply, ExpiresUnixMs: at.Add(4 * time.Hour).UnixMilli()}, IdempotencyKey: fmt.Sprintf("accepted-%d", n), Body: "inert reply body"}
				if n > 1 {
					at = at.Add(codexUnknownQueueLease - time.Millisecond)
					assertBusy()
					at = at.Add(time.Millisecond)
					probe.wake = prior
					assertQuiet()
					probe.wake = next
				}
				if result, err := queue(ctx, "inert", "", nil); err != nil || len(retained) != n || (len(result.Diagnostic) != 0) != (n <= tc.uncertain) {
					t.Fatal("eligible incoming work was not submitted exactly once", n, retained, result, err)
				}
				body, err := backend.MessagesWait(ctx, gateway.MessagesWaitInput{WaitMs: 0})
				if err != nil || body.Outcome != wire.OutcomePage || len(body.Messages) != 1 || body.Messages[0].Header.MessageID != probe.message.Header.MessageID || body.Cursor == "" {
					t.Fatal("manual body receive", body, err)
				}
				ack, err := backend.MessagesWait(ctx, gateway.MessagesWaitInput{WaitMs: 0, After: body.Cursor})
				if err != nil || ack.Outcome != wire.OutcomePage || len(ack.Messages) != 0 || !probe.acked {
					t.Fatal("explicit received-page ACK", ack, err)
				}
				assertQuiet()
				assertNewerBusy()
			}
			if tc.uncertain < tc.submissions {
				at = at.Add(2 * codexWakeLease)
				assertNewerBusy()
			}
			for n, prompt := range retained {
				ticket := wakeTicketInPrompt(prompt)
				if len(ticket) != 8 || prompt != codexQueuedWakePrompt(ticket) {
					t.Fatal("pointer did not carry a complete short ticket", prompt)
				}
				page, err := backend.MessagesWait(ctx, gateway.MessagesWaitInput{WaitMs: 0, WakeTicket: ticket})
				if err != nil || page.Outcome != wire.OutcomePage || page.Cursor != "" || len(page.Messages) != 0 || len(page.Headers) != 0 {
					t.Fatal("delayed pointer did not receive an empty page after earlier ACK", page, err)
				}
				if n+1 < tc.submissions {
					assertNewerBusy()
				}
				assertQuiet()
			}
			if high, err := state.Load(ctx); err != nil || high != probe.wake || len(retained) != tc.submissions {
				t.Fatal("delayed empty reads changed high-water or resubmitted work", high, retained, err)
			}
			last := wakeTicketInPrompt(retained[len(retained)-1])
			if _, confirmed, err := state.ConfirmChatWake(ctx, last); err != nil || confirmed {
				t.Fatal("the fixture's matching live empty body read did not consume its ticket", confirmed, err)
			}
		})
	}
}

func TestCodexQueuedWakeRequiresMatchingTicket(t *testing.T) {
	for _, scenario := range []struct {
		name  string
		match bool
		want  int
	}{
		{name: "ticketless empty reads", want: 1},
		{name: "matching empty reads", match: true, want: 10},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, ctx, _ := diagnosticFixture(t)
			binding := codexReceiverTestBinding(t)
			binding.context = "chat-" + strings.Repeat("c", 48)
			at := time.Unix(1000000, 0)
			state, err := codexWakeReceipt(ctx, "oa-codex-integration", binding.context, binding.thread, func() time.Time { return at })
			if err != nil {
				t.Fatal(err)
			}
			probe := &delayedPointerMessages{messagesProbe: new(messagesProbe)}
			backend := &messagesBackend{application: "oa-codex-integration", instance: "owned-source", contextName: binding.context, native: func(context.Context) (nativeMessages, error) {
				return wire.NewMessagesClient(&wire.MessagesDispatcher{Handler: probe}), nil
			}}
			backend.useWakeState(state)
			var accepted []string
			queue := codexReceivingQueue(ctx, backend, binding.cli, binding.thread, func(_ context.Context, _ string, _ string, args []string) (codexCLIResult, error) {
				accepted = append(accepted, wakeTicketInPrompt(args[4]))
				return codexCLIResult{}, nil
			}, state)
			for n := 1; n <= 10; n++ {
				at = at.Add(codexWakeCooldown)
				probe.wake = fmt.Sprintf("%04d", n)
				probe.acked = false
				probe.message.Header = wire.Header{MessageID: fmt.Sprintf("note-%d", n), From: "app:peer", FromContext: "peer", To: "app:oa-codex-integration", Context: binding.context, ContextID: "inert-thread", Kind: wire.KindNote, ExpiresUnixMs: at.Add(time.Hour).UnixMilli()}
				if _, err := queue(ctx, "inert", "", nil); err != nil && !errors.Is(err, errCodexWakeBusy) {
					t.Fatalf("queue note %d: %v", n, err)
				}

				// The mailbox no longer has a body to return. The independent
				// host queue retains its item until the host delivers it.
				probe.acked = true
				read := gateway.MessagesWaitInput{}
				if scenario.match {
					if len(accepted) != n || len(accepted[n-1]) != 8 {
						t.Fatalf("expected matching host item for note %d, got %v", n, accepted)
					}
					read.WakeTicket = accepted[n-1]
				}
				page, err := backend.MessagesWait(ctx, read)
				if err != nil || page.Outcome != wire.OutcomePage || len(page.Messages) != 0 {
					t.Fatalf("expected an empty body page, got %+v, %v", page, err)
				}
			}
			if len(accepted) != scenario.want {
				t.Fatalf("host accepted %d enqueue calls after 10 notes and empty reads, want %d", len(accepted), scenario.want)
			}
		})
	}
}
