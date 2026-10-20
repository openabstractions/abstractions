//go:build windows

package main

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Owner's PC, 2026-11-12 (R36): about 40 OpenCode and Codex chats each held a
// headers wait, which reread its inbox ten times a second. Every reread
// rechecks the caller's admission under the directory lock, so the runtime
// spent two cores on them and a Claude listing took 30 s; with Prepare it
// passed the gateway's 35 s budget and Send answered reply_lost. Thirty
// receiving chats here each hold a headers wait while a sender lists and
// prepares through the runtime Messages service.
func TestMessagesManyWaitingChatsKeepListingAndPrepareFast(t *testing.T) {
	r := newRoutingRun(t)
	var contexts []string
	for i := range 30 {
		_, c := r.open(fmt.Sprintf("waiting-%d", i), fmt.Sprintf("Waiting %d", i))
		contexts = append(contexts, c)
	}
	var checks atomic.Int64
	r.f.run.d.agentStamp = func(program string) (applicationAgentStamp, error) {
		checks.Add(1)
		return readApplicationAgentStamp(program)
	}
	ctx, cancel := context.WithCancel(r.f.run.ctx)
	var wg sync.WaitGroup
	defer func() {
		cancel()
		wg.Wait()
	}()
	for _, c := range contexts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			receiver := &messagesReceiver{host: r.f.other.host, call: r.f.other.call, ctx: ctx}
			for ctx.Err() == nil {
				page := receiver.headersWait(c, "", 0)
				if page.Outcome != msg.OutcomePage {
					t.Error("headers read", c, page.Outcome)
					return
				}
				receiver.headersWait(c, page.Revision, 30000)
			}
		}()
	}
	time.Sleep(time.Second)
	start, began := checks.Load(), time.Now()
	time.Sleep(3 * time.Second)
	rate := float64(checks.Load()-start) / time.Since(began).Seconds()
	// One reread a second per wait checks admission twice (its read and its
	// recheck); the inbox watch adds one check a second.
	if limit := float64(2*len(contexts) + 10); rate > limit {
		t.Fatalf("%d held headers waits checked admission %.0f times a second; want at most %.0f", len(contexts), rate, limit)
	}
	began = time.Now()
	whole, _ := r.f.reader.inboxesListWhole(true)
	listed := time.Since(began)
	if whole.Outcome != msg.OutcomePage || len(whole.Inboxes) < len(contexts) {
		t.Fatal("listing", whole.Outcome, len(whole.Inboxes))
	}
	began = time.Now()
	prepared := r.prepare("")
	took := time.Since(began)
	if prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil {
		t.Fatal("prepare", prepared)
	}
	t.Logf("checks=%.0f/s listing=%v prepare=%v", rate, listed, took)
	if listed > 3*time.Second || took > 3*time.Second {
		t.Fatalf("with %d held headers waits a listing took %v and Prepare %v", len(contexts), listed, took)
	}
}
