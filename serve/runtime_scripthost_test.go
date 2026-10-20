package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-asks/go/scripthostcases"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func TestNativeRoleReceiptStartupFailureKeepsEligibilityUnavailable(t *testing.T) {
	f := newAnswererFixture(t)
	f.enroll(t, f.answerer)
	question := f.ask(t, f.comfy, host.ModelLookupAction, "hf")
	state := t.TempDir()
	d := registration1459Directory(t, state)
	d.owner = f.answerer.Account
	d.roles.Store(&applicationRoles{})
	f.a.rights.applications = d
	if err := os.WriteFile(filepath.Join(state, "applications-v3", "receipts"), []byte("blocked receipt directory"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := enableApplicationV3(t.Context(), &applicationsHost{directory: d}, f.a.rights, f.a, nil); err == nil {
		t.Fatal("receipt startup should fail")
	}
	if got := f.a.rights.policy.ExplainForContext(t.Context(), f.answerer, asks.AnswerAction, rights.MetaScopeApps); got.Outcome != rwire.DecisionOutcomeUnavailable || got.PolicyRevision != "" || got.Source != nil {
		t.Error("failed startup kept nil owner safe", got)
	}
	if f.answerable(f.me(), question, "allow") {
		t.Error("failed startup retained answer-use")
	}
	if f.a.rights.applications != nil || d.roles.Load() != nil {
		t.Error("failed rejoin retained old verified-empty owner")
	}
	permit := true
	revision := f.revision(t)
	edit, err := f.a.rights.policy.ChangeRuleContext(t.Context(), revision, f.answerer, asks.AnswerAction, rights.MetaScopeApps, rights.RuleEdit{Permit: &permit, By: f.panel, TTL: rights.MetaRuleLapse}, func() error { return nil })
	if edit.Outcome != rwire.PolicyEditOutcomeUnavailable || !errors.Is(err, rights.ErrMetaHolderUnavailable) || f.revision(t) != revision {
		t.Fatal("failed startup permitted enrollment edit", edit, err)
	}
	// A verified complete empty directory is available; a missing actual
	// runtime owner is a different state and cannot regain this permit.
	d.roleDecode = func([]byte, string) (bool, error) { return false, nil }
	f.a.rights.applications = d
	if err := d.refreshRoles(t.Context()); err != nil {
		t.Fatal(err)
	}
	if got := f.a.rights.policy.ExplainForContext(t.Context(), f.answerer, asks.AnswerAction, rights.MetaScopeApps); got.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatal("verified empty owner", got)
	}
	if !f.answerable(f.me(), question, "allow") {
		t.Fatal("verified empty owner refused eligible enrolled app")
	}
	f.a.rights.requireApplicationRoles()
	if got := f.a.rights.policy.ExplainForContext(t.Context(), f.answerer, asks.AnswerAction, rights.MetaScopeApps); got.Outcome != rwire.DecisionOutcomeUnavailable || got.PolicyRevision != "" {
		t.Fatal("armed no-owner looked empty", got)
	}
}

func TestDeclaredHostVetoesExistingEnrollmentAndRuntimeConsumers(t *testing.T) {
	f := newAnswererFixture(t)
	if got := f.enroll(t, f.answerer); got.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal(got)
	}
	question := f.ask(t, f.comfy, host.ModelLookupAction, "hf")
	// The holder seam supplies immutable facts, separately from outer refresh.
	f.a.rights.policy.RefreshMetaHolder = nil
	d := &applicationDirectory{}
	d.roles.Store(&applicationRoles{hosts: []asks.DeclaredHost{{Program: f.answerer.Program}}})
	f.a.rights.applications = d
	who := answerer{subject: f.answerer}
	if err := f.a.rights.refuseAnswerer(f.answerer); !errors.Is(err, rights.ErrMetaHolder) {
		t.Fatal(err)
	}
	if got := f.a.rights.decide(context.Background(), f.answerer, asks.AnswerAction, rights.MetaScopeApps); got.Outcome != rwire.DecisionOutcomeForbidden || got.PolicyRevision != "" {
		t.Fatal(got)
	}
	if f.answerable(who, question, "allow") {
		t.Error("retained enrollment answered legacy first use")
	}
	if _, err := f.a.authorizeChoiceAnswerer(context.Background(), who, asks.ChoiceCandidate{AskerProgram: f.comfy.Program}); !errors.Is(err, asks.ErrAnswererForbidden) {
		t.Error("retained enrollment answered choice", err)
	}
	if got := f.a.askForAnswering(context.Background(), who); got.Outcome != rwire.DecisionOutcomeForbidden {
		t.Error("enrollment request", got)
	}
	// No directory acquisition is possible while the atomic guard is read.
	d.mu.Lock()
	done := make(chan error, 1)
	go func() { done <- f.a.rights.refuseAnswerer(f.answerer) }()
	select {
	case err := <-done:
		if !errors.Is(err, rights.ErrMetaHolder) {
			t.Error(err)
		}
	case <-time.After(time.Second):
		t.Error("guard acquired directory lock")
	}
	d.mu.Unlock()
	d.roles.Store(nil)
	if _, _, err := f.a.answererGate(context.Background(), who); !errors.Is(err, rights.ErrMetaHolderUnavailable) {
		t.Error("unknown facts", err)
	}
	d.roles.Store(&applicationRoles{})
	if enrolled, _, err := f.a.answererGate(context.Background(), who); err != nil || !enrolled {
		t.Error("verified false did not restore existing eligible enrollment", enrolled, err)
	}
}

