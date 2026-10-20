package main

import (
	"context"
	"os/user"
	"path/filepath"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type operatorAllowFixture struct {
	a              *runtimeAsks
	path, bookPath string
	target, panel  rwire.Subject
}

func newOperatorAllowFixture(t *testing.T, actions []string) *operatorAllowFixture {
	t.Helper()
	dir := t.TempDir()
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	f := &operatorAllowFixture{path: filepath.Join(dir, "rights.json"), bookPath: filepath.Join(dir, "asks.json"), target: rwire.Subject{Account: owner.Uid, Program: filepath.Join(dir, "requester.exe")}, panel: rwire.Subject{Account: owner.Uid, Program: filepath.Join(dir, "Panel.exe")}}
	f.reload(t, actions)
	return f
}

func (f *operatorAllowFixture) reload(t *testing.T, actions []string) {
	t.Helper()
	p, err := rights.LoadDecisionPolicy(f.path, actions)
	if err != nil {
		t.Fatal(err)
	}
	b, err := asks.LoadApplicationBook(f.bookPath)
	if err != nil {
		t.Fatal(err)
	}
	f.a = &runtimeAsks{book: b, scope: "runtime", display: "runtime", rights: &runtimeRights{policy: p, owner: f.target.Account}, report: func(err error) { t.Error(err) }}
}

func (f *operatorAllowFixture) question(t *testing.T, action, resource string) asks.Record {
	t.Helper()
	f.a.admit(f.target, nil, action, resource)
	records, _, err := f.a.book.ApplicationHistory()
	if err != nil || len(records) != 1 {
		t.Fatalf("question: %+v %v", records, err)
	}
	return records[0]
}

func (f *operatorAllowFixture) revision(t *testing.T) string {
	t.Helper()
	state, err := f.a.rights.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return state.Revision
}

func TestOperatorAllowReasksAfterImmediateRevokeAndRestart(t *testing.T) {
	for _, action := range []string{"abstraction.model/lookup", contentWriteAction} {
		t.Run(action, func(t *testing.T) {
			actions := []string{"abstraction.model/lookup", contentWriteAction, contentReadAction}
			f := newOperatorAllowFixture(t, actions)
			resource := "hf"
			if action == contentWriteAction {
				resource = "output:image"
			}
			q1 := f.question(t, action, resource)
			if _, _, err := f.a.book.AnswerApplication(q1.ID, "allow"); err != nil {
				t.Fatal(err)
			}
			// Model a crash/lost reply immediately after the synchronous commit:
			// no application request or marker write runs before revocation.
			_, err := f.a.applyOperatorAllow(context.Background(), f.panel, f.revision(t), q1.ID, func() error { return nil })
			if err != nil {
				t.Fatal(err)
			}
			lapse := action
			if action == contentWriteAction {
				lapse = contentReadAction
			}
			if err := f.a.rights.policy.Revoke(f.target, lapse, resource); err != nil {
				t.Fatal(err)
			}
			f.reload(t, actions)
			before := f.revision(t)
			replay, err := f.a.applyOperatorAllow(context.Background(), f.panel, before, q1.ID, func() error { return nil })
			if err != nil || replay.Outcome != rwire.PolicyEditOutcomeConflict || f.revision(t) != before {
				t.Fatalf("completed Retry changed rights: %+v %v", replay, err)
			}
			q2 := f.question(t, action, resource)
			if !q2.Pending() || q2.ID == q1.ID {
				t.Fatalf("lapsed Allow did not re-ask: Q1=%s Q2=%+v", q1.ID, q2)
			}
			if proof, err := f.a.rights.policy.AppliedFirstUseAllow(q2.ID); err != nil || proof {
				t.Fatalf("Q2 inherited Q1 proof: %v %v", proof, err)
			}
		})
	}
}

func TestOperatorAllowFailedWriteRetainsRetryableOccurrence(t *testing.T) {
	actions := []string{"abstraction.model/lookup", contentWriteAction, contentReadAction}
	f := newOperatorAllowFixture(t, actions)
	q := f.question(t, "abstraction.model/lookup", "hf")
	if _, _, err := f.a.book.AnswerApplication(q.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	failed, err := f.a.applyOperatorAllow(context.Background(), f.panel, "stale", q.ID, func() error { return nil })
	if err != nil || failed.Outcome != rwire.PolicyEditOutcomeConflict {
		t.Fatalf("failed write: %+v %v", failed, err)
	}
	f.reload(t, actions)
	current := f.question(t, "abstraction.model/lookup", "hf")
	if current.ID != q.ID || current.Option != "allow" {
		t.Fatalf("never-landed Allow lost Retry: %+v", current)
	}
	if edit, err := f.a.applyOperatorAllow(context.Background(), f.panel, f.revision(t), q.ID, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("explicit Retry: %+v %v", edit, err)
	}
}

func TestOperatorAllowReasksAfterPairMemberExpiresBeforeRetry(t *testing.T) {
	actions := []string{contentWriteAction, contentReadAction}
	f := newOperatorAllowFixture(t, actions)
	now := time.Now().UTC().Add(-time.Hour)
	f.a.rights.policy.Clock = func() time.Time { return now }
	q1 := f.question(t, contentWriteAction, "output:image")
	if _, _, err := f.a.book.AnswerApplication(q1.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	if edit, err := f.a.applyOperatorAllow(context.Background(), f.panel, f.revision(t), q1.ID, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("complete Allow: %+v %v", edit, err)
	}
	// An ordinary operator replacement may add an expiry. No application
	// request runs between completion and this pair member's lapse.
	permit := true
	if edit, err := f.a.rights.policy.ChangeRule(f.revision(t), f.target, contentReadAction, "output:image", rights.RuleEdit{Permit: &permit, By: f.panel, TTL: time.Millisecond, Exact: true}, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("expiry replacement: %+v %v", edit, err)
	}
	now = now.Add(time.Second)
	if decision := f.a.rights.policy.Decide(f.target, contentReadAction, "output:image"); decision.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("pair member did not expire: %+v", decision)
	}
	f.reload(t, actions)
	before := f.revision(t)
	if edit, err := f.a.applyOperatorAllow(context.Background(), f.panel, before, q1.ID, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeConflict || f.revision(t) != before {
		t.Fatalf("expired completed Allow restored rights: %+v %v", edit, err)
	}
	q2 := f.question(t, contentWriteAction, "output:image")
	if !q2.Pending() || q2.ID == q1.ID {
		t.Fatalf("expired Allow did not re-ask: Q1=%s Q2=%+v", q1.ID, q2)
	}
}

func TestOperatorAllowDerivesFullPairAndRejectsOtherAnswers(t *testing.T) {
	f := newOperatorAllowFixture(t, []string{contentWriteAction})
	q := f.question(t, contentWriteAction, "output:image")
	if _, _, err := f.a.book.AnswerApplication(q.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	before := f.revision(t)
	edit, err := f.a.applyOperatorAllow(context.Background(), f.panel, before, q.ID, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeInvalid || f.revision(t) != before {
		t.Fatalf("incomplete pair applied: %+v %v", edit, err)
	}
	if proof, err := f.a.rights.policy.AppliedFirstUseAllow(q.ID); err != nil || proof {
		t.Fatalf("partial proof: %v %v", proof, err)
	}
	for _, kind := range []string{"pending", "refuse", "never", "responder", "foreign"} {
		t.Run(kind, func(t *testing.T) {
			f := newOperatorAllowFixture(t, []string{"abstraction.model/lookup"})
			scope := f.a.scope
			if kind == "foreign" {
				scope = "foreign"
			}
			q, _, err := f.a.book.AskApplication(scope, "runtime", "test-key", asks.FirstUseKey, map[string]string{"program": f.target.Program, "action": "abstraction.model/lookup", "resource": "hf"}, listen.Seen{})
			if err != nil {
				t.Fatal(err)
			}
			if kind == "responder" {
				_, _, err = f.a.book.AnswerApplicationAsResponder(q.ID, "allow", f.panel.Program, func(asks.Record, string) error { return nil })
			} else if kind != "pending" {
				option := kind
				if kind == "foreign" {
					option = "allow"
				}
				_, _, err = f.a.book.AnswerApplication(q.ID, option)
			}
			if err != nil {
				t.Fatal(err)
			}
			before := f.revision(t)
			edit, err := f.a.applyOperatorAllow(context.Background(), f.panel, before, q.ID, func() error { return nil })
			if err != nil || edit.Outcome != rwire.PolicyEditOutcomeInvalid || f.revision(t) != before {
				t.Fatalf("other answer applied: %+v %v", edit, err)
			}
		})
	}
}
