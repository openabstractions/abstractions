package main

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
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
