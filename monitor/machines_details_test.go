package main

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	"github.com/openabstractions/abstraction-facade/go/probe"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

type machinesPolicyFixture struct {
	pages []wire.PolicyPage
	read  map[string]wire.RuleRead
	reads []string
	index int
}

func TestMachinesDetailsNativeCurrentPolicyAndExpiry(t *testing.T) {
	own(t)
	definitions, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{})
	if err != nil {
		t.Fatal(err)
	}
	var allow, outage atomic.Bool
	allow.Store(true)
	panelRightsRuntimeWithDefinitions(t, &allow, &outage, definitions)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	second, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	page, err := second.ListPolicyContext(ctx, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	key := strings.Repeat("a", 64)
	account := probe.Self().Account
	rule := wire.PolicyRule{Subject: wire.Subject{Account: account, Program: "remote:" + key}, Action: panelRightsAction, Resource: "sha256:" + strings.Repeat("c", 64), Permit: true}
	edit, err := second.SetRuleContext(ctx, page.Revision, rule)
	if err != nil || edit.Outcome != wire.PolicyEditOutcomeApplied {
		t.Fatal(edit, err)
	}
	read := func() machinePermissions {
		return joinMachinePermissionSelectors(ctx, readMachinePermissions(ctx, second, account, key), account, key)
	}
	if got := read(); got.Outcome != "page" || !got.Complete || len(got.Rules) != 1 || !got.Rules[0].Permit {
		t.Fatalf("actual canonical remote policy: %+v", got)
	}
	expiring, err := second.SetRuleForContext(ctx, edit.Revision, rule, 250*time.Millisecond, "private detail expiry")
	if err != nil || expiring.Outcome != wire.PolicyEditOutcomeApplied {
		t.Fatal(expiring, err)
	}
	initial, err := second.ObservePolicyContext(ctx, "", 0)
	if err != nil {
		t.Fatal(err)
	}
	notice, err := followRightsRevision(ctx, second, initial.Next, time.Second)
	if err != nil || notice.Outcome != "changed" || notice.Revision != expiring.Revision {
		t.Fatal("same-revision expiry", notice, err)
	}
	if got := read(); len(got.Rules) != 1 || !got.Rules[0].Expired {
		t.Fatalf("expired current rule: %+v", got)
	}
	removed, err := second.RevokeRuleContext(ctx, expiring.Revision, rule.Subject, rule.Action, rule.Resource)
	if err != nil || removed.Outcome != wire.PolicyEditOutcomeApplied {
		t.Fatal(removed, err)
	}
	if got := read(); got.Outcome != "page" || len(got.Rules) != 0 {
		t.Fatalf("removed current rule: %+v", got)
	}
	allow.Store(false)
	if got := read(); got.Outcome != "unavailable" || len(got.Rules) != 0 {
		t.Fatalf("authority loss kept stale access: %+v", got)
	}
}

func (f *machinesPolicyFixture) ListPolicyContext(context.Context, string, int64) (wire.PolicyPage, error) {
	if f.index >= len(f.pages) {
		return wire.PolicyPage{}, errors.New("owned page unavailable")
	}
	p := f.pages[f.index]
	f.index++
	return p, nil
}
func (f *machinesPolicyFixture) ReadRuleContext(_ context.Context, s wire.Subject, action, resource string) (wire.RuleRead, error) {
	f.reads = append(f.reads, s.Program)
	r, ok := f.read[s.Program+action+resource]
	if !ok {
		return r, errors.New("owned rule unavailable")
	}
	return r, nil
}

func TestMachinesDetailsCurrentExactPolicy(t *testing.T) {
	key := strings.Repeat("a", 64)
	subject := wire.Subject{Account: "owned", Program: "remote:" + key}
	permit := wire.PolicyRule{Subject: subject, Action: "abstraction.inference/complete", Resource: "host:owned", Permit: true}
	block := wire.PolicyRule{Subject: subject, Action: "abstraction.facade/remote.serve", Resource: "account", Permit: false}
	foreign := permit
	foreign.Subject.Program = "remote:" + strings.Repeat("b", 64)
	fixture := &machinesPolicyFixture{pages: []wire.PolicyPage{{Outcome: wire.PolicyPageOutcomePage, Revision: "current", Rules: []wire.PolicyRule{foreign, permit, block}, Complete: true}}, read: map[string]wire.RuleRead{}}
	for _, rule := range []wire.PolicyRule{permit, block} {
		fixture.read[rule.Subject.Program+rule.Action+rule.Resource] = wire.RuleRead{Outcome: wire.RuleReadOutcomeFound, Revision: "current", Record: &wire.RuleRecord{Rule: rule, SetBy: wire.Subject{Account: "owned", Program: "recorded-operator"}, Why: "actual recorded reason"}}
	}
	view := readMachinePermissions(context.Background(), fixture, "owned", key)
	if view.Outcome != "page" || !view.Complete || len(view.Rules) != 2 || len(fixture.reads) != 2 || view.Rules[1].Permit || view.Rules[0].Why != "actual recorded reason" {
		t.Fatalf("current exact local rules: %+v reads=%v", view, fixture.reads)
	}
	fixture.index = 0
	fixture.read[subject.Program+permit.Action+permit.Resource] = wire.RuleRead{Outcome: wire.RuleReadOutcomeExpired, Revision: "current", Record: &wire.RuleRecord{Rule: permit}}
	if got := readMachinePermissions(context.Background(), fixture, "owned", key); !got.Rules[0].Expired {
		t.Fatal("expired permit presented as active", got)
	}
}

func TestMachinesDetailsPartialOrChangingPolicyNeverClaimsAbsence(t *testing.T) {
	key := strings.Repeat("a", 64)
	fixture := &machinesPolicyFixture{pages: []wire.PolicyPage{{Outcome: wire.PolicyPageOutcomePage, Revision: "one", Next: "next"}, {Outcome: wire.PolicyPageOutcomePage, Revision: "two", Complete: true}}}
	if got := readMachinePermissions(context.Background(), fixture, "owned", key); got.Outcome != "unavailable" || got.Complete {
		t.Fatal("changing policy projected current absence", got)
	}
	fixture = &machinesPolicyFixture{}
	for i := 0; i < 16; i++ {
		fixture.pages = append(fixture.pages, wire.PolicyPage{Outcome: wire.PolicyPageOutcomePage, Revision: "same", Next: strings.Repeat("x", i+1)})
	}
	if got := readMachinePermissions(context.Background(), fixture, "owned", key); got.Outcome != "partial" || got.Complete {
		t.Fatal("bounded incomplete policy projected absence", got)
	}
	fixture = &machinesPolicyFixture{}
	if got := readMachinePermissions(context.Background(), fixture, "owned", strings.ToUpper(key)); got.Outcome != "unavailable" || fixture.index != 0 {
		t.Fatal("noncanonical peer subject used", got)
	}
}
