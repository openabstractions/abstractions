package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// enrolmentQuestions are the pending first-use questions for who's
// enrollment, the right abstraction.asks/question.answer on apps.
func (f *answererFixture) enrolmentQuestions(t *testing.T, who rwire.Subject) []asks.Record {
	t.Helper()
	entries, _, err := f.a.book.ApplicationHistoryScoped()
	if err != nil {
		t.Fatal(err)
	}
	var out []asks.Record
	for _, e := range entries {
		if p, act, res, ok := asks.FirstUse(e.Record.Key, e.Record.About, e.Record.Text); ok && e.Record.Pending() && samePrograms(p, who.Program) && act == asks.AnswerAction && res == rights.MetaScopeApps {
			out = append(out, e.Record)
		}
	}
	return out
}

// ASK-R10: an assistant asks for its enrollment like any other right. The
// request reads not_granted and admits one first-use question; asking again
// replays it; another enrolled assistant can neither see nor answer it; the
// person's Allow writes the enrollment the Panel writes, with its lapse and
// reason; afterwards the request reads permitted and the assistant answers.
func TestAnAssistantAsksForItsEnrollmentLikeAnyRight(t *testing.T) {
	f := newAnswererFixture(t)
	ctx := context.Background()
	if d := f.a.askForAnswering(ctx, f.me()); d.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("the first request %v", d.Outcome)
	}
	f.a.askForAnswering(ctx, f.me())
	pending := f.enrolmentQuestions(t, f.answerer)
	if len(pending) != 1 {
		t.Fatalf("the request admitted %d questions", len(pending))
	}
	q := pending[0]
	if tier := grants.FirstUseTier(asks.AnswerAction, rights.MetaScopeApps, grants.TierFacts{}); tier.Answerable() {
		t.Fatalf("the enrollment question is %s, answerable by an assistant", tier)
	}
	// Another assistant, enrolled, is no answerer of it.
	if edit := f.enroll(t, f.cli); edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(edit.Outcome)
	}
	other := answerer{subject: f.cli}
	if f.answerable(other, q, "allow") || f.answerable(other, q, "refuse") {
		t.Fatal("an assistant may answer the enrollment question")
	}
	if outcome, _ := f.answer(other, q.ID, "allow"); outcome != "forbidden" {
		t.Fatalf("an assistant answered the enrollment question: %s", outcome)
	}
	// The person's Allow.
	if _, _, err := f.a.book.AnswerApplication(q.ID, "allow"); err != nil {
		t.Fatal(err)
	}
	edit, err := f.a.applyOperatorAllow(ctx, f.panel, f.revision(t), q.ID, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("the Allow %+v %v", edit, err)
	}
	read := f.a.rights.policy.ReadRule(f.answerer, asks.AnswerAction, rights.MetaScopeApps)
	if read.Record == nil || !read.Record.Rule.Permit || !samePrograms(read.Record.SetBy.Program, f.panel.Program) || read.Record.Why != grants.EnrollmentWhy("opencode") {
		t.Fatalf("the enrollment %+v", read.Record)
	}
	expires, err := time.Parse(time.RFC3339Nano, read.Record.Expires)
	if err != nil || time.Until(expires) < rights.MetaRuleLapse-time.Hour || time.Until(expires) > rights.MetaRuleLapse+time.Hour {
		t.Fatalf("the enrollment does not lapse as the Panel's does: %q %v", read.Record.Expires, err)
	}
	if d := f.a.askForAnswering(ctx, f.me()); d.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatalf("after the Allow %v", d.Outcome)
	}
	routine := f.ask(t, f.comfy, host.ModelLookupAction, "hf")
	if !f.answerable(f.me(), routine, "allow") {
		t.Fatal("the enrolled assistant cannot answer a routine question")
	}
}

// ASK-R10, ASK-R2: the person's Not now stands for the first-use refuse
// interval; a request inside it asks nothing, and one after it asks again.
func TestANotNowEnrollmentIsAskedAgainOnlyAfterTheInterval(t *testing.T) {
	previous := firstUseRefuseInterval
	firstUseRefuseInterval = time.Second
	t.Cleanup(func() { firstUseRefuseInterval = previous })
	f := newAnswererFixture(t)
	ctx := context.Background()
	f.a.askForAnswering(ctx, f.me())
	first := f.enrolmentQuestions(t, f.answerer)
	if len(first) != 1 {
		t.Fatalf("questions %d", len(first))
	}
	if _, _, err := f.a.book.AnswerApplication(first[0].ID, "refuse"); err != nil {
		t.Fatal(err)
	}
	answered := time.Now()
	if d := f.a.askForAnswering(ctx, f.me()); d.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("after Not now %v", d.Outcome)
	}
	if again := f.enrolmentQuestions(t, f.answerer); len(again) != 0 {
		if time.Since(answered) < firstUseRefuseInterval {
			t.Fatalf("Not now was asked again inside the interval: %d", len(again))
		}
		t.Skip("the request outlasted the interval on this machine")
	}
	time.Sleep(time.Until(answered.Add(firstUseRefuseInterval + 200*time.Millisecond)))
	f.a.askForAnswering(ctx, f.me())
	second := f.enrolmentQuestions(t, f.answerer)
	if len(second) != 1 || second[0].ID == first[0].ID {
		t.Fatalf("not asked again after the interval: %+v", second)
	}
	if read := f.a.rights.policy.ReadRule(f.answerer, asks.AnswerAction, rights.MetaScopeApps); read.Record != nil {
		t.Fatalf("Not now wrote a rule %+v", read.Record)
	}
}

