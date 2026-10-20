package main

import (
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-facade/go/client"
)

const (
	titleContextOne = "chat-1111111111111111111111111111111111111111111ab33"
	titleContextTwo = "chat-2222222222222222222222222222222222222222222cd44"
	titleClaude     = "app:oa-claude-integration"
)

var titleNow = time.Date(2026, 11, 9, 15, 0, 0, 0, time.Local)

func titleAt(hour, minute, second int) time.Time {
	return time.Date(2026, 11, 9, hour, minute, second, 0, time.Local)
}

func titleRows() ([]msg.Inbox, map[string]messageInboxStart) {
	rows := []msg.Inbox{
		{Program: titleClaude, Context: titleContextOne, ChatTitle: "AI abstractions"},
		{Program: titleClaude, Context: titleContextTwo, ChatTitle: "AI abstractions"},
		{Program: "app:oa-codex-integration", Context: "chat-3333", ChatTitle: "AI abstractions"},
		{Program: titleClaude, Context: "chat-4444", ChatTitle: "Pictures"},
		{Program: titleClaude, Context: "chat-5555"},
		{Program: titleClaude, Context: "chat-6666"},
		{Program: titleClaude, Context: "inactive"},
	}
	starts := map[string]messageInboxStart{
		messageInboxStartKey(titleClaude, titleContextOne):            {at: titleAt(12, 40, 5), word: messageStartedWord},
		messageInboxStartKey(titleClaude, titleContextTwo):            {at: titleAt(14, 2, 0), word: messageStartedWord},
		messageInboxStartKey("app:oa-codex-integration", "chat-3333"): {at: titleAt(9, 0, 0), word: messageStartedWord},
		messageInboxStartKey(titleClaude, "chat-4444"):                {at: titleAt(9, 0, 0), word: messageStartedWord},
		messageInboxStartKey(titleClaude, "chat-5555"):                {at: titleAt(12, 40, 0), word: messageStartedWord},
		messageInboxStartKey(titleClaude, "chat-6666"):                {at: time.Date(2026, 11, 8, 23, 15, 0, 0, time.Local), word: messageLastSeenWord},
		messageInboxStartKey(titleClaude, "inactive"):                 {},
	}
	return rows, starts
}

func titlesByContext(rows []msg.Inbox) map[string]string {
	got := map[string]string{}
	for _, inbox := range rows {
		got[inbox.Context] = inbox.ChatTitle
	}
	return got
}

// Two Claude sessions in one folder report the same title, and some chats
// have none. Each listed chat carries a name a person can tell apart, made of
// its title and local start time, never its internal context.
func TestMessagesInboxListingNamesEveryChat(t *testing.T) {
	rows, starts := titleRows()
	got := titlesByContext(messageListedInboxTitles(rows, starts, titleNow))
	want := map[string]string{
		titleContextOne: "AI abstractions · 12:40",
		titleContextTwo: "AI abstractions · 14:02",
		"chat-3333":     "AI abstractions",
		"chat-4444":     "Pictures",
		"chat-5555":     "Untitled chat · started 12:40",
		"chat-6666":     "Untitled chat · last seen Nov 8 23:15",
		"inactive":      "New chat",
	}
	for context, title := range want {
		if got[context] != title {
			t.Fatalf("%s listed as %q, want %q", context, got[context], title)
		}
	}
	if rows[0].ChatTitle != "AI abstractions" || rows[4].ChatTitle != "" {
		t.Fatal("listing changed the host titles", rows)
	}
	// Order never changes a name.
	reversed := messageListedInboxTitles([]msg.Inbox{rows[1], rows[0]}, starts, titleNow)
	if reversed[0].ChatTitle != want[titleContextTwo] || reversed[1].ChatTitle != want[titleContextOne] {
		t.Fatal(reversed)
	}
}

// Chats started in one minute add seconds; chats that still share a name are
// counted in the order they started.
func TestMessagesInboxListingWidensSharedNames(t *testing.T) {
	rows := []msg.Inbox{
		{Program: "app:a", Context: "chat-b", ChatTitle: "T"},
		{Program: "app:a", Context: "chat-a", ChatTitle: "T"},
		{Program: "app:a", Context: "chat-c"},
		{Program: "app:a", Context: "chat-d"},
	}
	starts := map[string]messageInboxStart{
		messageInboxStartKey("app:a", "chat-a"): {at: titleAt(12, 40, 5), word: messageStartedWord},
		messageInboxStartKey("app:a", "chat-b"): {at: titleAt(12, 40, 9), word: messageStartedWord},
		messageInboxStartKey("app:a", "chat-c"): {at: titleAt(12, 41, 1), word: messageStartedWord},
		messageInboxStartKey("app:a", "chat-d"): {at: titleAt(12, 41, 1), word: messageStartedWord},
	}
	got := titlesByContext(messageListedInboxTitles(rows, starts, titleNow))
	want := map[string]string{
		"chat-a": "T · 12:40:05",
		"chat-b": "T · 12:40:09",
		"chat-c": "Untitled chat · started 12:41:01",
		"chat-d": "Untitled chat · started 12:41:01 (2)",
	}
	for context, title := range want {
		if got[context] != title {
			t.Fatalf("%s listed as %q, want %q", context, got[context], title)
		}
	}
}

