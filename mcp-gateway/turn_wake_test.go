package main

import (
	"context"
	"slices"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// W5 (AB-1s ruling 2026-11-09): a ticketed read calls TurnWake with the key
// the ticket reserved; a ticketless read whose pointer was never issued calls
// nothing; a ticketless read of an issued pointer stamps its key; a turn
// signal calls it with an empty key; anything else calls nothing.
func TestGatewayTurnWakeCalls(t *testing.T) {
	_, ctx, _ := diagnosticFixture(t)
	binding := codexReceiverTestBinding(t)
	chat := "chat-" + strings.Repeat("7", 48)
	state, err := claudeWakeReceipt(ctx, "oa-claude-integration", chat, binding.thread)
	if err != nil {
		t.Fatal(err)
	}
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage}}
	b := &messagesBackend{application: "oa-claude-integration", instance: "owned", contextName: chat, native: func(context.Context) (nativeMessages, error) { return probe, nil }, wakeConfirm: state.ConfirmChatWake, wakeReserve: func(ctx context.Context, wake, ticket string) (string, error) {
		if ticket != "" {
			return state.Refresh(ctx, ticket, wake)
		}
		return state.Reserve(ctx, wake)
	}, wakePointer: state.Pointer, adapter: new(messagesAdapter)}
	key := strings.Repeat("ab", 16)
	if b.adapter.present(key) != "accepted" {
		t.Fatal("adapter key not registered")
	}
	read := func(ticket string) {
		t.Helper()
		if page, err := b.MessagesWait(ctx, gateway.MessagesWaitInput{WakeTicket: ticket}); err != nil || page.Outcome != wire.OutcomePage {
			t.Fatal(page, err)
		}
	}

	// A ticketless read with nothing held calls nothing.
	read("")
	if len(probe.turnWakes) != 0 {
		t.Fatal("an unwoken read stated a wake", probe.turnWakes)
	}

	// The adapter reserves through the watch tool and receives the pointer.
	probe.page = wire.Page{Outcome: wire.OutcomePage, Wake: "0001"}
	watched, err := b.MessagesWatch(ctx, gateway.MessagesWatchInput{AdapterKey: key, ReserveWake: "0001"})
	if err != nil || watched.WakeTicket == "" || watched.WakePointer == "" {
		t.Fatal(watched, err)
	}
	probe.page = wire.Page{Outcome: wire.OutcomePage}
	read(watched.WakeTicket)
	if !slices.Equal(probe.turnWakes, []string{"0001"}) || probe.contextName != chat || probe.instance != "owned" {
		t.Fatal("ticketed read did not state its reserved key", probe.turnWakes, probe.instance, probe.contextName)
	}
	// Reading again in the same turn keeps the chain: nothing more is called.
	read("")
	read(watched.WakeTicket)
	if len(probe.turnWakes) != 1 {
		t.Fatal("a later read in the woken turn changed its chain", probe.turnWakes)
	}

	// A reservation whose pointer was never issued sets nothing, though the
	// ticketless read still consumes it.
	if ticket, err := state.Reserve(ctx, "0002"); err != nil || ticket == "" {
		t.Fatal(ticket, err)
	}
	read("")
	if len(probe.turnWakes) != 1 {
		t.Fatal("an unissued pointer stated a wake", probe.turnWakes)
	}
	if ticket, err := state.Reserve(ctx, "0003"); err != nil || ticket == "" {
		t.Fatal("ticketless read left the unissued reservation held", ticket, err)
	} else if _, err := state.Pointer(ctx, ticket, "Claude"); err != nil {
		t.Fatal(err)
	}
	// A ticketless read of an issued pointer is the woken turn's read.
	read("")
	if !slices.Equal(probe.turnWakes, []string{"0001", "0003"}) {
		t.Fatal("ticketless read of an issued pointer", probe.turnWakes)
	}

	// Turn signals end the chain with an empty key; a bad one calls nothing.
	for _, turn := range []string{"ended", "person"} {
		if out, err := b.MessagesWatch(ctx, gateway.MessagesWatchInput{AdapterKey: key, Turn: turn}); err != nil || out.Outcome.String() != "page" {
			t.Fatal(turn, out, err)
		}
	}
	if out, err := b.MessagesWatch(ctx, gateway.MessagesWatchInput{AdapterKey: key, Turn: "maybe"}); err != nil || out.Outcome.String() != "invalid" {
		t.Fatal(out, err)
	}
	if !slices.Equal(probe.turnWakes, []string{"0001", "0003", "", ""}) {
		t.Fatal("turn signals", probe.turnWakes)
	}
}
