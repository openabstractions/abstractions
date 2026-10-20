package main

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// factsRulesWhy is factsRules whose records carry why.
type factsRulesWhy struct {
	factsRules
	why string
}

func (f factsRulesWhy) ReadRuleContext(ctx context.Context, s rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error) {
	read, err := f.factsRules.ReadRuleContext(ctx, s, action, resource)
	if read.Record != nil {
		read.Record.Why = f.why
	}
	return read, err
}

// Architect R35: a kept OA integration's app page says it shares the
// graphics card. The facts read that from the app's card hold the runtime
// wrote on its Keep ("integration keep"); a card hold with another
// provenance, or a deny, says nothing.
func TestAppFactsSayAKeptIntegrationSharesTheCard(t *testing.T) {
	for _, c := range []struct {
		why    string
		permit bool
		want   bool
	}{{integrationHoldWhy, true, true}, {"set by the person", true, false}, {integrationHoldWhy, false, false}} {
		apps, p := appPagePanel(t, false)
		mine := rightsclient.Subject{Account: probe.Self().Account, Program: apps.entries[0].Descriptor.Program}
		rules := []rightsclient.PolicyRule{{Subject: mine, Action: resourceservice.ActionHold, Resource: instrument.Card0, Permit: c.permit}}
		saved := assistantRuleOperator
		assistantRuleOperator = func() assistantRules { return factsRulesWhy{factsRules: factsRules{rules: rules}, why: c.why} }
		rec := appPageGet(t, p, "/app/example/facts?k="+appPageKey)
		assistantRuleOperator = saved
		var facts appFacts
		if err := json.Unmarshal(rec.Body.Bytes(), &facts); err != nil {
			t.Fatal(err)
		}
		if facts.RulesOutcome != "page" || facts.SharesCard != c.want {
			t.Errorf("why %q permit %v: sharesCard %v, rules %s", c.why, c.permit, facts.SharesCard, facts.RulesOutcome)
		}
	}
}
