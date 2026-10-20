package main

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func remoteStoreActor() messageActor {
	return messageActor{Account: "account", Program: "remote:" + strings.Repeat("a", 64), Application: "claimed-agent", Context: "source", Remote: &messageRemoteActor{PeerKey: strings.Repeat("a", 64), LocalKey: strings.Repeat("b", 64), Generation: strings.Repeat("c", 64), Namespace: strings.Repeat("d", 64), ProgramLabel: "unverified.exe", ReceiverKey: strings.Repeat("e", 64)}}
}
func remoteStoreRequest(now time.Time, actor messageActor, key string) msg.SendRequest {
	return msg.SendRequest{To: "app:receiver", Context: "target", Kind: msg.KindNote, Subject: key, Body: key, IdempotencyKey: key, ExpiresUnixMs: now.Add(time.Hour).UnixMilli(), PeerKey: actor.Remote.LocalKey, PairingEpoch: actor.Remote.Generation, Origin: &msg.OriginClaim{Namespace: actor.Remote.Namespace, Application: actor.Application, ProgramLabel: actor.Remote.ProgramLabel}}
}

func TestMessagesRemoteWithdrawalRefusesBeforeOwners(t *testing.T) {
	r := &messagesReceiver{}
	request := msg.SendRequest{PeerKey: strings.Repeat("a", 64), WithdrawReference: &msg.MessageRef{OwnerPeerKey: strings.Repeat("b", 64), MessageID: "foreign"}}
	// Nil owners are intentional: unsupported withdrawal must not reach native
	// admission, permission questions, transport, or the canonical budget store.
	for _, out := range []msg.SendResult{r.sendPairedMessage("", "", request), r.receiveRemoteMessage("", "", request)} {
		if out.Outcome != msg.OutcomeInvalid || out.Reason != "remote_withdrawal_unsupported" {
			t.Fatal(out)
		}
	}
}

