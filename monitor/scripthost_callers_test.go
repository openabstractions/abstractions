package main

import (
	"context"
	"slices"
	"testing"

	"github.com/openabstractions/abstraction-asks/go/scripthostcases"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// asks ASK-R7: the one script-host rule's shared table runs through the
// Panel's callers. Choosing an assistant refuses a host, the chooser never
// offers one, and a host's prompt shows no icon read from its file.
func TestScriptHostRuleForEveryPanelCaller(t *testing.T) {
	own(t)
	p := &servicePanel{}
	ctx := context.Background()
	cases := scripthostcases.Cases(t)
	var rules []rightsclient.PolicyRule
	for _, c := range scripthostcases.WithoutEvidence(cases) {
		if err := p.refuseAssistant(ctx, c.Program); (err != nil) != c.Host {
			t.Errorf("choosing %s as assistant: %v, want refused=%t", c.Name, err, c.Host)
		}
		rules = append(rules, rightsclient.PolicyRule{Subject: rightsclient.Subject{Account: "account", Program: c.Program}, Action: "abstraction.model/lookup", Resource: "hf", Permit: true})
	}
	offered := p.assistantCandidates(ctx, rules)
	for _, c := range scripthostcases.WithoutEvidence(cases) {
		if slices.Contains(offered, c.Program) == c.Host {
			t.Errorf("assistant chooser offers %s: %t, want %t", c.Name, c.Host, !c.Host)
		}
	}
	for _, c := range cases {
		if c.Evidence.Subject == "" || !c.Host {
			continue
		}
		publisher := trayPublisher{Subject: c.Evidence.Subject, Trusted: c.Evidence.Trusted, OriginalName: c.Evidence.OriginalName}
		if got := permissionIconProgram("", c.Program, publisher); got != "" {
			t.Errorf("%s: the prompt reads an icon from %q", c.Name, got)
		}
	}
}
