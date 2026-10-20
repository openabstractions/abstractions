//go:build windows

package main

import (
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// Owner R34 (2026-11-12): OpenCode asked Claude two questions with the
// default ten-minute lifetime. Claude replied within a minute; both replies
// expired with their questions, unread, and reported undelivered "expired".

func replyExpirySweep(t *testing.T, f *messagesObserveRun) {
	t.Helper()
	for range 4 {
		if err := f.store.sweepExpiry(f.run.ctx); err != nil {
			t.Fatal(err)
		}
	}
}

// Message IDs carry random bits; the key outlives readByID at expiry.
var replyExpiryKeys = map[string]string{}

// replyExpiryRecord reads the canonical record, or its tombstone after expiry.
func replyExpiryRecord(t *testing.T, f *messagesObserveRun, id string) *messageRecord {
	t.Helper()
	if _, ok := replyExpiryKeys[id]; !ok {
		_, key, err := f.store.readByID(f.run.ctx, id)
		if err != nil || key == "" {
			t.Fatal("record missing", id, err)
		}
		replyExpiryKeys[id] = key
	}
	record, err := f.store.readRecord(f.run.ctx, replyExpiryKeys[id])
	if err != nil || record == nil {
		t.Fatal("record missing", id, err)
	}
	return record
}

func replyExpiryQuestion(t *testing.T, f *messagesObserveRun, sender *msg.MessagesClient, si, body string) msg.SendResult {
	t.Helper()
	prepared, err := sender.Prepare(si, "source", msg.PrepareRequest{Receiver: "app:other-agent", Subject: "Collaboration request", Body: body, AnswerWithinMs: 60000})
	if err != nil || prepared.Outcome != msg.OutcomePage || prepared.SendRequest == nil {
		t.Fatal(prepared, err)
	}
	sent, err := sender.Send(si, "source", *prepared.SendRequest)
	if err != nil || sent.Outcome != msg.OutcomeSent {
		t.Fatal(sent, err)
	}
	return sent
}

func replyExpiryFind(page msg.Page, replyTo string) *msg.Message {
	for i := range page.Messages {
		if page.Messages[i].Header.Kind == msg.KindReply && page.Messages[i].Header.ReplyTo == replyTo {
			return &page.Messages[i]
		}
	}
	return nil
}

// A reply waits for an asker that reads its inbox only after the question
// itself expired, and its delivery then reports delivered.
func TestMessagesReplyOutlivesQuestionUntilAskerReads(t *testing.T) {
	f := messagesObserveFixture(t)
	si, asker := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, replier := messagesStateOpen(t, f, f.other, "other-agent", "target")
	question := replyExpiryQuestion(t, f, asker, si, "can you help?")
	if page, err := replier.Wait(ri, "target", msg.ViewBody, "", "", 0); err != nil || len(page.Messages) != 1 {
		t.Fatal(page, err)
	}
	*f.now = f.now.Add(time.Minute - time.Second)
	reply, err := replier.Reply(ri, "target", question.MessageID, "yes, here is the answer")
	if err != nil || reply.Outcome != msg.OutcomeSent {
		t.Fatal(reply, err)
	}
	original, err := decodeRetainedMessageRequest(replyExpiryRecord(t, f, question.MessageID).Spec)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := replyExpiryRecord(t, f, reply.MessageID).ExpiresUnixMs, original.AnswerByUnixMs+messageReplyGrace.Milliseconds(); got != want {
		t.Fatal("reply lifetime", got, want)
	}
	// The asker's chat reads nothing until well past the question's expiry.
	*f.now = f.now.Add(2 * time.Hour)
	replyExpirySweep(t, f)
	if record := replyExpiryRecord(t, f, reply.MessageID); record.Delivery != nil {
		t.Fatal("an unread reply ended with its question", record.Delivery)
	}
	page, err := asker.Wait(si, "source", msg.ViewBody, "", "", 0)
	if err != nil || page.Outcome != msg.OutcomePage {
		t.Fatal(page, err)
	}
	got := replyExpiryFind(page, question.MessageID)
	if got == nil || got.Body != "yes, here is the answer" {
		t.Fatal("the asker did not receive the reply", page)
	}
	if next, err := asker.Wait(si, "source", msg.ViewBody, page.Cursor, "", 0); err != nil || next.Outcome != msg.OutcomePage {
		t.Fatal(next, err)
	}
	if record := replyExpiryRecord(t, f, reply.MessageID); record.Delivery == nil || record.Delivery.State != msg.DeliveryDelivered {
		t.Fatal("acknowledged reply delivery", record.Delivery)
	}
}

// The reply's lifetime ends at answer_by plus the grace: one millisecond
// before, it still waits; at the boundary, unread, it reports expired.
func TestMessagesReplyExpiryBoundary(t *testing.T) {
	f := messagesObserveFixture(t)
	si, asker := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, replier := messagesStateOpen(t, f, f.other, "other-agent", "target")
	question := replyExpiryQuestion(t, f, asker, si, "boundary question")
	reply, err := replier.Reply(ri, "target", question.MessageID, "boundary answer")
	if err != nil || reply.Outcome != msg.OutcomeSent {
		t.Fatal(reply, err)
	}
	expires := replyExpiryRecord(t, f, reply.MessageID).ExpiresUnixMs
	*f.now = time.UnixMilli(expires - 1)
	replyExpirySweep(t, f)
	if record := replyExpiryRecord(t, f, reply.MessageID); record.Profile != "messages-record@1" || record.Delivery != nil {
		t.Fatal("reply ended before its expiry", record.Profile, record.Delivery)
	}
	*f.now = time.UnixMilli(expires)
	replyExpirySweep(t, f)
	record := replyExpiryRecord(t, f, reply.MessageID)
	if record.Delivery == nil || record.Delivery.State != msg.DeliveryUndelivered || record.Delivery.Reason != msg.UndeliveredReasonExpired {
		t.Fatal("an unread reply at its expiry", record.Delivery)
	}
	if page, err := asker.Wait(si, "source", msg.ViewBody, "", "", 0); err != nil || replyExpiryFind(page, question.MessageID) != nil {
		t.Fatal("an expired reply was handed out", page, err)
	}
}

// A body a Wait returned to its receiver reports delivered at its expiry even
// when the receiving model never passed the page's cursor back; a body never
// returned reports expired.
func TestMessagesHandedBodyExpiresDelivered(t *testing.T) {
	f := messagesObserveFixture(t)
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "other-agent", "target")
	read, err := sender.Send(si, "source", messagesStateRequest(f, "handed-note", msg.KindNote))
	if err != nil || read.Outcome != msg.OutcomeSent {
		t.Fatal(read, err)
	}
	if page, err := receiver.Wait(ri, "target", msg.ViewBody, "", "", 0); err != nil || len(page.Messages) != 1 || page.Messages[0].Header.MessageID != read.MessageID {
		t.Fatal(page, err)
	}
	unread, err := sender.Send(si, "source", messagesStateRequest(f, "unread-note", msg.KindNote))
	if err != nil || unread.Outcome != msg.OutcomeSent {
		t.Fatal(unread, err)
	}
	replyExpiryRecord(t, f, read.MessageID)
	replyExpiryRecord(t, f, unread.MessageID)
	*f.now = f.now.Add(2 * time.Hour)
	replyExpirySweep(t, f)
	if record := replyExpiryRecord(t, f, read.MessageID); record.Delivery == nil || record.Delivery.State != msg.DeliveryDelivered {
		t.Fatal("a note its receiver read expired undelivered", record.Delivery)
	}
	if record := replyExpiryRecord(t, f, unread.MessageID); record.Delivery == nil || record.Delivery.State != msg.DeliveryUndelivered || record.Delivery.Reason != msg.UndeliveredReasonExpired {
		t.Fatal("a note no Wait returned", record.Delivery)
	}
}

// A reply to a question past its answer_by tells the replier at once.
func TestMessagesReplyToClosedQuestionSaysWhy(t *testing.T) {
	f := messagesObserveFixture(t)
	si, asker := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, replier := messagesStateOpen(t, f, f.other, "other-agent", "target")
	question := replyExpiryQuestion(t, f, asker, si, "late question")
	*f.now = f.now.Add(2 * time.Minute)
	out, err := replier.Reply(ri, "target", question.MessageID, "too late")
	if err != nil || out.Outcome != msg.OutcomeForbidden || out.Reason != "question_closed" {
		t.Fatal(out, err)
	}
}
