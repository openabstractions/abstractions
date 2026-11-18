package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	asksservice "github.com/openabstractions/abstraction-asks/go/application"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The open question stays until answered (architect ruling 2026-11-22,
// APPLICATIONS.md). These tests run applications@3 Open and legacy Activate
// through its native client, the real Rights policy, the real Asks Book and
// the real agent enrollment owner. The start stand-in counts how far an open
// reaches: launches counts the owned start's entry for this application.

type openLaterRun struct {
	f          *applicationV3GatewayFixture
	permission *answererFixture
	keep       atomic.Pointer[string]
	removed    atomic.Bool
	launches   *atomic.Int32
	notes      chan applicationV3OpenOutcome
	logs       *elevatedLogs
	// running restores the fixture's running instance, as if the person
	// opened the app themselves.
	running func()
}

// openLaterFixture is a kept agent opening the stopped, kept fixture
// application, whose Keep identity the test can change or remove.
func openLaterFixture(t *testing.T) *openLaterRun {
	t.Helper()
	saved := applicationV3AnswerWait
	applicationV3AnswerWait = 300 * time.Millisecond
	t.Cleanup(func() { applicationV3AnswerWait = saved })
	f, permission := agentOpenFixture(t, appmodel.CommandClassReveal, true)
	run := &openLaterRun{f: f, permission: permission, notes: make(chan applicationV3OpenOutcome, 4), logs: &elevatedLogs{}}
	keep := "keep-1"
	run.keep.Store(&keep)
	original := f.g.load
	f.g.load = func(ctx context.Context, peer *identity.Peer, name string) (*applicationV3Kept, error) {
		if run.removed.Load() {
			return nil, errApplicationV3NotFound
		}
		k, err := original(ctx, peer, name)
		if err == nil {
			k.keepIdentity = *run.keep.Load()
		}
		return k, err
	}
	f.g.mu.Lock()
	owned := f.g.owned
	f.g.mu.Unlock()
	f.g.directory.mu.Lock()
	instances := f.g.directory.instances
	f.g.directory.mu.Unlock()
	run.running = func() {
		f.g.mu.Lock()
		f.g.owned = owned
		f.g.mu.Unlock()
		f.g.directory.mu.Lock()
		f.g.directory.instances = instances
		f.g.directory.mu.Unlock()
	}
	run.launches, _ = stopFixture(f)
	f.g.noteOpen = func(o applicationV3OpenOutcome) { run.notes <- o }
	permission.a.rights.useLogs(run.logs)
	t.Cleanup(f.g.later.close)
	return run
}

// park runs one open that the person does not answer within the call's wait
// and returns its answer.
func (run *openLaterRun) park(t *testing.T, n int) applications.OpenResult {
	t.Helper()
	got, err := run.f.c.Open(receiptKey(n, time.Now()), applications.OpenRequest{Request: openRequest})
	if err != nil || got.Result.Outcome != applications.ActivationOutcomeNotReady || got.Result.Reason != "consent" || got.Question == "" || got.Handle == "" {
		t.Fatalf("an unanswered open: %+v %v", got, err)
	}
	if record, found, err := run.permission.a.book.ApplicationRecord(got.Question); err != nil || !found || !record.Pending() {
		t.Fatalf("the question did not stay up after the call's wait: found %v %v", found, err)
	}
	if run.launches.Load() != 0 {
		t.Fatal("an unanswered open reached the start")
	}
	return got
}

func (run *openLaterRun) note(t *testing.T) applicationV3OpenOutcome {
	t.Helper()
	select {
	case o := <-run.notes:
		return o
	case <-time.After(5 * time.Second):
		t.Fatal("the Allow delivered no result to the requester")
		return applicationV3OpenOutcome{}
	}
}

// noNote checks that nothing was performed or delivered.
func (run *openLaterRun) noNote(t *testing.T, why string) {
	t.Helper()
	select {
	case o := <-run.notes:
		t.Fatalf("%s: a result was delivered: %+v", why, o)
	case <-time.After(500 * time.Millisecond):
	}
	if run.launches.Load() != 0 || run.f.effects.Load() != 0 || run.logs.events(openLaterEvent) != 0 {
		t.Fatalf("%s: launches%d effects%d activity%d", why, run.launches.Load(), run.f.effects.Load(), run.logs.events(openLaterEvent))
	}
}

// Points 1, 3 and 6: the question outlives the call and states a 24-hour
// expiry; a second open of the same request joins it and shares the handle;
// the person's later Allow performs the open once, as the requester, and the
// result reaches the requester as a note naming the handle and as an
// Activity row attributed to the requester. A Block standing for another
// program, the Panel, does not decide the requester's open.
func TestOpenLaterAllowPerformsTheExactOpenOnceAsTheRequester(t *testing.T) {
	run := openLaterFixture(t)
	asked := time.Now()
	first := run.park(t, 200)
	record, _, err := run.permission.a.book.ApplicationRecord(first.Question)
	if err != nil || record.Content == nil || record.Key != asks.ApplicationCommandOnceKey || record.AskerProgram != run.f.subject.Program || !strings.HasPrefix(record.Text, "Open Fixture? ") {
		t.Fatalf("the open question: %+v %v", record, err)
	}
	until, err := time.Parse(time.RFC3339, record.Content.Slots["until"])
	if err != nil || until.Sub(asked) < 24*time.Hour-time.Minute || until.Sub(asked) > 24*time.Hour+time.Minute {
		t.Fatalf("the question states expiry %q, want 24 hours after it was asked (%v)", record.Content.Slots["until"], err)
	}
	if got := run.permission.a.book.PendingUntil(record); got.Sub(record.Asked) != 24*time.Hour {
		t.Fatalf("the Book keeps the question %v, want 24 hours", got.Sub(record.Asked))
	}
	second := run.park(t, 201)
	if second.Question != first.Question || second.Handle != first.Handle {
		t.Fatalf("a second open asked again: %+v, first %+v", second, first)
	}
	if err := run.permission.a.rights.policy.Set(run.permission.panel, ActionApplicationActivate, "app:fixture", false); err != nil {
		t.Fatal(err)
	}
	answerOpen(t, run.permission, first.Question, "once")
	o := run.note(t)
	if o.Question != first.Question || o.Handle != first.Handle || o.Requester != run.f.subject.Program || o.Application != "fixture" || o.Title != "Fixture" {
		t.Fatalf("the note: %+v", o)
	}
	if run.launches.Load() != 1 {
		t.Fatalf("the Allow reached the start %d times, want once", run.launches.Load())
	}
	if run.logs.events(openLaterEvent) != 1 {
		t.Fatalf("Activity rows: %+v", run.logs.records)
	}
	for _, r := range run.logs.records {
		if r.Attrs["oa_event"] == openLaterEvent && (r.Attrs["program"] != run.f.subject.Program || r.Attrs["handle"] != first.Handle || r.Attrs["application"] != "fixture") {
			t.Fatalf("the Activity row names another actor: %+v", r.Attrs)
		}
	}
	// The occurrence is used: no second effect, however often it is seen.
	time.Sleep(300 * time.Millisecond)
	if run.f.g.later.claim(first.Question) || run.launches.Load() != 1 || len(run.notes) != 0 {
		t.Fatalf("the occurrence ran again: launches%d notes%d", run.launches.Load(), len(run.notes))
	}
}

// Point 2: everything is rechecked when the Allow arrives. A Block on the
// activation right or on the verb's class, a target whose Keep identity
// changed or that was removed, each stops the open; the requester hears the
// refusal.
func TestOpenLaterRechecksEverythingAtEffectTime(t *testing.T) {
	for _, tc := range []struct {
		name    string
		change  func(*testing.T, *openLaterRun)
		outcome applications.ActivationOutcome
		reason  string
	}{
		{"activation blocked", func(t *testing.T, run *openLaterRun) {
			if err := run.permission.a.rights.policy.Set(run.f.subject, ActionApplicationActivate, "app:fixture", false); err != nil {
				t.Fatal(err)
			}
		}, applications.ActivationOutcomeForbidden, "blocked"},
		{"class never", func(t *testing.T, run *openLaterRun) {
			if err := run.permission.a.rights.policy.Set(run.f.subject, "abstraction.facade/command.reveal", "app:fixture", false); err != nil {
				t.Fatal(err)
			}
		}, applications.ActivationOutcomeForbidden, "blocked"},
		{"target changed since connected", func(t *testing.T, run *openLaterRun) {
			changed := "keep-2"
			run.keep.Store(&changed)
		}, applications.ActivationOutcomeUnavailable, "manifest"},
		{"target removed", func(t *testing.T, run *openLaterRun) { run.removed.Store(true) }, applications.ActivationOutcomeUnavailable, "manifest"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			run := openLaterFixture(t)
			parked := run.park(t, 210)
			tc.change(t, run)
			answerOpen(t, run.permission, parked.Question, "once")
			o := run.note(t)
			if o.Handle != parked.Handle || o.Result.Outcome != tc.outcome || o.Result.Reason != tc.reason {
				t.Fatalf("the rechecked open: %+v", o)
			}
			if run.launches.Load() != 0 || run.f.effects.Load() != 0 {
				t.Fatalf("a rechecked open reached the start: launches%d effects%d", run.launches.Load(), run.f.effects.Load())
			}
		})
	}
}

