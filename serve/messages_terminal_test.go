package main

import (
	"context"
	"encoding/json"
	"errors"
	cas "github.com/openabstractions/abstraction-cas/go"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesTerminalAcceptanceSurvivesChildExpiryAndKeyReuse(t *testing.T) {
	for _, mode := range []string{"reply", "cancel", "held-pending", "held-refused", "held-authorized"} {
		t.Run(mode, func(t *testing.T) {
			s, now, actor, request, target, thread := messagesReplyFixture(t)
			ctx := context.Background()
			question, err := s.readRecord(ctx, target.key)
			if err != nil {
				t.Fatal(err)
			}
			original, err := decodeRetainedMessageRequest(question.Spec)
			if err != nil {
				t.Fatal(err)
			}
			held := mode == "held-pending" || mode == "held-refused" || mode == "held-authorized"
			if mode == "cancel" {
				actor = question.Actor
				request = msg.SendRequest{To: original.To, Context: original.Context, Kind: msg.KindNote, IdempotencyKey: "cancel", Withdraw: question.ID}
				target = &messageReplyTarget{key: target.key, token: target.token, operation: "cancel"}
			}
			request.ExpiresUnixMs = now.Add(time.Second).UnixMilli()
			s.boundary = func(at string) error {
				if at == "terminal_child_published" {
					return errors.New("lost terminal projection")
				}
				return nil
			}
			receiver := question.Actor.Program
			if mode == "cancel" {
				receiver = question.Receiver
			}
			first := s.acceptReply(ctx, actor, receiver, request, held, thread, target)
			if first.Outcome != msg.OutcomeUnknown {
				t.Fatal(first)
			}
			key := s.key(actor, request.IdempotencyKey)
			child, err := s.readRecord(ctx, key)
			if err != nil || child == nil {
				t.Fatal(child, err)
			}
			s.boundary = nil
			if mode == "held-authorized" {
				s.boundary = func(at string) error {
					if at == "before_state_record" {
						return errors.New("lost release projection")
					}
					return nil
				}
				if err = s.transitionState(ctx, key, child, "release"); err == nil {
					t.Fatal("release fault not retained")
				}
				s.boundary = nil
			} else if mode == "held-refused" {
				if err = s.transitionState(ctx, key, child, "refuse"); err != nil {
					t.Fatal(err)
				}
			}
			*now = now.Add(2 * time.Second)
			if err = s.sweepExpiry(ctx); err != nil {
				t.Fatal(err)
			}
			expired, err := s.readRecord(ctx, key)
			if err != nil || expired == nil || expired.Profile != "messages-expired@1" || len(expired.Spec) != 0 {
				t.Fatal("child not purged", expired, err)
			}
			replacement := msg.SendRequest{To: request.To, Context: request.Context, Kind: msg.KindNote, Body: "new unrelated request", IdempotencyKey: request.IdempotencyKey, ExpiresUnixMs: now.Add(time.Hour).UnixMilli()}
			if got := s.accept(ctx, actor, receiver, replacement, false, ""); got.Outcome != msg.OutcomeSent {
				t.Fatal("expired key reuse", got)
			}
			*now = now.Add(2 * time.Minute)
			if err = s.transitionState(ctx, target.key, question, "fail"); err != nil {
				t.Fatal(err)
			}
			state, err := s.currentState(ctx, target.key, question)
			if err != nil {
				t.Fatal(err)
			}
			expected := msg.TaskStateCompleted
			if mode == "cancel" {
				expected = msg.TaskStateCanceled
			} else if mode == "held-pending" || mode == "held-refused" {
				expected = msg.TaskStateFailed
			}
			if state.TaskState != expected {
				t.Fatal("accepted terminal intent lost", state.TaskState, expected)
			}
		})
	}
}

func TestMessagesTerminalAcceptanceAnchorsBeforeDirectKeyReuse(t *testing.T) {
	for _, cancel := range []bool{false, true} {
		t.Run(map[bool]string{false: "reply", true: "cancel"}[cancel], func(t *testing.T) {
			s, now, actor, request, target, thread := messagesReplyFixture(t)
			ctx := context.Background()
			question, err := s.readRecord(ctx, target.key)
			if err != nil {
				t.Fatal(err)
			}
			receiver := question.Actor.Program
			if cancel {
				actor = question.Actor
				receiver = question.Receiver
				original, err := decodeRetainedMessageRequest(question.Spec)
				if err != nil {
					t.Fatal(err)
				}
				request = msg.SendRequest{To: original.To, Context: original.Context, Kind: msg.KindNote, IdempotencyKey: "cancel", Withdraw: question.ID}
				target.operation = "cancel"
			}
			request.ExpiresUnixMs = now.Add(time.Second).UnixMilli()
			s.boundary = func(at string) error {
				if at == "terminal_child_published" {
					return errors.New("lost terminal projection")
				}
				return nil
			}
			if got := s.acceptReply(ctx, actor, receiver, request, false, thread, target); got.Outcome != msg.OutcomeUnknown {
				t.Fatal(got)
			}
			s.boundary = nil
			*now = now.Add(2 * time.Second)
			reuse := msg.SendRequest{To: request.To, Context: request.Context, Kind: msg.KindNote, IdempotencyKey: request.IdempotencyKey, Body: "new unrelated request", ExpiresUnixMs: now.Add(time.Hour).UnixMilli()}
			if got := s.accept(ctx, actor, receiver, reuse, false, ""); got.Outcome != msg.OutcomeSent {
				t.Fatal(got)
			}
			*now = now.Add(2 * time.Minute)
			if err = s.transitionState(ctx, target.key, question, "fail"); err != nil {
				t.Fatal(err)
			}
			state, err := s.currentState(ctx, target.key, question)
			if err != nil {
				t.Fatal(err)
			}
			want := msg.TaskStateCompleted
			if cancel {
				want = msg.TaskStateCanceled
			}
			if state.TaskState != want {
				t.Fatal(state.TaskState, want)
			}
		})
	}
}

func TestMessagesTerminalTargetDigestsRefuseMalformedStoredPaths(t *testing.T) {
	for _, field := range []string{"key", "token", "partial"} {
		t.Run(field, func(t *testing.T) {
			s, _, actor, request, target, thread := messagesReplyFixture(t)
			ctx := context.Background()
			if got := s.acceptReply(ctx, actor, "native-codex", request, false, thread, target); got.Outcome != msg.OutcomeSent {
				t.Fatal(got)
			}
			key := s.key(actor, request.IdempotencyKey)
			if err := cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func(raw []byte) ([]byte, error) {
				var record messageRecord
				if err := json.Unmarshal(raw, &record); err != nil {
					return nil, err
				}
				switch field {
				case "key":
					record.TargetKey = "../outside"
				case "token":
					record.TargetToken = "../outside"
				default:
					record.TargetToken = ""
				}
				return json.Marshal(record)
			}); err != nil {
				t.Fatal(err)
			}
			if _, err := s.readRecord(ctx, key); err == nil {
				t.Fatal("malformed target admitted")
			}
		})
	}
}

func TestMessagesExpiryHintCannotShortenCanonicalRetention(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	got := s.accept(ctx, actor, "native-claude", request, false, "")
	if got.Outcome != msg.OutcomeSent {
		t.Fatal(got)
	}
	key := s.key(actor, request.IdempotencyKey)
	record, err := s.readRecord(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	ref := messagePointer{Key: key, Token: record.Token, ExpiresUnixMs: now.Add(-time.Second).UnixMilli()}
	if err = s.expireRecord(ctx, ref); err == nil {
		t.Fatal("untrusted hint shortened canonical retention")
	}
	current, err := s.readRecord(ctx, key)
	if err != nil || current == nil || current.Profile != "messages-record@1" || len(current.Spec) == 0 {
		t.Fatal("accepted body lost", current, err)
	}
}
