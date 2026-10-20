package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func messagesReplyFixture(t *testing.T) (*messageStore, *time.Time, messageActor, msg.SendRequest, *messageReplyTarget, string) {
	t.Helper()
	s, now, sender, question := messagesStoreFixture(t)
	question.Kind = msg.KindQuestion
	question.AnswerByUnixMs = now.Add(time.Minute).UnixMilli()
	sent := s.accept(context.Background(), sender, "native-claude", question, false, "")
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	record, key, err := s.readByID(context.Background(), sent.MessageID)
	if err != nil || record == nil {
		t.Fatal(record, err)
	}
	actor := messageActor{Account: sender.Account, Program: "native-claude", Application: "claude", Context: question.Context}
	reply := msg.SendRequest{To: "app:codex", Context: sender.Context, Kind: msg.KindReply, Subject: "reply", Body: "answer", IdempotencyKey: "reply", ReplyTo: sent.MessageID, ExpiresUnixMs: question.ExpiresUnixMs}
	return s, now, actor, reply, &messageReplyTarget{key: key, token: record.Token}, sent.ContextID
}

func TestMessagesReplyCanonicalReservationReplayAliasAndConflict(t *testing.T) {
	s, _, actor, request, target, thread := messagesReplyFixture(t)
	ctx := context.Background()
	first := s.acceptReply(ctx, actor, "native-codex", request, false, thread, target)
	if first.Outcome != msg.OutcomeSent {
		t.Fatal(first)
	}
	if again := s.acceptReply(ctx, actor, "native-codex", request, false, thread, target); again.MessageID != first.MessageID || again.Outcome != msg.OutcomeSent {
		t.Fatal(again)
	}
	alias := request
	alias.IdempotencyKey = "fresh-alias"
	if got := s.acceptReply(ctx, actor, "native-codex", alias, false, thread, target); got.Outcome != msg.OutcomeSent || got.MessageID != first.MessageID {
		t.Fatal("equal alias", got)
	}
	changed := request
	changed.Body = "changed"
	if got := s.acceptReply(ctx, actor, "native-codex", changed, false, thread, target); got.Outcome != msg.OutcomeConflict {
		t.Fatal("key conflict", got)
	}
	changed.IdempotencyKey = "another-reply"
	if got := s.acceptReply(ctx, actor, "native-codex", changed, false, thread, target); got.Outcome == msg.OutcomeSent {
		t.Fatal("second distinct reply accepted", got)
	}
}

func TestMessagesReplyCrashReservationAndDeadlinePublication(t *testing.T) {
	for _, point := range []string{"reply_reserved", "expiry_linked"} {
		t.Run(point, func(t *testing.T) {
			s, now, actor, request, target, thread := messagesReplyFixture(t)
			ctx := context.Background()
			s.boundary = func(at string) error {
				if at == point {
					if point == "expiry_linked" {
						*now = now.Add(2 * time.Minute)
						return nil
					}
					return errors.New("inert lost reservation")
				}
				return nil
			}
			result := s.acceptReply(ctx, actor, "native-codex", request, false, thread, target)
			if result.Outcome != msg.OutcomeUnknown {
				t.Fatal(result)
			}
			canonical, err := s.readRecord(ctx, s.key(actor, request.IdempotencyKey))
			if err != nil || canonical != nil {
				t.Fatal("unpublished reply became accepted", canonical, err)
			}
			if point == "reply_reserved" {
				reopened, err := openMessageStore(filepath.Dir(s.dir), s.now)
				if err != nil {
					t.Fatal(err)
				}
				if got := reopened.acceptReply(ctx, actor, "native-codex", request, false, thread, target); got.Outcome != msg.OutcomeSent {
					t.Fatal("recovery", got)
				}
			}
		})
	}
}