// Point 2: the question lives only while the requester stays enrolled. When
// the person removes the agent's enrollment, the question is withdrawn and
// a late answer performs nothing.
func TestOpenLaterEndsWithTheRequestersEnrollment(t *testing.T) {
	run := openLaterFixture(t)
	parked := run.park(t, 220)
	srun := &selfRegisteredRun{f: run.permission, d: run.f.g.directory, ctx: t.Context()}
	current := srun.d.currentAgentEnrollmentImage(t.Context(), "fixture", nil)
	if current.Enrollment == nil {
		t.Fatal(current)
	}
	agent1478Set(t, srun, "fixture", current.Enrollment.Revision, false)
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		record, found, err := run.permission.a.book.ApplicationRecord(parked.Question)
		if err != nil {
			t.Fatal(err)
		}
		if !found || !record.Pending() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the question outlived the requester's enrollment")
		}
	}
	if _, state, err := run.permission.a.book.AnswerApplicationScopedAuthorized(parked.Question, "once", func(asks.Record, string) error { return nil }); err == nil && state == "answered" {
		t.Fatal("a withdrawn question took an answer")
	}
	run.noNote(t, "after the enrollment ended")
}

// Point 4: an expired or withdrawn question performs nothing, even when an
// answer arrives late.
func TestOpenLaterExpiredOrWithdrawnQuestionPerformsNothing(t *testing.T) {
	t.Run("expired", func(t *testing.T) {
		saved := applicationV3OpenQuestionLife
		applicationV3OpenQuestionLife = 1200 * time.Millisecond
		t.Cleanup(func() { applicationV3OpenQuestionLife = saved })
		run := openLaterFixture(t)
		parked := run.park(t, 230)
		time.Sleep(1500 * time.Millisecond)
		if _, state, err := run.permission.a.book.AnswerApplicationScopedAuthorized(parked.Question, "once", func(asks.Record, string) error { return nil }); err == nil && state == "answered" {
			t.Fatal("an expired question took an answer")
		}
		run.noNote(t, "after expiry")
	})
	t.Run("withdrawn", func(t *testing.T) {
		run := openLaterFixture(t)
		parked := run.park(t, 231)
		if err := run.permission.a.book.Forget(parked.Question); err != nil {
			t.Fatal(err)
		}
		if _, state, err := run.permission.a.book.AnswerApplicationScopedAuthorized(parked.Question, "once", func(asks.Record, string) error { return nil }); err == nil && state == "answered" {
			t.Fatal("a withdrawn question took an answer")
		}
		run.noNote(t, "after withdrawal")
	})
	t.Run("not now", func(t *testing.T) {
		run := openLaterFixture(t)
		parked := run.park(t, 232)
		answerOpen(t, run.permission, parked.Question, "refuse")
		run.noNote(t, "after Not now")
	})
}

