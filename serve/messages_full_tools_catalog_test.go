package main

import (
	"context"
	"path/filepath"
	"slices"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// S5 ruling 2026-11-22: the receiving agent's MCP connection decides its own
// full-tools rule on a sender, so the runtime's rights catalogue carries the
// action wherever Messages run. Without it the rule is an unknown action and
// every peer-started turn stays reply-only.
func TestMessagesRightsCatalogCarriesFullToolsRule(t *testing.T) {
	var o host.Options
	configureApplications(&o, &applicationsHost{messages: &messageStore{}}, "")
	if !slices.Contains(o.RightsActions, grants.ApplicationMessageFullToolsAction) || !slices.Contains(o.RightsActions, ActionApplicationMessage) {
		t.Fatal("rights actions", o.RightsActions)
	}
	var none host.Options
	configureApplications(&none, &applicationsHost{}, "")
	if slices.Contains(none.RightsActions, grants.ApplicationMessageFullToolsAction) {
		t.Fatal("the full-tools rule was registered without Messages")
	}
}

// S5 review: with the runtime's real policy and definitions, the rule the
// Panel's switch writes for receiving program P through the operator edit
// is permitted when P decides it, and refused for another agent's image and
// for another sender.
func TestMessagesFullToolsRuleDecidesOnlyForTheReceiverProgram(t *testing.T) {
	var o host.Options
	configureApplications(&o, &applicationsHost{messages: &messageStore{}}, "")
	catalog, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	policy, err := rights.LoadDecisionPolicyWithDefinitions(filepath.Join(state, "decisions.json"), rwire.ResourceActions, catalog)
	if err != nil {
		t.Fatal(err)
	}
	r := &runtimeRights{policy: policy, owner: "person", path: filepath.Join(state, "decisions.json"), marker: filepath.Join(state, defaultsMarkerFile)}
	if err := r.install(o.RightsActions, nil); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	claude := rwire.Subject{Account: "person", Program: filepath.Join(dir, "agent-integrations", "oa-claude-integration.exe")}
	opencode := rwire.Subject{Account: "person", Program: filepath.Join(dir, "agent-integrations", "oa-opencode-integration.exe")}
	const sender = "app:oa-codex-integration"
	snapshot, err := policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	permit := true
	edit, err := policy.EditRule(snapshot.Revision, claude, grants.ApplicationMessageFullToolsAction, sender, &permit, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatal("the Panel's write was refused", edit, err)
	}
	ctx := context.Background()
	if got := r.decide(ctx, claude, grants.ApplicationMessageFullToolsAction, sender).Outcome; got != rwire.DecisionOutcomePermitted {
		t.Fatal("the receiver's own decision", got)
	}
	if got := r.decide(ctx, opencode, grants.ApplicationMessageFullToolsAction, sender).Outcome; got == rwire.DecisionOutcomePermitted {
		t.Fatal("another agent's image got the receiver's rule")
	}
	if got := r.decide(ctx, claude, grants.ApplicationMessageFullToolsAction, "app:oa-opencode-integration").Outcome; got == rwire.DecisionOutcomePermitted {
		t.Fatal("the rule covered another sender")
	}
}
