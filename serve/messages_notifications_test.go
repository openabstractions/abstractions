package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	awire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func messagesNoticeFixture(t *testing.T) (*applicationsHost, *time.Time, messageActor, msg.SendRequest) {
	t.Helper()
	s, now, actor, request := messagesStoreFixture(t)
	*now = time.Now()
	request.Kind = msg.KindQuestion
	request.ExpiresUnixMs = now.Add(time.Hour).UnixMilli()
	request.AnswerByUnixMs = now.Add(30 * time.Minute).UnixMilli()
	a, book := firstUseBook(t)
	a.rights = testRights(t, t.TempDir())
	if err := a.rights.policy.RegisterAction(notificationPostAction); err != nil {
		t.Fatal(err)
	}
	if err := a.rights.policy.Set(a.rights.self(), notificationPostAction, notificationPostResource, true); err != nil {
		t.Fatal(err)
	}
	a.book = book
	d := &applicationDirectory{instances: map[string]applicationLease{}, now: s.now}
	h := &applicationsHost{messages: s, messageNotices: a, directory: d}
	return h, now, actor, request
}

func TestMessagesNotificationsWaitingCoalescesReceiverHourAndReplays(t *testing.T) {
	ctx := context.Background()
	h, _, actor, request := messagesNoticeFixture(t)
	for i := 0; i < 3; i++ {
		a := actor
		a.Application = fmt.Sprintf("source-%d", i)
		a.Program = fmt.Sprintf("native-source-%d", i)
		q := request
		q.IdempotencyKey = fmt.Sprintf("key-%d", i)
		q.Body = fmt.Sprintf("private body-%d", i)
		q.AnswerByUnixMs += int64(i)
		result := h.messages.accept(ctx, a, "native-target", q, false, "")
		if result.Outcome != msg.OutcomeSent {
			t.Fatal(result)
		}
		key := h.messages.key(a, q.IdempotencyKey)
		r, _ := h.messages.readRecord(ctx, key)
		if err := h.progressMessageNotifications(ctx, key, r); err != nil {
			t.Fatal(err)
		}
		if err := h.progressMessageNotifications(ctx, key, r); err != nil {
			t.Fatal(err)
		}
	}
	rows, _, err := h.messageNotices.book.NotificationHistory()
	if err != nil || len(rows) != 1 || rows[0].Program != h.messageNotices.rights.self().Program || len(rows[0].Request.Actions) != 0 {
		t.Fatal(rows, err)
	}
	if rows[0].Request.Title != "Waiting for an answer" {
		t.Fatal(rows)
	}
}

func TestMessagesNotificationsNotesRunningTerminalAndBlock(t *testing.T) {
	for _, mode := range []string{"note", "running", "terminal", "block"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			h, now, actor, q := messagesNoticeFixture(t)
			receiver := filepath.Join(t.TempDir(), "native-target.exe")
			if err := os.WriteFile(receiver, []byte("inert identity fixture"), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "note" {
				q.Kind = msg.KindNote
				q.AnswerByUnixMs = 0
			}
			if mode == "running" {
				h.directory.instances["native"] = applicationLease{owner: rights.Subject{Program: receiver}, deadline: now.Add(time.Minute)}
			}
			if mode == "block" {
				if err := h.messageNotices.rights.policy.Set(h.messageNotices.rights.self(), notificationPostAction, notificationPostResource, false); err != nil {
					t.Fatal(err)
				}
			}
			result := h.messages.accept(ctx, actor, receiver, q, false, "")
			if result.Outcome != msg.OutcomeSent {
				t.Fatal(result)
			}
			key := h.messages.key(actor, q.IdempotencyKey)
			r, _ := h.messages.readRecord(ctx, key)
			if mode == "terminal" {
				if err := h.messages.transitionState(ctx, key, r, "cancel"); err != nil {
					t.Fatal(err)
				}
			}
			if err := h.progressMessageNotifications(ctx, key, r); err != nil {
				t.Fatal(err)
			}
			rows, _, err := h.messageNotices.book.NotificationHistory()
			if err != nil || len(rows) != 0 {
				t.Fatal(rows, err)
			}
		})
	}
}

