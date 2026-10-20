//go:build windows

package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesHeadersFinalCaptureUncertaintyRecoversButRevocationRefuses(t *testing.T) {
	f := messagesObserveFixture(t)
	// This test targets each final verification. The current image cache's
	// documented unreadable-stamp path verifies on every admission.
	f.run.d.agentStamp = func(string) (applicationAgentStamp, error) {
		return applicationAgentStamp{}, errors.New("fixture requires independent verification")
	}
	type observedCall struct{}
	reader := *f.reader
	reader.ctx = context.WithValue(reader.ctx, observedCall{}, true)
	read := f.run.d.agentImage
	if read == nil {
		read = readApplicationAgentImage
	}
	var calls, failAt atomic.Int32
	f.run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
		if ctx.Value(observedCall{}) == true {
			if n := calls.Add(1); failAt.Load() != 0 && n == failAt.Load() {
				return applicationAgentImage{}, errors.New("temporary image metadata read unavailable")
			}
		}
		return read(ctx, program, binding)
	}
	baseline := reader.headersWait("same-context", "", 0)
	if baseline.Outcome != msg.OutcomePage || calls.Load() < 2 {
		t.Fatal(baseline, calls.Load())
	}
	// The last image read belongs to the final receiving check, after the
	// snapshot and Gate recheck have both succeeded.
	failAt.Store(calls.Load())
	calls.Store(0)
	uncertain := reader.headersWait("same-context", "", 0)
	if uncertain.Outcome != msg.OutcomeUnavailable {
		t.Fatal("final capture uncertainty became an authority refusal", uncertain, calls.Load(), failAt.Load())
	}
	failAt.Store(0)
	if current := reader.headersWait("same-context", "", 0); current.Outcome != msg.OutcomePage {
		t.Fatal("same independently checked binding did not recover", current)
	}
	pin, err := reader.call.RetainProcess()
	if err != nil {
		t.Fatal(err)
	}
	until := pin.EvidenceValidUntil()
	_ = pin.Close()
	if !until.IsZero() {
		clock := f.run.d.now
		f.run.d.now = func() time.Time { return until }
		expired := reader.headersWait("same-context", "", 0)
		f.run.d.now = clock
		if expired.Outcome != msg.OutcomeUnavailable || len(expired.Headers) != 0 {
			t.Fatal("expired capture disclosed a page or claimed denial", expired)
		}
	}
	enrollment := f.run.d.readAgentEnrollment(f.run.ctx, f.run.f.panel, "native-agent")
	if enrollment.Enrollment == nil {
		t.Fatal(enrollment)
	}
	agent1478Set(t, f.run, "native-agent", enrollment.Enrollment.Revision, false)
	if revoked := reader.headersWait("same-context", "", 0); revoked.Outcome != msg.OutcomeForbidden || len(revoked.Headers) != 0 {
		t.Fatal("revoked enrollment recovered", revoked)
	}
}

func TestMessagesHeadersWatchPressureIsUnavailableWithoutRevoking(t *testing.T) {
	f := messagesObserveFixture(t)
	page := f.reader.headersWait("same-context", "", 0)
	if page.Outcome != msg.OutcomePage {
		t.Fatal(page)
	}
	for _, count := range []int{14, 63, 64} {
		h := f.reader.host
		h.messageWatchMu.Lock()
		h.messageWatches = make(map[*nativeMessageWatch]struct{}, count)
		for i := 0; i < count; i++ {
			h.messageWatches[&nativeMessageWatch{}] = struct{}{}
		}
		h.messageWatchMu.Unlock()
		got := f.reader.headersWait("same-context", page.Revision, 1)
		want := msg.OutcomePage
		if count == 64 {
			want = msg.OutcomeUnavailable
		}
		if got.Outcome != want {
			t.Fatal("watch pressure changed authority", count, got)
		}
		h.messageWatchMu.Lock()
		remaining := len(h.messageWatches)
		h.messageWatches = nil
		h.messageWatchMu.Unlock()
		if remaining != count || f.store.gate.Waiting() != 0 {
			t.Fatal("watch pressure leaked a slot", count, remaining, f.store.gate.Waiting())
		}
	}
	if got := f.reader.headersWait("same-context", "", 0); got.Outcome != msg.OutcomePage {
		t.Fatal("capacity relief did not recover", got)
	}
}
