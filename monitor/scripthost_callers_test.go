package main

import (
	"context"
	"slices"
	"testing"

	"github.com/openabstractions/abstraction-asks/go/scripthostcases"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// asks ASK-A8, ASK-R7: the shared table runs through the Panel's callers.
// Choosing an assistant refuses a program that runs foreign code and the
// chooser never offers one; a script host's prompt is orange, shows no icon
// read from its file and adds the script-host sentence, and a program that
// runs foreign code without being a script host (stock OpenCode) gets its
// publisher's level, its own icon and no sentence, all three from one rule.
func TestScriptHostRuleForEveryPanelCaller(t *testing.T) {
	own(t)
	assistantQuietFixture(t, func(string) rwire.DecisionOutcome { return rwire.DecisionOutcomeNotGranted })
	p := &servicePanel{}
	ctx := context.Background()
	cases := scripthostcases.Cases(t)
	var rules []rightsclient.PolicyRule
	for _, c := range scripthostcases.WithoutEvidence(cases) {
		if err := p.refuseAssistant(ctx, c.Program); (err != nil) != c.Foreign {
			t.Errorf("choosing %s as assistant: %v, want refused=%t", c.Name, err, c.Foreign)
		}
		rules = append(rules, rightsclient.PolicyRule{Subject: rightsclient.Subject{Account: "account", Program: c.Program}, Action: "abstraction.model/lookup", Resource: "hf", Permit: true})
	}
	offered := p.assistantCandidates(ctx, rules)
	for _, c := range scripthostcases.WithoutEvidence(cases) {
		if slices.Contains(offered, c.Program) == c.Foreign {
			t.Errorf("assistant chooser offers %s: %t, want %t", c.Name, c.Foreign, !c.Foreign)
		}
	}
	for _, c := range cases {
		if !c.Evidence.Trusted {
			continue
		}
		publisher := trayPublisher{Subject: c.Evidence.Subject, Trusted: true, Description: c.Evidence.Description, OriginalName: c.Evidence.OriginalName}
		if got := permissionIconProgram("", c.Program, publisher); (got == "") != c.Host {
			t.Errorf("%s: the prompt's icon program is %q, want none=%t", c.Name, got, c.Host)
		}
		wantLevel := promptLevelBlue
		if c.Host {
			wantLevel = promptLevelOrange
		}
		if got := promptLevelFor(promptEvidence{Program: c.Program, Publisher: publisher}); got != wantLevel {
			t.Errorf("%s: prompt level %v, want %v", c.Name, got, wantLevel)
		}
		if got := scriptHostSentence(publisher, c.Program); (got != "") != c.Host {
			t.Errorf("%s: script-host sentence %q, want one=%t", c.Name, got, c.Host)
		}
	}
}