// S2 (architect ruling 2026-11-09): the second refusal pauses the pair for 8
// hours and posts one notice naming it with Resume; the person's Resume
// admits the next send; the pause also ends on its own after 8 hours.
func TestMessagePausePostsResumeAndExpires(t *testing.T) {
	ctx := context.Background()
	h, now, actor, q := messagesNoticeFixture(t)
	// Every step below the Resume runs within one hour.
	*now = now.Truncate(time.Hour).Add(time.Minute)
	q.ExpiresUnixMs, q.AnswerByUnixMs = now.Add(time.Hour).UnixMilli(), now.Add(30*time.Minute).UnixMilli()
	fill := func(prefix string) {
		t.Helper()
		for i := 0; i < 20; i++ {
			q.IdempotencyKey = fmt.Sprintf("%s-%d", prefix, i)
			q.Body = q.IdempotencyKey
			if got := h.messages.accept(ctx, actor, "native-target", q, false, ""); got.Outcome != msg.OutcomeSent {
				t.Fatal(prefix, i, got)
			}
		}
		for i := 0; i < 2; i++ {
			q.IdempotencyKey = fmt.Sprintf("%s-overflow-%d", prefix, i)
			q.Body = q.IdempotencyKey
			if got := h.messages.accept(ctx, actor, "native-target", q, false, ""); got.Outcome != msg.OutcomeExhausted {
				t.Fatal(got)
			}
			if _, err := h.progressPairNotification(ctx, actor, "native-target", q); err != nil {
				t.Fatal(err)
			}
		}
	}
	fill("first")
	if _, err := h.progressPairNotification(ctx, actor, "native-target", q); err != nil {
		t.Fatal(err)
	}
	rows, _, err := h.messageNotices.book.NotificationHistory()
	if err != nil || len(rows) != 1 || rows[0].State != awire.NotificationStatePosted {
		t.Fatal(rows, err)
	}
	notice := rows[0].Request
	if notice.Title != "Messages paused" || notice.Body != "“codex” sent “claude” 20 messages in an hour. Resumes in 8 hours." || len(notice.Actions) != 1 || notice.Actions[0].ID != "resume" || notice.Actions[0].Title != "Resume" {
		t.Fatal("pause notice", notice)
	}
	pause := messagesReadPair(t, h.messages, actor, "native-target").PausedUntil
	if pause != now.Add(8*time.Hour).UnixMilli() || notice.ExpiresMs != pause {
		t.Fatal("pause length", pause, notice.ExpiresMs)
	}
	// Before the person acts, the pause holds.
	q.IdempotencyKey, q.Body = "still-paused", "still-paused"
	if err := h.resumeMessageBudget(ctx, actor, "native-target", q); err != nil {
		t.Fatal(err)
	}
	if got := h.messages.accept(ctx, actor, "native-target", q, false, ""); got.Outcome != msg.OutcomeExhausted || got.RetryAfterMs != 8*time.Hour.Milliseconds() {
		t.Fatal("paused pair admitted", got)
	}
	// B1: the person's Resume, recorded by the Act owner, admits the next
	// sends within the same hour; the hour's recount starts at the Resume.
	*now = now.Add(time.Minute)
	if decided, err := h.messageNotices.book.DecideNotification(rows[0].ID, asks.NotificationAct, "resume", filepath.Join(t.TempDir(), "panel.exe"), nil); err != nil || decided.Outcome != awire.NotificationDecisionOutcomeActed {
		t.Fatal(decided, err)
	}
	for i := 0; i < 2; i++ {
		q.IdempotencyKey, q.Body = fmt.Sprintf("resumed-%d", i), fmt.Sprintf("resumed-%d", i)
		if err := h.resumeMessageBudget(ctx, actor, "native-target", q); err != nil {
			t.Fatal(err)
		}
		if got := h.messages.accept(ctx, actor, "native-target", q, false, ""); got.Outcome != msg.OutcomeSent {
			t.Fatal("resumed pair refused within the hour", i, got)
		}
	}
	// Dismiss is no Resume.
	*now = now.Add(time.Hour)
	q.ExpiresUnixMs, q.AnswerByUnixMs = now.Add(time.Hour).UnixMilli(), now.Add(30*time.Minute).UnixMilli()
	fill("dismissed")
	second := messagesReadPair(t, h.messages, actor, "native-target").PausedUntil
	secondRef := messagePauseRef(actor, "native-target", "", second)
	all, _, err := h.messageNotices.book.NotificationHistory()
	if err != nil {
		t.Fatal(err)
	}
	dismissed := false
	for _, row := range all {
		if row.Request.RequestRef.Key == secondRef.Key {
			decided, err := h.messageNotices.book.DecideNotification(row.ID, asks.NotificationDismiss, "", filepath.Join(t.TempDir(), "panel.exe"), nil)
			dismissed = err == nil && decided.Outcome == awire.NotificationDecisionOutcomeDismissed
		}
	}
	if !dismissed {
		t.Fatal("second pause notice not dismissed", all)
	}
	q.IdempotencyKey, q.Body = "after-dismiss", "after-dismiss"
	if err := h.resumeMessageBudget(ctx, actor, "native-target", q); err != nil {
		t.Fatal(err)
	}
	if got := h.messages.accept(ctx, actor, "native-target", q, false, ""); got.Outcome != msg.OutcomeExhausted {
		t.Fatal("Dismiss resumed the pair", got)
	}
	// The pause ends on its own after 8 hours.
	*now = now.Add(8 * time.Hour)
	q.IdempotencyKey, q.Body = "after-pause", "after-pause"
	q.ExpiresUnixMs, q.AnswerByUnixMs = now.Add(time.Hour).UnixMilli(), now.Add(30*time.Minute).UnixMilli()
	if got := h.messages.accept(ctx, actor, "native-target", q, false, ""); got.Outcome != msg.OutcomeSent {
		t.Fatal("expired pause still refuses", got)
	}
}

