package main

import (
	"context"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// policyOperator edits a real rights decision policy in a temporary folder,
// the store the runtime's rights service decides from. by is the setter the
// service establishes for the editing party: this Panel, or the person's
// own prompt.
type policyOperator struct {
	p  *rights.DecisionPolicy
	by rightsclient.Subject
}

func (o policyOperator) ReadRuleContext(_ context.Context, subject rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error) {
	return o.p.ReadRule(subject, action, resource), nil
}

func (o policyOperator) SetRuleForContext(ctx context.Context, expected string, rule rightsclient.PolicyRule, ttl time.Duration, why string) (rightsclient.PolicyEdit, error) {
	permit := rule.Permit
	// The service's SetRuleFor is an exact edit (authorization/operator.go).
	return o.p.ChangeRuleContext(ctx, expected, rule.Subject, rule.Action, rule.Resource, rights.RuleEdit{Permit: &permit, By: o.by, TTL: ttl, Why: why, Exact: true}, func() error { return nil })
}

func (o policyOperator) RevokeRuleContext(ctx context.Context, expected string, subject rightsclient.Subject, action, resource string) (rightsclient.PolicyEdit, error) {
	return o.p.ChangeRuleContext(ctx, expected, subject, action, resource, rights.RuleEdit{By: o.by}, func() error { return nil })
}

// set writes one rule as by would, outside Setup: the person's own answer.
func (o policyOperator) set(t *testing.T, subject rightsclient.Subject, resource string, permit bool, why string) {
	t.Helper()
	read := o.p.ReadRule(subject, inference.ActionComplete, resource)
	edit, err := o.SetRuleForContext(context.Background(), read.Revision, rightsclient.PolicyRule{Subject: subject, Action: inference.ActionComplete, Resource: resource, Permit: permit}, 0, why)
	if err != nil || edit.Outcome != rightsclient.PolicyEditOutcomeApplied {
		t.Fatalf("set %s: %+v, %v", resource, edit, err)
	}
}

// chatRights is a real rights policy with this Panel's operator and the
// person's prompt as two setters, and the code app's program file.
type chatRights struct {
	policy        *rights.DecisionPolicy
	panel, person policyOperator
	app           setup.StepApp
	subject       rightsclient.Subject
}

func newChatRights(t *testing.T) chatRights {
	t.Helper()
	dir := t.TempDir()
	app := chatStepApp(t, dir)
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "decisions.json"), []string{inference.ActionComplete, grants.ContentWriteAction, grants.ContentReadAction})
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	panel, err := rights.NormalizeDecisionSubject(rightsclient.Subject{Account: account.Uid, Program: self})
	if err != nil {
		t.Fatal(err)
	}
	prompt := filepath.Join(dir, "prompt.exe")
	if err := os.WriteFile(prompt, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	person, err := rights.NormalizeDecisionSubject(rightsclient.Subject{Account: account.Uid, Program: prompt})
	if err != nil {
		t.Fatal(err)
	}
	return chatRights{policy: policy, panel: policyOperator{policy, panel}, person: policyOperator{policy, person}, app: app,
		subject: rightsclient.Subject{Account: account.Uid, Program: app.Program}}
}

// write settles rules for the app's program as Setup does.
func (c chatRights) write(t *testing.T, rules, previous []setup.ApprovedRule) []setup.ApprovedRule {
	t.Helper()
	written, err := writeSetupChatRules(context.Background(), c.panel, c.panel.by, c.subject, rules, previous)
	if err != nil {
		t.Fatal(err)
	}
	return written
}

func (c chatRights) decide(program, resource string) string {
	return c.policy.Decide(rightsclient.Subject{Account: c.subject.Account, Program: program}, inference.ActionComplete, resource).Outcome.String()
}

// chatStepApp is the catalogue's app the setup step gives code, with a
// program file in dir.
func chatStepApp(t *testing.T, dir string) setup.StepApp {
	t.Helper()
	for _, choice := range setup.NewCatalog().StepChoices() {
		if choice.Use != setup.UseCode {
			continue
		}
		program := filepath.Join(dir, "Programs", "app", "App.exe")
		if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(program, []byte("fixture"), 0o600); err != nil {
			t.Fatal(err)
		}
		return setup.StepApp{Name: string(choice.ID), Title: choice.Title, Use: setup.UseCode, Program: program}
	}
	t.Fatal("the catalogue gives no app code")
	return setup.StepApp{}
}