// Point 5: the parked question is the person's own, as every open question
// is: neither the requesting agent nor an enrolled assistant answers it, and
// only the person's answer through the operator performs the open.
func TestOpenLaterOnlyThePersonsAnswerCounts(t *testing.T) {
	run := openLaterFixture(t)
	parked := run.park(t, 240)
	record, _, err := run.permission.a.book.ApplicationRecord(parked.Question)
	if err != nil {
		t.Fatal(err)
	}
	run.permission.enroll(t, run.permission.answerer)
	for _, who := range []answerer{{subject: run.f.subject}, run.permission.me()} {
		for _, choice := range append([]string{"allow"}, record.Options...) {
			if _, err := run.permission.a.authorizeAnswerer(t.Context(), who, record, run.permission.a.scope, choice); !errors.Is(err, asksservice.ErrResponderForbidden) {
				t.Fatalf("%s may answer the parked open question with %s: %v", who.subject.Program, choice, err)
			}
		}
	}
	if outcome, _ := run.permission.answer(run.permission.me(), parked.Question, "once"); outcome != "forbidden" {
		t.Fatalf("the enrolled assistant answered the parked question: %s", outcome)
	}
	run.noNote(t, "after the assistant's attempts")
	answerOpen(t, run.permission, parked.Question, "once")
	if o := run.note(t); o.Handle != parked.Handle || run.launches.Load() != 1 {
		t.Fatalf("the person's Allow: %+v launches%d", o, run.launches.Load())
	}
}