// asks ASK-R7, rights RIGHTS-R22: the shared table runs through every
// runtime caller. The download default refuses a script host (Host); an
// answerer enrollment, a first-use answer and a shared choice answer refuse
// every program that runs foreign code (Foreign), a host named by the
// receiving peer's publisher evidence included. Enrollment reads the program
// alone: a row whose program alone runs foreign code is refused there and is
// enrolled around the check before the answer checks; a row foreign only by
// its evidence passes enrollment and is refused by the answer checks.
func TestScriptHostRuleForEveryRuntimeCaller(t *testing.T) {
	cases := scripthostcases.Cases(t)
	r := testRights(t, t.TempDir())
	if err := r.install(defaultActions, nil); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// aloneForeign is what enrollment, which reads the program alone, refuses.
	aloneForeign := map[string]bool{}
	for _, c := range scripthostcases.WithoutEvidence(cases) {
		aloneForeign[c.Program] = c.Foreign
		subject := rwire.Subject{Account: r.owner, Program: c.Program}
		want := rwire.DecisionOutcomePermitted
		if c.Host {
			want = rwire.DecisionOutcomeNotGranted
		}
		if got := r.decide(ctx, subject, rights.ActionJobSubmit, "job:download").Outcome; got != want {
			t.Errorf("download default for %s: %s, want %s", c.Name, got, want)
		}
		if err := r.refuseAnswerer(subject); (err != nil) != c.Foreign {
			t.Errorf("answerer enrollment of %s: %v, want refused=%t", c.Name, err, c.Foreign)
		}
	}

	f := newAnswererFixture(t)
	question := f.ask(t, f.comfy, host.ModelLookupAction, "hf")
	for _, c := range cases {
		if c.Evidence == (asks.Publisher{}) {
			continue
		}
		subject := rwire.Subject{Account: f.answerer.Account, Program: c.Program}
		wantEnroll := rwire.PolicyEditOutcomeApplied
		if aloneForeign[c.Program] {
			wantEnroll = rwire.PolicyEditOutcomeInvalid
		}
		if got := f.enroll(t, subject).Outcome; got != wantEnroll {
			t.Errorf("answerer enrollment of %s: %v, want %v", c.Name, got, wantEnroll)
		}
		if wantEnroll != rwire.PolicyEditOutcomeApplied {
			// A rule written before this check, or around it: the answer
			// checks below must refuse it on the answerer rule alone.
			holder := f.a.rights.policy.MetaHolder
			f.a.rights.policy.MetaHolder = nil
			if got := f.enroll(t, subject).Outcome; got != rwire.PolicyEditOutcomeApplied {
				t.Fatalf("enrollment of %s around the check: %v", c.Name, got)
			}
			f.a.rights.policy.MetaHolder = holder
		}
		who := answerer{subject: subject, publisher: c.Evidence}
		if f.answerable(who, question, "allow") == c.Foreign {
			t.Errorf("first-use answer by %s: answerable=%t, want %t", c.Name, !c.Foreign, !c.Foreign)
		}
		choice := asks.ChoiceCandidate{AskerProgram: filepath.Join(t.TempDir(), "asker.exe")}
		_, err := f.a.authorizeChoiceAnswerer(ctx, who, choice)
		if (err != nil) != c.Foreign {
			t.Errorf("shared choice answer by %s: %v, want refused=%t", c.Name, err, c.Foreign)
		}
	}
}