// stopAs writes what the Panel's Stop writes (monitor rights_assistant.go):
// the enrollment removed, then a deny on it, for the refuse interval or,
// for "Stop and don't ask again", with no expiry.
func (f *answererFixture) stopAs(t *testing.T, dontAskAgain bool) {
	t.Helper()
	f.stop(t)
	ttl, why := grants.FirstUseRefuseInterval, grants.EnrollmentStoppedWhy
	if dontAskAgain {
		ttl, why = 0, grants.EnrollmentNeverAgainWhy
	}
	deny := false
	edit, err := f.a.rights.policy.ChangeRule(f.revision(t), f.answerer, asks.AnswerAction, rights.MetaScopeApps,
		rights.RuleEdit{Permit: &deny, By: f.panel, TTL: ttl, Why: why, Exact: true}, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("the Stop's deny %+v %v", edit, err)
	}
}

// enrolByPrompt is the person's Allow of the enrollment question.
func (f *answererFixture) enrolByPrompt(t *testing.T) {
	t.Helper()
	f.a.askForAnswering(context.Background(), f.me())
	pending := f.enrolmentQuestions(t, f.answerer)
	if len(pending) != 1 {
		t.Fatalf("enrollment questions %d", len(pending))
	}
	if _, _, err := f.a.book.AnswerApplication(pending[0].ID, "allow"); err != nil {
		t.Fatal(err)
	}
	if edit, err := f.a.applyOperatorAllow(context.Background(), f.panel, f.revision(t), pending[0].ID, func() error { return nil }); err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("the Allow %+v %v", edit, err)
	}
}

// ASK-R10: a plain Stop keeps the enrollment question away for the refuse
// interval, then the assistant's request asks the person again; "Stop and
// don't ask again" never asks again.
func TestStopQuietensTheEnrollmentAskAndStopForeverEndsIt(t *testing.T) {
	ctx := context.Background()
	t.Run("plain", func(t *testing.T) {
		f := newAnswererFixture(t)
		f.enrolByPrompt(t)
		f.stopAs(t, false)
		if d := f.a.askForAnswering(ctx, f.me()); d.Outcome != rwire.DecisionOutcomeDenied {
			t.Fatalf("inside the quiet period %v", d.Outcome)
		}
		if pending := f.enrolmentQuestions(t, f.answerer); len(pending) != 0 {
			t.Fatal("asked inside the quiet period")
		}
		f.advance(grants.FirstUseRefuseInterval + time.Second)
		if d := f.a.askForAnswering(ctx, f.me()); d.Outcome != rwire.DecisionOutcomeNotGranted {
			t.Fatalf("after the quiet period %v", d.Outcome)
		}
		if pending := f.enrolmentQuestions(t, f.answerer); len(pending) != 1 {
			t.Fatalf("not asked again after the quiet period: %d", len(pending))
		}
	})
	t.Run("don't ask again", func(t *testing.T) {
		f := newAnswererFixture(t)
		f.enrolByPrompt(t)
		f.stopAs(t, true)
		f.advance(rights.MetaRuleLapse + time.Hour)
		if d := f.a.askForAnswering(ctx, f.me()); d.Outcome != rwire.DecisionOutcomeDenied {
			t.Fatalf("after don't ask again %v", d.Outcome)
		}
		if pending := f.enrolmentQuestions(t, f.answerer); len(pending) != 0 {
			t.Fatal("asked after don't ask again")
		}
	})
}

// ASK-R7, ASK-R10: a script host, an operator program, a signed interpreter
// renamed after a client and the runtime itself cannot ask for an
// enrollment, and no question is admitted for them.
func TestScriptHostsAndOperatorsCannotAskForAnEnrollment(t *testing.T) {
	f := newAnswererFixture(t)
	python := rwire.Subject{Account: f.answerer.Account, Program: filepath.Join(filepath.Dir(f.answerer.Program), "python.exe")}
	if err := os.WriteFile(python.Program, []byte("python"), 0o755); err != nil {
		t.Fatal(err)
	}
	runtimeProgram := rwire.Subject{Account: f.answerer.Account, Program: f.a.rights.self().Program}
	node := answerer{subject: f.lemonade, publisher: asks.Publisher{Subject: "OpenJS Foundation", Trusted: true, OriginalName: "node.exe"}}
	for _, who := range []answerer{{subject: python}, {subject: f.panel}, node, {subject: runtimeProgram}} {
		if d := f.a.askForAnswering(context.Background(), who); d.Outcome != rwire.DecisionOutcomeForbidden {
			t.Fatalf("%s asked for an enrollment: %v", who.subject.Program, d.Outcome)
		}
		if pending := f.enrolmentQuestions(t, who.subject); len(pending) != 0 {
			t.Fatalf("a question was admitted for %s", who.subject.Program)
		}
	}
}