// A call still waiting when the person allows opens at once, and the parked
// open of the same question performs nothing more.
func TestOpenLaterWaitingCallAndParkedOpenRunOnce(t *testing.T) {
	run := openLaterFixture(t)
	parked := run.park(t, 250)
	applicationV3AnswerWait = 5 * time.Second
	done, joined := beginOpen(t, run.f, run.permission, 251)
	if joined.ID != parked.Question {
		t.Fatalf("the waiting call asked its own question %s, not %s", joined.ID, parked.Question)
	}
	answerOpen(t, run.permission, parked.Question, "once")
	waitOpen(t, done)
	select {
	case <-run.notes:
	case <-time.After(time.Second):
	}
	if run.launches.Load() != 1 {
		t.Fatalf("one Allow reached the start %d times", run.launches.Load())
	}
}

// describe reads one open handle's state through the runtime's DescribeOpen.
func (run *openLaterRun) describe(t *testing.T, handle, application string) appmodel.ApplicationListing {
	t.Helper()
	got, err := run.f.c.DescribeOpen(appmodel.OpenHandleQuery{Application: application, OpenHandle: handle})
	if err != nil || got.Outcome != applications.OutcomePage || got.Answer == nil {
		t.Fatalf("describe %s: %+v %v", handle, got, err)
	}
	return *got.Answer
}

func (run *openLaterRun) handleState(t *testing.T, handle string) appmodel.OpenHandle {
	t.Helper()
	for deadline := time.Now().Add(3 * time.Second); ; time.Sleep(10 * time.Millisecond) {
		listing := run.describe(t, handle, "")
		if listing.Outcome != appmodel.ReadingOutcomePage || listing.Open == nil || len(listing.Applications) != 0 {
			t.Fatalf("the handle's state: %+v", listing)
		}
		if listing.Open.State != appmodel.OpenHandleStatePending || time.Now().After(deadline) {
			return *listing.Open
		}
	}
}

// Architect ruling 2026-11-22 (c): oa_applications_describe shows the open
// handle's state, pending, opened, expired or refused, to the caller it was
// given to; an unknown handle, or one named with another application, reads
// not_found and discloses nothing.
func TestOpenLaterDescribeShowsTheHandleState(t *testing.T) {
	t.Run("pending then opened", func(t *testing.T) {
		run := openLaterFixture(t)
		parked := run.park(t, 260)
		if got := run.describe(t, parked.Handle, "fixture"); got.Open == nil || got.Open.State != appmodel.OpenHandleStatePending || got.Open.Application != "fixture" || got.Open.Handle != parked.Handle {
			t.Fatalf("a parked open: %+v", got)
		}
		for _, q := range []struct{ handle, application string }{{"no-such-handle", ""}, {parked.Handle, "other"}} {
			if got := run.describe(t, q.handle, q.application); got.Outcome != appmodel.ReadingOutcomeNotFound || got.Reason != "open_handle" || got.Open != nil {
				t.Fatalf("describe %+v: %+v", q, got)
			}
		}
		run.running()
		answerOpen(t, run.permission, parked.Question, "once")
		run.note(t)
		if got := run.handleState(t, parked.Handle); got.State != appmodel.OpenHandleStateOpened || got.Instance != run.f.instance {
			t.Fatalf("an allowed open: %+v", got)
		}
	})
	t.Run("refused by the person", func(t *testing.T) {
		run := openLaterFixture(t)
		parked := run.park(t, 261)
		answerOpen(t, run.permission, parked.Question, "refuse")
		if got := run.handleState(t, parked.Handle); got.State != appmodel.OpenHandleStateRefused || got.Reason != "consent" {
			t.Fatalf("Not now: %+v", got)
		}
	})
	t.Run("refused at the recheck", func(t *testing.T) {
		run := openLaterFixture(t)
		parked := run.park(t, 262)
		if err := run.permission.a.rights.policy.Set(run.f.subject, ActionApplicationActivate, "app:fixture", false); err != nil {
			t.Fatal(err)
		}
		answerOpen(t, run.permission, parked.Question, "once")
		run.note(t)
		if got := run.handleState(t, parked.Handle); got.State != appmodel.OpenHandleStateRefused || got.Reason != "forbidden:blocked" {
			t.Fatalf("a blocked open: %+v", got)
		}
	})
	t.Run("expired", func(t *testing.T) {
		saved := applicationV3OpenQuestionLife
		applicationV3OpenQuestionLife = 1200 * time.Millisecond
		t.Cleanup(func() { applicationV3OpenQuestionLife = saved })
		run := openLaterFixture(t)
		parked := run.park(t, 263)
		time.Sleep(1500 * time.Millisecond)
		if got := run.handleState(t, parked.Handle); got.State != appmodel.OpenHandleStateExpired {
			t.Fatalf("an expired question: %+v", got)
		}
	})
	t.Run("withdrawn", func(t *testing.T) {
		run := openLaterFixture(t)
		parked := run.park(t, 264)
		if err := run.permission.a.book.Forget(parked.Question); err != nil {
			t.Fatal(err)
		}
		if got := run.handleState(t, parked.Handle); got.State != appmodel.OpenHandleStateExpired || got.Reason != "withdrawn" {
			t.Fatalf("a withdrawn question: %+v", got)
		}
	})
}

