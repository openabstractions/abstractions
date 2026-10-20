package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesStartActivityUsesOneCanonicalPrivateRow(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	request.Kind, request.AnswerByUnixMs = msg.KindQuestion, now.Add(time.Minute).UnixMilli()
	sink := &messageAuditSink{}
	h := &applicationsHost{messages: s}
	if err := h.configureMessageAudit(sink); err != nil {
		t.Fatal(err)
	}
	defer h.messageAudit.close()
	if got := s.accept(context.Background(), actor, "native-receiver", request, false, ""); got.Outcome != msg.OutcomeSent {
		t.Fatal(got)
	}
	key := s.key(actor, request.IdempotencyKey)
	r, err := s.readRecord(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.updateMessageNotice(context.Background(), key, r.Token, func(current *messageRecord) {
		current.Start = &messageStartAttempt{Resource: "app:claude/start:" + strings.Repeat("a", 64), Attempt: strings.Repeat("b", 64), State: "unknown", At: now.UnixMilli()}
	}); err != nil {
		t.Fatal(err)
	}
	r, err = s.readRecord(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := h.flushMessageStartAudit(context.Background(), key, r); err != nil {
			t.Fatal(err)
		}
	}
	if len(sink.records) != 1 || sink.records[0].Attrs["message_id"] != r.ID+":start" || sink.records[0].Attrs["outcome"] != "receiver_start_unknown" {
		t.Fatal(sink.records)
	}
	raw, err := json.Marshal(sink.records)
	if err != nil || bytes.Contains(raw, []byte(request.Body)) || bytes.Contains(raw, []byte(request.Subject)) {
		t.Fatal("startup activity disclosed message text", err)
	}
}

func TestMessagesStartPairChargeIsDurableAndIdempotent(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	request.Kind, request.AnswerByUnixMs = msg.KindQuestion, now.Add(time.Minute).UnixMilli()
	sent := s.accept(context.Background(), actor, "native-receiver", request, false, "")
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	key := s.key(actor, request.IdempotencyKey)
	r, err := s.readRecord(context.Background(), key)
	if err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for i := 0; i < 8; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if ok, err := s.chargeMessageStart(context.Background(), key, r); err != nil || !ok {
				t.Errorf("charge %v %v", ok, err)
			}
		}()
	}
	workers.Wait()
	reopened, err := openMessageStore(s.dir+"/..", s.now)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := reopened.chargeMessageStart(context.Background(), key, r); err != nil || !ok {
		t.Fatal(ok, err)
	}
	p := messagesReadPair(t, s, actor, "native-receiver")
	if p.Count != 2 || len(p.Starts) != 1 {
		t.Fatalf("pair %+v", p)
	}
	// Recovery before another request preserves the start's hourly cost.
	request.IdempotencyKey, request.Body = "second", "different"
	if got := s.accept(context.Background(), actor, "native-receiver", request, false, ""); got.Outcome != msg.OutcomeSent {
		t.Fatal(got)
	}
	if p := messagesReadPair(t, s, actor, "native-receiver"); p.Count != 3 {
		t.Fatal("lost startup charge", p.Count)
	}
}

func TestMessagesStartPairBudgetRefusesBeforeDispatch(t *testing.T) {
	s, now, actor, request := messagesStoreFixture(t)
	request.Kind, request.AnswerByUnixMs = msg.KindQuestion, now.Add(time.Minute).UnixMilli()
	var first *messageRecord
	var key string
	for i := 0; i < 20; i++ {
		request.IdempotencyKey = string(rune('a' + i))
		request.Body = request.IdempotencyKey
		if got := s.accept(context.Background(), actor, "native-receiver", request, false, ""); got.Outcome != msg.OutcomeSent {
			t.Fatal(got)
		}
		if i == 0 {
			key = s.key(actor, request.IdempotencyKey)
			first, _ = s.readRecord(context.Background(), key)
		}
	}
	if ok, err := s.chargeMessageStart(context.Background(), key, first); err != nil || ok {
		t.Fatal("exhausted pair started", ok, err)
	}
}

func TestMessagesStartNotesAndRepliesNeverPrepareProfiles(t *testing.T) {
	for _, kind := range []msg.Kind{msg.KindNote, msg.KindReply} {
		s, _, actor, request := messagesStoreFixture(t)
		request.Kind = kind
		r := &messageRecord{Actor: actor, Spec: msg.EncodeSendRequest(&request)}
		h := &applicationsHost{messages: s}
		if err := h.progressMessageStartSettled(context.Background(), "unused", r); err != nil {
			t.Fatal(err)
		}
	}
}
