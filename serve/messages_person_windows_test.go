//go:build windows

package main

import (
	"context"
	"os"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func messagesPersonPermitOperator(t *testing.T, f *messagesObserveRun) {
	t.Helper()
	policy := f.reader.host.messageRights.policy
	if err := policy.RegisterAction(ActionApplicationManage); err != nil {
		t.Fatal(err)
	}
	if err := policy.Set(f.self, ActionApplicationManage, "account", true); err != nil {
		t.Fatal(err)
	}
	f.run.d.operator = func(program string) bool { return program == f.self.Program }
}

func TestMessagesPersonNativeOperatorRetainedExactReadAndExpiry(t *testing.T) {
	f := messagesObserveFixture(t)
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	request := msg.SendRequest{To: "app:native-agent", Context: "closed", Kind: msg.KindNote, Subject: "private subject", Body: "exact private text\né 9007199254740993", IdempotencyKey: "person-read", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
	sent := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
	if sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent)
	}
	if page, err := messagesStateClient(f.reader).ReadForPerson(sent.MessageID); err != nil || page.Outcome != msg.OutcomeForbidden || len(page.Messages) != 0 {
		t.Fatal("agent became person reader", page, err)
	}
	messagesPersonPermitOperator(t, f)
	client := messagesStateClient(f.reader)
	page, err := client.ReadForPerson(sent.MessageID)
	if err != nil || page.Outcome != msg.OutcomePage || len(page.Messages) != 1 || page.Messages[0].Body != request.Body || page.Cursor != "" || page.Revision != "" || len(page.Headers) != 0 {
		t.Fatal(page, err)
	}
	key := f.store.key(actor, request.IdempotencyKey)
	record, err := f.store.readRecord(f.run.ctx, key)
	if err != nil || record.Acked {
		t.Fatal("person read acknowledged body", record, err)
	}
	if f.run.d.nativeInboxes != nil && len(f.run.d.nativeInboxes.rows) != 0 {
		t.Fatal("person read opened inbox")
	}
	entries, err := os.ReadDir(f.store.dir + "/cursors")
	if err != nil || len(entries) != 0 {
		t.Fatal("person read created cursor", entries, err)
	}
	if foreign, err := messagesStateClient(f.other).ReadForPerson(sent.MessageID); err != nil || foreign.Outcome != msg.OutcomeForbidden || len(foreign.Messages) != 0 {
		t.Fatal("other kept image read as person", foreign, err)
	}
	if missing, err := client.ReadForPerson("missing-message"); err != nil || missing.Outcome != msg.OutcomePage || len(missing.Messages) != 0 {
		t.Fatal(missing, err)
	}
	*f.now = f.now.Add(2 * time.Hour)
	if expired, err := client.ReadForPerson(sent.MessageID); err != nil || expired.Outcome != msg.OutcomePage || len(expired.Messages) != 0 {
		t.Fatal("expired text disclosed", expired, err)
	}
}

func TestMessagesPersonRechecksOperatorAndKeepsAcknowledgedBody(t *testing.T) {
	f := messagesObserveFixture(t)
	sourceInstance, sender := messagesStateOpen(t, f, f.other, "other-agent", "source")
	receiverInstance, receiver := messagesStateOpen(t, f, f.reader, "native-agent", "target")
	request := msg.SendRequest{To: "app:native-agent", Context: "target", Kind: msg.KindNote, Subject: "read after delivery", Body: "private acknowledged text", IdempotencyKey: "person-ack", ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
	sent, err := sender.Send(sourceInstance, "source", request)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	body, err := receiver.Wait(receiverInstance, "target", msg.ViewBody, "", "", 0)
	if err != nil || len(body.Messages) != 1 {
		t.Fatal(body, err)
	}
	if ack, err := receiver.Wait(receiverInstance, "target", msg.ViewBody, body.Cursor, "", 0); err != nil || len(ack.Messages) != 0 {
		t.Fatal(ack, err)
	}
	messagesPersonPermitOperator(t, f)
	if page, err := receiver.ReadForPerson(sent.MessageID); err != nil || len(page.Messages) != 1 || page.Messages[0].Body != request.Body {
		t.Fatal("ack removed retained text", page, err)
	}
	calls := 0
	f.run.d.operator = func(string) bool { calls++; return calls == 1 }
	if page, err := receiver.ReadForPerson(sent.MessageID); err != nil || page.Outcome != msg.OutcomeForbidden || len(page.Messages) != 0 || calls != 2 {
		t.Fatal("operator not rechecked", page, err, calls)
	}
	ctx, cancel := context.WithCancel(f.run.ctx)
	cancel()
	canceled := &messagesReceiver{host: f.reader.host, call: f.reader.call, ctx: ctx}
	if page, err := canceled.ReadForPerson(sent.MessageID); err != nil || page.Outcome != msg.OutcomeUnavailable || len(page.Messages) != 0 {
		t.Fatal(page, err)
	}
}

func TestMessagesPersonNeverOfferedRemotely(t *testing.T) {
	for _, method := range []string{"ReadForPerson", "InboxOpen", "Wait"} {
		if remoteOfferedMethod("abstraction.facade/messages@1", method) {
			t.Fatal("native message method offered remotely", method)
		}
	}
	for _, method := range []string{"InboxesList", "Send"} {
		if !remoteOfferedMethod("abstraction.facade/messages@1", method) {
			t.Fatal("approved paired method missing", method)
		}
	}
}
