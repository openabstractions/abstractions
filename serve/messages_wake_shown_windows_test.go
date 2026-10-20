//go:build windows

package main

import (
	"errors"
	"os"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// S5 (architect ruling 2026-11-09, review B2 and E1): read state belongs to
// the service. A body Wait that answers its inbox owner with a message ends
// that message's wake for the 30-minute lease, even when the reader never
// passes after and the chat reopens. A Wait whose final recheck fails marks
// nothing. A message still unacknowledged after the lease wakes once more,
// and only once. A message that arrives later wakes.
func TestShownMessageProjectsNoWake(t *testing.T) {
	f := messagesObserveFixture(t)
	instance, client := messagesStateOpen(t, f, f.reader, "native-agent", "target")
	actor := messageActor{Account: f.otherSubject.Account, Program: f.otherProgram, ObservedProgram: f.otherSubject.Program, Application: "other-agent", Context: "source"}
	note := func(key string) (string, map[string]string) {
		t.Helper()
		request := msg.SendRequest{To: "app:native-agent", Context: "target", Kind: msg.KindNote, Subject: key, Body: key, IdempotencyKey: key, ExpiresUnixMs: f.now.Add(2 * time.Hour).UnixMilli()}
		sent := f.store.accept(f.run.ctx, actor, f.selfProgram, request, false, "")
		if sent.Outcome != msg.OutcomeSent {
			t.Fatal(sent)
		}
		r, k, err := f.store.readByID(f.run.ctx, sent.MessageID)
		if err != nil {
			t.Fatal(err)
		}
		return sent.MessageID, map[string]string{k: r.Token}
	}
	wakes := func() bool {
		t.Helper()
		page := f.reader.headersWait("target", "", 0)
		if page.Outcome != msg.OutcomePage {
			t.Fatal(page)
		}
		return page.Wake != ""
	}
	first, permitted := note("first")
	if !wakes() {
		t.Fatal("an unread note did not wake")
	}
	// B2: a Wait whose final recheck fails returns no page and marks nothing.
	messageWaitRecheckFault = func() error { return errors.New("ownership ended") }
	dropped, err := client.Wait(instance, "target", msg.ViewBody, "", "", 0)
	messageWaitRecheckFault = nil
	if err != nil || dropped.Outcome == msg.OutcomePage {
		t.Fatal("a failed recheck still answered", dropped, err)
	}
	if !wakes() {
		t.Fatal("a dropped page ended the wake")
	}
	// The reader takes the page and never passes after.
	body, err := client.Wait(instance, "target", msg.ViewBody, "", "", 0)
	if err != nil || body.Outcome != msg.OutcomePage || len(body.Messages) != 1 || body.Messages[0].Header.MessageID != first {
		t.Fatal(body, err)
	}
	read := f.reader.headersWait("target", "", 0)
	if read.Wake != "" || *read.Unread != 1 {
		t.Fatal("a shown note still woke, or left the unread count", read)
	}
	// The chat reopens under a new owner; its page alone marks nothing new.
	if again := f.store.bodyPage(f.run.ctx, f.self.Account, f.selfProgram, "target", "owner-2", "", permitted); len(again.Messages) != 1 {
		t.Fatal("the unacknowledged note was not delivered again", again)
	}
	if wakes() {
		t.Fatal("a reopened chat was woken for a note it already read")
	}
	// E1: after the lease the unacknowledged note wakes once more.
	*f.now = f.now.Add(messageWakeShownLease)
	if !wakes() {
		t.Fatal("a lost page stranded its note past the lease")
	}
	if again, err := client.Wait(instance, "target", msg.ViewBody, "", "", 0); err != nil || len(again.Messages) != 1 {
		t.Fatal(again, err)
	}
	*f.now = f.now.Add(2 * messageWakeShownLease)
	if wakes() {
		t.Fatal("a note woke a second time after its lease")
	}
	// An unreadable mark suppresses nothing.
	key := ""
	for k := range permitted {
		key = k
	}
	if err := os.WriteFile(f.store.wakeShownPath(key, permitted[key]), []byte("{"), 0600); err != nil {
		t.Fatal(err)
	}
	if !wakes() {
		t.Fatal("an unreadable shown mark suppressed the wake")
	}
	// A message that arrives later wakes.
	note("second")
	if later := f.reader.headersWait("target", "", 0); later.Wake == "" || *later.Unread != 2 {
		t.Fatal("a new note did not wake", later)
	}
}
