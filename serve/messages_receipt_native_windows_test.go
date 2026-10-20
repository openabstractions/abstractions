//go:build windows

package main

import (
	"bytes"
	"context"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesNativeReceiptReplaysCurrentHoldAndWatching(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(map[bool]string{true: "allow", false: "never"}[allow], func(t *testing.T) {
			f := messagesObserveFixture(t, true)
			instance, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
			request := messagesStateRequest(f, "receipt", msg.KindNote)
			first, err := sender.Send(instance, "source", request)
			if err != nil || first.Outcome != msg.OutcomeSent || !first.AwaitingPermission || first.ReceiverWatching {
				t.Fatal(first, err)
			}
			alias := request
			alias.IdempotencyKey = "accepted-alias"
			acceptedAlias, err := sender.Send(instance, "source", alias)
			if err != nil || acceptedAlias.Outcome != msg.OutcomeSent || acceptedAlias.MessageID != first.MessageID || !acceptedAlias.AwaitingPermission {
				t.Fatal(acceptedAlias, err)
			}
			initial := f.other.headersWait("target", "", 0)
			waiting := *f.other
			ctx, cancel := context.WithCancel(f.other.ctx)
			waiting.ctx = ctx
			done := make(chan msg.Page, 1)
			go func() { done <- waiting.headersWait("target", initial.Revision, 30000) }()
			deadline := time.Now().Add(3 * time.Second)
			for !f.reader.host.messageIsWatching(f.run.ctx, f.self.Account, f.otherProgram, "target") && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			live, err := sender.Send(instance, "source", request)
			cancel()
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("headers watcher did not join")
			}
			if err != nil || live.Outcome != msg.OutcomeSent || !live.ReceiverWatching || !live.AwaitingPermission {
				t.Fatal("live replay flags", live, err)
			}
			queuePath := f.store.queuePath(f.self.Account, f.otherProgram, "target")
			queueBefore, err := cas.ReadLimitContext(f.run.ctx, queuePath, 64<<10)
			if err != nil {
				t.Fatal(err)
			}
			history, _, err := f.run.f.a.book.ApplicationHistory()
			if err != nil {
				t.Fatal(err)
			}
			if allow {
				messagesReceiverPersonAllow(t, f.run, "app:other-agent")
			} else {
				id := ""
				for _, record := range history {
					if record.Pending() && messagePermissionRecord(f.self.Account, record, "app:other-agent") {
						id = record.ID
					}
				}
				if id == "" {
					t.Fatal("missing person question")
				}
				if _, _, err = f.run.f.a.book.AnswerApplication(id, "never"); err != nil {
					t.Fatal(err)
				}
				if err = f.run.f.a.rights.policy.Set(f.self, ActionApplicationMessage, "app:other-agent", false); err != nil {
					t.Fatal(err)
				}
			}
			for _, same := range []msg.SendRequest{request, alias} {
				replay, err := sender.Send(instance, "source", same)
				if err != nil || replay.Outcome != msg.OutcomeSent || replay.MessageID != first.MessageID || replay.ContextID != first.ContextID || replay.AwaitingPermission || replay.ReceiverWatching {
					t.Fatal("current canonical receipt", replay, err)
				}
			}
			fresh := request
			fresh.IdempotencyKey = "fresh-after-decision"
			projected, err := sender.Send(instance, "source", fresh)
			if err != nil {
				t.Fatal(err)
			}
			if allow {
				if projected.Outcome != msg.OutcomeSent || projected.MessageID != first.MessageID || projected.AwaitingPermission {
					t.Fatal(projected)
				}
			} else if projected.Outcome != msg.OutcomeForbidden {
				t.Fatal("new denied key bypassed direction refusal", projected)
			}
			actor := messageActor{Account: f.self.Account, Program: f.selfProgram, Application: "native-agent", Context: "source"}
			pair := messagesReadPair(t, f.store, actor, f.otherProgram)
			if pair.Count != 1 || len(pair.Pointers) != 1 {
				t.Fatal("replay charged admission", pair)
			}
			queueAfter, err := cas.ReadLimitContext(f.run.ctx, queuePath, 64<<10)
			if err != nil || !bytes.Equal(queueBefore, queueAfter) {
				t.Fatal("replay wrote delivery queue", err)
			}
			after, _, err := f.run.f.a.book.ApplicationHistory()
			if err != nil || len(after) != len(history) {
				t.Fatal("replay created a question", len(after), len(history), err)
			}
		})
	}
}
