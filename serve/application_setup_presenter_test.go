package main

import (
	"context"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Review 25: asked only while a presenter watches. With no presenter read
// waiting or ended within the grace, the request answers cannot_run and
// nothing is kept for a presenter that starts later.
func TestRequestSetupNeedsAWatchingPresenter(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)
	q := &setupRequests{now: func() time.Time { return now }}
	d := setupDirectory(setupGrants())
	app := rights.Subject{Account: setupTestAccount, Program: setupTestApp}
	if got := q.request(ctx, d, app, "code"); got.Outcome != applications.SetupOutcomeCannotRun || got.Reason != "OpenAbstractions' popup host is not running" || q.pending != nil {
		t.Fatalf("no presenter = %+v pending %v", got, q.pending)
	}
	// A presenter's read that ended within the grace still watches.
	q.lastRead = now.Add(-20 * time.Second)
	if got := q.request(ctx, d, app, "code"); got.Outcome != applications.SetupOutcomeAsked || q.pending == nil {
		t.Fatalf("recent read = %+v", got)
	}
	// One that ended longer ago does not, and the expired request is gone.
	q.pending = nil
	q.lastRead = now.Add(-26 * time.Second)
	if got := q.request(ctx, d, app, "code"); got.Outcome != applications.SetupOutcomeCannotRun || q.pending != nil {
		t.Fatalf("stale read = %+v", got)
	}
	// A read waiting now watches; its request is handed over at once.
	readCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan applications.SetupRequestRead, 1)
	go func() {
		done <- q.read(readCtx, d, rights.Subject{Account: setupTestAccount, Program: setupTestPanel}, "")
	}()
	deadline := time.Now().Add(5 * time.Second)
	for {
		q.mu.Lock()
		reading := q.reading
		q.mu.Unlock()
		if reading > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if got := q.request(ctx, d, app, "image"); got.Outcome != applications.SetupOutcomeAsked {
		t.Fatalf("waiting read = %+v", got)
	}
	if read := <-done; read.Request == nil || read.Request.Use != "image" {
		t.Fatalf("handed over = %+v", read)
	}
}