// Owner, 2026-11-01: Setup's answered question grants the app it set up the
// use of the model Setup included. The offer states one sentence and the
// one rule; the grant writes exactly that rule for the registered program,
// and the rights policy then permits that program's chat on the model's
// host, for the subject a first-use question would name, and nothing else.
func TestSetupChatGrantPermitsTheIncludedModelOnly(t *testing.T) {
	c := newChatRights(t)
	official, ok := setup.OfficialProgramOf(setup.AppID(c.app.Name))
	if !ok {
		t.Fatal("the code app has no official program in the catalogue")
	}
	var granted []string
	registered := fwire.ApplicationEntry{Descriptor: fwire.ApplicationDescriptor{Name: c.app.Name, Program: c.app.Program, Title: c.app.Title}}
	o := stepChatOwners{
		apps: func(context.Context) (setupApplications, error) {
			return &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{registered}}}, nil
		},
		grant: func(ctx context.Context, program string, rules, previous []setup.ApprovedRule) ([]setup.ApprovedRule, error) {
			granted = append(granted, program)
			return writeSetupChatRules(ctx, c.panel, c.panel.by, rightsclient.Subject{Account: c.subject.Account, Program: program}, rules, previous)
		},
		signer: func(string) (string, bool) { return official.Signer, true },
	}
	offer, err := o.offer(context.Background(), c.app, setup.StepPick{Title: "Qwen3.6 35B"})
	if err != nil || offer.Reason != "" {
		t.Fatalf("offer = %+v, %v", offer, err)
	}
	if offer.Permission != c.app.Title+" can use Qwen3.6 35B on this computer." || len(offer.Rules) != 1 ||
		offer.Rules[0] != (setup.ApprovedRule{Action: inference.ActionComplete, Resource: "host:" + setup.ChatHost}) {
		t.Fatalf("offer = %+v", offer)
	}
	written, settled, err := o.grantOffer(context.Background(), offer, nil)
	if err != nil || !settled || !slices.Equal(written, offer.Rules) {
		t.Fatalf("grant = %+v %v, %v", written, settled, err)
	}
	// The rule names the registered program, as a first-use question does.
	if len(granted) != 1 || granted[0] != c.app.Program {
		t.Fatalf("granted for %v", granted)
	}
	if got := c.decide(identity.CanonicalProgramPath(c.app.Program), "host:"+setup.ChatHost); got != "permitted" {
		t.Fatalf("the app's chat on the included model's host decided %s", got)
	}
	if got := c.decide(c.app.Program, "host:other-engine"); got == "permitted" {
		t.Fatal("the grant reached another host")
	}
	if got := c.decide(filepath.Join(filepath.Dir(c.app.Program), "Other.exe"), "host:"+setup.ChatHost); got == "permitted" {
		t.Fatal("the grant reached another program")
	}
	read := c.policy.ReadRule(c.subject, inference.ActionComplete, "host:"+setup.ChatHost)
	if !setupWrote(read.Record, c.panel.by) {
		t.Fatalf("provenance = %+v", read)
	}
	// Settling again writes nothing new and keeps the rule as Setup's.
	if again := c.write(t, offer.Rules, written); !slices.Equal(again, offer.Rules) {
		t.Fatalf("settled again = %+v", again)
	}
}

