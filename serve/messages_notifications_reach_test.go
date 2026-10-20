package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// A present receiver with an open inbox, as the owner's Codex helper held one
// without a bound thread (2026-11-01).
func messagesReachNoticeFixture(t *testing.T, application string) (*applicationsHost, *time.Time, messageActor, msg.SendRequest, string) {
	t.Helper()
	h, now, actor, q := messagesNoticeFixture(t)
	receiver := filepath.Join(t.TempDir(), application+".exe")
	if err := os.WriteFile(receiver, []byte("inert identity fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	h.directory.instances["native"] = applicationLease{application: application, owner: rights.Subject{Program: receiver}, deadline: now.Add(time.Hour)}
	h.directory.descriptors = map[string]wire.ApplicationDescriptor{application: {Name: application, Title: "Codex Desktop", Program: receiver}, actor.Application: {Name: actor.Application, Title: "Claude Code", Program: actor.Program}}
	q.To, q.Context = "app:"+application, "chat"
	return h, now, actor, q, receiver
}

func messagesReachNotices(t *testing.T, h *applicationsHost, actor messageActor, receiver string, q msg.SendRequest, count int) int {
	t.Helper()
	ctx := context.Background()
	for i := 0; i < count; i++ {
		q.IdempotencyKey = fmt.Sprintf("reach-%d", i)
		q.Body = q.IdempotencyKey
		if got := h.messages.accept(ctx, actor, receiver, q, false, ""); got.Outcome != msg.OutcomeSent {
			t.Fatal(got)
		}
		key := h.messages.key(actor, q.IdempotencyKey)
		r, _ := h.messages.readRecord(ctx, key)
		if err := h.progressMessageNotifications(ctx, key, r); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, err := h.messageNotices.book.NotificationHistory()
	if err != nil {
		t.Fatal(err)
	}
	return len(rows)
}

func TestMessagesNotificationsCodexInboxWithoutWatchIsNotRunning(t *testing.T) {
	h, _, actor, q, receiver := messagesReachNoticeFixture(t, "oa-codex-integration")
	if got := messagesReachNotices(t, h, actor, receiver, q, 3); got != 1 {
		t.Fatal("one waiting notice per receiver per hour", got)
	}
	rows, _, _ := h.messageNotices.book.NotificationHistory()
	body := rows[0].Request.Body
	if !strings.Contains(body, "“Codex Desktop”") || !strings.Contains(body, "“Claude Code”") || strings.Contains(body, "oa-codex-integration") {
		t.Fatal("notice names the registered applications", body)
	}
}

func TestMessagesNotificationsWatchedOrClaudeReceiverSuppresses(t *testing.T) {
	for _, mode := range []string{"watched", "watch-ended-recently", "claude", "other-context-watched", "watch-ended-long-ago"} {
		t.Run(mode, func(t *testing.T) {
			application := "oa-codex-integration"
			if mode == "claude" {
				application = "oa-claude-integration"
			}
			h, now, actor, q, receiver := messagesReachNoticeFixture(t, application)
			watched := "chat"
			if mode == "other-context-watched" {
				watched = "another-chat"
			}
			switch mode {
			case "watched", "other-context-watched":
				h.messageWatches = map[*nativeMessageWatch]struct{}{{actor: messageActor{Program: receiver, Context: watched}, ctx: context.Background()}: {}}
			case "watch-ended-recently":
				h.messageWatchEnds = map[string]time.Time{receiver + "\x00chat": now.Add(-messageWatchGrace / 2)}
			case "watch-ended-long-ago":
				h.messageWatchEnds = map[string]time.Time{receiver + "\x00chat": now.Add(-messageWatchGrace)}
			}
			want := 0
			if mode == "other-context-watched" || mode == "watch-ended-long-ago" {
				want = 1
			}
			if got := messagesReachNotices(t, h, actor, receiver, q, 1); got != want {
				t.Fatal(mode, got, want)
			}
		})
	}
}

func TestMessagesInboxReachQueuedForUnwatchedCodexInbox(t *testing.T) {
	h := &applicationsHost{directory: &applicationDirectory{now: time.Now}}
	program := filepath.Join(t.TempDir(), "oa-codex-integration.exe")
	if err := os.WriteFile(program, []byte("inert identity fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		application string
		watching    bool
		want        msg.InboxReach
	}{
		{"oa-codex-integration", false, msg.InboxReachQueued},
		{"oa-codex-integration", true, msg.InboxReachOpen},
		{"oa-claude-integration", false, msg.InboxReachOpen},
	}
	for _, c := range cases {
		if got := h.messageInboxReach(c.application, program, "chat", "", c.watching); got != c.want {
			t.Fatal(c, got)
		}
	}
	h.messageWatchEnds = map[string]time.Time{program + "\x00chat": time.Now()}
	if got := h.messageInboxReach("oa-codex-integration", program, "chat", "", false); got != msg.InboxReachOpen {
		t.Fatal("a waker between two watches is still reached", got)
	}
}

func TestMessagesInboxReachQueuedForUnconfirmedCodexWake(t *testing.T) {
	h := &applicationsHost{directory: &applicationDirectory{now: time.Now}}
	program := filepath.Join(t.TempDir(), "oa-codex-integration.exe")
	status := "Watching | " + messageWakeUnconfirmedStatus + " | receiver:busy"
	if got := h.messageInboxReach("oa-codex-integration", program, "chat", status, true); got != msg.InboxReachQueued {
		t.Fatal("a watched Codex chat whose wake is unconfirmed reads as reached", got)
	}
	if got := h.messageInboxReach("oa-codex-integration", program, "chat", "Watching | receiver:busy", true); got != msg.InboxReachOpen {
		t.Fatal("a briefly held pointer alone changed reach", got)
	}
	if got := h.messageInboxReach("oa-claude-integration", program, "chat", status, true); got != msg.InboxReachOpen {
		t.Fatal("only the Codex receiver's claim changes reach", got)
	}
}