func TestMessagesRemoteForeignReplyNeverArbitratesLocalQuestion(t *testing.T) {
	now := time.Now()
	s, err := openMessageStore(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	actor := remoteStoreActor()
	question := remoteStoreRequest(now, actor, "question-local-collision")
	question.Kind = msg.KindQuestion
	question.AnswerByUnixMs = now.Add(time.Minute).UnixMilli()
	q := s.accept(t.Context(), actor, "receiver", question, false, "")
	record, key, err := s.readByID(t.Context(), q.MessageID)
	if err != nil || record == nil {
		t.Fatal(q, err)
	}
	reply := remoteStoreRequest(now, actor, "foreign-reply")
	reply.Kind = msg.KindReply
	reply.ReplyReference = &msg.MessageRef{OwnerPeerKey: actor.Remote.PeerKey, MessageID: q.MessageID}
	out := s.accept(t.Context(), actor, "receiver", reply, false, "")
	state, err := s.currentState(t.Context(), key, record)
	if out.Outcome != msg.OutcomeSent || out.MessageID == q.MessageID || err != nil || messageTerminal(state.TaskState) || state.ReplyKey != "" {
		t.Fatal(out, state, err)
	}
}
func TestMessagesRemoteNamespacePartitionsOnlyReceipts(t *testing.T) {
	now := time.Now()
	s, err := openMessageStore(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	actor := remoteStoreActor()
	first := remoteStoreRequest(now, actor, "same")
	a := s.accept(ctx, actor, "receiver", first, false, "")
	copyActor := actor
	remoteCopy := *actor.Remote
	copyActor.Remote = &remoteCopy
	copyActor.Remote.Namespace = strings.Repeat("e", 64)
	second := remoteStoreRequest(now, copyActor, "same")
	second.Body = "another body"
	b := s.accept(ctx, copyActor, "receiver-alias", second, false, "")
	if a.Outcome != msg.OutcomeSent || b.Outcome != msg.OutcomeSent || a.MessageID == b.MessageID || s.key(actor, "same") == s.key(copyActor, "same") || s.pairPath(actor, "receiver") != s.pairPath(copyActor, "receiver") {
		t.Fatal(a, b)
	}
	for i := 0; i < 18; i++ {
		key := string(rune('f' + i))
		request := remoteStoreRequest(now, actor, key)
		if out := s.accept(ctx, actor, "receiver", request, false, ""); out.Outcome != msg.OutcomeSent {
			t.Fatal(i, out)
		}
	}
	copyActor.Remote.Namespace = strings.Repeat("f", 64)
	if out := s.accept(ctx, copyActor, "another-receiver-alias", remoteStoreRequest(now, copyActor, "over"), false, ""); out.Outcome != msg.OutcomeExhausted {
		t.Fatal("namespace escaped peer/receiver quota", out)
	}
	if replay, found := s.lookupReceipt(ctx, actor, first); !found || replay.MessageID != a.MessageID {
		t.Fatal("limit hid replay", replay)
	}
}

func TestMessagesRemoteReplyUncertaintyRetainsOneTerminalFence(t *testing.T) {
	now := time.Now()
	s, err := openMessageStore(t.TempDir(), func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	actor := remoteStoreActor()
	request := remoteStoreRequest(now, actor, "question")
	request.Kind = msg.KindQuestion
	request.AnswerByUnixMs = now.Add(time.Minute).UnixMilli()
	out := s.accept(ctx, actor, "receiver", request, false, "")
	if out.Outcome != msg.OutcomeSent {
		t.Fatal(out)
	}
	question, key, err := s.readByID(ctx, out.MessageID)
	if err != nil || question == nil {
		t.Fatal(question, err)
	}
	reply := msg.SendRequest{PeerKey: actor.Remote.PeerKey, To: "app:claimed-agent", Context: "source", Kind: msg.KindReply, IdempotencyKey: "reply", Subject: "answer", Body: "answer", ExpiresUnixMs: now.Add(time.Hour).UnixMilli(), ReplyReference: out.Reference}
	if err = s.reserveRemoteReply(ctx, key, question, reply); err != nil {
		t.Fatal(err)
	}
	// Simulated crash/lost transport result: there is no remote absence proof.
	if state, e := s.currentState(ctx, key, question); e != nil || state.RemoteReply == nil || state.RemoteReply.Completed || state.RemoteReply.Failed {
		t.Fatal(state, e)
	}
	if err = s.reserveRemoteReply(ctx, key, question, reply); err != nil {
		t.Fatal("same fingerprint reconciliation refused", err)
	}
	changed := reply
	changed.Body = "changed"
	if err = s.reserveRemoteReply(ctx, key, question, changed); err == nil {
		t.Fatal("another reply replaced uncertain terminal intent")
	}
	state, err := s.currentState(ctx, key, question)
	if err != nil {
		t.Fatal(err)
	}
	if opposite, e := s.oppositeQuestionOperation(ctx, state, "cancel"); e != nil || !opposite {
		t.Fatal("cancellation escaped remote fence", opposite, e)
	}
	bad := msg.SendResult{Outcome: msg.OutcomeSent, MessageID: "foreign", Reference: &msg.MessageRef{OwnerPeerKey: strings.Repeat("e", 64), MessageID: "foreign"}}
	if err = s.finishRemoteReply(ctx, key, question, reply, bad); err == nil {
		t.Fatal("foreign owner settled question")
	}
	result := msg.SendResult{Outcome: msg.OutcomeSent, MessageID: "remote-1", Reference: &msg.MessageRef{OwnerPeerKey: actor.Remote.PeerKey, MessageID: "remote-1"}}
	if err = s.finishRemoteReply(ctx, key, question, reply, result); err != nil {
		t.Fatal(err)
	}
	state, err = s.currentState(ctx, key, question)
	if err != nil || state.TaskState != msg.TaskStateCompleted || !state.RemoteReply.Completed {
		t.Fatal(state, err)
	}
	if err = s.finishRemoteReply(ctx, key, question, reply, result); err != nil {
		t.Fatal("canonical completion replay", err)
	}
}

func TestMessagesRemotePageScopeAndUnavailableAreEmpty(t *testing.T) {
	rows := make([]msg.Inbox, 65)
	for i := range rows {
		rows[i] = msg.Inbox{Program: "app:receiver", Context: "context", Revision: "revision"}
	}
	page := messageRemoteInboxPage(rows, "peer-generation-one", "")
	if len(page.Inboxes) != 64 || page.Cursor == "" {
		t.Fatal(page)
	}
	if next := messageRemoteInboxPage(rows, "peer-generation-one", page.Cursor); len(next.Inboxes) != 1 || next.Cursor != "" {
		t.Fatal(next)
	}
	if stale := messageRemoteInboxPage(rows, "peer-generation-two", page.Cursor); stale.Outcome != msg.OutcomeGap || len(stale.Inboxes) != 0 {
		t.Fatal(stale)
	}
	if out := messageForwardFailure(errors.New("reply lost after possible publication")); out.Outcome != msg.OutcomeUnknown || out.MessageID != "" {
		t.Fatal(out)
	}
	actor := remoteStoreActor()
	actor.Remote.PeerLabel = "Home PC"
	if label := messageSenderLabel(actor); !strings.Contains(label, "Home PC") || !strings.Contains(label, "unverified program claim") || !strings.Contains(label, "unverified.exe") {
		t.Fatal(label)
	}
	if label := messageSenderLabel(messageActor{Application: "local"}); label != "local" {
		t.Fatal("local wording changed", label)
	}
}