// Review of 17b1c8dea: a rule the person already holds for the same
// program, action and resource stays exactly as they set it. A first-use
// permit keeps its reason and gets no Undo row; a Don't allow stays a deny.
func TestSetupChatGrantLeavesThePersonsRuleAsItIs(t *testing.T) {
	host := "host:" + setup.ChatHost
	rules := []setup.ApprovedRule{{Action: inference.ActionComplete, Resource: host}}
	c := newChatRights(t)
	c.person.set(t, c.subject, host, true, "allowed at first use")
	if written := c.write(t, rules, nil); len(written) != 0 {
		t.Fatalf("Setup claimed the person's permit: %+v", written)
	}
	read := c.policy.ReadRule(c.subject, inference.ActionComplete, host)
	if read.Record == nil || read.Record.Why != "allowed at first use" || !read.Record.Rule.Permit {
		t.Fatalf("the person's permit changed: %+v", read)
	}
	if records, _, err := setupGrantRecords(context.Background(), policyList{c.panel}, c.panel.by); err != nil || len(records) != 0 {
		t.Fatalf("Activity offers Undo on the person's permit: %+v, %v", records, err)
	}

	c = newChatRights(t)
	c.person.set(t, c.subject, host, false, "Don't allow")
	if written := c.write(t, rules, nil); len(written) != 0 {
		t.Fatalf("Setup claimed the person's deny: %+v", written)
	}
	if got := c.decide(c.app.Program, host); got == "permitted" {
		t.Fatal("a reused answer turned the person's Don't allow into an allow")
	}
	if read := c.policy.ReadRule(c.subject, inference.ActionComplete, host); read.Record == nil || read.Record.Rule.Permit || read.Record.Why != "Don't allow" {
		t.Fatalf("the person's deny changed: %+v", read)
	}
}

// Shared owner (grants.WriteProduct): a picture Setup writes its approved
// rules as product grants too. A Don't allow and a first-use permit the
// person holds on the picture engine's host stay as they set them; the
// rules no rule held yet are written.
func TestPictureSetupLeavesThePersonsRule(t *testing.T) {
	approved, err := picturePermissionRules()
	if err != nil {
		t.Fatal(err)
	}
	complete := approved[0]
	if complete.Action != inference.ActionComplete {
		t.Fatalf("the picture rules start with %+v", complete)
	}
	for _, permit := range []bool{false, true} {
		c := newChatRights(t)
		c.person.set(t, c.subject, complete.Resource, permit, "the person's")
		result, err := writeApprovedPermissions(context.Background(), c.panel, c.subject, approved)
		if err != nil || len(result.Held) != 1 || len(result.Written) != len(approved)-1 {
			t.Fatalf("permit=%v: %+v, %v", permit, result, err)
		}
		read := c.policy.ReadRule(c.subject, complete.Action, complete.Resource)
		if read.Record == nil || read.Record.Rule.Permit != permit || read.Record.Why != "the person's" {
			t.Fatalf("permit=%v: the person's rule changed: %+v", permit, read)
		}
		for _, rule := range approved[1:] {
			if read := c.policy.ReadRule(c.subject, rule.Action, rule.Resource); read.Record == nil || read.Record.Why != approvedInSetup {
				t.Fatalf("%s on %s was not written: %+v", rule.Action, rule.Resource, read)
			}
		}
	}
}

// When the model's host is renamed, the rule Setup wrote for the old host
// goes with the renewal, while it is still Setup's; a rule the person set
// on the old host since stays.
func TestSetupChatGrantRemovesItsOldHostRuleOnly(t *testing.T) {
	old := []setup.ApprovedRule{{Action: inference.ActionComplete, Resource: "host:" + setup.ChatHost}}
	renamed := []setup.ApprovedRule{{Action: inference.ActionComplete, Resource: "host:modelhost-renamed"}}
	c := newChatRights(t)
	written := c.write(t, old, nil)
	if written = c.write(t, renamed, written); !slices.Equal(written, renamed) {
		t.Fatalf("renewal wrote %+v", written)
	}
	if read := c.policy.ReadRule(c.subject, inference.ActionComplete, old[0].Resource); read.Outcome != rightsclient.RuleReadOutcomeUnknown {
		t.Fatalf("Setup's old-host rule stayed: %+v", read)
	}
	if got := c.decide(c.app.Program, renamed[0].Resource); got != "permitted" {
		t.Fatalf("the renamed host decided %s", got)
	}

	c = newChatRights(t)
	written = c.write(t, old, nil)
	c.person.set(t, c.subject, old[0].Resource, true, "mine")
	c.write(t, renamed, written)
	if read := c.policy.ReadRule(c.subject, inference.ActionComplete, old[0].Resource); read.Record == nil || read.Record.Why != "mine" {
		t.Fatalf("the person's rule on the old host was removed: %+v", read)
	}
}

