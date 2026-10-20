package main

import (
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesSimpleReplyCanonicalChatReplayConflictAndDeadline(t *testing.T) {
	f := messagesObserveFixture(t)
	ai, chat := messagesTypedChatAt(t, f, f.reader, "native-agent", "reply-chat", "reply chat", "reply-source")
	bi, foreign := messagesTypedChatAt(t, f, f.reader, "native-agent", "foreign-chat", "other chat", "foreign-source")
	messagesStateOpen(t, f, f.other, "other-agent", "sender-source")
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "sender-source"}
	request := msg.SendRequest{To: "app:native-agent", Context: chat, Kind: msg.KindQuestion, Body: "original", IdempotencyKey: "canonical-owner", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli(), AnswerByUnixMs: f.now.Add(time.Minute).UnixMilli()}
	accepted := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
	if accepted.Outcome != msg.OutcomeSent {
		t.Fatal(accepted)
	}
	if out, err := f.reader.Reply(bi, foreign, accepted.MessageID, "answer"); err != nil || out.Outcome != msg.OutcomeForbidden {
		t.Fatal("foreign chat replied", out, err)
	}
	first, err := f.reader.Reply(ai, chat, accepted.MessageID, "answer")
	if err != nil || first.Outcome != msg.OutcomeSent {
		t.Fatal("canonical owner without prior body read", first, err)
	}
	if out, err := f.reader.Reply(ai, chat, accepted.MessageID, "answer"); err != nil || out.Outcome != msg.OutcomeSent || out.MessageID != first.MessageID {
		t.Fatal("same-body replay changed custody", out, err)
	}
	if out, err := f.reader.Reply(ai, chat, accepted.MessageID, "different answer"); err != nil || out.Outcome != msg.OutcomeConflict {
		t.Fatal("answered question accepted a changed body", out, err)
	}
	request.IdempotencyKey = "deadline-owner"
	request.Body = "distinct deadline question"
	late := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
	if late.Outcome != msg.OutcomeSent || late.MessageID == accepted.MessageID {
		t.Fatal(late)
	}
	*f.now = f.now.Add(2 * time.Minute)
	if out, err := f.reader.Reply(ai, chat, late.MessageID, "late answer"); err != nil || out.Outcome == msg.OutcomeSent || out.Outcome == msg.OutcomeUnknown {
		t.Fatal("deadline did not refuse a new reply", out, err)
	}
}
