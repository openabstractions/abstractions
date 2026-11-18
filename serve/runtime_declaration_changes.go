package main

import (
	"context"
	"errors"
	"net/url"
	"slices"
	"strings"
	"unicode/utf8"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

// Rights written by provider declarations and inference host adds (architect
// ruling (3) of 2026-11-22, rights RIGHTS-O17). A declaration or host add
// through any program other than the Panel window becomes one typed pending
// request carrying the declared name, the program and the exact rule set it
// writes. Allow applies the declaration and its rules. An elevated caller is
// exempt as under design A, and so is a declaration whose name and program
// pair has product standing: OA giving its own components their standard
// rights, which is how Setup and the installer keep working.

// declaredRule is one permit a declaration or host add writes for program, set
// by by with why. An existing rule on the target, a deny included, stays.
type declaredRule struct {
	by                             rwire.Subject
	program, action, resource, why string
}

// hostRuleList is the rule set a host writes: complete on the host for the
// runtime's operator programs and the caller, and, for a hosted host with a
// credential, the runtime's own credential apply rule its listing reads need.
// A remote runtime applies its own credential.
func (o *inferenceOperator) hostRuleList(caller inference.Subject, entry iwire.HostEntry) []declaredRule {
	r := o.credentials.runtimeRights
	by := rwire.Subject{Account: caller.Account, Program: caller.Program}
	programs := slices.Clone(r.operators)
	if !slices.ContainsFunc(programs, func(p string) bool { return samePrograms(p, caller.Program) }) {
		programs = append(programs, caller.Program)
	}
	var rules []declaredRule
	for _, program := range programs {
		rules = append(rules, declaredRule{by, program, inference.ActionComplete, inference.ResourceHost(entry.Name), hostAddWhy})
	}
	if entry.Hosted && entry.Credential != "" && entry.Kind != router.WireRemote {
		rules = append(rules, declaredRule{by, o.credentials.operators[0], credentials.ActionApply, credentials.ResourceFor(entry.Credential), hostAddWhy})
	}
	return rules
}

// providerRuleList is the rule set a provider declaration writes: each
// capability reads its own acceptance rule. The person accepts each named
// store from this program (inventory.provide on store:<name>, content.thrift
// InventorySource); the resource table's card is held by the program that
// declared it, with hold on awake, since a card holder runs GPU work and holds
// the wake lease while it does (resource RES-G5, RES-A1); and a host or a
// remote runtime's hosts serve the operator programs (hostRuleList).
func (o *inferenceOperator) providerRuleList(caller inference.Subject, d providerDeclaration) []declaredRule {
	by := rwire.Subject{Account: caller.Account, Program: caller.Program}
	var rules []declaredRule
	for _, store := range d.resources("store") {
		rules = append(rules, declaredRule{by, d.Program, ActionInventoryProvide, ResourceStore(store), providerAddWhy})
	}
	cards := d.resources("card")
	for _, card := range cards {
		rules = append(rules, declaredRule{by, d.Program, resourceservice.ActionHold, ResourceCard(card), providerAddWhy})
	}
	if len(cards) > 0 {
		rules = append(rules, declaredRule{by, d.Program, resourceservice.ActionHold, resourceservice.ResourceAwake, providerAddWhy})
	}
	if d.host() {
		rules = append(rules, o.hostRuleList(caller, (providerFile{Declaration: d}).hostEntry())...)
	} else if d.remote() {
		rules = append(rules, o.hostRuleList(caller, iwire.HostEntry{Name: d.Name, Hosted: true, Kind: router.WireRemote})...)
	}
	return rules
}

// writeDeclaredRules writes each rule a target does not hold yet.
func (o *inferenceOperator) writeDeclaredRules(rules []declaredRule) error {
	var errs []error
	for _, rule := range rules {
		errs = append(errs, o.permitRuleWhy(rule.by, rule.program, rule.action, rule.resource, rule.why))
	}
	return errors.Join(errs...)
}

// policyRules is rules as the typed request carries them.
func (o *inferenceOperator) policyRules(rules []declaredRule) []rwire.PolicyRule {
	out := make([]rwire.PolicyRule, 0, len(rules))
	for _, rule := range rules {
		out = append(out, rwire.PolicyRule{Subject: rwire.Subject{Account: o.credentials.runtimeRights.owner, Program: rule.program}, Action: rule.action, Resource: rule.resource, Permit: true})
	}
	return out
}

// declarationRoute decides how a declaration or host add by the bound peer
// applies: pending when the person must confirm it.
func (c *rightsChanges) declarationRoute(ctx context.Context, peer *identity.Peer, by rwire.Subject, standing bool) (route changeRoute, pending bool) {
	if c == nil || standing || c.direct(ctx, peer) {
		return routeDirect, false
	}
	if c.exempt(ctx, peer, by) {
		return routeExempt, false
	}
	return routeAllowed, true
}

// declarationUndo is the Undo of a declaration's rules: revoke each rule it
// is about to write that no target holds yet.
func (c *rightsChanges) declarationUndo(rules []rwire.PolicyRule) []rwire.PolicyChange {
	var undo []rwire.PolicyChange
	for _, rule := range rules {
		if c.r.policy.ReadRule(rule.Subject, rule.Action, rule.Resource).Outcome == rwire.RuleReadOutcomeUnknown {
			rule := rule
			rule.Permit = false
			undo = append(undo, rwire.PolicyChange{Kind: rwire.ChangeKindRevokeRule, Rule: &rule})
		}
	}
	return undo
}

// applyDeclaration runs apply, the declaration and its rule writes, and
// writes the Activity line of an exempt or allowed one.
func (c *rightsChanges) applyDeclaration(by rwire.Subject, change rwire.PolicyChange, route changeRoute, apply func() bool) bool {
	if c == nil || route == routeDirect {
		return apply()
	}
	undo := c.declarationUndo(change.Rules)
	if !apply() {
		return false
	}
	c.recordActivity(by, change, undo, route == routeExempt)
	return true
}

// requestDeclaration holds a declaration the person must confirm and asks
// them; apply runs on Allow. It returns the request's state.
func (c *rightsChanges) requestDeclaration(by rwire.Subject, change rwire.PolicyChange, apply func(context.Context) bool) rwire.ChangeRequestState {
	req, outcome := c.admitWith(by, change, func(ctx context.Context) (rwire.ChangeRequestOutcome, string) {
		if c.applyDeclaration(by, change, routeAllowed, func() bool { return apply(ctx) }) {
			return rwire.ChangeRequestOutcomeApplied, ""
		}
		return rwire.ChangeRequestOutcomeUnavailable, ""
	})
	if req == nil {
		return rwire.ChangeRequestState{Outcome: outcome}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return req.state()
}

// declarationWords completes "Let <requester> ...?" for a declaration: the
// kind and name, the program, and what its rules let it do.
func declarationWords(change rwire.PolicyChange) string {
	kind := "the provider"
	var phrases []string
	add := func(phrase string) {
		if !slices.Contains(phrases, phrase) {
			phrases = append(phrases, phrase)
		}
	}
	for _, rule := range change.Rules {
		switch {
		case rule.Action == inference.ActionComplete:
			kind = "the model server"
		case rule.Action == resourceservice.ActionHold && strings.HasPrefix(rule.Resource, "card:"):
			add("use the graphics card")
		case rule.Action == resourceservice.ActionHold && rule.Resource != resourceservice.ResourceAwake:
			add("hold " + rule.Resource)
		case rule.Action == ActionInventoryProvide:
			add("provide models")
		case rule.Action == credentials.ActionApply:
			add("use your saved key for it")
		}
	}
	words := "add " + kind + " '" + change.Name + "'"
	if change.Address != "" {
		words += " at " + addressWords(change.Address)
	}
	if change.Program != "" {
		words += " (" + programFit(change.Program, declarationProgramWords) + ")"
	}
	switch len(phrases) {
	case 0:
		return words
	case 1:
		return words + " with permission to " + phrases[0]
	}
	return words + " with permission to " + strings.Join(phrases[:len(phrases)-1], ", ") + " and " + phrases[len(phrases)-1]
}

// addressWords is a server address as a declaration's change sentence names
// it: its scheme and host always whole, and its path shortened so the whole
// fits 80 characters where the host leaves room.
func addressWords(address string) string {
	value := oneLine(address)
	if utf8.RuneCountInString(value) <= 80 {
		return value
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" {
		return string([]rune(value)[:79]) + "…"
	}
	origin := oneLine(parsed.Scheme + "://" + parsed.Host)
	rest := []rune(strings.TrimPrefix(value, origin))
	keep := max(80-utf8.RuneCountInString(origin)-1, 0)
	return origin + string(rest[:min(keep, len(rest))]) + "…"
}

// addressFit fits a server address to limit characters on one line, keeping
// its scheme and host whole and shortening only the path: a long subdomain
// never hides the domain. A host too long for the room keeps its end, where
// the domain is, after the scheme and an ellipsis.
func addressFit(address string, limit int) string {
	value := oneLine(address)
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	parsed, err := url.Parse(address)
	if err != nil || parsed.Host == "" {
		return string([]rune(value)[:limit-1]) + "…"
	}
	origin := oneLine(parsed.Scheme + "://" + parsed.Host)
	if n := utf8.RuneCountInString(origin); n+1 >= limit {
		scheme := oneLine(parsed.Scheme + "://")
		host := []rune(oneLine(parsed.Host))
		keep := limit - utf8.RuneCountInString(scheme) - 1
		return scheme + "…" + string(host[len(host)-keep:])
	}
	rest := []rune(strings.TrimPrefix(value, origin))
	keep := limit - utf8.RuneCountInString(origin) - 1
	return origin + string(rest[:min(keep, len(rest))]) + "…"
}

// oneLine is value on one line: control characters read as spaces.
func oneLine(value string) string {
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
}

// declarationProgramWords is the room a program path has in a declaration's
// change sentence, which shares one question slot with its other words.
const declarationProgramWords = 80

// pendingReason is the reason a declaration or host edit carries while its
// request waits for the person: the change request id, which `openabstractions
// rights requests --follow` follows.
func pendingReason(state rwire.ChangeRequestState) string {
	return "pending:" + state.ID
}
