package main

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	inference "github.com/openabstractions/abstraction-inference/go"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

// testRights composes the runtime's rights in a fresh state directory, with the
// test executable and an installed Panel beside it as operator programs.
func testRights(t *testing.T, state string) *runtimeRights {
	t.Helper()
	options, _ := isolatedRuntime(t)
	options.stateDir = state
	r, err := composeRights(options)
	if err != nil {
		t.Fatal(err)
	}
	panel := filepath.Join(filepath.Dir(r.operators[0]), "Abstraction Panel")
	if runtime.GOOS == "windows" {
		panel += ".exe"
	}
	r.operators = []string{r.operators[0], panel}
	return r
}

func installedRules(t *testing.T, r *runtimeRights) []rwire.PolicyRule {
	t.Helper()
	snapshot, err := r.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot.Rules
}

func readMarker(t *testing.T, r *runtimeRights) defaultsMarker {
	t.Helper()
	marker, err := r.readMarker()
	if err != nil || marker.Profile != defaultsMarkerProfile {
		t.Fatalf("marker %+v %v", marker, err)
	}
	return marker
}

// A fresh state gets one installation rule per operator program and rule, each
// recorded with its provenance and in the marker. A revoked rule stays revoked
// across a restart; an action seeded later adds only its own rules and leaves a
// rule the person already set as it is. A removed policy file is an outage.
func TestInstallationRulesAndTheDefaultsMarker(t *testing.T) {
	state := t.TempDir()
	r := testRights(t, state)
	exe, panel := r.operators[0], r.operators[1]
	rules := []installationRule{{host.ConfigEditAction, host.ConfigEditResource}, {host.LogHistoryAction, host.LogHistoryResource}}
	if err := r.install(nil, rules); err != nil {
		t.Fatal(err)
	}
	if got := installedRules(t, r); len(got) != 4 {
		t.Fatalf("fresh state: %d rules %+v, want 4", len(got), got)
	}
	for _, program := range r.operators {
		for _, rule := range rules {
			read := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, rule.Action, rule.Resource)
			if read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit || read.Record.Why != installationWhy ||
				read.Record.SetBy != r.self() || read.Record.Expires != "" || read.Record.SetAt == "" {
				t.Fatalf("installation rule %s for %s: %+v %+v", rule.Action, program, read, read.Record)
			}
		}
	}
	if marker := readMarker(t, r); len(marker.Applied) != 4 {
		t.Fatalf("marker %+v", marker)
	}
	if !r.policy.StateRequired {
		t.Fatal("an installed policy file is not required")
	}

	panelSubject := rwire.Subject{Account: r.owner, Program: panel}
	if err := r.policy.Revoke(panelSubject, host.ConfigEditAction, host.ConfigEditResource); err != nil {
		t.Fatal(err)
	}
	restarted := testRights(t, state)
	if err := restarted.install(nil, rules); err != nil {
		t.Fatal(err)
	}
	if read := restarted.policy.ReadRule(panelSubject, host.ConfigEditAction, host.ConfigEditResource); read.Outcome != rwire.RuleReadOutcomeUnknown {
		t.Fatalf("a revoked rule came back after a restart: %+v", read)
	}
	if got := installedRules(t, restarted); len(got) != 3 {
		t.Fatalf("after restart: %d rules, want 3", len(got))
	}

	// A new action: the Panel already holds a deny the person set, the runtime
	// gets the installation permit, and nothing else changes.
	seeded := append(slices.Clone(rules), installationRule{host.ModelLookupAction, "hf"})
	if err := restarted.policy.Set(panelSubject, host.ModelLookupAction, "hf", false); err != nil {
		t.Fatal(err)
	}
	upgraded := testRights(t, state)
	if err := upgraded.install(nil, seeded); err != nil {
		t.Fatal(err)
	}
	if read := upgraded.policy.ReadRule(rwire.Subject{Account: r.owner, Program: exe}, host.ModelLookupAction, "hf"); read.Outcome != rwire.RuleReadOutcomeFound || read.Record.Why != installationWhy {
		t.Fatalf("new action rule for the runtime: %+v", read)
	}
	if read := upgraded.policy.ReadRule(panelSubject, host.ModelLookupAction, "hf"); read.Outcome != rwire.RuleReadOutcomeFound || read.Record.Rule.Permit {
		t.Fatalf("the person's deny was replaced: %+v", read)
	}
	if read := upgraded.policy.ReadRule(panelSubject, host.ConfigEditAction, host.ConfigEditResource); read.Outcome != rwire.RuleReadOutcomeUnknown {
		t.Fatalf("an upgrade restored a revoked rule: %+v", read)
	}
	if got := installedRules(t, upgraded); len(got) != 5 {
		t.Fatalf("after a new action: %d rules, want 5", len(got))
	}
	if marker := readMarker(t, upgraded); len(marker.Applied) != 6 {
		t.Fatalf("marker after a new action: %+v", marker)
	}

	if err := os.Rename(upgraded.path, upgraded.path+".moved"); err != nil {
		t.Fatal(err)
	}
	if d := upgraded.policy.Decide(rwire.Subject{Account: r.owner, Program: exe}, host.ModelLookupAction, "hf"); d.Outcome != rwire.DecisionOutcomeUnavailable {
		t.Fatalf("removed policy file: %+v", d)
	}
}

