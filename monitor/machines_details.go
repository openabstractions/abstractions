package main

import (
	"context"
	"encoding/hex"

	facade "github.com/openabstractions/abstraction-facade/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type machinePolicyReader interface {
	ListPolicyContext(context.Context, string, int64) (rwire.PolicyPage, error)
	ReadRuleContext(context.Context, rwire.Subject, string, string) (rwire.RuleRead, error)
}

// Reuse the Rights page's definition/selector projection at the same revision.
// Legacy-only or unavailable selector data cannot establish complete access.
func joinMachinePermissionSelectors(ctx context.Context, current machinePermissions, account, key string) machinePermissions {
	if current.Outcome != "page" || !current.Complete {
		return current
	}
	unavailable := func() machinePermissions {
		return machinePermissions{Outcome: "unavailable", Rules: []policyRuleView{}, ActionPlain: rightsActionPlain}
	}
	reader, err := panelMachine().ResolveRightsSelector(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if selectorUnsupported(err) {
		current.Outcome = "partial"
		current.Complete = false
		return current
	}
	if err != nil {
		return unavailable()
	}
	operator, err := panelMachine().ResolveRightsSelectorOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return unavailable()
	}
	joined := joinPolicySelectors(ctx, policyPageView{Outcome: "page", Revision: current.Revision, Rules: current.Rules, Complete: true}, reader, operator)
	if joined.Outcome != "page" {
		return unavailable()
	}
	current.Rules = []policyRuleView{}
	for _, rule := range joined.Rules {
		if rule.Subject.Account == account && rule.Subject.Program == "remote:"+key {
			if len(current.Rules) == 64 {
				current.Outcome = "partial"
				current.Complete = false
				break
			}
			current.Rules = append(current.Rules, rule)
		}
	}
	if !joined.SelectorAvailable {
		current.Outcome = "partial"
		current.Complete = false
	}
	return current
}

type machinePermissions struct {
	Outcome     string
	Revision    string
	Complete    bool
	Rules       []policyRuleView
	ActionPlain map[string]string
}

func machinePeerKey(key string) bool {
	decoded, err := hex.DecodeString(key)
	return len(key) == 64 && err == nil && hex.EncodeToString(decoded) == key
}

// Read only current local policy for the exact canonical remote subject.
// Consent/preparation data never supplies current permissions. Partial or
// changing snapshots never establish absence, and reads confer no authority.
func readMachinePermissions(ctx context.Context, reader machinePolicyReader, account, key string) machinePermissions {
	refuse := func() machinePermissions {
		return machinePermissions{Outcome: "unavailable", Rules: []policyRuleView{}, ActionPlain: rightsActionPlain}
	}
	if account == "" || !machinePeerKey(key) {
		return refuse()
	}
	view := machinePermissions{Outcome: "partial", Rules: []policyRuleView{}, ActionPlain: rightsActionPlain}
	cursor := ""
	exact := "remote:" + key
	for range 16 {
		page, err := reader.ListPolicyContext(ctx, cursor, 64)
		if err != nil || page.Outcome != rwire.PolicyPageOutcomePage || page.Revision == "" {
			return refuse()
		}
		if view.Revision == "" {
			view.Revision = page.Revision
		} else if view.Revision != page.Revision {
			return refuse()
		}
		for _, rule := range page.Rules {
			if rule.Subject.Account != account || rule.Subject.Program != exact {
				continue
			}
			if len(view.Rules) == 64 {
				return view
			}
			read, err := reader.ReadRuleContext(ctx, rule.Subject, rule.Action, rule.Resource)
			if err != nil || read.Record == nil || read.Revision != view.Revision || (read.Outcome != rwire.RuleReadOutcomeFound && read.Outcome != rwire.RuleReadOutcomeExpired) {
				return refuse()
			}
			r := read.Record
			if r.Rule.Subject != rule.Subject || r.Rule.Action != rule.Action || r.Rule.Resource != rule.Resource {
				return refuse()
			}
			setter := r.SetBy
			view.Rules = append(view.Rules, policyRuleView{Subject: r.Rule.Subject, Action: r.Rule.Action, Resource: r.Rule.Resource, Permit: r.Rule.Permit, SetBy: &setter, SetAt: r.SetAt, Why: r.Why, Expires: r.Expires, Expired: read.Outcome == rwire.RuleReadOutcomeExpired})
		}
		if page.Complete {
			view.Complete = true
			view.Outcome = "page"
			return view
		}
		if page.Next == "" || page.Next == cursor {
			return refuse()
		}
		cursor = page.Next
	}
	return view
}
