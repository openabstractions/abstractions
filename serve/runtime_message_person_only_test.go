package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// messageConsentRecord admits one first-use question through the owner the
// production caller uses and returns its pending record.
func messageConsentRecord(t *testing.T, run *selfRegisteredRun, subject rwire.Subject, action, resource string) asks.Record {
	t.Helper()
	id := ""
	if strings.HasPrefix(subject.Program, "remote:") {
		h := &applicationsHost{directory: run.d, messageRights: run.f.a.rights}
		if pending, err := h.pairedPermissionQuestion(subject, action, resource, strings.TrimPrefix(subject.Program, "remote:"), "Home PC", "agent.exe", &id); err != nil || !pending {
			t.Fatal("paired question", pending, err)
		}
	} else {
		id = run.f.a.admitFirstUse(subject, nil, action, resource)
	}
	record, found, err := run.f.a.book.ApplicationRecord(id)
	if err != nil || !found || !record.Pending() {
		t.Fatal("no pending question", action, resource, id, err)
	}
	return record
}

// AB-1m, VISION 2026-10-31: only the person allows one assistant to message
// or wake another, on this computer or from a paired one. Every enrolled
// assistant, a party to the pair or not, reads forbidden on those questions,
// never sees them listed, and leaves them pending without a permit, while the
// unrelated one still answers a routine question.
func TestMessageConsentQuestionsStayWithThePerson(t *testing.T) {
	run := messagePartiesFixture(t)
	f := run.f
	if err := f.a.rights.policy.RegisterAction(grants.ApplicationMessageStartAction); err != nil {
		t.Fatal(err)
	}
	f.enroll(t, f.comfy)
	f.enroll(t, f.cli)
	messagePartyGrant(t, run, false, true)
	routine := f.ask(t, f.lemonade, inference.ActionComplete, "host:lemonade")
	if !f.answerable(answerer{subject: f.cli}, routine, "allow") {
		t.Fatal("control: the unrelated enrolled assistant cannot answer a routine question")
	}
	paired := rwire.Subject{Account: f.answerer.Account, Program: "remote:" + strings.Repeat("a", 64)}
	start := "app:receiver/start:" + strings.Repeat("b", 64)
	questions := []struct {
		name     string
		subject  rwire.Subject
		action   string
		resource string
	}{
		{"message", f.cli, ActionApplicationMessage, "app:receiver"},
		{"start", f.answerer, grants.ApplicationMessageStartAction, start},
		{"paired message", paired, ActionApplicationMessage, "app:receiver"},
		{"paired start", paired, grants.ApplicationMessageStartAction, start},
	}
	for _, q := range questions {
		t.Run(q.name, func(t *testing.T) {
			record := messageConsentRecord(t, run, q.subject, q.action, q.resource)
			for _, who := range []rwire.Subject{f.answerer, f.comfy, f.cli} {
				for _, option := range record.Options {
					if f.answerable(answerer{subject: who}, record, option) {
						t.Fatal("assistant may answer the person's question", who.Program, option)
					}
				}
				if outcome, _ := f.answer(answerer{subject: who}, record.ID, "allow"); outcome != "forbidden" {
					t.Fatal("assistant answer", who.Program, outcome)
				}
			}
			after, found, err := f.a.book.ApplicationRecord(record.ID)
			if err != nil || !found || !after.Pending() {
				t.Fatal("an assistant's attempt settled the person's question", after, err)
			}
			if got := f.decide(q.subject, q.action, q.resource); got != rwire.DecisionOutcomeNotGranted {
				t.Fatal("permit written without the person", got)
			}
		})
	}
}

// AB-1m step 3, ASK-R3: an exact, expiring delegation answers no question of
// a program its holder is paired with for messages, in either direction, and
// answers again once the pair ends.
func TestMessagingPairRefusesExactDelegation(t *testing.T) {
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			run := messagePartiesFixture(t)
			f := run.f
			f.stop(t)
			q := f.ask(t, f.comfy, inference.ActionComplete, "host:lemonade")
			resource, err := asks.FirstUseAnswerResource(canonicalSubjectProgram(f.comfy.Program), inference.ActionComplete, "host:lemonade", "allow")
			if err != nil {
				t.Fatal(err)
			}
			permit := true
			if _, err := f.a.rights.policy.ChangeRule(f.revision(t), f.answerer, asks.AnswerAction, resource, rights.RuleEdit{Permit: &permit, By: f.panel, TTL: 5 * time.Minute}, func() error { return nil }); err != nil {
				t.Fatal(err)
			}
			if _, err := f.a.authorizeAnswerer(run.ctx, f.me(), q, f.a.scope, "allow"); err != nil {
				t.Fatal("control: exact delegation refused before the pair", err)
			}
			messagePartyGrant(t, run, reverse, true)
			if _, err := f.a.authorizeAnswerer(run.ctx, f.me(), q, f.a.scope, "allow"); !errors.Is(err, asks.ErrAnswererForbidden) {
				t.Fatal("paired exact delegation", err)
			}
			if outcome, _ := f.answer(f.me(), q.ID, "allow"); outcome != "forbidden" {
				t.Fatal("paired exact delegation answered", outcome)
			}
			messagePartyGrant(t, run, reverse, false)
			if _, err := f.a.authorizeAnswerer(run.ctx, f.me(), q, f.a.scope, "allow"); err != nil {
				t.Fatal("ended pair still refuses", err)
			}
		})
	}
}
