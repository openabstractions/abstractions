package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	awire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// S1 (architect ruling 2026-11-09): a same-app message is budgeted per
// directed chat pair. An exhausted chat pair leaves the app's other pairs
// open, and its second refusal pauses only that pair.
func TestSameAppBudgetPerChatPair(t *testing.T) {
	ctx := context.Background()
	s, now, _, r := messagesStoreFixture(t)
	chat := func(name string) messageActor {
		return messageActor{Account: "account", Program: "native-claude", Application: "claude", Context: name}
	}
	a, b, c := chat("chat-a"), chat("chat-b"), chat("chat-c")
	send := func(from messageActor, to, key string) msg.SendResult {
		t.Helper()
		q := r
		q.To, q.Context, q.IdempotencyKey, q.Body = "app:claude", to, key, key
		q.ExpiresUnixMs = now.Add(time.Hour).UnixMilli()
		return s.accept(ctx, from, "native-claude", q, false, "")
	}
	for i := 0; i < 20; i++ {
		if got := send(a, "chat-b", fmt.Sprintf("a-b-%d", i)); got.Outcome != msg.OutcomeSent {
			t.Fatal("a→b", i, got)
		}
	}
	if got := send(a, "chat-b", "a-b-over-1"); got.Outcome != msg.OutcomeExhausted {
		t.Fatal("a→b past its budget", got)
	}
	for _, step := range []struct {
		from messageActor
		to   string
	}{{a, "chat-c"}, {c, "chat-b"}, {b, "chat-a"}} {
		if got := send(step.from, step.to, step.from.Context+"-"+step.to); got.Outcome != msg.OutcomeSent {
			t.Fatal("another chat pair refused", step.from.Context, step.to, got)
		}
	}
	if got := send(a, "chat-b", "a-b-over-2"); got.Outcome != msg.OutcomeExhausted || got.RetryAfterMs != (8*time.Hour).Milliseconds() {
		t.Fatal("second refusal did not pause", got)
	}
	p := messagesReadPair(t, s, a, "native-claude")
	if p.paused(now.UnixMilli()) || len(p.Chats) != 4 {
		t.Fatal("pause left the chat pair", p)
	}
	paused := 0
	for _, chatBudget := range p.Chats {
		if chatBudget.paused(now.UnixMilli()) {
			paused++
			if chatBudget.Scope != messageBudgetScope(a, "native-claude", msg.SendRequest{Context: "chat-b"}) {
				t.Fatal("the wrong chat pair paused", chatBudget)
			}
		}
	}
	if paused != 1 {
		t.Fatal("paused chat pairs", paused)
	}
	if got := send(a, "chat-c", "a-c-2"); got.Outcome != msg.OutcomeSent {
		t.Fatal("a paused pair stopped another", got)
	}
	// A different app pair keeps the program-pair budget.
	if scope := messageBudgetScope(messageActor{Account: "account", Program: "native-codex", Context: "chat-x"}, "native-claude", msg.SendRequest{Context: "chat-b"}); scope != "" {
		t.Fatal("a cross-app pair got a chat scope", scope)
	}
}