// policyList lists a real policy's rules for Activity's reader.
type policyList struct{ policyOperator }

func (l policyList) ListPolicyContext(context.Context, string, int64) (rightsclient.PolicyPage, error) {
	snapshot, err := l.p.OperatorSnapshot()
	if err != nil {
		return rightsclient.PolicyPage{}, err
	}
	return rightsclient.PolicyPage{Outcome: rightsclient.PolicyPageOutcomePage, Revision: snapshot.Revision, Catalog: snapshot.Catalog, Rules: snapshot.Rules, Complete: true}, nil
}

// Nothing is granted to a registration not confirmed yet, to a program that
// changed since the question, to a file without the publisher's signature
// or to a shared interpreter.
func TestSetupChatGrantRefusesWhatTheQuestionDidNotName(t *testing.T) {
	dir := t.TempDir()
	app := chatStepApp(t, dir)
	official, _ := setup.OfficialProgramOf(setup.AppID(app.Name))
	entry := fwire.ApplicationEntry{Descriptor: fwire.ApplicationDescriptor{Name: app.Name, Program: app.Program, Title: app.Title}, SelfRegistered: true}
	writes := 0
	signed := true
	o := stepChatOwners{
		apps: func(context.Context) (setupApplications, error) {
			return &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{entry}}}, nil
		},
		grant: func(_ context.Context, _ string, rules, _ []setup.ApprovedRule) ([]setup.ApprovedRule, error) {
			writes++
			return rules, nil
		},
		signer: func(string) (string, bool) { return official.Signer, signed },
	}
	offer, err := o.offer(context.Background(), app, setup.StepPick{Title: "Qwen3.6 35B"})
	if err != nil || offer.Reason != "" {
		t.Fatalf("offer = %+v, %v", offer, err)
	}
	if _, settled, err := o.grantOffer(context.Background(), offer, nil); err != nil || settled || writes != 0 {
		t.Fatalf("unconfirmed registration: %v, %v writes %d", settled, err, writes)
	}
	entry.SelfRegistered, entry.Descriptor.Program = false, filepath.Join(dir, "Elsewhere.exe")
	if _, settled, err := o.grantOffer(context.Background(), offer, nil); err == nil || settled || writes != 0 {
		t.Fatalf("changed program: %v, %v writes %d", settled, err, writes)
	}
	signed = false
	if offer, err := o.offer(context.Background(), app, setup.StepPick{Title: "Qwen3.6 35B"}); err != nil || !strings.Contains(offer.Reason, "signature") {
		t.Fatalf("unsigned offer = %+v, %v", offer, err)
	}
	shared := app
	shared.Program = filepath.Join(dir, "python.exe")
	signed = true
	if offer, err := o.offer(context.Background(), shared, setup.StepPick{Title: "Qwen3.6 35B"}); err != nil || offer.Reason == "" {
		t.Fatalf("shared interpreter offer = %+v, %v", offer, err)
	}
}

// fakeGrantRules is the rights operator Activity's Setup permissions read.
type fakeGrantRules struct {
	rules   []rightsclient.PolicyRule
	why     map[string]string
	setBy   map[string]rightsclient.Subject
	revoked []rightsclient.PolicyRule
}

func (f *fakeGrantRules) ListPolicyContext(context.Context, string, int64) (rightsclient.PolicyPage, error) {
	return rightsclient.PolicyPage{Outcome: rightsclient.PolicyPageOutcomePage, Revision: "r1", Catalog: []string{inference.ActionComplete}, Rules: f.rules, Complete: true}, nil
}

func (f *fakeGrantRules) ReadRuleContext(_ context.Context, subject rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error) {
	for _, rule := range f.rules {
		if rule.Subject == subject && rule.Action == action && rule.Resource == resource {
			return rightsclient.RuleRead{Outcome: rightsclient.RuleReadOutcomeFound, Revision: "r1",
				Record: &rightsclient.RuleRecord{Rule: rule, SetBy: f.setBy[subject.Program], SetAt: "2026-11-01T12:00:00.000Z", Why: f.why[subject.Program]}}, nil
		}
	}
	return rightsclient.RuleRead{Outcome: rightsclient.RuleReadOutcomeUnknown, Revision: "r1"}, nil
}

