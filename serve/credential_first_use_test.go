package main

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

func TestNamedCredentialUseRemainsSensitiveToEnrolledAndExactAnswerers(t *testing.T) {
	for _, delegated := range []bool{false, true} {
		f := newAnswererFixture(t)
		resource := credentials.ResourceFor("demo-secret")
		question := f.ask(t, f.comfy, credentials.ActionApply, resource)
		if grants.FirstUseTier(credentials.ActionApply, resource, f.a.tierFacts) != asks.TierSensitive {
			t.Fatal("named credential Use lost its owning tier")
		}
		if delegated {
			name, err := asks.FirstUseAnswerResource(f.comfy.Program, credentials.ActionApply, resource, "allow")
			if err != nil {
				t.Fatal(err)
			}
			permit := true
			edit, err := f.a.rights.policy.ChangeRule(f.revision(t), f.answerer, asks.AnswerAction, name,
				rights.RuleEdit{Permit: &permit, By: f.panel, TTL: rights.MetaRuleLapse}, func() error { return nil })
			if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
				t.Fatal("exact answer delegation control failed")
			}
		} else if edit := f.enroll(t, f.answerer); edit.Outcome != rwire.PolicyEditOutcomeApplied {
			t.Fatal("answerer enrollment control failed")
		}
		if f.answerable(f.me(), question, "allow") || f.answerable(f.me(), question, "refuse") {
			t.Fatal("named credential question was listed for an answerer")
		}
		if outcome, _ := f.answer(f.me(), question.ID, "allow"); outcome != "forbidden" {
			t.Fatal("answerer could answer named credential Use")
		}
		if record, ok, _ := f.a.book.ApplicationRecord(question.ID); !ok || !record.Pending() {
			t.Fatal("refused answerer removed the person's pending question")
		}
		if rule := f.a.rights.policy.ReadRule(f.comfy, credentials.ActionApply, resource); rule.Record != nil {
			t.Fatal("refused answerer wrote a credential permit")
		}
	}
}