// A pause written before PausedUntil existed ends at the end of its day.
func TestMessageLegacyPauseEndsWithItsDay(t *testing.T) {
	day := time.Date(2026, 11, 9, 0, 0, 0, 0, time.UTC)
	b := messageBudget{Paused: true, Day: day.UnixMilli() / messageDayMs, Exhaustions: 2}
	b.roll(day.Add(20 * time.Hour).UnixMilli())
	if !b.paused(day.Add(20*time.Hour).UnixMilli()) || b.PausedUntil != day.Add(24*time.Hour).UnixMilli() || b.Paused {
		t.Fatal("legacy pause", b)
	}
	b.roll(day.Add(25 * time.Hour).UnixMilli())
	if b.paused(day.Add(25*time.Hour).UnixMilli()) || b.Exhaustions != 0 {
		t.Fatal("legacy pause did not end", b)
	}
}

func TestMessagesAuditCanonicalTaskOutcomeUpdates(t *testing.T) {
	ctx := context.Background()
	s, _, actor, q := messagesStoreFixture(t)
	q.Kind = msg.KindQuestion
	q.AnswerByUnixMs = q.ExpiresUnixMs
	h := &applicationsHost{messages: s}
	sink := &messageAuditSink{}
	if err := h.configureMessageAudit(sink); err != nil {
		t.Fatal(err)
	}
	defer h.messageAudit.close()
	got := s.accept(ctx, actor, "native-target", q, true, "")
	if got.Outcome != msg.OutcomeSent {
		t.Fatal(got)
	}
	key := s.key(actor, q.IdempotencyKey)
	r, _ := s.readRecord(ctx, key)
	for _, op := range []string{"", "release", "complete"} {
		if op != "" {
			if err := s.transitionState(ctx, key, r, op); err != nil {
				t.Fatal(err)
			}
		}
		if err := h.flushMessageAudit(ctx, key, r.Token); err != nil {
			t.Fatal(err)
		}
		if err := h.flushMessageAudit(ctx, key, r.Token); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.records) != 3 {
		t.Fatal(len(sink.records))
	}
	for i, want := range []string{"awaiting_permission", "submitted", "completed"} {
		a := sink.records[i].Attrs
		if a["outcome"] != want || a["message_id"] != got.MessageID || a["content_hmac"] != sink.records[0].Attrs["content_hmac"] {
			t.Fatal(a)
		}
	}
}

func TestMessagesNotificationsLostPostReplyUsesImmutableRequest(t *testing.T) {
	ctx := context.Background()
	h, _, actor, q := messagesNoticeFixture(t)
	got := h.messages.accept(ctx, actor, "native-target", q, false, "")
	if got.Outcome != msg.OutcomeSent {
		t.Fatal(got)
	}
	key := h.messages.key(actor, q.IdempotencyKey)
	r, _ := h.messages.readRecord(ctx, key)
	h.messages.boundary = func(at string) error {
		if at == "notification_posted" {
			return errors.New("inert lost reply")
		}
		return nil
	}
	if err := h.progressMessageNotifications(ctx, key, r); err == nil {
		t.Fatal("missing lost reply")
	}
	h.messages.boundary = nil
	if err := h.progressMessageNotifications(ctx, key, r); err != nil {
		t.Fatal(err)
	}
	rows, _, err := h.messageNotices.book.NotificationHistory()
	if err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	current, err := h.messages.readRecord(ctx, key)
	if err != nil || !current.WaitingNotice.Settled || current.WaitingNotice.NotificationID != rows[0].ID {
		t.Fatal(current, err)
	}
}