// Architect ruling 2026-11-22: the 40 s wait is the default, capped by the
// caller's own wait_ms. The wait is no part of the bound request: opens
// with different waits join one question and share one handle.
func TestOpenLaterCallersWaitCapsTheWait(t *testing.T) {
	run := openLaterFixture(t)
	applicationV3AnswerWait = 20 * time.Second
	// The caller waits as long as the test may. An answer within half of
	// applicationV3AnswerWait shows its wait_ms capped the wait, also on a
	// loaded machine.
	caller := run.f.client(openTestWait(t))
	capped := applicationV3AnswerWait / 2
	short := applications.OpenRequest{Request: openRequest, WaitMs: new(int64(200))}
	began := time.Now()
	first, err := caller.Open(receiptKey(270, time.Now()), short)
	if waited := time.Since(began); err != nil || waited > capped || first.Result.Outcome != applications.ActivationOutcomeNotReady || first.Handle == "" {
		t.Fatalf("an open with wait_ms 200 answered after %v: %+v %v", waited, first, err)
	}
	other := applications.OpenRequest{Request: openRequest, WaitMs: new(int64(300))}
	second, err := caller.Open(receiptKey(271, time.Now()), other)
	if err != nil || second.Question != first.Question || second.Handle != first.Handle {
		t.Fatalf("a second open with another wait: %+v, first %+v %v", second, first, err)
	}
	zero := applications.OpenRequest{Request: openRequest, WaitMs: new(int64(0))}
	began = time.Now()
	if third, err := caller.Open(receiptKey(272, time.Now()), zero); err != nil || time.Since(began) > capped || third.Handle != first.Handle {
		t.Fatalf("an open with wait_ms 0: %+v %v after %v", third, err, time.Since(began))
	}
}

