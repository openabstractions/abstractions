//go:build windows

package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type capturedLogs struct {
	mu      sync.Mutex
	records []logging.Record
}

func (c *capturedLogs) Write(r logging.Record) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	// The Asks question records have their own test
	// (TestRuntimeAsksLogsOneRaisedAndOneSettledRecord).
	if r.Msg != questionLogMessage {
		c.records = append(c.records, r)
	}
	return nil
}

// failMetaHolderRefresh fails the nth preparation of the permit owner, as a
// save fails on the owner's install (R13, 2026-11-08).
func failMetaHolderRefresh(f *messagesObserveRun, n int32) (restore func()) {
	policy := f.run.f.a.rights.policy
	refresh := policy.RefreshMetaHolder
	var calls atomic.Int32
	policy.RefreshMetaHolder = func(ctx context.Context) error {
		if calls.Add(1) == n {
			return errors.New("messages: target preparation unavailable")
		}
		if refresh == nil {
			return nil
		}
		return refresh(ctx)
	}
	return func() { policy.RefreshMetaHolder = refresh }
}

// Owner 2026-11-09: the person allowed Claude to message OpenCode four times
// and every save failed, while each answer stood as "allow". An Allow that
// is not saved is released: the requester reads not permitted, and its next
// request, under the same key, asks the person afresh.
func TestMessageAllowNotSavedIsReleasedAndAskedAfresh(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	logs := &capturedLogs{}
	a.useLogs(logs)
	intent := messagePermissionIntent("other-agent", "save-fails")
	first, err := client.RequestContext(ctx, intent)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restore := failMetaHolderRefresh(f, 1)
	edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, first.Answer.ID, func() error { return nil })
	restore()
	if edit.Outcome == rwire.PolicyEditOutcomeApplied {
		t.Fatal("the injected failure saved the permission", edit, err)
	}
	if _, found, err := a.book.ApplicationRecord(first.Answer.ID); err != nil || found {
		t.Fatal("the unsaved Allow still stands", found, err)
	}
	if got := a.rights.policy.Decide(f.self, ActionApplicationMessage, "app:other-agent"); got.Outcome == rwire.DecisionOutcomePermitted {
		t.Fatal("unsaved Allow permits", got)
	}
	again, err := client.RequestContext(ctx, intent)
	if err != nil || again.Outcome != askswire.PermissionOutcomePending || again.Answer == nil || again.Answer.ID == first.Answer.ID {
		t.Fatal("the same key did not ask afresh", again, err)
	}
	if len(logs.records) != 1 {
		t.Fatal("not-saved records", logs.records)
	}
	attrs := logs.records[0].Attrs
	if attrs["question.id"] != first.Answer.ID || attrs["question.outcome"] != "not_applied" || attrs["permission.error"] != "rights: meta-rule holder eligibility unavailable" {
		t.Fatal("not-saved record", attrs)
	}
	// The fresh question saves normally.
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, again.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	revision, _, err = a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, again.Answer.ID, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	if _, found, err := a.book.ApplicationRecord(again.Answer.ID); err != nil || !found {
		t.Fatal("a saved Allow was released", found, err)
	}
}

// A saved Allow whose later save attempt fails keeps its question: Rights'
// completion receipt, read under the Book lock, decides.
func TestSavedMessageAllowStaysWhenALaterSaveFails(t *testing.T) {
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	first, err := client.RequestContext(ctx, messagePermissionIntent("other-agent", "saved-then-fails"))
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	if out, err := operator.AnswerPermissionIfPendingContext(ctx, first.Answer.ID, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
		t.Fatal(out, err)
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, first.Answer.ID, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	revision, _, err = a.rights.policy.SelectorSnapshotContext(ctx)
	if err != nil {
		t.Fatal(err)
	}
	restore := failMetaHolderRefresh(f, 1)
	edit, err := a.applyPermissionAllow(ctx, f.run.f.panel, revision, first.Answer.ID, func() error { return nil })
	restore()
	if err == nil && edit.Outcome == rwire.PolicyEditOutcomeApplied {
		t.Fatal("the injected failure did not fail", edit)
	}
	if _, found, err := a.book.ApplicationRecord(first.Answer.ID); err != nil || !found {
		t.Fatal("a saved Allow was released", found, err)
	}
	if got := a.rights.policy.Decide(f.self, ActionApplicationMessage, "app:other-agent"); got.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatal("the saved permission was lost", got)
	}
}

