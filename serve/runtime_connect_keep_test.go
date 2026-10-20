package main

import (
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// pendingKeepQuestions are the pending Keep questions the presenter reads
// from the runtime's Book (asks operator@1 Pending lists the same records).
func pendingKeepQuestions(t *testing.T, run *selfRegisteredRun) []asks.Record {
	t.Helper()
	history, _, err := run.f.a.book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	var out []asks.Record
	for _, record := range history {
		if record.Pending() && record.Key == asks.SelfRegisteredKey {
			out = append(out, record)
		}
	}
	return out
}

func onePendingKeep(t *testing.T, run *selfRegisteredRun, program, why string) asks.Record {
	t.Helper()
	pending := pendingKeepQuestions(t, run)
	if len(pending) != 1 || pending[0].About != program {
		t.Fatalf("%s: want one pending Keep question about %s, got %+v", why, program, pending)
	}
	return pending[0]
}

func connectEntryState(run *selfRegisteredRun) string {
	run.d.mu.Lock()
	defer run.d.mu.Unlock()
	return run.d.v3.entries["comfyui"].State
}

// restart stops the runtime's question followers and starts it again from
// the same state folder and Book, in the runtime's start order.
func (run *selfRegisteredRun) restart(t *testing.T, followed ...string) {
	t.Helper()
	run.cancel()
	for _, id := range followed {
		waitUnfollowed(t, run.k, id)
	}
	run.start(t)
}

// Owner on R30, 2026-11-11: Connect on the Catalog's ComfyUI row left the
// connector's own registration pending and nothing asked the person. The
// connector's register-self asks nothing; the Panel's Connect then calls
// AskConfirmation, and exactly one Keep question reaches the presenter. A
// restart while it is pending delivers the same question; a restart after it
// expired asks again; Allow (keep) keeps the entry.
func TestConnectKeepIsAskedOnceAndSurvivesRestart(t *testing.T) {
	run := newSelfRegisteredRunWith(t, true)
	program := run.f.comfy.Program
	registration1459Publish(t, run.d, "comfyui", program, "ComfyUI", "self", "")
	if state := connectEntryState(run); state != "pending" {
		t.Fatalf("register-self: state %q", state)
	}
	if pending := pendingKeepQuestions(t, run); len(pending) != 0 {
		t.Fatalf("register-self asked: %+v", pending)
	}

	// The Panel's Connect, pressed twice: one question.
	for i := 0; i < 2; i++ {
		if r := run.d.askConfirmation(run.ctx, run.f.panel, "comfyui"); r.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatalf("Connect's AskConfirmation: %+v", r)
		}
	}
	first := onePendingKeep(t, run, program, "after Connect")

	// A restart while the question is pending delivers it again unchanged.
	run.restart(t, first.ID)
	if again := onePendingKeep(t, run, program, "after a restart"); again.ID != first.ID {
		t.Fatalf("a restart replaced the pending question: %s, was %s", again.ID, first.ID)
	}
	if !run.k.followed(first.ID) {
		t.Fatal("the restarted runtime does not follow the pending question")
	}

	// The Book drops the question unanswered, as its 15-minute expiry does
	// over a long upgrade (TestProgramSelfRegistrationExpiryKeepsPending runs
	// the expiry itself); the next start asks again.
	if _, outcome, err := run.f.a.book.RetirePendingApplicationAuthorized(first.ID, func() error { return nil }); err != nil || outcome == "unknown" {
		t.Fatalf("retire: %s %v", outcome, err)
	}
	if pending := pendingKeepQuestions(t, run); len(pending) != 0 {
		t.Fatalf("the question is still pending: %+v", pending)
	}
	run.restart(t, first.ID)
	second := onePendingKeep(t, run, program, "after a restart with the question gone")
	if second.ID == first.ID {
		t.Fatal("the expired question came back instead of a new one")
	}
	if state := connectEntryState(run); state != "pending" {
		t.Fatalf("asking again changed the entry: %q", state)
	}

	answer(t, run.f, second.ID, "keep")
	waitDirectory(t, run.d, func() bool { return connectEntryState(run) == "enabled" })
	run.restart(t, second.ID)
	if pending := pendingKeepQuestions(t, run); len(pending) != 0 || connectEntryState(run) != "enabled" {
		t.Fatalf("a kept entry was asked about again: %+v state %q", pending, connectEntryState(run))
	}
}
