package main

import (
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// messageInboxStart is when OA first saw a listed chat, and the word the
// listing uses for that time. A row with no word is a new chat that a
// question would start.
type messageInboxStart struct {
	at   time.Time
	word string
}

const (
	messageStartedWord    = "started"
	messageLastSeenWord   = "last seen"
	messageLastActiveWord = "last active"
	messageUntitledChat   = "Untitled chat"
	messageNewChat        = "New chat"
)

func messageInboxStartKey(program, context string) string { return program + "\x00" + context }

// messageListedInboxTitles gives every listed chat a name a person can tell
// apart from the others of its program. A host title stays as the host set
// it. An untitled chat is named by when OA first saw it, in local time, as
// "Untitled chat · started 12:40". Chats of one program that share a name
// add the time they started, "AI abstractions · 12:40", then seconds, then a
// count. Internal context names never appear. The result depends only on the
// rows, their start times and the current day, so two snapshots of an
// unchanged listing name every chat alike.
func messageListedInboxTitles(rows []msg.Inbox, starts map[string]messageInboxStart, now time.Time) []msg.Inbox {
	out, _ := messageListedInboxNames(rows, starts, now)
	return out
}

// messageListedName is what a listed name stands for. host is the chat's own
// title, and group gathers the chats of one program listed from one base
// name. fixed holds when every part of the name stays with that chat while it
// is listed: its host title and its start time. A count, a last-seen time or
// a last-active time can move to another chat when the listing changes.
type messageListedName struct {
	host, group string
	fixed       bool
}

func messageListedInboxNames(rows []msg.Inbox, starts map[string]messageInboxStart, now time.Time) ([]msg.Inbox, []messageListedName) {
	out := append([]msg.Inbox{}, rows...)
	facts := make([]messageListedName, len(out))
	start := func(i int) messageInboxStart {
		s, ok := starts[messageInboxStartKey(out[i].Program, out[i].Context)]
		if !ok {
			return messageInboxStart{word: messageStartedWord}
		}
		if s.at.UnixMilli() <= 0 {
			s.at = time.Time{}
		}
		return s
	}
	plain := make([]string, len(out))
	for i := range out {
		plain[i] = out[i].ChatTitle
		facts[i] = messageListedName{host: plain[i], group: out[i].Program + "\x00" + plain[i], fixed: true}
		if plain[i] == "" {
			s := start(i)
			out[i].ChatTitle = messageFallbackTitle(s, now, false)
			base := messageUntitledChat
			if s.word == "" {
				base = messageNewChat
			}
			facts[i].group = out[i].Program + "\x00\x00" + base
			facts[i].fixed = s.word == "" || s.word == messageStartedWord && !s.at.IsZero()
		}
	}
	groups := map[[2]string][]int{}
	for i, row := range out {
		key := [2]string{row.Program, row.ChatTitle}
		groups[key] = append(groups[key], i)
	}
	keys := make([][2]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		return keys[i][0] < keys[j][0] || keys[i][0] == keys[j][0] && keys[i][1] < keys[j][1]
	})
	for _, key := range keys {
		members := groups[key]
		if len(members) < 2 {
			continue
		}
		label := func(i int, seconds bool) string {
			if plain[i] == "" {
				return messageFallbackTitle(start(i), now, seconds)
			}
			s := start(i)
			if s.at.IsZero() {
				return plain[i]
			}
			facts[i].fixed = s.word == messageStartedWord
			return messageSuffixedTitle(plain[i], " · "+messageListedTime(s.at, now, seconds))
		}
		names := map[int]string{}
		for _, seconds := range []bool{false, true} {
			seen := map[string]bool{}
			distinct := true
			for _, i := range members {
				names[i] = label(i, seconds)
				distinct = distinct && !seen[names[i]]
				seen[names[i]] = true
			}
			if distinct {
				break
			}
		}
		// Chats that still share a name are counted in the order they started.
		sort.SliceStable(members, func(a, b int) bool {
			x, y := start(members[a]).at, start(members[b]).at
			if !x.Equal(y) {
				return x.Before(y)
			}
			return out[members[a]].Context < out[members[b]].Context
		})
		// A counted name, and the first name its count extends, pass to
		// another chat when one of them closes.
		count := map[string]int{}
		for _, i := range members {
			count[names[i]]++
		}
		seen := map[string]int{}
		for _, i := range members {
			name := names[i]
			if count[name] > 1 {
				facts[i].fixed = false
			}
			if seen[name]++; seen[name] > 1 {
				names[i] = messageSuffixedTitle(name, " ("+strconv.Itoa(seen[name])+")")
			}
			out[i].ChatTitle = names[i]
		}
	}
	return out, facts
}

func messageFallbackTitle(s messageInboxStart, now time.Time, seconds bool) string {
	if s.word == "" {
		return messageNewChat
	}
	if s.at.IsZero() {
		return messageUntitledChat
	}
	return messageUntitledChat + " · " + s.word + " " + messageListedTime(s.at, now, seconds)
}

// A time on another day carries its date, "Nov 8 12:40".
func messageListedTime(at, now time.Time, seconds bool) string {
	at, now = at.In(time.Local), now.In(time.Local)
	layout := "15:04"
	if seconds {
		layout = "15:04:05"
	}
	if y, m, d := at.Date(); y != now.Year() || m != now.Month() || d != now.Day() {
		layout = "Jan 2 " + layout
	}
	return at.Format(layout)
}

// The suffixed title stays within the chat-title bounds Prepare accepts:
// 200 characters and 800 bytes.
func messageSuffixedTitle(title, suffix string) string {
	runes := []rune(title)
	for len(runes) > 0 && (len(runes)+utf8.RuneCountInString(suffix) > 200 || len(string(runes))+len(suffix) > 800) {
		runes = runes[:len(runes)-1]
	}
	return strings.TrimRight(string(runes), " ") + suffix
}

// messageListedNameMatches chooses the chats a context_title names among one
// receiver's listed rows. A name listed for exactly one chat and fixed to it
// selects that chat. A listed name that can move between chats returns every
// chat of its group as choices, so a name seen earlier never selects another
// chat on its own. Otherwise the title matches the chats whose own host title
// it is.
func messageListedNameMatches(rows []msg.Inbox, names map[string]messageListedName, title string) (matched []msg.Inbox, choose bool) {
	fact := func(inbox msg.Inbox) messageListedName {
		if name, ok := names[messageInboxStartKey(inbox.Program, inbox.Context)]; ok {
			return name
		}
		return messageListedName{host: inbox.ChatTitle, group: inbox.Program + "\x00" + inbox.ChatTitle, fixed: true}
	}
	if title == "" {
		return rows, false
	}
	var exact []msg.Inbox
	groups := map[string]bool{}
	for _, inbox := range rows {
		if inbox.ChatTitle == title {
			exact = append(exact, inbox)
			groups[fact(inbox).group] = true
		}
	}
	if len(exact) == 1 && fact(exact[0]).fixed {
		return exact, false
	}
	if len(exact) > 0 {
		for _, inbox := range rows {
			if groups[fact(inbox).group] {
				matched = append(matched, inbox)
			}
		}
		return matched, true
	}
	for _, inbox := range rows {
		if fact(inbox).host == title {
			matched = append(matched, inbox)
		}
	}
	return matched, false
}