// The typed save releases only a record it attempted to save. A first-use
// question answered Allow belongs to the legacy save, and stays.
func TestTypedSaveKeepsARecordItNeverSaves(t *testing.T) {
	f, _, _ := messagePermissionFixture(t)
	a := f.run.f.a
	record, _, err := a.book.AskApplication(a.scope, a.display, "legacy-first-use", asks.FirstUseKey,
		map[string]string{"program": f.selfProgram, "action": "abstraction.model/lookup", "resource": "hf"}, listen.Seen{})
	if err != nil {
		t.Fatal(err)
	}
	if _, outcome, err := a.book.AnswerApplicationIfPendingAuthorized(record.ID, "allow", nil); err != nil || outcome != "answered" {
		t.Fatal(outcome, err)
	}
	revision, _, err := a.rights.policy.SelectorSnapshotContext(f.run.ctx)
	if err != nil {
		t.Fatal(err)
	}
	edit, err := a.applyPermissionAllow(f.run.ctx, f.run.f.panel, revision, record.ID, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeInvalid {
		t.Fatal(edit, err)
	}
	if _, found, err := a.book.ApplicationRecord(record.ID); err != nil || !found {
		t.Fatal("the typed save released a record it never saves", found, err)
	}
}

// An Allow whose save fails again within the hour stays released, and the
// app's next request reads unavailable instead of asking the person again
// until the hour passes (security review 2026-11-09).
func TestUnsavedAllowReasksOncePerHour(t *testing.T) {
	now := time.Now()
	saved := unsavedReasks
	unsavedReasks = &unsavedReaskBook{items: map[string]unsavedReask{}, now: func() time.Time { return now }}
	defer func() { unsavedReasks = saved }()
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	intent := messagePermissionIntent("other-agent", "save-fails-twice")
	failOnce := func(id string) {
		t.Helper()
		if out, err := operator.AnswerPermissionIfPendingContext(ctx, id, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
			t.Fatal(out, err)
		}
		revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		restore := failMetaHolderRefresh(f, 1)
		a.applyPermissionAllow(ctx, f.run.f.panel, revision, id, func() error { return nil })
		restore()
		if _, found, err := a.book.ApplicationRecord(id); err != nil || found {
			t.Fatal("the unsaved Allow still stands", found, err)
		}
	}
	first, err := client.RequestContext(ctx, intent)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	failOnce(first.Answer.ID)
	again, err := client.RequestContext(ctx, intent)
	if err != nil || again.Outcome != askswire.PermissionOutcomePending || again.Answer == nil {
		t.Fatal("the first failed save did not re-ask", again, err)
	}
	failOnce(again.Answer.ID)
	held, err := client.RequestContext(ctx, intent)
	if err != nil || held.Outcome != askswire.PermissionOutcomeUnavailable {
		t.Fatal("a second failed save within the hour re-asked", held, err)
	}
	now = now.Add(unsavedReaskInterval)
	later, err := client.RequestContext(ctx, intent)
	if err != nil || later.Outcome != askswire.PermissionOutcomePending || later.Answer == nil {
		t.Fatal("no re-ask after the hour", later, err)
	}
}

// A saved Allow clears its bound: after fail, re-ask and a successful save, a
// later failed save of the same target re-asks once again (review 2026-11-09).
func TestUnsavedAllowSavedClearsReaskBound(t *testing.T) {
	now := time.Now()
	saved := unsavedReasks
	unsavedReasks = &unsavedReaskBook{items: map[string]unsavedReask{}, now: func() time.Time { return now }}
	defer func() { unsavedReasks = saved }()
	f, client, operator := messagePermissionFixture(t)
	ctx := f.run.ctx
	a := f.run.f.a
	intent := messagePermissionIntent("other-agent", "save-fails-then-saves")
	allow := func(id string, fail bool) rwire.SelectorEdit {
		t.Helper()
		if out, err := operator.AnswerPermissionIfPendingContext(ctx, id, "allow"); err != nil || out.Outcome != askswire.OperatorDecisionOutcomeAnswered {
			t.Fatal(out, err)
		}
		revision, _, err := a.rights.policy.SelectorSnapshotContext(ctx)
		if err != nil {
			t.Fatal(err)
		}
		restore := func() {}
		if fail {
			restore = failMetaHolderRefresh(f, 1)
		}
		edit, _ := a.applyPermissionAllow(ctx, f.run.f.panel, revision, id, func() error { return nil })
		restore()
		return edit
	}
	first, err := client.RequestContext(ctx, intent)
	if err != nil || first.Outcome != askswire.PermissionOutcomePending || first.Answer == nil {
		t.Fatal(first, err)
	}
	record, _, err := a.book.ApplicationRecord(first.Answer.ID)
	if err != nil {
		t.Fatal(err)
	}
	key := unsavedRecordKey(record)
	allow(first.Answer.ID, true)
	again, err := client.RequestContext(ctx, intent)
	if err != nil || again.Outcome != askswire.PermissionOutcomePending || again.Answer == nil {
		t.Fatal("the failed save did not re-ask", again, err)
	}
	if edit := allow(again.Answer.ID, false); edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal("the re-asked Allow was not saved", edit)
	}
	if unsavedReasks.held(key) {
		t.Fatal("a saved Allow left its re-ask bound")
	}
	// The person revokes it later and the next save fails: it re-asks once.
	unsavedReasks.released(key)
	if unsavedReasks.held(key) {
		t.Fatal("a failed save after a saved Allow suppressed its re-ask")
	}
}

// The first-use path keys its bound by PermissionRequest, which names the app:
// a spent re-ask for one app leaves another app's first use asking.
func TestUnsavedFirstUseBoundIsPerApp(t *testing.T) {
	now := time.Now()
	saved := unsavedReasks
	unsavedReasks = &unsavedReaskBook{items: map[string]unsavedReask{}, now: func() time.Time { return now }}
	defer func() { unsavedReasks = saved }()
	request := func(program string) asks.PermissionRequest {
		t.Helper()
		p, err := asks.NewPermissionRequest(asks.PermissionTarget{Account: "owner", Program: program, Action: "files.read", Resource: "documents"})
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	one, other := request(`C:\Apps\one.exe`), request(`C:\Apps\other.exe`)
	// releaseUnsavedAllow keys the legacy record; admit keys the request.
	if unsavedRecordKey(asks.Record{Permission: &one}) != unsavedTargetKey(one) {
		t.Fatal("record and first-use keys differ")
	}
	unsavedReasks.released(unsavedTargetKey(one))
	unsavedReasks.released(unsavedTargetKey(one))
	if !unsavedReasks.held(unsavedTargetKey(one)) {
		t.Fatal("the first-use re-ask was not bounded")
	}
	if unsavedReasks.held(unsavedTargetKey(other)) {
		t.Fatal("one app's spent re-ask held another app's first use")
	}
}
