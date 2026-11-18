//go:build windows

package main

import (
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// A question that gives answer_within_ms longer than the default lifetime and
// no ttl_ms lives as long as its answer time; Prepare no longer answers
// "lifetime" for it.
func TestMessagesPrepareAnswerTimeWithoutLifetime(t *testing.T) {
	f := messagesObserveFixture(t)
	si, _ := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	messagesStateOpen(t, f, f.other, "other-agent", "target")
	within := int64(time.Hour / time.Millisecond)
	out, err := f.reader.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Body: "long question", AnswerWithinMs: within})
	if err != nil || out.Outcome != msg.OutcomePage || out.SendRequest == nil {
		t.Fatal(out, err)
	}
	if out.SendRequest.ExpiresUnixMs != f.now.UnixMilli()+within || out.SendRequest.AnswerByUnixMs != f.now.UnixMilli()+within {
		t.Fatal("answer time lifetime", out.SendRequest)
	}
	note := msg.KindNote
	if out, err := f.reader.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Kind: &note, Body: "note", AnswerWithinMs: 1000}); err != nil || out.Outcome != msg.OutcomeInvalid || out.Reason != "lifetime" {
		t.Fatal("a note with an answer time", out, err)
	}
}