// panelOperatorRules are the rules the Panel's eleven "Grant to this Panel"
// controls wrote for its own program before 0.4.0 (hostile critic A4). The
// Panel now holds them from installation and shows no such control.
var panelOperatorRules = []installationRule{
	{host.JobInventoryAction, host.JobResource},
	{host.JobCancelAction, host.JobResource},
	{host.ResourceTableReadAction, host.ResourceTableReadResource},
	{credentials.ActionManage, credentials.ResourceAccount},
	{credentials.ActionRead, credentials.ResourceAccount},
	{inference.ActionHostManage, credentials.ResourceAccount},
	{inference.ActionKeyIssue, credentials.ResourceAccount},
	{inference.ActionAuditRead, credentials.ResourceAccount},
	{inference.ActionSelectionManage, credentials.ResourceAccount},
	{inference.ActionSelectionManageFor, credentials.ResourceAccount},
	{ActionProviderManage, credentials.ResourceAccount},
	{routerservice.ActionInventory, routerservice.ResourceInventory},
	{ActionApplicationManage, credentials.ResourceAccount},
}

// Installation names no app (VISION 2026-10-06; default-policy D4): reading
// and starting an entry follow its registration.
func TestInstallationRulesNameNoApplication(t *testing.T) {
	o := host.Options{Applications: struct{ host.ApplicationsService }{}}
	for _, rule := range installationRules(o, nil) {
		if strings.HasPrefix(rule.Resource, "app:") {
			t.Fatalf("installation rule %s on %s names an app", rule.Action, rule.Resource)
		}
	}
}

// A fresh runtime that composes every service the Panel's pages use writes
// the Panel's operator rules once, as installation rules; a second start
// writes nothing.
func TestFreshRuntimeWritesThePanelOperatorRulesOnce(t *testing.T) {
	o := host.Options{JobRoot: t.TempDir(), Router: &router.Router{}, ResourceTable: &resourceservice.Table{},
		Credentials: struct{ host.CredentialsService }{}, Inference: struct{ host.InferenceService }{},
		Registry: struct{ host.RegistryService }{}, Applications: struct{ host.ApplicationsService }{}}
	rules := installationRules(o, nil)
	for _, want := range panelOperatorRules {
		if !slices.Contains(rules, want) {
			t.Fatalf("installation rules lack %s on %s", want.Action, want.Resource)
		}
	}
	actions := []string{}
	for _, rule := range rules {
		if !slices.Contains(actions, rule.Action) {
			actions = append(actions, rule.Action)
		}
	}
	state := t.TempDir()
	r := testRights(t, state)
	if err := r.install(actions, rules); err != nil {
		t.Fatal(err)
	}
	panel := rwire.Subject{Account: r.owner, Program: r.operators[1]}
	for _, want := range panelOperatorRules {
		read := r.policy.ReadRule(panel, want.Action, want.Resource)
		if read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit || read.Record.Why != installationWhy {
			t.Fatalf("Panel rule %s on %s: %+v", want.Action, want.Resource, read)
		}
	}
	first := installedRules(t, r)
	if len(first) != 2*len(rules) {
		t.Fatalf("fresh runtime wrote %d rules, want %d", len(first), 2*len(rules))
	}
	before, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	again := testRights(t, state)
	if err := again.install(actions, rules); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(again.path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("a second start rewrote the policy")
	}
}

