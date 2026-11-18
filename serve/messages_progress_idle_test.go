package main

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Retained messages live up to seven days. Once a pass has reconciled them
// and nothing changed, an idle progress tick must not re-reconcile every live
// record: the installed 0.8.0 candidate spent 2.3 cores on 145 retained
// messages between two enrolled agents and starved presence renewals.
func TestMessageProgressIdleWorkDoesNotScaleWithRetainedMessages(t *testing.T) {
	idle := func(n int) (int, time.Duration) {
		h, _, _, _, _ := messagesStartProgressFixture(t)
		messagesProgressSends(t, h, msg.KindNote, n)
		visits := 0
		h.messages.inspect = func(name string, _ int) {
			if name == "progress_record" {
				visits++
			}
		}
		var reports []error
		h.ctx, h.report = t.Context(), func(err error) { reports = append(reports, err) }
		// Settle: the first passes reconcile every record once.
		for range 3 {
			if failing := h.messageProgressTick(""); failing != "" {
				t.Fatal("settling pass failed", n, failing, reports)
			}
		}
		visits = 0
		began := time.Now()
		const ticks = 5
		for range ticks {
			if failing := h.messageProgressTick(""); failing != "" {
				t.Fatal("idle pass failed", n, failing, reports)
			}
		}
		return visits / ticks, time.Since(began) / ticks
	}
	fewVisits, fewTime := idle(4)
	manyVisits, manyTime := idle(40)
	t.Logf("idle tick: 4 messages %d records %v; 40 messages %d records %v", fewVisits, fewTime, manyVisits, manyTime)
	if manyVisits > fewVisits+1 {
		t.Fatalf("idle progress reconciles every retained message: %d records per tick for 40 messages, %d for 4", manyVisits, fewVisits)
	}
}

// A settled question to a running receiver is skipped while nothing changes.
// When the receiver's presence lapses, the next tick takes the start path and
// asks for startup consent; the lapse moves the store's change count instead
// of waiting for the settle interval.
func TestMessageProgressPresenceLapseReachesSettledStartNextTick(t *testing.T) {
	h, _, _, calls, _ := messagesStartProgressFixture(t)
	messagesProgressSends(t, h, msg.KindQuestion, 3)
	var consents atomic.Int32
	h.messageRights.firstUse = func(_ rights.Subject, _ func() asks.Publisher, action, _ string) string {
		if action == grants.ApplicationMessageStartAction {
			consents.Add(1)
		}
		return "pending-native-person-question"
	}
	h.messageStartSettings = func(context.Context, string) (messageStartSettings, error) {
		return defaultMessageStartSettings(), nil
	}
	receiver := rights.Subject{Account: h.messageRights.owner, Program: h.directory.descriptors["receiver"].Program}
	h.directory.instances["running"] = applicationLease{application: "receiver", owner: receiver, session: "fixture", deadline: time.Now().Add(time.Hour)}
	var reports []error
	h.ctx, h.report = t.Context(), func(err error) { reports = append(reports, err) }
	stop := h.followMessageChanges(t.Context())
	defer stop()
	for range 3 {
		if failing := h.messageProgressTick(""); failing != "" {
			t.Fatal("settling pass failed", failing, reports)
		}
	}
	if calls.Load() != 0 || consents.Load() != 0 {
		t.Fatal("running receiver started", calls.Load(), consents.Load())
	}
	before := h.messages.changes.Load()
	if !h.directory.lock(t.Context()) {
		t.Fatal("directory lock")
	}
	lease := h.directory.instances["running"]
	lease.deadline = time.Now().Add(-time.Second)
	h.directory.instances["running"] = lease
	h.directory.expireLocked()
	h.directory.unlock()
	// The directory's recheck publishes the change on its own goroutine.
	for i := 0; h.messages.changes.Load() == before; i++ {
		if i == 500 {
			t.Fatal("presence lapse did not advance the message store")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if failing := h.messageProgressTick(""); failing != "" {
		t.Fatal("pass after lapse failed", failing, reports)
	}
	if consents.Load() == 0 {
		t.Fatal("lapsed receiver's start was not reached on the next tick")
	}
}