// B1: the person's Resume of a paused chat pair admits its next send within
// the same hour.
func TestSameAppResumeWithinTheHour(t *testing.T) {
	ctx := context.Background()
	h, now, _, q := messagesNoticeFixture(t)
	*now = now.Truncate(time.Hour).Add(time.Minute)
	a := messageActor{Account: "account", Program: "native-target", Application: "target", Context: "chat-a"}
	q.To, q.Context, q.Kind, q.AnswerByUnixMs = "app:target", "chat-b", msg.KindNote, 0
	q.ExpiresUnixMs = now.Add(time.Hour).UnixMilli()
	send := func(key string) msg.SendResult {
		q.IdempotencyKey, q.Body = key, key
		if err := h.resumeMessageBudget(ctx, a, "native-target", q); err != nil {
			t.Fatal(err)
		}
		return h.messages.accept(ctx, a, "native-target", q, false, "")
	}
	for i := 0; i < 20; i++ {
		if got := send(fmt.Sprintf("fill-%d", i)); got.Outcome != msg.OutcomeSent {
			t.Fatal(i, got)
		}
	}
	for i := 0; i < 2; i++ {
		if got := send(fmt.Sprintf("over-%d", i)); got.Outcome != msg.OutcomeExhausted {
			t.Fatal(got)
		}
	}
	if _, err := h.progressPairNotification(ctx, a, "native-target", q); err != nil {
		t.Fatal(err)
	}
	scope := messageBudgetScope(a, "native-target", q)
	until := messagesReadPair(t, h.messages, a, "native-target").peekBudget(scope, now.UnixMilli()).PausedUntil
	ref := messagePauseRef(a, "native-target", scope, until)
	rows, _, err := h.messageNotices.book.NotificationHistory()
	if err != nil || len(rows) != 1 || rows[0].Request.RequestRef.Key != ref.Key {
		t.Fatal("chat pair pause notice", rows, err)
	}
	if rows[0].Request.Body != "“target chat” sent “target chat” 20 messages in an hour. Resumes in 8 hours." {
		t.Fatal("chat pair notice words", rows[0].Request.Body)
	}
	if decided, err := h.messageNotices.book.DecideNotification(rows[0].ID, asks.NotificationAct, "resume", filepath.Join(t.TempDir(), "panel.exe"), nil); err != nil || decided.Outcome != awire.NotificationDecisionOutcomeActed {
		t.Fatal(decided, err)
	}
	if got := send("resumed"); got.Outcome != msg.OutcomeSent {
		t.Fatal("resumed chat pair refused within the hour", got)
	}
}

// B3: the 65th chat pair never resets an active pair's budget. It waits for
// the next hour, when a pair idle that hour and unrefused today gives way.
func TestSameAppChatPairsFullKeepsActiveBudgets(t *testing.T) {
	ctx := context.Background()
	s, now, _, r := messagesStoreFixture(t)
	*now = now.Truncate(time.Hour).Add(time.Minute)
	a := messageActor{Account: "account", Program: "native-claude", Application: "claude", Context: "chat-a"}
	send := func(to, key string) msg.SendResult {
		t.Helper()
		q := r
		q.To, q.Context, q.IdempotencyKey, q.Body = "app:claude", to, key, key
		q.ExpiresUnixMs = now.Add(time.Hour).UnixMilli()
		return s.accept(ctx, a, "native-claude", q, false, "")
	}
	for i := 0; i < 20; i++ {
		if got := send("chat-0", fmt.Sprintf("full-%d", i)); got.Outcome != msg.OutcomeSent {
			t.Fatal(i, got)
		}
	}
	if got := send("chat-0", "refused"); got.Outcome != msg.OutcomeExhausted {
		t.Fatal(got)
	}
	for i := 1; i < messageChatBudgets; i++ {
		if got := send(fmt.Sprintf("chat-%d", i), fmt.Sprintf("one-%d", i)); got.Outcome != msg.OutcomeSent {
			t.Fatal(i, got)
		}
	}
	got := send("chat-new", "sixty-fifth")
	if got.Outcome != msg.OutcomeExhausted || got.Reason != messageChatPairsFull || got.RetryAfterMs != (59*time.Minute).Milliseconds() {
		t.Fatal("the 65th chat pair", got)
	}
	if got := send("chat-0", "still-full"); got.Outcome != msg.OutcomeExhausted {
		t.Fatal("an active chat pair's budget was reset", got)
	}
	// Next hour: an idle unrefused pair gives way; the refused one stays.
	*now = now.Add(time.Hour)
	if got := send("chat-new", "next-hour"); got.Outcome != msg.OutcomeSent {
		t.Fatal("no idle pair gave way", got)
	}
	p := messagesReadPair(t, s, a, "native-claude")
	kept := false
	for _, c := range p.Chats {
		kept = kept || c.Scope == messageBudgetScope(a, "native-claude", msg.SendRequest{Context: "chat-0"}) && c.Exhaustions > 0
	}
	if !kept || len(p.Chats) != messageChatBudgets {
		t.Fatal("the refused pair was dropped", len(p.Chats))
	}
}
