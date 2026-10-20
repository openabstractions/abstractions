package main

import (
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"testing"
)

func TestMessagesHeaderWakeHintQuestionPolicy(t *testing.T) {
	submitted := msg.TaskStateSubmitted
	original := msg.Header{Kind: msg.KindQuestion, ExpiresUnixMs: 3000, AnswerByUnixMs: 2000, TaskState: &submitted}
	cases := map[string]func(*msg.Header){
		"completed":      func(h *msg.Header) { v := msg.TaskStateCompleted; h.TaskState = &v },
		"canceled":       func(h *msg.Header) { v := msg.TaskStateCanceled; h.TaskState = &v },
		"failed":         func(h *msg.Header) { v := msg.TaskStateFailed; h.TaskState = &v },
		"rejected":       func(h *msg.Header) { v := msg.TaskStateRejected; h.TaskState = &v },
		"held":           func(h *msg.Header) { v := msg.TaskStateAuthRequired; h.TaskState = &v },
		"missing-state":  func(h *msg.Header) { h.TaskState = nil },
		"refused":        func(h *msg.Header) { h.Refused = true },
		"expired":        func(h *msg.Header) { h.ExpiresUnixMs = 1000 },
		"answer-expired": func(h *msg.Header) { h.AnswerByUnixMs = 1000 },
	}
	for name, alter := range cases {
		t.Run(name, func(t *testing.T) {
			h := original
			alter(&h)
			if pending, reply := messageWakeHints(h, 1000); pending != 0 || reply != 0 {
				t.Fatal(pending, reply)
			}
		})
	}
	if pending, reply := messageWakeHints(original, 1000); pending != 2000 || reply != 0 {
		t.Fatal(pending, reply)
	}
	original.Kind = msg.KindReply
	original.AnswerByUnixMs = 0
	original.TaskState = nil
	if pending, reply := messageWakeHints(original, 1000); pending != 0 || reply != 3000 {
		t.Fatal(pending, reply)
	}
	original.Refused = true
	if pending, reply := messageWakeHints(original, 1000); pending != 0 || reply != 0 {
		t.Fatal(pending, reply)
	}
	original.Refused = false
	original.ExpiresUnixMs = 1000
	if pending, reply := messageWakeHints(original, 1000); pending != 0 || reply != 0 {
		t.Fatal("expired reply", pending, reply)
	}
}

// Architect ruling B: an unacknowledged note wakes until it expires; refused
// and expired notes do not.
func TestMessagesHeaderWakeHintNotePolicy(t *testing.T) {
	note := msg.Header{Kind: msg.KindNote, ExpiresUnixMs: 3000}
	if pending, reply := messageWakeHints(note, 1000); pending != 3000 || reply != 0 {
		t.Fatal("note did not wake", pending, reply)
	}
	refused := note
	refused.Refused = true
	expired := note
	expired.ExpiresUnixMs = 1000
	for name, h := range map[string]msg.Header{"refused": refused, "expired": expired} {
		if pending, reply := messageWakeHints(h, 1000); pending != 0 || reply != 0 {
			t.Fatal(name, pending, reply)
		}
	}
}
