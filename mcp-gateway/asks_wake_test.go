package main

import (
	"context"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	waitwire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/responderwait"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// asksWakeResponder is the service's answerable view: it already holds only
// routine questions about other programs and shared choice options.
type asksWakeResponder struct {
	questions []asks.RecordMetadata
	choices   []asks.AnswerableChoice
	lists     int
	partial   bool
}

func (r *asksWakeResponder) RequestAnsweringContext(context.Context) (rights.Decision, error) {
	return rights.Decision{}, nil
}
func (r *asksWakeResponder) ListAnswerableContext(context.Context, string, int64) (asks.OperatorPage, error) {
	r.lists++
	return asks.OperatorPage{Outcome: asks.OperatorPageOutcomePage, Records: r.questions, Complete: !r.partial}, nil
}
func (r *asksWakeResponder) ListChoicesContext(context.Context, string, int64) (asks.AnswerableChoicePage, error) {
	return asks.AnswerableChoicePage{Outcome: asks.OperatorPageOutcomePage, Choices: r.choices, Complete: true}, nil
}
func (r *asksWakeResponder) AnswerContext(context.Context, string, string) (asks.ResponderDecision, error) {
	return asks.ResponderDecision{}, nil
}
func (r *asksWakeResponder) AnswerChoiceContext(context.Context, string, string) (asks.ChoiceDecision, error) {
	return asks.ChoiceDecision{}, nil
}

func TestAsksWakeKeyAdvancesOnlyForNewOccurrences(t *testing.T) {
	var s asksWakeState
	at := time.UnixMilli(1762950000000)
	now := func() time.Time { return at }
	key := func(ids []string) string { _, newest := s.keys(ids, true, now); return newest }
	if got := key(nil); got != "" {
		t.Fatal("nothing answerable produced a key", got)
	}
	first := key([]string{"question:a"})
	if first == "" || key([]string{"question:a"}) != first {
		t.Fatal("the same occurrence moved the key", first)
	}
	oldest, second := s.keys([]string{"question:a", "choice:b", "choice:b"}, true, now)
	if oldest != first || second <= first {
		t.Fatal("a new occurrence did not advance the key", first, second)
	}
	if oldest, got := s.keys([]string{"choice:b"}, true, now); oldest != second || got != second {
		t.Fatal("an answered occurrence moved the key", second, got)
	}
	if oldest, third := s.keys([]string{"question:c"}, true, now); oldest != third || oldest <= second {
		t.Fatal("fast refill did not retire previous coverage", oldest, second, third)
	}
	// A restarted connection observes later; its keys compare greater.
	var restarted asksWakeState
	at = at.Add(time.Second)
	if _, got := restarted.keys([]string{"question:c"}, true, now); got <= second || len(got) > 128 {
		t.Fatal("a restarted connection's key is not greater", got)
	}
}

func TestMessagesWatchAsksReadsKeepTheCallerDeadline(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	deadline, _ := ctx.Deadline()
	reads := 0
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Revision: "r1"}}
	responder := &asksWakeResponder{questions: []asks.RecordMetadata{{ID: "routine"}}}
	b := &messagesBackend{application: "oa-claude-integration", instance: "owned", contextName: "chat-" + strings.Repeat("5", 48),
		native: func(context.Context) (nativeMessages, error) { return probe, nil }, adapter: new(messagesAdapter),
		asksAccess: func(read context.Context) (string, error) {
			reads++
			if got, ok := read.Deadline(); !ok || !got.Equal(deadline) {
				t.Errorf("Asks read replaced the caller deadline: got %v, want %v", got, deadline)
			}
			return "permitted", read.Err()
		},
		responder: func(context.Context) (gateway.AsksResponder, error) { return responder, nil }}
	out, err := b.MessagesWatch(ctx, gateway.MessagesWatchInput{WaitMs: 25000})
	if err != nil || out.AsksWake == "" || reads != 3 || probe.waitMs != asksWakeWaitMs {
		t.Fatal("caller-owned enrollment and final observation", out, reads, probe.waitMs, err)
	}
}