func (f *fakeGrantRules) RevokeRuleContext(_ context.Context, expected string, subject rightsclient.Subject, action, resource string) (rightsclient.PolicyEdit, error) {
	if expected != "r1" {
		return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeConflict, Revision: "r1"}, nil
	}
	for i, rule := range f.rules {
		if rule.Subject == subject && rule.Action == action && rule.Resource == resource {
			f.revoked = append(f.revoked, rule)
			f.rules = append(f.rules[:i], f.rules[i+1:]...)
			return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeApplied, Revision: "r2"}, nil
		}
	}
	return rightsclient.PolicyEdit{Outcome: rightsclient.PolicyEditOutcomeConflict, Revision: "r1"}, nil
}

// CONV-U1: Activity lists the chat permission Setup wrote once, as a
// timeline row with Undo, and Undo removes exactly that rule. A rule with
// another reason, or with Setup's reason text but another setter, is
// neither listed nor undone here.
func TestActivityListsSetupPermissionsWithUndo(t *testing.T) {
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	panel := rightsclient.Subject{Account: account.Uid, Program: filepath.Join(root, "Abstraction Panel.exe")}
	rule := func(program string) rightsclient.PolicyRule {
		return rightsclient.PolicyRule{Subject: rightsclient.Subject{Account: account.Uid, Program: filepath.Join(root, program)}, Action: inference.ActionComplete, Resource: "host:" + setup.ChatHost, Permit: true}
	}
	setupRule, personRule, lookalike := rule("coder.exe"), rule("other.exe"), rule("copy.exe")
	f := &fakeGrantRules{rules: []rightsclient.PolicyRule{setupRule, personRule, lookalike},
		why: map[string]string{setupRule.Subject.Program: setupChatProvenance, personRule.Subject.Program: "allowed at first use", lookalike.Subject.Program: setupChatProvenance},
		setBy: map[string]rightsclient.Subject{setupRule.Subject.Program: panel, personRule.Subject.Program: panel,
			lookalike.Subject.Program: lookalike.Subject}}
	before := setupGrantOperator
	t.Cleanup(func() { setupGrantOperator = before })
	setupGrantOperator = func(context.Context) (setupGrantRules, rightsclient.Subject, error) { return f, panel, nil }
	h := newTestPanel(t).handler("test-key")
	got := panelRequest(t, h, "/activity/setup-grants", nil)
	body := got.Body.String()
	if got.Code != http.StatusOK || strings.Count(body, `"undo"`) != 1 || !strings.Contains(body, `coder.exe`) || strings.Contains(body, "other.exe") || strings.Contains(body, "copy.exe") ||
		!strings.Contains(body, "Setup let it use the coding model on this computer") {
		t.Fatalf("list: %d %s", got.Code, body)
	}
	for _, other := range []rightsclient.PolicyRule{personRule, lookalike} {
		undo := activityUndo{Program: other.Subject.Program, Action: other.Action, Resource: other.Resource}
		if got := panelRequest(t, h, "/activity/setup-grants", undo); got.Code != http.StatusConflict || len(f.revoked) != 0 {
			t.Fatalf("undo of %s: %d revoked %v", other.Subject.Program, got.Code, f.revoked)
		}
	}
	undo := activityUndo{Program: setupRule.Subject.Program, Action: setupRule.Action, Resource: setupRule.Resource}
	got = panelRequest(t, h, "/activity/setup-grants", undo)
	if got.Code != http.StatusOK || len(f.revoked) != 1 || f.revoked[0] != setupRule || !strings.Contains(got.Body.String(), "Undone") {
		t.Fatalf("undo: %d %s revoked %v", got.Code, got.Body.String(), f.revoked)
	}
	if got := panelRequest(t, h, "/activity/setup-grants", undo); got.Code != http.StatusConflict {
		t.Fatalf("a second undo: %d", got.Code)
	}
}
