//go:build windows

package main

import (
	"context"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// 2026-11-22: Codex saw no message after 2941 while every Claude send
// reported receiver_watching=true. A Codex receiver that holds its watch but
// publishes that Codex did not confirm its wake reads as not watching.
func TestMessagesReceiptNotWatchingWhileCodexWakeUnconfirmed(t *testing.T) {
	f := messagesObserveFixtureNamed(t, "oa-codex-integration", "oa-codex-integration.exe")
	si, sender := messagesStateOpen(t, f, f.reader, "native-agent", "source")
	ri, receiver := messagesStateOpen(t, f, f.other, "oa-codex-integration", "chat")
	initial := f.other.headersWait("chat", "", 0)
	waiting := *f.other
	ctx, cancel := context.WithCancel(f.other.ctx)
	defer cancel()
	waiting.ctx = ctx
	// The receiver re-arms its watch after every page, as the Codex waker does.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for revision := initial.Revision; ctx.Err() == nil; {
			if page := waiting.headersWait("chat", revision, 30000); page.Outcome == msg.OutcomePage {
				revision = page.Revision
			}
		}
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("headers watcher did not join")
		}
	}()
	// A new message, and the state changes after it, end the receiver's
	// current watch until it re-arms. A replay of the same request reads the
	// receipt flags again; watching reports whether any replay made while the
	// watch was held read as watching.
	watching := func(key string) bool {
		t.Helper()
		request := msg.SendRequest{To: "app:oa-codex-integration", Context: "chat", Kind: msg.KindNote, Subject: key, Body: "note " + key, IdempotencyKey: key, ExpiresUnixMs: f.now.Add(time.Hour).UnixMilli()}
		for range 20 {
			for deadline := time.Now().Add(3 * time.Second); !f.reader.host.messageIsWatching(f.run.ctx, f.self.Account, f.otherProgram, "chat"); time.Sleep(5 * time.Millisecond) {
				if time.Now().After(deadline) {
					t.Fatal("receiver watch not held")
				}
			}
			out, err := sender.Send(si, "source", request)
			if err != nil || out.Outcome != msg.OutcomeSent {
				t.Fatal(out, err)
			}
			if out.ReceiverWatching {
				return true
			}
		}
		return false
	}
	if !watching("watched") {
		t.Fatal("a watching receiver read as not watching")
	}
	if opened, err := receiver.InboxOpen(ri, "chat", "Watching | "+messageWakeUnconfirmedStatus); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	if watching("stuck") {
		t.Fatal("a receiver whose wake is unconfirmed read as watching")
	}
	if opened, err := receiver.InboxOpen(ri, "chat", "Watching"); err != nil || opened.Outcome != msg.OutcomeOpened {
		t.Fatal(opened, err)
	}
	if !watching("confirmed") {
		t.Fatal("a confirmed wake still read as not watching")
	}
}