func TestMessagesWatchCarriesAsksWakeOnlyForAnEnrolledAnswerer(t *testing.T) {
	probe := &messagesProbe{page: wire.Page{Outcome: wire.OutcomePage, Revision: "r1"}}
	responder := &asksWakeResponder{}
	access := "permitted"
	waiting := waitwire.WaitingPage{Outcome: waitwire.OutcomePage, Complete: true}
	b := &messagesBackend{application: "oa-claude-integration", instance: "owned", contextName: "chat-" + strings.Repeat("5", 48),
		native:      func(context.Context) (nativeMessages, error) { return probe, nil },
		adapter:     new(messagesAdapter),
		asksAccess:  func(context.Context) (string, error) { return access, nil },
		responder:   func(context.Context) (gateway.AsksResponder, error) { return responder, nil },
		waitingAsks: func(context.Context) (waitwire.WaitingPage, error) { return waiting, nil }}
	watch := func(in gateway.MessagesWatchInput) gateway.MessagesWatchResult {
		t.Helper()
		out, err := b.MessagesWatch(t.Context(), in)
		if err != nil || out.Outcome.String() != "page" {
			t.Fatal(out, err)
		}
		return out
	}
	if out := watch(gateway.MessagesWatchInput{WaitMs: 25000}); out.AsksWake != "" || probe.waitMs != asksWakeWaitMs {
		t.Fatal("an enrolled answerer's watch", out, probe.waitMs)
	}
	responder.questions = []asks.RecordMetadata{{ID: "routine-1", Text: "Allow App A to use the local model host?"}}
	first := watch(gateway.MessagesWatchInput{WaitMs: 25000})
	if first.AsksWake == "" || first.OldestAsksWake != first.AsksWake || strings.Contains(first.AsksWake, "routine") || strings.Contains(first.AsksWake, "App A") {
		t.Fatal("answerable question gave no content-free wake", first.AsksWake)
	}
	if again := watch(gateway.MessagesWatchInput{WaitMs: 0}); again.AsksWake != first.AsksWake {
		t.Fatal("the same question moved the wake", first.AsksWake, again.AsksWake)
	}
	if first.AnswerableAsks == nil || *first.AnswerableAsks != 1 || first.WaitingAsks == nil || *first.WaitingAsks != 0 {
		t.Fatal("ordinary question counts", first.WatchMetadata)
	}
	waiting.IDs = []string{"unlisted", "routine-1", "routine-1"}
	urgent := watch(gateway.MessagesWatchInput{})
	if urgent.WaitingAsks == nil || *urgent.WaitingAsks != 1 || urgent.WaitingAsksWake == "" || urgent.AsksWake != first.AsksWake {
		t.Fatal("exact waiting subset", urgent.WatchMetadata)
	}
	waiting = waitwire.WaitingPage{Outcome: waitwire.OutcomeGap}
	if unknown := watch(gateway.MessagesWatchInput{}); unknown.WaitingAsks != nil || unknown.WaitingAsksWake != "" || unknown.AsksWake != first.AsksWake {
		t.Fatal("unavailable waiting service changed ordinary wake", unknown.WatchMetadata)
	}
	responder.partial = true
	responder.questions = []asks.RecordMetadata{{ID: "routine-2"}}
	partial := watch(gateway.MessagesWatchInput{})
	if partial.AnswerableAsks == nil || *partial.AnswerableAsks != 1 || partial.OldestAsksWake != "" || partial.AsksWake <= first.AsksWake {
		t.Fatal("partial scan claimed exact coverage", partial.WatchMetadata)
	}
	responder.partial = false
	responder.questions = []asks.RecordMetadata{{ID: "routine-1"}}
	if after := watch(gateway.MessagesWatchInput{}); after.AsksWake != first.AsksWake || after.OldestAsksWake != first.OldestAsksWake {
		t.Fatal("partial scan changed exact ordinals", after.WatchMetadata)
	}
	access = "unavailable"
	if unavailable := watch(gateway.MessagesWatchInput{}); unavailable.AnswerableAsks != nil {
		t.Fatal("uncertain enrollment cleared coverage", unavailable.WatchMetadata)
	}
	access = "permitted"
	// Adapter signals and reservations carry no Asks observation.
	key := strings.Repeat("cd", 16)
	b.adapter.present(key)
	lists := responder.lists
	for _, in := range []gateway.MessagesWatchInput{{Turn: "ended", AdapterKey: key}, {Health: "watching"}} {
		if out := watch(in); out.AsksWake != "" || out.AnswerableAsks != nil || out.WaitingAsks != nil {
			t.Fatal("a signal carried the Asks wake", in, out.AsksWake)
		}
	}
	if responder.lists != lists {
		t.Fatal("a signal listed answerable Asks")
	}
	responder.questions = nil
	if out := watch(gateway.MessagesWatchInput{}); out.AsksWake != "" {
		t.Fatal("an answered question kept its wake", out.AsksWake)
	}
	// Without a current enrollment nothing is listed and the held wait stays.
	access = "not_granted"
	responder.questions = []asks.RecordMetadata{{ID: "routine-2"}}
	lists = responder.lists
	if out := watch(gateway.MessagesWatchInput{WaitMs: 25000}); out.AsksWake != "" || out.AnswerableAsks == nil || *out.AnswerableAsks != 0 || responder.lists != lists || probe.waitMs != 25000 {
		t.Fatal("an unenrolled program observed Asks", out.AsksWake, probe.waitMs)
	}
}
