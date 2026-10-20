package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesDeliveryExpiredPageDoesNotHideNewBody(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	request.ExpiresUnixMs = now.Add(time.Minute).UnixMilli()
	first := s.accept(context.Background(), actor, "receiver", request, false, "")
	if first.Outcome != msg.OutcomeSent {
		t.Fatal(first)
	}
	page := s.bodyPage(context.Background(), actor.Account, "receiver", request.Context, "same-native", "", messagesPermitted(t, s, actor, "receiver", request.Context))
	if len(page.Messages) != 1 || page.Cursor == "" {
		t.Fatal(page)
	}
	*now = now.Add(2 * time.Minute)
	request.IdempotencyKey, request.ExpiresUnixMs = "second", now.Add(time.Hour).UnixMilli()
	second := s.accept(context.Background(), actor, "receiver", request, false, "")
	if second.Outcome != msg.OutcomeSent {
		t.Fatal(second)
	}
	page = s.bodyPage(context.Background(), actor.Account, "receiver", request.Context, "same-native", "", messagesPermitted(t, s, actor, "receiver", request.Context))
	if page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Messages[0].Header.MessageID != second.MessageID || page.Cursor == "" {
		t.Fatal("expired pinned page hid the new body", page)
	}
	r, err := s.readRecord(context.Background(), s.key(actor, request.IdempotencyKey))
	if err != nil || r.Acked {
		t.Fatal("fresh body was acknowledged without its cursor", r, err)
	}
}

