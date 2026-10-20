//go:build windows

package main

import (
	"strings"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Two Claude chats in one project folder: by distinct session titles each
// resolves alone; with one shared title the choices list distinct titles,
// and a listed title or exact context prepares a complete question that Send
// accepts.
func TestMessagesPrepareQuestionToOneOfTwoChatsInOneFolder(t *testing.T) {
	f := messagesObserveFixture(t)
	// Every chat opens at one instant, so chats sharing a title are counted.
	opened := f.run.d.now()
	f.run.d.now = func() time.Time { return opened }
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	_, architect := messagesTypedChat(t, f, f.other, "other-agent", "chat-architect", "OpenAbstractions architect (fork)")
	_, worker := messagesTypedChat(t, f, f.other, "other-agent", "chat-worker", "AI abstractions")
	prepare := func(in msg.PrepareRequest) msg.PrepareResult {
		t.Helper()
		in.Receiver, in.Body = "app:other-agent", "Which branch holds the fix?"
		out, err := sender.Prepare(si, "source", in)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	complete := func(out msg.PrepareResult, context string) msg.SendRequest {
		t.Helper()
		if out.Outcome != msg.OutcomePage || out.SendRequest == nil {
			t.Fatal("prepare did not resolve", out)
		}
		request := *out.SendRequest
		if request.To != "app:other-agent" || request.Context != context || request.Kind != msg.KindQuestion || request.IdempotencyKey == "" || request.ExpiresUnixMs <= f.now.UnixMilli() || request.AnswerByUnixMs <= f.now.UnixMilli() || request.AnswerByUnixMs > request.ExpiresUnixMs {
			t.Fatal("incomplete question arguments", request)
		}
		return request
	}
	complete(prepare(msg.PrepareRequest{ContextTitle: "OpenAbstractions architect (fork)"}), architect)
	complete(prepare(msg.PrepareRequest{ContextTitle: "AI abstractions"}), worker)

	_, second := messagesTypedChat(t, f, f.other, "other-agent", "chat-second", "AI abstractions")
	ambiguous := prepare(msg.PrepareRequest{ContextTitle: "AI abstractions"})
	if ambiguous.Outcome != msg.OutcomeConflict || ambiguous.Reason != "ambiguous_receiver" || len(ambiguous.Choices) != 2 {
		t.Fatal(ambiguous)
	}
	listed := map[string]string{}
	for _, choice := range ambiguous.Choices {
		if !strings.HasPrefix(choice.ChatTitle, "AI abstractions · ") {
			t.Fatal("choice title is not distinct", choice)
		}
		listed[choice.Context] = choice.ChatTitle
	}
	if len(listed) != 2 || listed[worker] == listed[second] {
		t.Fatal(listed)
	}
	// A counted name can pass to the other chat: preparing by it offers the
	// choices, and the chosen choice's context selects it.
	if !strings.HasSuffix(listed[worker], " (2)") && !strings.HasSuffix(listed[second], " (2)") {
		t.Fatal("same-instant chats are not counted", listed)
	}
	for _, context := range []string{worker, second} {
		again := prepare(msg.PrepareRequest{ContextTitle: listed[context]})
		if again.Outcome != msg.OutcomeConflict || again.Reason != "ambiguous_receiver" || again.SendRequest != nil || len(again.Choices) != 2 {
			t.Fatal("a counted name selected a chat on its own", again)
		}
	}
	byTitle := complete(prepare(msg.PrepareRequest{Context: second, ContextTitle: listed[second]}), second)
	complete(prepare(msg.PrepareRequest{Context: worker}), worker)
	sent, err := sender.Send(si, "source", byTitle)
	if err != nil || sent.Outcome != msg.OutcomeSent || sent.MessageID == "" {
		t.Fatal(sent, err)
	}
}

// A hand-built Send that misses or misstates a field is refused with one
// sentence naming that field.
func TestMessagesSendInvalidNamesTheField(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	messagesStateOpen(t, f, f.other, "other-agent", "target")
	now := f.now.UnixMilli()
	base := msg.SendRequest{To: "app:other-agent", Context: "target", Kind: msg.KindQuestion, Body: "question", IdempotencyKey: "key", ExpiresUnixMs: now + 600000, AnswerByUnixMs: now + 60000}
	cases := []struct {
		field  string
		change func(*msg.SendRequest)
	}{
		{"a question needs answer_by_unix_ms", func(r *msg.SendRequest) { r.AnswerByUnixMs = 0 }},
		{"answer_by_unix_ms", func(r *msg.SendRequest) { r.AnswerByUnixMs = r.ExpiresUnixMs + 1 }},
		{"answer_by_unix_ms", func(r *msg.SendRequest) { r.AnswerByUnixMs = now }},
		{"expires_unix_ms", func(r *msg.SendRequest) { r.ExpiresUnixMs = now / 1000 }},
		{"context", func(r *msg.SendRequest) { r.Context = "not a context" }},
		{"to", func(r *msg.SendRequest) { r.To = "other-agent" }},
		{"subject", func(r *msg.SendRequest) { r.Subject = "two\nlines" }},
	}
	for _, c := range cases {
		request := base
		c.change(&request)
		out, err := sender.Send(si, "source", request)
		if err != nil || out.Outcome != msg.OutcomeInvalid || !strings.Contains(out.Reason, c.field) || len(out.Reason) > 64 {
			t.Fatal(c.field, out, err)
		}
		if _, err := msg.DecodeSendResult(msg.EncodeSendResult(&out)); err != nil {
			t.Fatal(err)
		}
	}
	note := base
	note.Kind, note.AnswerByUnixMs, note.IdempotencyKey = msg.KindNote, 0, "note-key"
	if out, err := sender.Send(si, "source", note); err != nil || out.Outcome != msg.OutcomeSent {
		t.Fatal(out, err)
	}
	if out, err := sender.Send(si, "source", base); err != nil || out.Outcome != msg.OutcomeSent {
		t.Fatal(out, err)
	}
}
