package main

import (
	"context"
	"path/filepath"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-asks/go/scripthostcases"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// asks ASK-R7, rights RIGHTS-R22: the one script-host rule's shared table runs
// through every runtime caller. The download default refuses a host; an
// answerer enrollment refuses one; a first-use answer and a shared choice
// answer refuse a host named by the receiving peer's publisher evidence even
// when its file name passed enrollment.
func TestScriptHostRuleForEveryRuntimeCaller(t *testing.T) {
	cases := scripthostcases.Cases(t)
	r := testRights(t, t.TempDir())
	if err := r.install(defaultActions, nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, c := range scripthostcases.WithoutEvidence(cases) {
		subject := rwire.Subject{Account: r.owner, Program: c.Program}
		want := rwire.DecisionOutcomePermitted
		if c.Host {
			want = rwire.DecisionOutcomeNotGranted
		}
		if got := r.decide(ctx, subject, rights.ActionJobSubmit, "job:download").Outcome; got != want {
			t.Errorf("download default for %s: %s, want %s", c.Name, got, want)
		}
		if err := r.refuseAnswerer(subject); (err != nil) != c.Host {
			t.Errorf("answerer enrollment of %s: %v, want refused=%t", c.Name, err, c.Host)
		}
	}

	f := newAnswererFixture(t)
	question := f.ask(t, f.comfy, host.ModelLookupAction, "hf")
	for _, c := range cases {
		if c.Evidence == (asks.Publisher{}) {
			continue
		}
		subject := rwire.Subject{Account: f.answerer.Account, Program: c.Program}
		f.enroll(t, subject)
		who := answerer{subject: subject, publisher: c.Evidence}
		if f.answerable(who, question, "allow") == c.Host {
			t.Errorf("first-use answer by %s: answerable=%t, want %t", c.Name, !c.Host, !c.Host)
		}
		choice := asks.ChoiceCandidate{AskerProgram: filepath.Join(t.TempDir(), "asker.exe")}
		_, err := f.a.authorizeChoiceAnswerer(ctx, who, choice)
		if (err != nil) != c.Host {
			t.Errorf("shared choice answer by %s: %v, want refused=%t", c.Name, err, c.Host)
		}
	}
}