func TestMessagesReplyCrossInstancePublisherFence(t *testing.T) {
	s, _, actor, request, target, thread := messagesReplyFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	other, err := openMessageStore(filepath.Dir(s.dir), s.now)
	if err != nil {
		t.Fatal(err)
	}
	reserved, release := make(chan struct{}), make(chan struct{})
	s.boundary = func(at string) error {
		if at == "reply_reserved" {
			close(reserved)
			select {
			case <-release:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	}
	done := make(chan msg.SendResult, 1)
	go func() { done <- s.acceptReply(ctx, actor, "native-codex", request, false, thread, target) }()
	select {
	case <-reserved:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	newer := request
	newer.IdempotencyKey = "newer"
	newer.Body = "newer answer"
	winner := other.acceptReply(ctx, actor, "native-codex", newer, false, thread, target)
	close(release)
	old := <-done
	if winner.Outcome != msg.OutcomeSent || old.Outcome == msg.OutcomeSent {
		t.Fatal("stale publisher escaped fence", winner, old)
	}
}

// Review 2026-11-09: a question answered by a reply held for the reverse
// permit expired as undelivered. Reply marks its question delivered only
// when its reply is accepted unheld; the held reply completes later through
// completeReply at its release.
func TestMessagesReplyHeldThenReleasedLeavesQuestionDelivered(t *testing.T) {
	s, now, actor, request, target, thread := messagesReplyFixture(t)
	ctx := context.Background()
	held := s.acceptReply(ctx, actor, "native-codex", request, true, thread, target)
	if held.Outcome != msg.OutcomeSent || !held.AwaitingPermission {
		t.Fatal(held)
	}
	question, err := s.readRecord(ctx, target.key)
	if err != nil || question == nil || question.Delivery != nil {
		t.Fatal("a held reply delivered its question before release", question, err)
	}
	reply, replyKey, err := s.readByID(ctx, held.MessageID)
	if err != nil || reply == nil {
		t.Fatal(reply, err)
	}
	if err := s.transitionState(ctx, replyKey, reply, "release"); err != nil {
		t.Fatal(err)
	}
	if err := s.completeReply(ctx, reply, replyKey); err != nil {
		t.Fatal(err)
	}
	question, err = s.readRecord(ctx, target.key)
	if err != nil || question == nil || question.Delivery == nil || question.Delivery.State != msg.DeliveryDelivered {
		t.Fatal("a released reply left its question undelivered", question, err)
	}
	*now = now.Add(2 * time.Hour)
	if err := s.expireRecord(ctx, messagePointer{Key: target.key, Token: target.token, ExpiresUnixMs: question.ExpiresUnixMs}); err != nil {
		t.Fatal(err)
	}
	expired, err := s.recordForPointer(ctx, messagePointer{Key: target.key, Token: target.token, ExpiresUnixMs: question.ExpiresUnixMs})
	if err != nil || expired == nil || expired.Delivery == nil || expired.Delivery.State != msg.DeliveryDelivered {
		t.Fatal("an answered question expired as undelivered", expired, err)
	}
}

// A reply still held for the reverse permit at the question's expiry proves
// the question reached its receiver; expiry reports it delivered.
func TestMessagesReplyLinkedQuestionExpiresDelivered(t *testing.T) {
	s, now, actor, request, target, thread := messagesReplyFixture(t)
	ctx := context.Background()
	held := s.acceptReply(ctx, actor, "native-codex", request, true, thread, target)
	if held.Outcome != msg.OutcomeSent || !held.AwaitingPermission {
		t.Fatal(held)
	}
	question, err := s.readRecord(ctx, target.key)
	if err != nil || question == nil || question.Delivery != nil {
		t.Fatal(question, err)
	}
	*now = now.Add(2 * time.Hour)
	pointer := messagePointer{Key: target.key, Token: target.token, ExpiresUnixMs: question.ExpiresUnixMs}
	if err := s.expireRecord(ctx, pointer); err != nil {
		t.Fatal(err)
	}
	expired, err := s.recordForPointer(ctx, pointer)
	if err != nil || expired == nil || expired.Delivery == nil || expired.Delivery.State != msg.DeliveryDelivered {
		t.Fatal("a question with a linked reply expired as undelivered", expired, err)
	}
}

func TestMessagesAwaitedReplyRequiresExactCanonicalLink(t *testing.T) {
	s, now, actor, request, target, thread := messagesReplyFixture(t)
	ctx := t.Context()
	question, err := s.readRecord(ctx, target.key)
	if err != nil || question == nil {
		t.Fatal(question, err)
	}
	first := s.acceptReply(ctx, actor, question.Actor.Program, request, false, thread, target)
	if first.Outcome != msg.OutcomeSent {
		t.Fatal(first)
	}
	reply, key, err := s.readByID(ctx, first.MessageID)
	if err != nil || reply == nil {
		t.Fatal(reply, err)
	}
	owner := question.Actor
	if !s.awaitedReply(ctx, owner, key, reply, request.ReplyTo, now.UnixMilli()) {
		t.Fatal("canonical reply missed its original owner")
	}
	wrongOwner := owner
	wrongOwner.Context = "another-chat"
	unlinked := *reply
	unlinked.TargetKey, unlinked.TargetToken = "", ""
	for name, qualified := range map[string]bool{
		"foreign context": s.awaitedReply(ctx, wrongOwner, key, reply, request.ReplyTo, now.UnixMilli()),
		"ordinary reply":  s.awaitedReply(ctx, owner, key, &unlinked, request.ReplyTo, now.UnixMilli()),
		"another reply":   s.awaitedReply(ctx, owner, target.key, reply, request.ReplyTo, now.UnixMilli()),
		"answer expired":  s.awaitedReply(ctx, owner, key, reply, request.ReplyTo, now.Add(time.Minute).UnixMilli()),
	} {
		if qualified {
			t.Errorf("%s acquired urgency", name)
		}
	}
}

func TestMessagesAwaitedReplyRejectsPreviouslyCompletedQuestion(t *testing.T) {
	s, _, actor, request, target, thread := messagesReplyFixture(t)
	ctx := t.Context()
	question, err := s.readRecord(ctx, target.key)
	if err != nil || question == nil {
		t.Fatal(question, err)
	}
	if err := s.transitionState(ctx, target.key, question, "complete"); err != nil {
		t.Fatal(err)
	}
	if got := s.acceptReply(ctx, actor, question.Actor.Program, request, false, thread, target); got.Outcome == msg.OutcomeSent {
		t.Fatal("completed question admitted a later canonical reply", got)
	}
}
