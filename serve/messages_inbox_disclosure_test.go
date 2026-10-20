package main

import (
	"fmt"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

func TestMessagesInboxDisclosureIgnoresOnlyHeartbeatTime(t *testing.T) {
	base := msg.Inbox{Program: "app:agent", Context: "chat", ChatTitle: "title", Status: "ready", Revision: "revision", LastSeenUnixMs: 1, Watching: true, Reach: new(msg.InboxReachOpen), Mine: new(true)}
	for _, tc := range []struct {
		name   string
		change func(*msg.Inbox)
		equal  bool
	}{
		{"heartbeat", func(v *msg.Inbox) { v.LastSeenUnixMs++ }, true},
		{"program", func(v *msg.Inbox) { v.Program = "app:other" }, false},
		{"context", func(v *msg.Inbox) { v.Context = "other" }, false},
		{"title", func(v *msg.Inbox) { v.ChatTitle = "other" }, false},
		{"status", func(v *msg.Inbox) { v.Status = "other" }, false},
		{"revision", func(v *msg.Inbox) { v.Revision = "other" }, false},
		{"watching", func(v *msg.Inbox) { v.Watching = false }, false},
		{"reach", func(v *msg.Inbox) { v.Reach = new(msg.InboxReachQueued) }, false},
		{"mine", func(v *msg.Inbox) { v.Mine = new(false) }, false},
		{"mine-presence", func(v *msg.Inbox) { v.Mine = nil }, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			changed := base
			tc.change(&changed)
			if got := messageInboxDisclosureEqual([]msg.Inbox{base}, []msg.Inbox{changed}); got != tc.equal {
				t.Fatalf("equal = %t, want %t", got, tc.equal)
			}
			if base.LastSeenUnixMs != 1 {
				t.Fatal("comparison mutated original snapshot")
			}
			// Pagination binds every field except the observation timestamp.
			beforeRows := make([]msg.Inbox, 65)
			for i := range beforeRows {
				beforeRows[i] = base
			}
			beforePage := messageRemoteInboxPage(beforeRows, "scope", "")
			beforeRows[0] = changed
			afterPage := messageRemoteInboxPage(beforeRows, "scope", "")
			if got := beforePage.Cursor == afterPage.Cursor; got != tc.equal {
				t.Fatalf("cursor identity unchanged = %t, want %t", got, tc.equal)
			}
		})
	}
	if messageInboxDisclosureEqual([]msg.Inbox{base}, nil) {
		t.Fatal("membership loss accepted")
	}
}

func TestMessagesInboxDisclosureChecksBeyondReturnedPage(t *testing.T) {
	before := make([]msg.Inbox, 65)
	for i := range before {
		before[i] = msg.Inbox{Program: "app:agent", Context: fmt.Sprintf("context-%02d", i), Revision: "revision", LastSeenUnixMs: 1}
	}
	after := append([]msg.Inbox(nil), before...)
	after[64].Status = "changed"
	first := messageRemoteInboxPage(before, "scope", "")
	second := messageRemoteInboxPage(after, "scope", "")
	if !messageInboxDisclosureEqual(first.Inboxes, second.Inboxes) || first.Cursor == second.Cursor {
		t.Fatal("fixture did not isolate a change beyond the page")
	}
	if messageInboxDisclosureEqual(before, after) {
		t.Fatal("complete snapshot change beyond page accepted")
	}
	after[64] = before[64]
	after[64].LastSeenUnixMs++
	if !messageInboxDisclosureEqual(before, after) {
		t.Fatal("heartbeat beyond page refused")
	}
	if continued := messageRemoteInboxPage(after, "scope", first.Cursor); continued.Outcome != msg.OutcomePage || continued.Inboxes[0].LastSeenUnixMs != after[64].LastSeenUnixMs {
		t.Fatal("renewed pagination lost fresh observation", continued)
	}
	if messageInboxDisclosureEqual(before, after[:64]) {
		t.Fatal("membership loss beyond page accepted")
	}
}

func TestMessagesDiscoveryNamesAppearOnlyOnFirstPage(t *testing.T) {
	rows := make([]msg.Inbox, 65)
	for i := range rows {
		rows[i] = msg.Inbox{Program: "app:permitted", Context: fmt.Sprint(i), Revision: "revision"}
	}
	needs := []msg.AgentApplication{{Program: "app:other", Title: "Other agent"}}
	first := messageInboxesPage(rows, "epoch.digest", "", needs...)
	last := messageInboxesPage(rows, "epoch.digest", first.Cursor, needs...)
	if first.Outcome != msg.OutcomePage || len(first.NeedsPermission) != 1 || last.Outcome != msg.OutcomePage || len(last.NeedsPermission) != 0 || len(last.Inboxes) != 1 {
		t.Fatal(first, last)
	}
}