func TestMessagesInboxListingSuffixStaysPreparable(t *testing.T) {
	long := strings.Repeat("é", 200)
	starts := map[string]messageInboxStart{
		messageInboxStartKey("app:a", titleContextOne): {at: titleAt(12, 40, 0), word: messageStartedWord},
		messageInboxStartKey("app:a", titleContextTwo): {at: titleAt(12, 41, 0), word: messageStartedWord},
	}
	listed := messageListedInboxTitles([]msg.Inbox{
		{Program: "app:a", Context: titleContextOne, ChatTitle: long},
		{Program: "app:a", Context: titleContextTwo, ChatTitle: long},
	}, starts, titleNow)
	for _, inbox := range listed {
		normalized, ok := client.NormalizeChatTitle(inbox.ChatTitle)
		if !ok || normalized != inbox.ChatTitle || utf8.RuneCountInString(inbox.ChatTitle) != 200 || !strings.Contains(inbox.ChatTitle, " · 12:4") {
			t.Fatal(len(inbox.ChatTitle), utf8.RuneCountInString(inbox.ChatTitle), ok)
		}
	}
}

// namedList serves one listing the way Prepare reads it: pages of listed
// rows and what each listed name stands for.
func namedList(rows []msg.Inbox, starts map[string]messageInboxStart) func(msg.InboxQuery) (msg.InboxPage, map[string]messageListedName, error) {
	listed, facts := messageListedInboxNames(rows, starts, titleNow)
	names := map[string]messageListedName{}
	for i, row := range listed {
		names[messageInboxStartKey(row.Program, row.Context)] = facts[i]
	}
	return func(query msg.InboxQuery) (msg.InboxPage, map[string]messageListedName, error) {
		return messageInboxesPage(listed, "epoch.digest", query.Cursor), names, nil
	}
}

func contexts(rows []msg.Inbox) []string {
	var out []string
	for _, row := range rows {
		out = append(out, row.Context)
	}
	sort.Strings(out)
	return out
}

// Prepare reads the same listing, so a shown name fixed to its chat selects
// it. The plain shared title offers both chats as choices.
func TestMessagesPrepareSelectsByListedTitle(t *testing.T) {
	rows, starts := titleRows()
	list := namedList(rows, starts)
	request := msg.PrepareRequest{Receiver: titleClaude, ContextTitle: "AI abstractions · 14:02"}
	if normalized, ok := client.NormalizeChatTitle(request.ContextTitle); !ok || normalized != request.ContextTitle {
		t.Fatal("listed title is not a valid context_title")
	}
	candidates, choose, outcome, reason := messagePrepareCandidates(t.Context(), request, list)
	if outcome != msg.OutcomePage || reason != "" || choose || len(candidates) != 1 || candidates[0].Context != titleContextTwo {
		t.Fatal(candidates, choose, outcome, reason)
	}
	request.ContextTitle = "AI abstractions"
	candidates, choose, outcome, _ = messagePrepareCandidates(t.Context(), request, list)
	if outcome != msg.OutcomePage || choose || len(candidates) != 2 {
		t.Fatal(candidates, outcome)
	}
	for title, context := range map[string]string{"Pictures": "chat-4444", "Untitled chat · started 12:40": "chat-5555", "New chat": "inactive"} {
		request.ContextTitle = title
		candidates, choose, _, _ = messagePrepareCandidates(t.Context(), request, list)
		if choose || len(candidates) != 1 || candidates[0].Context != context {
			t.Fatal(title, candidates)
		}
	}
	// A last-seen name moves with use; it offers the untitled chats as choices.
	request.ContextTitle = "Untitled chat · last seen Nov 8 23:15"
	if candidates, choose, _, _ = messagePrepareCandidates(t.Context(), request, list); !choose || len(candidates) != 2 {
		t.Fatal(candidates, choose)
	}
	for _, title := range []string{"AI abstractions · zz", "AI", "AI abstractions ·", "AI abstractions · 12:41", "Untitled chat"} {
		request.ContextTitle = title
		if candidates, _, _, _ = messagePrepareCandidates(t.Context(), request, list); len(candidates) != 0 {
			t.Fatal(title, candidates)
		}
	}
}