func TestMessagesDeliveryExpiredCursorRequiresFreshSnapshot(t *testing.T) {
	for _, event := range []bool{false, true} {
		for _, collected := range []bool{false, true} {
			t.Run(fmt.Sprintf("event=%t/collected=%t", event, collected), func(t *testing.T) {
				s, now, a, request := messagesStoreFixture(t)
				ctx := context.Background()
				request.Kind, request.AnswerByUnixMs = msg.KindQuestion, now.Add(time.Minute).UnixMilli()
				request.ExpiresUnixMs = now.Add(2 * time.Minute).UnixMilli()
				if got := s.accept(ctx, a, "receiver", request, false, ""); got.Outcome != msg.OutcomeSent {
					t.Fatal(got)
				}
				key := s.key(a, request.IdempotencyKey)
				r, err := s.readRecord(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				var events []messageEventRef
				if event {
					*now = now.Add(time.Minute)
					if err := s.transitionState(ctx, key, r, "fail"); err != nil {
						t.Fatal(err)
					}
					actor := messageActor{Account: a.Account, Program: "receiver", Context: request.Context}
					events, err = s.messageEventCandidates(ctx, actor, s.stateEventCheckFor(actor))
					if err != nil || len(events) != 1 {
						t.Fatal(events, err)
					}
				}
				allowed := messagesPermitted(t, s, a, "receiver", request.Context)
				page, _ := s.deliveryPage(ctx, a.Account, "receiver", request.Context, "native", "", allowed, events)
				if page.Outcome != msg.OutcomePage || page.Cursor == "" {
					t.Fatal(page)
				}
				*now = time.UnixMilli(request.ExpiresUnixMs)
				if collected {
					if err := s.expireRecord(ctx, messagePointer{Key: key, Token: r.Token, ExpiresUnixMs: r.ExpiresUnixMs}); err != nil {
						t.Fatal(err)
					}
				}
				request.IdempotencyKey, request.ExpiresUnixMs, request.AnswerByUnixMs = "later", now.Add(time.Hour).UnixMilli(), now.Add(time.Hour).UnixMilli()
				later := s.accept(ctx, a, "receiver", request, false, "")
				if later.Outcome != msg.OutcomeSent {
					t.Fatal(later)
				}
				allowed = messagesPermitted(t, s, a, "receiver", request.Context)
				for i := 0; i < 2; i++ {
					gap := s.bodyPage(ctx, a.Account, "receiver", request.Context, "native", page.Cursor, allowed)
					if gap.Outcome != msg.OutcomeGap || len(gap.Messages) != 0 || gap.Cursor != "" {
						t.Fatal(gap)
					}
				}
				fresh := s.bodyPage(ctx, a.Account, "receiver", request.Context, "native", "", allowed)
				if fresh.Outcome != msg.OutcomePage || len(fresh.Messages) != 1 || fresh.Messages[0].Header.MessageID != later.MessageID {
					t.Fatal(fresh)
				}
				newRecord, err := s.readRecord(ctx, s.key(a, request.IdempotencyKey))
				if err != nil || newRecord.Acked {
					t.Fatal("fresh page acknowledged unread body", newRecord, err)
				}
			})
		}
	}
}

func TestMessagesDeliveryUnexpiredRecordFailureStaysUnknown(t *testing.T) {
	for _, corrupt := range []bool{false, true} {
		t.Run(fmt.Sprint(corrupt), func(t *testing.T) {
			s, _, a, request := messagesStoreFixture(t)
			ctx := context.Background()
			if got := s.accept(ctx, a, "receiver", request, false, ""); got.Outcome != msg.OutcomeSent {
				t.Fatal(got)
			}
			allowed := messagesPermitted(t, s, a, "receiver", request.Context)
			page := s.bodyPage(ctx, a.Account, "receiver", request.Context, "native", "", allowed)
			if page.Outcome != msg.OutcomePage || page.Cursor == "" {
				t.Fatal(page)
			}
			path := s.recordPath(s.key(a, request.IdempotencyKey))
			if corrupt {
				if err := os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if got := s.bodyPage(ctx, a.Account, "receiver", request.Context, "native", page.Cursor, allowed); got.Outcome != msg.OutcomeUnknown {
				t.Fatal(got)
			}
		})
	}
}

func messagesPermitted(t *testing.T, s *messageStore, a messageActor, receiver, contextName string) map[string]string {
	t.Helper()
	refs, err := s.queueCandidates(context.Background(), a.Account, receiver, contextName)
	if err != nil {
		t.Fatal(err)
	}
	allowed := map[string]string{}
	for _, ref := range refs {
		allowed[ref.Key] = ref.Token
	}
	return allowed
}

func TestMessagesDeliveryLostPageAndAckReply(t *testing.T) {
	for _, loss := range []string{"page-reply", "ack-index"} {
		t.Run(loss, func(t *testing.T) {
			s, _, a, request := messagesStoreFixture(t)
			first := s.accept(context.Background(), a, "receiver", request, false, "")
			if first.Outcome != msg.OutcomeSent {
				t.Fatal(first)
			}
			allowed := messagesPermitted(t, s, a, "receiver", request.Context)
			if loss == "page-reply" {
				s.boundary = func(at string) error {
					if at == "delivery_page_persisted" {
						return errors.New("lost reply")
					}
					return nil
				}
			}
			page := s.bodyPage(context.Background(), a.Account, "receiver", request.Context, "native-occurrence-a", "", allowed)
			if loss == "page-reply" && page.Outcome != msg.OutcomeUnknown {
				t.Fatal(page)
			}
			s.boundary = nil
			page = s.bodyPage(context.Background(), a.Account, "receiver", request.Context, "native-occurrence-a", "", allowed)
			if page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Cursor == "" || page.Messages[0].Header.MessageID != first.MessageID {
				t.Fatal(page)
			}
			r, err := s.readRecord(context.Background(), s.key(a, request.IdempotencyKey))
			if err != nil || r.Acked {
				t.Fatal("read/lost reply acknowledged a body", r, err)
			}
			originalCursor := page.Cursor
			next := request
			next.IdempotencyKey, next.Body = "next", "next body"
			second := s.accept(context.Background(), a, "receiver", next, false, "")
			allowed = messagesPermitted(t, s, a, "receiver", request.Context)
			// A replay shows the unacknowledged page first and the later body
			// after it under a fresh cursor; the original cursor still
			// acknowledges only the first body.
			if repeat := s.bodyPage(context.Background(), a.Account, "receiver", request.Context, "native-occurrence-a", "", allowed); repeat.Cursor == originalCursor || repeat.Cursor == "" || len(repeat.Messages) != 2 || repeat.Messages[0].Header.MessageID != first.MessageID || repeat.Messages[1].Header.MessageID != second.MessageID {
				t.Fatal("replay lost its page or hid later content", repeat)
			}
			if loss == "ack-index" {
				s.boundary = func(at string) error {
					if at == "records_acknowledged" {
						return errors.New("lost ack index")
					}
					return nil
				}
			}
			page = s.bodyPage(context.Background(), a.Account, "receiver", request.Context, "native-occurrence-a", originalCursor, allowed)
			if loss == "ack-index" && page.Outcome != msg.OutcomeUnknown {
				t.Fatal(page)
			}
			s.boundary = nil
			page = s.bodyPage(context.Background(), a.Account, "receiver", request.Context, "native-occurrence-a", originalCursor, allowed)
			if page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Messages[0].Header.MessageID != second.MessageID || page.Cursor == originalCursor {
				t.Fatal("ack reconciliation lost next page", page)
			}
			r, err = s.readRecord(context.Background(), s.key(a, next.IdempotencyKey))
			if err != nil || r.Acked {
				t.Fatal("lost ack reply acknowledged undisclosed next body", r, err)
			}
		})
	}
}

func TestMessagesDeliveryConcurrentAndFreshOccurrenceCursor(t *testing.T) {
	s, _, a, request := messagesStoreFixture(t)
	if got := s.accept(context.Background(), a, "receiver", request, false, ""); got.Outcome != msg.OutcomeSent {
		t.Fatal(got)
	}
	allowed := messagesPermitted(t, s, a, "receiver", request.Context)
	pages := make(chan msg.Page, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			pages <- s.bodyPage(context.Background(), a.Account, "receiver", request.Context, "old-native", "", allowed)
		}()
	}
	wg.Wait()
	close(pages)
	cursor := ""
	for page := range pages {
		if page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Cursor == "" || cursor != "" && cursor != page.Cursor {
			t.Fatal("concurrent reads minted different pending pages", page)
		}
		cursor = page.Cursor
	}
	if got := s.bodyPage(context.Background(), a.Account, "receiver", request.Context, "new-native", cursor, allowed); got.Outcome != msg.OutcomeGap {
		t.Fatal("new occurrence acknowledged old cursor", got)
	}
	page := s.bodyPage(context.Background(), a.Account, "receiver", request.Context, "new-native", "", allowed)
	if page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Cursor == cursor {
		t.Fatal("new owner did not receive its own page", page)
	}
	r, err := s.readRecord(context.Background(), s.key(a, request.IdempotencyKey))
	if err != nil || r.Acked {
		t.Fatal("occurrence change acknowledged old body", r, err)
	}
}

// Owner R15 (2026-11-09): an OpenCode chat read a note without passing its
// cursor, asked a question, and its after-less reads kept replaying the note
// page while the reply waited behind it.
func TestMessagesDeliveryReplayShowsReplyQueuedBehindUnacknowledgedPage(t *testing.T) {
	s, now, answerer, reply, target, thread := messagesReplyFixture(t)
	ctx := context.Background()
	asker := "native-codex"
	note := msg.SendRequest{To: "app:codex", Context: reply.Context, Kind: msg.KindNote, Subject: "earlier", Body: "earlier note", IdempotencyKey: "earlier-note", ExpiresUnixMs: now.Add(time.Hour).UnixMilli()}
	earlier := s.accept(ctx, answerer, asker, note, false, "")
	if earlier.Outcome != msg.OutcomeSent {
		t.Fatal(earlier)
	}
	read := func(after string) msg.Page {
		t.Helper()
		return s.bodyPage(ctx, answerer.Account, asker, reply.Context, "asker-chat", after, messagesPermitted(t, s, answerer, asker, reply.Context))
	}
	held := read("")
	if held.Outcome != msg.OutcomePage || len(held.Messages) != 1 || held.Messages[0].Header.MessageID != earlier.MessageID {
		t.Fatal(held)
	}
	answered := s.acceptReply(ctx, answerer, asker, reply, false, thread, target)
	if answered.Outcome != msg.OutcomeSent {
		t.Fatal(answered)
	}
	page := read("")
	if page.Outcome != msg.OutcomePage || len(page.Messages) != 2 || page.Messages[1].Header.MessageID != answered.MessageID || page.Messages[1].Header.Kind != msg.KindReply || page.Cursor == held.Cursor {
		t.Fatal("the reply stayed hidden behind the replayed note page", page)
	}
	if again := read(""); again.Cursor != page.Cursor || len(again.Messages) != 2 {
		t.Fatal("an unchanged replay changed its cursor", again)
	}
	if next := read(page.Cursor); next.Outcome != msg.OutcomePage || len(next.Messages) != 0 {
		t.Fatal("the grown page did not acknowledge", next)
	}
	for _, id := range []string{earlier.MessageID, answered.MessageID} {
		r, _, err := s.readByID(ctx, id)
		if err != nil || r == nil || !r.Acked {
			t.Fatal("grown page cursor left a shown body unacknowledged", id, r, err)
		}
	}
}

func TestMessagesDeliveryEarlierReplayCursorAcknowledgesOnlyWhatItShowed(t *testing.T) {
	s, _, a, request := messagesStoreFixture(t)
	ctx := context.Background()
	first := s.accept(ctx, a, "receiver", request, false, "")
	read := func(after string) msg.Page {
		t.Helper()
		return s.bodyPage(ctx, a.Account, "receiver", request.Context, "native", after, messagesPermitted(t, s, a, "receiver", request.Context))
	}
	held := read("")
	next := request
	next.IdempotencyKey, next.Body = "next", "next body"
	second := s.accept(ctx, a, "receiver", next, false, "")
	if grown := read(""); len(grown.Messages) != 2 || grown.Cursor == held.Cursor {
		t.Fatal(grown)
	}
	rest := read(held.Cursor)
	if rest.Outcome != msg.OutcomePage || len(rest.Messages) != 1 || rest.Messages[0].Header.MessageID != second.MessageID {
		t.Fatal("earlier cursor acknowledged a body it never showed", rest)
	}
	if r, _, err := s.readByID(ctx, first.MessageID); err != nil || !r.Acked {
		t.Fatal("earlier cursor did not acknowledge its own body", r, err)
	}
	if r, _, err := s.readByID(ctx, second.MessageID); err != nil || r.Acked {
		t.Fatal("unshown body acknowledged", r, err)
	}
}
