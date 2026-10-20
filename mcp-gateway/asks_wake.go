package main

import (
	"context"
	"fmt"
	"log"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	waitwire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/responderwait"
	asksclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/client"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// asksWakeWaitMs bounds one held Messages watch while this connection's
// program is an enrolled answerer. A new answerable Asks occurrence then
// reaches the chat's wake path within about 15 seconds (ASK-R14).
const asksWakeWaitMs = 15000
const asksWakeReadTimeout = time.Second
const maxTrackedAsksWakeIDs = 1024

// asksWakeState keeps the answerable Asks occurrences one connection last
// listed. A key combines the connection's first observation time and an
// occurrence introduction ordinal so later introductions sort byte by byte.
type asksWakeState struct {
	epoch int64
	count int64
	seen  map[string]int64
}

// keys applies one observation under the connection's Asks read lock.
// A partial page adds known IDs without retiring any absent ID. Only a
// complete page can prove whether prior coverage is still present.
func (s *asksWakeState) keys(ids []string, complete bool, now func() time.Time) (oldest, newest string) {
	if s.epoch == 0 {
		s.epoch = now().UnixMilli()
	}
	current := make(map[string]int64, len(ids))
	var first, last int64
	for _, id := range ids {
		ordinal := current[id]
		if ordinal == 0 {
			ordinal = s.seen[id]
		}
		if ordinal == 0 {
			s.count++
			ordinal = s.count
		}
		current[id] = ordinal
		if first == 0 || ordinal < first {
			first = ordinal
		}
		last = max(last, ordinal)
	}
	if complete {
		s.seen = current
	} else {
		if s.seen == nil {
			s.seen = make(map[string]int64)
		}
		for id, ordinal := range current {
			if s.seen[id] != 0 || len(s.seen) < maxTrackedAsksWakeIDs {
				s.seen[id] = ordinal
			}
		}
	}
	if len(current) == 0 {
		return "", ""
	}
	if complete {
		oldest = fmt.Sprintf("%013d.%010d", s.epoch, first)
	}
	return oldest, fmt.Sprintf("%013d.%010d", s.epoch, last)
}

// MessagesWatch adds the Asks wake key (ASK-R14) to the chat's own headers
// observation. Only the adapter's plain observation carries it; turn signals,
// health claims and wake reservations stay Messages-only.
func (b *messagesBackend) MessagesWatch(ctx context.Context, in gateway.MessagesWatchInput) (gateway.MessagesWatchResult, error) {
	observeAsks := in.Turn == "" && in.Health == "" && in.ReserveWake == ""
	if observeAsks && in.WaitMs > asksWakeWaitMs {
		read, cancel := context.WithTimeout(ctx, asksWakeReadTimeout)
		answerer := b.asksEnrollment(read) == rights.DecisionOutcomePermitted.String()
		cancel()
		if answerer {
			in.WaitMs = asksWakeWaitMs
		}
	}
	out, err := b.messagesWatch(ctx, in)
	if err == nil && observeAsks && out.Outcome.String() == "page" {
		b.readAsksWake(ctx, &out.WatchMetadata)
	}
	return out, err
}

// asksEnrollment returns the native decision word or an unknown result.
func (b *messagesBackend) asksEnrollment(ctx context.Context) string {
	if b.asksAccess == nil || b.responder == nil {
		return ""
	}
	access, err := b.asksAccess(ctx)
	if err != nil {
		log.Printf("asks wake: enrollment unread: %v", err)
		return ""
	}
	if ctx.Err() != nil {
		return ""
	}
	return access
}

// readAsksWake lists what the service lets this program answer now:
// routine first-use questions about other programs (ASK-R5) and shared
// options of ordinary choices (ASK-R9). Only IDs enter the key.
func (b *messagesBackend) readAsksWake(ctx context.Context, out *gateway.WatchMetadata) {
	// Concurrent Watch calls may complete Messages in a different order.
	// Only one owns the subsequent Asks snapshot and state update at a time.
	if !b.asksObservation.TryLock() {
		return
	}
	defer b.asksObservation.Unlock()
	ctx, cancel := context.WithTimeout(ctx, asksWakeReadTimeout)
	defer cancel()
	if !b.admitAsksWakeRead(ctx, out) {
		return
	}
	responder, err := b.AsksResponder(ctx)
	if err != nil {
		log.Printf("asks wake: responder unavailable: %v", err)
		return
	}
	questions, err := responder.ListAnswerableContext(ctx, "", 64)
	if err != nil || questions.Outcome != asks.OperatorPageOutcomePage {
		log.Printf("asks wake: questions unread: %v %v", questions.Outcome, err)
		return
	}
	choices, err := responder.ListChoicesContext(ctx, "", 64)
	if err != nil || choices.Outcome != asks.OperatorPageOutcomePage {
		log.Printf("asks wake: choices unread: %v %v", choices.Outcome, err)
		return
	}
	complete := questions.Complete && choices.Complete
	ids := make([]string, 0, len(questions.Records)+len(choices.Choices))
	questionIDs := make(map[string]struct{}, len(questions.Records))
	for _, q := range questions.Records {
		questionIDs[q.ID] = struct{}{}
		ids = append(ids, "question:"+q.ID)
	}
	for _, c := range choices.Choices {
		ids = append(ids, "choice:"+c.ID)
	}
	if !b.admitAsksWakeRead(ctx, out) {
		return
	}
	if complete || len(ids) > 0 {
		out.OldestAsksWake, out.AsksWake = b.asksWake.keys(ids, complete, time.Now)
		out.AnswerableAsks = new(int64(len(ids)))
	}
	var waitingIDs []string
	waitingComplete := b.waitingAsks != nil && len(questionIDs) == 0
	if b.waitingAsks != nil && len(questionIDs) > 0 {
		waiting, err := b.waitingAsks(ctx)
		waitingComplete = err == nil && waiting.Outcome == waitwire.OutcomePage && waiting.Complete
		if waitingComplete {
			for _, id := range waiting.IDs {
				if _, ok := questionIDs[id]; ok {
					waitingIDs = append(waitingIDs, id)
					delete(questionIDs, id)
				}
			}
		}
	}
	if waitingComplete && (questions.Complete || len(waitingIDs) > 0) && b.admitAsksWakeRead(ctx, out) {
		_, out.WaitingAsksWake = b.asksWaitingWake.keys(waitingIDs, questions.Complete, time.Now)
		out.WaitingAsks = new(int64(len(waitingIDs)))
	}
}

func (b *messagesBackend) admitAsksWakeRead(ctx context.Context, out *gateway.WatchMetadata) bool {
	switch b.asksEnrollment(ctx) {
	case rights.DecisionOutcomeDenied.String(), rights.DecisionOutcomeNotGranted.String(), rights.DecisionOutcomeForbidden.String():
		b.asksWake.keys(nil, true, time.Now)
		b.asksWaitingWake.keys(nil, true, time.Now)
		out.AnswerableAsks, out.WaitingAsks = new(int64(0)), new(int64(0))
		out.AsksWake, out.OldestAsksWake, out.WaitingAsksWake = "", "", ""
	case rights.DecisionOutcomePermitted.String():
		return true
	}
	return false
}

func nativeWaitingAsks(machine *client.Machine) func(context.Context) (waitwire.WaitingPage, error) {
	return func(ctx context.Context) (waitwire.WaitingPage, error) {
		binding, err := machine.ResolveService(ctx, asksclient.ResponderWaitContract, client.Requirements{Scope: client.ScopeLocal})
		if err != nil {
			return waitwire.WaitingPage{}, err
		}
		return asksclient.NewResponderWaitWithTransport(binding.Transport()).ListWaitingContext(ctx, "", 64)
	}
}