func TestMessagesNotificationsRollingBoundaryAndConcurrentReservation(t *testing.T) {
	s, now, _, _ := messagesStoreFixture(t)
	*now = time.Date(2026, 10, 27, 12, 59, 59, 0, time.UTC)
	ctx := context.Background()
	notice := messageNotice{ReceiverProgram: "native-target", Request: awire.NotificationRequest{RequestRef: awire.RequestRef{Key: "notice", ReplayUntilMs: now.Add(2 * time.Hour).UnixMilli()}, Title: "Waiting for an answer", Body: "fixture", Delivery: awire.NotificationDeliveryNow, ExpiresMs: now.Add(time.Hour).UnixMilli(), Actions: []awire.NotificationAction{}}}
	key, token := messageDigest("first"), strings.Repeat("1", 48)
	_, owned, err := s.reserveReceiverNotice(ctx, "local-account", "native-target", key, token, notice)
	if err != nil || !owned {
		t.Fatal(owned, err)
	}
	*now = now.Add(2 * time.Second)
	if _, owned, err = s.reserveReceiverNotice(ctx, "local-account", "native-target", messageDigest("second"), token, notice); err != nil || owned {
		t.Fatal("UTC rollover reset rolling budget", owned, err)
	}
	replay, owned, err := s.reserveReceiverNotice(ctx, "local-account", "native-target", key, token, notice)
	if err != nil || !owned || replay.Request.RequestRef != notice.Request.RequestRef {
		t.Fatal(replay, owned, err)
	}
	*now = now.Add(time.Hour)
	var wg sync.WaitGroup
	winners := make(chan bool, 12)
	failures := make(chan error, 12)
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, ok, err := s.reserveReceiverNotice(ctx, "local-account", "native-target", messageDigest(fmt.Sprint(i)), token, notice)
			winners <- ok
			failures <- err
		}(i)
	}
	wg.Wait()
	close(winners)
	close(failures)
	count := 0
	for winner := range winners {
		if winner {
			count++
		}
	}
	for err := range failures {
		if err != nil {
			t.Fatal(err)
		}
	}
	if count != 1 {
		t.Fatal("concurrent receiver budget", count)
	}
}

func TestMessagesAuditTerminalOutcomeSurvivesLogGapAndExpiry(t *testing.T) {
	for _, operation := range []string{"complete", "cancel", "fail", "refuse"} {
		t.Run(operation, func(t *testing.T) {
			ctx := context.Background()
			s, now, actor, q := messagesStoreFixture(t)
			q.Kind = msg.KindQuestion
			q.AnswerByUnixMs = q.ExpiresUnixMs
			h := &applicationsHost{messages: s}
			sink := &messageAuditSink{}
			if err := h.configureMessageAudit(sink); err != nil {
				t.Fatal(err)
			}
			defer h.messageAudit.close()
			got := s.accept(ctx, actor, "native-target", q, false, "")
			if got.Outcome != msg.OutcomeSent {
				t.Fatal(got)
			}
			key := s.key(actor, q.IdempotencyKey)
			r, _ := s.readRecord(ctx, key)
			if err := h.flushMessageAudit(ctx, key, r.Token); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(4 * time.Minute)
			if err := s.transitionState(ctx, key, r, operation); err != nil {
				t.Fatal(err)
			}
			*now = now.Add(2 * time.Hour)
			if _, err := s.expiryProgress(ctx); err != nil {
				t.Fatal(err)
			}
			if err := h.flushMessageAudit(ctx, key, r.Token); err != nil {
				t.Fatal(err)
			}
			if len(sink.records) != 2 {
				t.Fatal(len(sink.records))
			}
			want := map[string]string{"complete": "completed", "cancel": "canceled", "fail": "failed", "refuse": "rejected"}[operation]
			last := sink.records[1].Attrs
			if last["outcome"] != want || last["state_version"] != "1" || last["message_id"] != got.MessageID {
				t.Fatal(last)
			}
		})
	}
}