// A counted name passes to another chat when an earlier one closes. A name
// shown before the change offers choices or finds nothing; it never selects
// the other chat.
func TestMessagesPrepareCountedNameNeverSelectsAnotherChat(t *testing.T) {
	at := titleAt(14, 2, 11)
	rows := []msg.Inbox{
		{Program: "app:a", Context: "chat-a", ChatTitle: "T"},
		{Program: "app:a", Context: "chat-b", ChatTitle: "T"},
		{Program: "app:a", Context: "chat-c", ChatTitle: "T"},
		{Program: "app:a", Context: "chat-d", ChatTitle: "Other"},
	}
	starts := map[string]messageInboxStart{}
	for _, row := range rows {
		starts[messageInboxStartKey(row.Program, row.Context)] = messageInboxStart{at: at, word: messageStartedWord}
	}
	before := titlesByContext(messageListedInboxTitles(rows, starts, titleNow))
	if before["chat-a"] != "T · 14:02:11" || before["chat-b"] != "T · 14:02:11 (2)" || before["chat-c"] != "T · 14:02:11 (3)" {
		t.Fatal(before)
	}
	prepare := func(rows []msg.Inbox, title string) ([]msg.Inbox, bool) {
		t.Helper()
		candidates, choose, outcome, _ := messagePrepareCandidates(t.Context(), msg.PrepareRequest{Receiver: "app:a", ContextTitle: title}, namedList(rows, starts))
		if outcome != msg.OutcomePage {
			t.Fatal(outcome)
		}
		return candidates, choose
	}
	for _, name := range before {
		if name == "Other" {
			continue
		}
		if candidates, choose := prepare(rows, name); !choose || len(candidates) != 3 {
			t.Fatal(name, candidates, choose)
		}
	}
	// The chosen choice's context selects it; its title only confirms it.
	pinned, choose, _, _ := messagePrepareCandidates(t.Context(), msg.PrepareRequest{Receiver: "app:a", Context: "chat-c", ContextTitle: before["chat-c"]}, namedList(rows, starts))
	if choose || len(pinned) != 1 || pinned[0].Context != "chat-c" {
		t.Fatal(pinned, choose)
	}
	// The first chat closes: the chat listed as "(3)" is now listed as "(2)".
	closed := rows[1:]
	after := titlesByContext(messageListedInboxTitles(closed, starts, titleNow))
	if after["chat-c"] != before["chat-b"] {
		t.Fatal(after)
	}
	for _, name := range []string{before["chat-a"], before["chat-b"], before["chat-c"]} {
		candidates, choose := prepare(closed, name)
		if len(candidates) != 0 && (!choose || len(candidates) != 2) {
			t.Fatal(name, candidates, choose)
		}
	}
	if candidates, choose := prepare(closed, "Other"); choose || len(candidates) != 1 || candidates[0].Context != "chat-d" {
		t.Fatal(candidates, choose)
	}
}

// A last-active time moves as a session is used. A name shown before the
// sessions moved offers choices or finds nothing.
func TestMessagesPrepareLastActiveNameNeverSelectsAnotherSession(t *testing.T) {
	rows := []msg.Inbox{{Program: "app:a", Context: "s1"}, {Program: "app:a", Context: "s2"}}
	active := func(first, second time.Time) map[string]messageInboxStart {
		return map[string]messageInboxStart{
			messageInboxStartKey("app:a", "s1"): {at: first, word: messageLastActiveWord},
			messageInboxStartKey("app:a", "s2"): {at: second, word: messageLastActiveWord},
		}
	}
	shown := titlesByContext(messageListedInboxTitles(rows, active(titleAt(14, 5, 0), titleAt(14, 7, 0)), titleNow))["s1"]
	if shown != "Untitled chat · last active 14:05" {
		t.Fatal(shown)
	}
	for _, moved := range []map[string]messageInboxStart{
		active(titleAt(14, 20, 0), titleAt(14, 5, 0)),  // the other session is now listed by the shown name
		active(titleAt(14, 5, 10), titleAt(14, 5, 40)), // both sessions moved onto one minute
	} {
		candidates, choose, _, _ := messagePrepareCandidates(t.Context(), msg.PrepareRequest{Receiver: "app:a", ContextTitle: shown}, namedList(rows, moved))
		if len(candidates) != 0 && (!choose || strings.Join(contexts(candidates), ",") != "s1,s2") {
			t.Fatal(candidates, choose)
		}
	}
}