// Security review 2026-11-22: the open question binds the exact request
// body. Two opens of the same application and verb with different payloads
// raise two questions with two handles, each parked with its own payload,
// and the Allow on one performs that one alone.
func TestOpenLaterDifferentPayloadsAskSeparately(t *testing.T) {
	run := openLaterFixture(t)
	run.f.manifest.Verbs[0].InputSchema = `{"type":"object","properties":{"file":{"type":"string"}},"additionalProperties":false}`
	open := func(n int, payload string) applications.OpenResult {
		request := openRequest
		request.Payload = payload
		got, err := run.f.c.Open(receiptKey(n, time.Now()), applications.OpenRequest{Request: request})
		if err != nil || got.Result.Outcome != applications.ActivationOutcomeNotReady || got.Question == "" || got.Handle == "" {
			t.Fatalf("open %s: %+v %v", payload, got, err)
		}
		return got
	}
	a, b := open(280, `{"file":"a"}`), open(281, `{"file":"b"}`)
	if a.Question == b.Question || a.Handle == b.Handle {
		t.Fatalf("opens with different payloads joined: %+v %+v", a, b)
	}
	if again := open(282, `{"file":"a"}`); again.Question != a.Question || again.Handle != a.Handle {
		t.Fatalf("an identical open did not join: %+v, first %+v", again, a)
	}
	run.f.g.later.mu.Lock()
	parkedA, parkedB := run.f.g.later.pending[a.Question], run.f.g.later.pending[b.Question]
	run.f.g.later.mu.Unlock()
	if parkedA == nil || parkedB == nil || parkedA.request.Payload != `{"file":"a"}` || parkedB.request.Payload != `{"file":"b"}` {
		t.Fatalf("parked payloads: %+v %+v", parkedA, parkedB)
	}
	answerOpen(t, run.permission, a.Question, "once")
	if o := run.note(t); o.Handle != a.Handle || o.Question != a.Question {
		t.Fatalf("the Allow on the first question performed %+v", o)
	}
	time.Sleep(300 * time.Millisecond)
	if run.launches.Load() != 1 || len(run.notes) != 0 {
		t.Fatalf("one Allow performed %d opens, notes %d", run.launches.Load(), len(run.notes))
	}
	if got := run.handleState(t, b.Handle); got.State != appmodel.OpenHandleStatePending {
		t.Fatalf("the other payload's open moved: %+v", got)
	}
	if record, found, err := run.permission.a.book.ApplicationRecord(b.Question); err != nil || !found || !record.Pending() {
		t.Fatalf("the other payload's question: found %v %v", found, err)
	}
}

// An open handle names its requester's open only: another program asking
// the runtime's Describe for it reads not_found, with no state.
func TestOpenLaterHandleOfAnotherCallerReadsNotFound(t *testing.T) {
	run := openLaterFixture(t)
	parked := run.park(t, 290)
	peer, recheck := applicationV3NativePeer(t)
	other := applicationV3Caller{subject: rwire.Subject{Account: run.f.subject.Account, Program: run.permission.cli.Program}, peer: peer, recheck: recheck}
	got := run.f.g.describeOpen(other, appmodel.OpenHandleQuery{OpenHandle: parked.Handle})
	if got.Outcome != applications.OutcomePage || got.Answer == nil || got.Answer.Outcome != appmodel.ReadingOutcomeNotFound || got.Answer.Reason != "open_handle" || got.Answer.Open != nil {
		t.Fatalf("another caller read the handle: %+v", got)
	}
	if own := run.describe(t, parked.Handle, ""); own.Open == nil || own.Open.State != appmodel.OpenHandleStatePending {
		t.Fatalf("the requester's own read: %+v", own)
	}
}

// 0.8.1 release blocker (architect ruling 2026-11-23): a 0.8.0 client's
// Activate of an open the person has not answered reads the published
// not_ready/consent answer, which its closed decoder accepts. The question
// stays up; an Open of the same request joins it and receives its question
// id and handle.
func TestPublished080ReaderDecodesAParkedActivate(t *testing.T) {
	published := loadPublished080(t)
	run := openLaterFixture(t)
	frames := &capturedFrames{exchange: run.f.raw}
	old := applications.NewApplicationsClient(frames)
	got, err := old.Activate(receiptKey(300, time.Now()), openRequest)
	if err != nil || got.Outcome != applications.ActivationOutcomeNotReady || got.Reason != "consent" {
		t.Fatalf("a published Activate of an unanswered open: %+v %v", got, err)
	}
	var refused []string
	published.read("ActivationResult", replyPayload(t, frames.last(t)), "ActivationResult", &refused)
	if len(refused) != 0 {
		t.Fatalf("a 0.8.0 reader refuses %v in %s", refused, frames.last(t))
	}
	joined, err := run.f.c.Open(receiptKey(301, time.Now()), applications.OpenRequest{Request: openRequest})
	if err != nil || joined.Result.Outcome != applications.ActivationOutcomeNotReady || joined.Question == "" || joined.Handle == "" {
		t.Fatalf("an Open joining the parked question: %+v %v", joined, err)
	}
	if record, found, err := run.permission.a.book.ApplicationRecord(joined.Question); err != nil || !found || !record.Pending() {
		t.Fatalf("the published Activate's question did not stay up: found %v %v", found, err)
	}
	if run.launches.Load() != 0 {
		t.Fatal("an unanswered open reached the start")
	}
}
