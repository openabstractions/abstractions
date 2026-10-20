package main

import (
	"context"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"testing"
)

func TestMessagesPrepareGapRestartsWholePagination(t *testing.T) {
	request := msg.PrepareRequest{Receiver: "app:receiver"}
	queries := []string{}
	pages := []msg.InboxPage{
		{Outcome: msg.OutcomePage, Cursor: "old.64", Inboxes: []msg.Inbox{{Program: request.Receiver, Context: "stale"}}},
		{Outcome: msg.OutcomeGap},
		{Outcome: msg.OutcomePage, Inboxes: []msg.Inbox{{Program: request.Receiver, Context: "fresh-one"}, {Program: request.Receiver, Context: "fresh-two"}}},
	}
	rows, _, outcome, reason := messagePrepareCandidates(t.Context(), request, func(query msg.InboxQuery) (msg.InboxPage, map[string]messageListedName, error) {
		queries = append(queries, query.Cursor)
		page := pages[0]
		pages = pages[1:]
		return page, nil, nil
	})
	if outcome != msg.OutcomePage || reason != "" || len(queries) != 3 || queries[0] != "" || queries[1] != "old.64" || queries[2] != "" || len(rows) != 2 || rows[0].Context != "fresh-one" || rows[1].Context != "fresh-two" {
		t.Fatal(rows, outcome, reason, queries)
	}
}

func TestMessagesPrepareCollectionStopsAtDeadlineAndRefusal(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, _, outcome, reason := messagePrepareCandidates(ctx, msg.PrepareRequest{}, func(msg.InboxQuery) (msg.InboxPage, map[string]messageListedName, error) {
		t.Fatal("read after cancellation")
		return msg.InboxPage{}, nil, nil
	})
	if outcome != msg.OutcomeUnavailable || reason != "discovery_incomplete" {
		t.Fatal(outcome, reason)
	}
	ctx, cancel = context.WithCancel(t.Context())
	rows, _, outcome, reason := messagePrepareCandidates(ctx, msg.PrepareRequest{}, func(msg.InboxQuery) (msg.InboxPage, map[string]messageListedName, error) {
		cancel()
		return msg.InboxPage{Outcome: msg.OutcomePage, Inboxes: []msg.Inbox{{Program: "app:receiver", Context: "late"}}}, nil, nil
	})
	if outcome != msg.OutcomeUnavailable || reason != "discovery_incomplete" || len(rows) != 0 {
		t.Fatal(rows, outcome, reason)
	}
	calls := 0
	rows, _, outcome, _ = messagePrepareCandidates(t.Context(), msg.PrepareRequest{}, func(msg.InboxQuery) (msg.InboxPage, map[string]messageListedName, error) {
		calls++
		return msg.InboxPage{Outcome: msg.OutcomeForbidden}, nil, nil
	})
	if outcome != msg.OutcomeForbidden || len(rows) != 0 || calls != 1 {
		t.Fatal(rows, outcome, calls)
	}
}
