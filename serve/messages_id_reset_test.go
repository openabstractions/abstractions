package main

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesIDConcurrentCounterResetCannotReuseLocator(t *testing.T) {
	s, _, actor, _ := messagesStoreFixture(t)
	ctx := context.Background()
	seen := map[string]bool{}
	for round := 0; round < 4; round++ {
		if round > 0 {
			if err := os.Remove(filepath.Join(s.dir, "sequences", messageDigest(actor.Account)+".json")); err != nil {
				t.Fatal(err)
			}
		}
		ids := make(chan string, 16)
		failures := make(chan error, 16)
		var workers sync.WaitGroup
		for i := 0; i < 16; i++ {
			workers.Add(1)
			go func() {
				defer workers.Done()
				id, err := s.sequence(ctx, actor)
				if err != nil {
					failures <- err
					return
				}
				ids <- id
			}()
		}
		workers.Wait()
		close(ids)
		close(failures)
		for err := range failures {
			t.Fatal(err)
		}
		count := 0
		for id := range ids {
			count++
			if seen[id] || len(id) > 128 {
				t.Fatal("reused or invalid opaque ID", id)
			}
			seen[id] = true
		}
		if count != 16 {
			t.Fatal(count)
		}
	}
	actor.Application = strings.Repeat("a", 64)
	raw, _ := json.Marshal(uint64(math.MaxUint64 - 1))
	if err := os.WriteFile(filepath.Join(s.dir, "sequences", messageDigest(actor.Account)+".json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
	id, err := s.sequence(ctx, actor)
	if err != nil || len(id) != 118 {
		t.Fatal("maximum opaque ID escaped128-byte grammar", id, err)
	}
}

func TestMessagesIDEntropyFailureIsUnavailableBeforeAcceptance(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		name := "error"
		if invalid {
			name = "invalid"
		}
		t.Run(name, func(t *testing.T) {
			s, _, actor, request := messagesStoreFixture(t)
			ctx := context.Background()
			s.idNonce = func() (string, error) {
				if invalid {
					return "short", nil
				}
				return "", errors.New("inert entropy failure")
			}
			if out := s.accept(ctx, actor, "native-claude", request, false, ""); out.Outcome != msg.OutcomeUnavailable {
				t.Fatal(out)
			}
			r, err := s.readRecord(ctx, s.key(actor, request.IdempotencyKey))
			if err != nil || r != nil {
				t.Fatal("failed randomness became acceptance", r, err)
			}
		})
	}
}

func TestMessagesOldCounterIDReadReplyAndReceiptStayOpaque(t *testing.T) {
	s, _, actor, request := messagesStoreFixture(t)
	ctx := context.Background()
	request.Kind = msg.KindQuestion
	request.AnswerByUnixMs = request.ExpiresUnixMs
	out := s.accept(ctx, actor, "native-claude", request, false, "")
	if out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	key := s.key(actor, request.IdempotencyKey)
	old, err := s.readRecord(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	old.ID = "codex-1"
	old.ContextID = "codex-1"
	if err := cas.ChangeLimitContext(ctx, s.recordPath(key), messageRecordLimit, func([]byte) ([]byte, error) { return json.Marshal(old) }); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(s.statePath(key, old.Token)); err != nil {
		t.Fatal(err)
	}
	if err := s.publishID(ctx, old, key); err != nil {
		t.Fatal(err)
	}
	read, found, err := s.readByID(ctx, old.ID)
	if err != nil || read == nil || found != key || read.ID != old.ID {
		t.Fatal(read, found, err)
	}
	if receipt := s.accept(ctx, actor, "native-claude", request, false, ""); receipt.Outcome != msg.OutcomeSent || receipt.MessageID != old.ID {
		t.Fatal(receipt)
	}
	replier := messageActor{Account: actor.Account, Program: "native-claude", Application: "claude", Context: request.Context}
	reply := msg.SendRequest{To: "app:" + actor.Application, Context: actor.Context, Kind: msg.KindReply, Subject: "answer", Body: "answer", IdempotencyKey: "old-id-answer", ExpiresUnixMs: request.ExpiresUnixMs, ReplyTo: old.ID}
	accepted := s.acceptReply(ctx, replier, actor.Program, reply, false, old.ContextID, &messageReplyTarget{key: key, token: old.Token, operation: "complete"})
	if accepted.Outcome != msg.OutcomeSent {
		t.Fatal(accepted)
	}
	r, replyKey, err := s.readByID(ctx, accepted.MessageID)
	if err != nil || r == nil {
		t.Fatal(r, err)
	}
	if err := s.completeReply(ctx, r, replyKey); err != nil {
		t.Fatal(err)
	}
	state, err := s.currentState(ctx, key, old)
	if err != nil || state.TaskState != msg.TaskStateCompleted {
		t.Fatal(state, err)
	}
}