func TestNativeCallerMissingNamedUseAsksThenRetriesOnlyThatCredential(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := credentialsRuntime(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe = filepath.Clean(exe)
	var transcript strings.Builder
	command := func(stdin string, args ...string) error {
		out, diag, err := runCredentials(t, stdin, args...)
		transcript.WriteString(out + diag)
		if err != nil {
			transcript.WriteString(err.Error())
		}
		return err
	}
	store := func(name, target, consumer string) {
		t.Helper()
		err := command(testCredentialSecret+"\n", "add", name, "--target", target, "--for", consumer, "--from-stdin", "--endpoint", options.endpoint, "--timeout", "30s")
		requireUserScopeAdd(t, err, transcript.String())
	}
	store("demo-secret", "127.0.0.1", "abstraction.download/http-execution@1")
	store("other-secret", "127.0.0.1", "abstraction.download/http-execution@1")
	store("other-consumer", "127.0.0.1", "abstraction.model/resolver@1")
	origin := newGatedOrigin(t, []byte("named Use fixture bytes"))
	sink := t.TempDir()
	download := func(name, locator string) error {
		var out, diag bytes.Buffer
		err := downloadCommand([]string{locator, "--credential", name, "--endpoint", options.endpoint, "--out", sink, "--json", "--timeout", "90s"}, &out, &diag)
		transcript.WriteString(out.String() + diag.String())
		if err != nil {
			transcript.WriteString(err.Error())
		}
		return err
	}
	if err := download("demo-secret", origin.URL+"/first.bin"); err == nil || !strings.Contains(err.Error(), "credential:not_permitted:demo-secret") {
		t.Fatal("missing Use permit did not refuse the native caller")
	}
	if len(origin.authorizations()) != 0 {
		t.Fatal("refused first use reached the target")
	}
	assertCredentialAuditOutcome(t, options.endpoint, "not_granted")
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t))
	questions, err := machine.ResolveAsksOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := machine.ResolveRightsOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	resource := credentials.ResourceFor("demo-secret")
	var id string
	for _, record := range pendingFirstUse(t, options) {
		details, err := askclient.ReadDetailsContext(ctx, questions, probeSelf().Account, record.ID)
		if err != nil || details.Permission == nil {
			t.Fatal("current question lacks structured permission")
		}
		target := details.Permission.Targets[0]
		if target.Action == credentials.ActionApply && target.Resource == resource {
			if id != "" || len(details.Permission.Targets) != 1 || target.Account != probeSelf().Account || !samePrograms(target.Program, exe) {
				t.Fatal("named Use question does not bind exactly the native caller")
			}
			id = record.ID
		}
	}
	if id == "" {
		t.Fatal("missing named Use permit produced no exact OA ask")
	}
	answered := answerFirstUse(t, options, id, "allow")
	if answered.Edit == nil || answered.Edit.Outcome.String() != "applied" || answered.Rule == nil || answered.Rule.Action != credentials.ActionApply || answered.Rule.Resource != resource {
		t.Fatal("recorded Allow lacks its complete exact atomic permit")
	}
	attributedRead, err := questions.ReadAttributedQuestionContext(ctx, id)
	read := attributedRead.Question
	if err != nil || read.Record == nil || read.Record.Option != "allow" {
		t.Fatal("winning answer not retained")
	}
	if err := download("demo-secret", origin.URL+"/allowed.bin"); err != nil {
		t.Fatalf("retry failed: %v", err)
	}
	if got := origin.authorizations(); len(got) != 1 || got[0] != "Bearer "+testCredentialSecret {
		t.Fatal("designated target did not receive one credential application")
	}
	before := len(origin.authorizations())
	if err := command("", "allow", "other-consumer", "--program", exe, "--endpoint", options.endpoint, "--timeout", "30s"); err != nil {
		t.Fatal("control permit for the other consumer failed")
	}
	for _, call := range [][3]string{{"other-secret", origin.URL + "/other.bin", "not_permitted"}, {"other-consumer", origin.URL + "/consumer.bin", "consumer_refused"}, {"demo-secret", strings.Replace(origin.URL, "127.0.0.1", "localhost", 1) + "/target.bin", "target_refused"}} {
		if err := download(call[0], call[1]); err == nil || !strings.Contains(err.Error(), "credential:"+call[2]+":"+call[0]) {
			t.Fatal("name/consumer/target did not return its owning refusal")
		}
	}
	if len(origin.authorizations()) != before {
		t.Fatal("refused name/consumer/target reached the origin")
	}
	subject := rightsclient.Subject{Account: probeSelf().Account, Program: exe}
	rule, err := policy.ReadRuleContext(ctx, subject, credentials.ActionApply, resource)
	if err != nil || rule.Record == nil || !rule.Record.Rule.Permit || rule.Record.Why != grants.FirstUseWhy {
		t.Fatal("exact permit provenance missing")
	}
	revoked, err := policy.RevokeRuleContext(ctx, rule.Revision, subject, credentials.ActionApply, resource)
	if err != nil || revoked.Outcome.String() != "applied" {
		t.Fatal("exact permit revoke failed")
	}
	if replay, err := policy.ApplyFirstUseAllowContext(ctx, revoked.Revision, id); err != nil || replay.Outcome.String() != "conflict" {
		t.Fatal("old Allow recreated a revoked permit")
	}
	if err := download("demo-secret", origin.URL+"/revoked-rule.bin"); err == nil {
		t.Fatal("revoked permit still applied")
	}
	if err := command("", "allow", "demo-secret", "--program", exe, "--deny", "--endpoint", options.endpoint, "--timeout", "30s"); err != nil {
		t.Fatal("exact deny failed")
	}
	if err := download("demo-secret", origin.URL+"/denied.bin"); err == nil {
		t.Fatal("explicit deny still applied")
	}
	assertCredentialAuditOutcome(t, options.endpoint, "denied")
	if err := command("", "allow", "demo-secret", "--program", exe, "--endpoint", options.endpoint, "--timeout", "30s"); err != nil {
		t.Fatal("control permit before holder revoke failed")
	}
	if err := command("", "revoke", "demo-secret", "--endpoint", options.endpoint, "--timeout", "30s"); err != nil {
		t.Fatal("holder revoke failed")
	}
	if err := download("demo-secret", origin.URL+"/holder-revoked.bin"); err == nil || !strings.Contains(err.Error(), "credential:revoked:demo-secret") {
		t.Fatal("revoked credential did not return its owning refusal under a permit")
	}
	if len(origin.authorizations()) != before {
		t.Fatal("revoked/denied use reached the origin")
	}
	if err := command("", "audit", "--endpoint", options.endpoint, "--timeout", "30s"); err != nil {
		t.Fatal("audit read failed")
	}
	if strings.Contains(transcript.String(), testCredentialSecret) {
		t.Fatal("fixture secret appeared in a caller transcript")
	}
	err = filepath.WalkDir(filepath.Dir(options.stateDir), func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.Contains(path, string(filepath.Separator)+"items"+string(filepath.Separator)) {
			return err
		}
		data, err := os.ReadFile(path)
		if err == nil && bytes.Contains(data, []byte(testCredentialSecret)) {
			t.Error("fixture secret appeared in a runtime record")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if log, err := os.ReadFile(options.out); err == nil && bytes.Contains(log, []byte(testCredentialSecret)) {
		t.Fatal("fixture secret appeared in runtime log")
	}
}