// A policy file created before the marker holds the credentials rules the
// runtime granted at creation. Those count as received: a revoked one is not
// restored, and the other installation rules are added.
func TestInstallationRulesAfterAPolicyWithoutAMarker(t *testing.T) {
	state := t.TempDir()
	r := testRights(t, state)
	for _, action := range []string{credentials.ActionManage, credentials.ActionRead, credentials.ActionApply} {
		if err := r.policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	exe := rwire.Subject{Account: r.owner, Program: r.operators[0]}
	if err := r.policy.Set(exe, credentials.ActionManage, credentials.ResourceAccount, true); err != nil {
		t.Fatal(err)
	}
	rules := []installationRule{{credentials.ActionManage, credentials.ResourceAccount}, {credentials.ActionRead, credentials.ResourceAccount}, {host.ConfigEditAction, host.ConfigEditResource}}
	if err := r.install(nil, rules); err != nil {
		t.Fatal(err)
	}
	if read := r.policy.ReadRule(exe, credentials.ActionRead, credentials.ResourceAccount); read.Outcome != rwire.RuleReadOutcomeUnknown {
		t.Fatalf("a credentials rule absent before the marker was written: %+v", read)
	}
	for _, program := range r.operators {
		if read := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, host.ConfigEditAction, host.ConfigEditResource); read.Outcome != rwire.RuleReadOutcomeFound {
			t.Fatalf("config rule for %s: %+v", program, read)
		}
	}
}

// An installation that cannot make its marker (an I/O or lock failure)
// registers nothing, so no policy file appears without a marker: the next
// start would read such a file as one made before the marker and credit the
// credentials rules nobody wrote. Once the marker can be made, both rules
// are written.
func TestInstallationWithoutAMarkerCreatesNoPolicy(t *testing.T) {
	r := testRights(t, t.TempDir())
	if err := os.MkdirAll(r.marker, 0o700); err != nil {
		t.Fatal(err)
	}
	actions := []string{credentials.ActionManage, credentials.ActionRead}
	rules := []installationRule{{credentials.ActionManage, credentials.ResourceAccount}, {credentials.ActionRead, credentials.ResourceAccount}}
	if err := r.install(actions, rules); err == nil {
		t.Fatal("an installation whose marker is a folder succeeded")
	}
	if _, err := os.Stat(r.path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a policy file was created without a marker: %v", err)
	}
	if err := os.Remove(r.marker); err != nil {
		t.Fatal(err)
	}
	if err := r.install(actions, rules); err != nil {
		t.Fatal(err)
	}
	for _, program := range r.operators {
		for _, rule := range rules {
			read := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, rule.Action, rule.Resource)
			if read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit || read.Record.Why != installationWhy {
				t.Fatalf("credentials rule %s for %s after the marker could be made: %+v", rule.Action, program, read)
			}
		}
	}
}

