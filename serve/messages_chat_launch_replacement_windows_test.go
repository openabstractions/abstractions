//go:build windows

package main

import (
	"reflect"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesChatLaunchLatestClaimReplacesFactsBehindNativeFence(t *testing.T) {
	f := messagesObserveFixture(t)
	instance, canonical := messagesTypedChatAt(t, f, f.reader, "native-agent", "same-chat", "chat", "source-old")
	launch := &msg.ChatLaunch{Session: "same-chat", Origin: msg.ChatOriginDesktop, Cwd: t.TempDir(), Mode: msg.ChatModePlan}
	claim := msg.ChatBinding{ChatID: "same-chat", SourceInstance: instance, Launch: launch}
	if out, err := f.reader.InboxOpenChat(instance, "source-old", "", claim); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	read := func() messageChatBindingRecord {
		t.Helper()
		rows, err := f.store.retainedChats(f.run.ctx, f.self.Account, f.selfProgram)
		if err != nil || len(rows) != 1 || rows[0].Context != canonical {
			t.Fatal(rows, err)
		}
		return rows[0]
	}
	before := read()
	if before.Launch == nil || before.CwdKey == "" {
		t.Fatal(before)
	}
	if _, err := f.store.contextForChat(f.run.ctx, f.self.Account, f.selfProgram, "same-chat"); err != nil {
		t.Fatal(err)
	}
	if after := read(); !reflect.DeepEqual(before, after) {
		t.Fatal("plain lookup changed launch facts", before, after)
	}
	bad := *launch
	bad.Session = "foreign"
	claim.Launch = &bad
	if out, err := f.reader.InboxOpenChat(instance, "source-old", "", claim); err == nil && out.Outcome == msg.OutcomeOpened {
		t.Fatal("malformed claim accepted", out)
	}
	if after := read(); !reflect.DeepEqual(before, after) {
		t.Fatal("malformed claim mutated stored facts", before, after)
	}
	call, _ := messages1608Call(t)
	replacement := &messagesReceiver{host: f.reader.host, call: call, ctx: f.run.ctx}
	current, next := messagesTypedChatAt(t, f, replacement, "native-agent", "same-chat", "chat", "source-new")
	if next != canonical {
		t.Fatal("replacement changed durable chat", canonical, next)
	}
	if after := read(); after.Launch != nil || after.CwdKey != "" {
		t.Fatal("unsupported replacement retained stale launch", after)
	}
	claim.Launch = launch
	if out, err := f.reader.InboxOpenChat(instance, "source-old", "", claim); err != nil || out.Outcome != msg.OutcomeEnded {
		t.Fatal("retired source was not fenced", out, err)
	}
	if after := read(); after.Launch != nil {
		t.Fatal("retired source restored launch", after)
	}
	claim.SourceInstance = current
	if out, err := replacement.InboxOpenChat(current, "source-new", "", claim); err != nil || out.Outcome != msg.OutcomeOpened {
		t.Fatal(out, err)
	}
	before = read()
	claim.SourceInstance, claim.Launch = instance, nil
	if out, err := f.reader.InboxOpenChat(instance, "source-old", "", claim); err != nil || out.Outcome != msg.OutcomeEnded {
		t.Fatal(out, err)
	}
	if after := read(); !reflect.DeepEqual(before, after) {
		t.Fatal("retired source cleared replacement launch", before, after)
	}
}
