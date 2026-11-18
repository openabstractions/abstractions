package main

import (
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
)

// beginOpenWaiting runs one agent Activate of the stopped fixture through a
// caller that waits up to wait, and returns once the runtime admitted its
// open question.
func beginOpenWaiting(t *testing.T, f *applicationV3GatewayFixture, permission *answererFixture, n int, wait time.Duration) (<-chan openReceived, asks.Record) {
	t.Helper()
	key := receiptKey(n, time.Now())
	done := make(chan openReceived, 1)
	client := f.client(wait)
	go func() {
		result, err := client.Activate(key, openRequest)
		done <- openReceived{result, err}
	}()
	for deadline := time.Now().Add(3 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		if record := openAdmission(f, permission, key); record.ID != "" {
			return done, record
		}
	}
	t.Fatalf("open %d admitted no question", n)
	return nil, asks.Record{}
}

// Installed check 2026-11-21: every "Open ComfyUI?" question was withdrawn
// about 40 s after it was raised, whatever the agent waited. The question
// now stays up until the person answers (architect ruling 2026-11-22): it
// states a 24-hour expiry, a person answering while the caller still waits
// opens the app for that call, and a caller that stops waiting leaves the
// question up for a later Allow.
func TestApplicationV3GatewayOpenQuestionLastsWhileCallerWaits(t *testing.T) {
	run := openLaterFixture(t)
	applicationV3AnswerWait = 40 * time.Second
	f, permission, launches := run.f, run.permission, run.launches

	asked := time.Now()
	done, question := beginOpenWaiting(t, f, permission, 120, 30*time.Second)
	if question.Content == nil {
		t.Fatal("the open question retained no slots")
	}
	until, err := time.Parse(time.RFC3339, question.Content.Slots["until"])
	if err != nil {
		t.Fatalf("until slot %q: %v", question.Content.Slots["until"], err)
	}
	if lifetime := until.Sub(asked); lifetime < applicationV3OpenQuestionLife-time.Minute {
		t.Fatalf("the open question expires %v after it was asked, short of %v", lifetime, applicationV3OpenQuestionLife)
	}
	// The person answers well after the fixture client's ordinary 3 s
	// frame timeout; the caller still waits and the answer reaches the start.
	time.Sleep(3500 * time.Millisecond)
	if record, found, err := permission.a.book.ApplicationRecord(question.ID); err != nil || !found || !record.Pending() {
		t.Fatalf("the question did not stay open while its caller waited: found %v pending %v %v", found, record.Pending(), err)
	}
	answerOpen(t, permission, question.ID, "once")
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the answered open did not finish")
	}
	if launches.Load() != 1 {
		t.Fatalf("the person's late Allow did not reach the start: launches%d", launches.Load())
	}

	// A caller that stops waiting leaves its question up.
	left, second := beginOpenWaiting(t, f, permission, 121, 1500*time.Millisecond)
	select {
	case <-left:
	case <-time.After(5 * time.Second):
		t.Fatal("the caller's own wait did not end")
	}
	time.Sleep(300 * time.Millisecond)
	if record, found, err := permission.a.book.ApplicationRecord(second.ID); err != nil || !found || !record.Pending() {
		t.Fatalf("the question left with its caller: found %v %v", found, err)
	}
	if launches.Load() != 1 {
		t.Fatalf("an unanswered open reached the start: launches%d", launches.Load())
	}
	answerOpen(t, permission, second.ID, "once")
	if o := run.note(t); o.Question != second.ID || launches.Load() != 2 {
		t.Fatalf("the Allow after the caller left: %+v launches%d", o, launches.Load())
	}
}