// The marker only grows. A write from an installation that read the marker
// before another one recorded its rules keeps those rules: a lost entry would
// let a later start write again a rule the person revoked.
func TestDefaultsMarkerKeepsEveryRecordedRule(t *testing.T) {
	r := testRights(t, t.TempDir())
	first := appliedRule{r.operators[0], host.ConfigEditAction, host.ConfigEditResource}
	second := appliedRule{r.operators[1], host.LogHistoryAction, host.LogHistoryResource}
	if err := r.writeMarker(defaultsMarker{Profile: defaultsMarkerProfile, Applied: []appliedRule{first}}); err != nil {
		t.Fatal(err)
	}
	if err := r.writeMarker(defaultsMarker{Profile: defaultsMarkerProfile, Applied: []appliedRule{second}}); err != nil {
		t.Fatal(err)
	}
	if got := readMarker(t, r).Applied; len(got) != 2 || !slices.Contains(got, first) || !slices.Contains(got, second) {
		t.Fatalf("marker after two writes: %+v, want both rules", got)
	}
}

// Installations running at once on one state, as two runtime starts or a
// start beside a provider's installFor, write every rule exactly once and
// record every one in the marker. One installation's new policy file is
// never taken for a file made before the marker: the marker then credited
// credentials rules nobody wrote (Linux probe ce09e886, about one round in
// two), so the rounds repeat.
func TestConcurrentInstallationsWriteEachRuleOnce(t *testing.T) {
	// The rules name seeded actions, so installFor never waits on a
	// registration; the installations still register an action at once.
	rules := []installationRule{{host.ConfigEditAction, host.ConfigEditResource}, {host.LogHistoryAction, host.LogHistoryResource}}
	actions := []string{host.ResourceTableReadAction}
	for round := 0; round < 20; round++ {
		state := t.TempDir()
		installers := []*runtimeRights{testRights(t, state), testRights(t, state), testRights(t, state)}
		provider := filepath.Join(filepath.Dir(installers[0].operators[0]), "provider")
		if runtime.GOOS == "windows" {
			provider += ".exe"
		}
		var workers sync.WaitGroup
		errs := make(chan error, len(installers)+1)
		for _, r := range installers {
			workers.Add(1)
			go func() {
				defer workers.Done()
				errs <- r.install(actions, rules)
			}()
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			errs <- installers[0].installFor([]string{provider}, rules)
		}()
		workers.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("round %d: %v", round, err)
			}
		}
		r := installers[0]
		programs := append(slices.Clone(r.operators), provider)
		got := installedRules(t, r)
		if len(got) != len(programs)*len(rules) {
			t.Fatalf("round %d: %d rules %+v, want %d", round, len(got), got, len(programs)*len(rules))
		}
		marker := readMarker(t, r)
		for _, program := range programs {
			for _, rule := range rules {
				read := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, rule.Action, rule.Resource)
				if read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit || read.Record.Why != installationWhy {
					t.Fatalf("round %d: rule %s for %s: %+v", round, rule.Action, program, read)
				}
				if !slices.Contains(marker.Applied, appliedRule{program, rule.Action, rule.Resource}) {
					t.Fatalf("round %d: marker lacks %s for %s: %+v", round, rule.Action, program, marker.Applied)
				}
			}
		}
		if len(marker.Applied) != len(programs)*len(rules) {
			t.Fatalf("round %d: marker holds %d rules, want %d", round, len(marker.Applied), len(programs)*len(rules))
		}
	}
}

// The shipped runtime installs its rules when it composes: the operator program
// holds the config edit rule with why "installation", and the marker exists.
func TestRuntimeWritesInstallationRules(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on this transport")
	}
	options, _ := credentialsRuntime(t)
	self := probeSelf()
	reply, err := runRightsCommand(t, "read", "--endpoint", options.endpoint, "--program", self.Program, "--action", host.ConfigEditAction, "--resource", host.ConfigEditResource)
	if err != nil || reply.Outcome != "found" || reply.Record == nil || reply.Record.Why != installationWhy || reply.Record.SetBy.Program != self.Program {
		t.Fatalf("installation rule %v %+v", err, reply)
	}
	marker, err := os.ReadFile(filepath.Join(options.stateDir, "rights", defaultsMarkerFile))
	if err != nil || !strings.Contains(string(marker), defaultsMarkerProfile) {
		t.Fatalf("marker %q %v", marker, err)
	}
}