func TestMessagesNotificationsMaximumCanonicalMetadataFitsExistingOwners(t *testing.T) {
	ctx := context.Background()
	h, _, actor, q := messagesNoticeFixture(t)
	actor.Program = strings.Repeat("s", 4096)
	actor.ObservedProgram = actor.Program
	receiver := strings.Repeat("r", 4096)
	q.Body = strings.Repeat("\x01", 8192)
	q.Subject = strings.Repeat("é", 80)
	sink := &messageAuditSink{}
	if err := h.configureMessageAudit(sink); err != nil {
		t.Fatal(err)
	}
	defer h.messageAudit.close()
	got := h.messages.accept(ctx, actor, receiver, q, false, "")
	if got.Outcome != msg.OutcomeSent {
		t.Fatal("existing canonical capacity", got)
	}
	key := h.messages.key(actor, q.IdempotencyKey)
	r, err := h.messages.readRecord(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if err = h.progressMessageNotifications(ctx, key, r); err != nil {
		t.Fatal("notice metadata enlarged required capacity", err)
	}
	if _, err = h.messages.readRecord(ctx, key); err != nil {
		t.Fatal(err)
	}
}

func TestMessagesNotificationsTerminalWithdrawsAndPrepostRechecks(t *testing.T) {
	for _, point := range []string{"posted", "reserved"} {
		t.Run(point, func(t *testing.T) {
			ctx := context.Background()
			h, _, actor, q := messagesNoticeFixture(t)
			got := h.messages.accept(ctx, actor, "native-target", q, false, "")
			if got.Outcome != msg.OutcomeSent {
				t.Fatal(got)
			}
			key := h.messages.key(actor, q.IdempotencyKey)
			r, _ := h.messages.readRecord(ctx, key)
			if point == "reserved" {
				h.messages.boundary = func(at string) error {
					if at == "notification_reserved" {
						return h.messages.transitionState(ctx, key, r, "cancel")
					}
					return nil
				}
			}
			if err := h.progressMessageNotifications(ctx, key, r); err != nil {
				t.Fatal(err)
			}
			if point == "posted" {
				if err := h.messages.transitionState(ctx, key, r, "complete"); err != nil {
					t.Fatal(err)
				}
				if err := h.progressMessageNotifications(ctx, key, r); err != nil {
					t.Fatal(err)
				}
			}
			rows, _, err := h.messageNotices.book.NotificationHistory()
			if err != nil {
				t.Fatal(err)
			}
			if point == "reserved" && len(rows) != 0 {
				t.Fatal("terminal question posted", rows)
			}
			if point == "posted" && (len(rows) != 1 || rows[0].State != awire.NotificationStateWithdrawn) {
				t.Fatal("answered notification stayed waiting", rows)
			}
		})
	}
}

func TestMessagesNotificationsCreationBoundaryExpiryAndKeyReuse(t *testing.T) {
	for _, mode := range []string{"expiry", "replacement"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			h, now, actor, q := messagesNoticeFixture(t)
			got := h.messages.accept(ctx, actor, "native-target", q, false, "")
			if got.Outcome != msg.OutcomeSent {
				t.Fatal(got)
			}
			key := h.messages.key(actor, q.IdempotencyKey)
			r, _ := h.messages.readRecord(ctx, key)
			h.messages.boundary = func(at string) error {
				if at != "before_notice_record" {
					return nil
				}
				h.messages.boundary = nil
				*now = now.Add(2 * time.Hour)
				if mode == "expiry" {
					return h.messages.expireRecord(ctx, messagePointer{Key: key, Token: r.Token, ID: r.ID, AcceptedAt: r.AcceptedAt, ExpiresUnixMs: r.ExpiresUnixMs})
				}
				q.Body = "replacement"
				q.ExpiresUnixMs = now.Add(time.Hour).UnixMilli()
				q.AnswerByUnixMs = q.ExpiresUnixMs
				if replacement := h.messages.accept(ctx, actor, "native-target", q, false, ""); replacement.Outcome != msg.OutcomeSent {
					return fmt.Errorf("replacement outcome %s", replacement.Outcome)
				}
				return nil
			}
			if err := h.progressMessageNotifications(ctx, key, r); err != nil {
				t.Fatal(err)
			}
			rows, _, err := h.messageNotices.book.NotificationHistory()
			if err != nil || len(rows) != 0 {
				t.Fatal("obsolete work posted", rows, err)
			}
			current, err := h.messages.readRecord(ctx, key)
			if err != nil || current == nil || current.WaitingNotice != nil {
				t.Fatal(current, err)
			}
			if mode == "replacement" && current.Token == r.Token {
				t.Fatal("key replacement fixture did not replace")
			}
		})
	}
}
