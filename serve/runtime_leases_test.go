package main

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// Installation writes the rules leases need: holding the card and the wake,
// and yielding each engine this release has a mechanism for. A host with no
// rule is never unloaded, so the rule written once is what lets the service
// arbitrate at all.
func TestInstallationWritesTheLeaseRules(t *testing.T) {
	rules := resourceRules()
	want := map[string]string{
		resourceservice.ActionHold + " " + instrument.Card0:              "the card",
		resourceservice.ActionHold + " " + resourceservice.ResourceAwake: "the wake",
		resourceservice.ActionYield + " host:lmstudio":                   "LM Studio",
		resourceservice.ActionYield + " host:ollama":                     "Ollama",
		resourceservice.ActionYield + " host:comfyui":                    "ComfyUI",
		resourceservice.ActionYield + " host:" + modelHostName:           "the model host",
	}
	for _, rule := range rules {
		delete(want, rule.Action+" "+rule.Resource)
	}
	if len(want) != 0 {
		t.Fatalf("installation writes no rule for %v", want)
	}
	if len(rules) != 6 {
		t.Fatalf("installation writes %d rules: %+v", len(rules), rules)
	}
}

// The name a yield rule is written on is the name the table's rows carry, so
// a person reading who holds the card and a person writing the rule are
// naming the same thing.
func TestAYieldRuleNamesTheRowThePersonSees(t *testing.T) {
	claims := hostClaims(func() []router.HostState {
		return []router.HostState{{Host: "lmstudio", Up: true, Resident: []string{"gemma"}}}
	})()
	if len(claims) != 1 {
		t.Fatalf("claims %+v", claims)
	}
	holder := attachedHost{name: "lmstudio"}.Holder()
	if holder != claims[0].Program {
		t.Fatalf("the rule names %q and the table's row says %q", holder, claims[0].Program)
	}
	var named bool
	for _, rule := range resourceRules() {
		if rule.Action == resourceservice.ActionYield && rule.Resource == holder {
			named = true
		}
	}
	if !named {
		t.Fatalf("no installation rule names %s", holder)
	}
}

// Each attached holder is one of the machine's own engines, holding the card,
// and it reports nothing when the router has no such host.
func TestAttachedHostsAreTheEnginesWithAMechanism(t *testing.T) {
	r := router.New()
	attached := attachedHosts(r)
	if len(attached) != len(unloadableHosts) {
		t.Fatalf("%d attached holders for %v", len(attached), unloadableHosts)
	}
	for _, holder := range attached {
		if holder.Resource() != instrument.Card0 {
			t.Fatalf("%s holds %s", holder.Holder(), holder.Resource())
		}
		if !strings.HasPrefix(holder.Holder(), claimedHostPrefix) {
			t.Fatalf("holder %q does not name a host", holder.Holder())
		}
		if _, err := holder.Holding(context.Background()); err == nil {
			t.Fatalf("%s claims to hold something on a router with no hosts", holder.Holder())
		}
	}
}

// Without a state directory there is no lease book: a lease that does not
// survive a restart is not a lease, and the table is served read-only.
func TestARuntimeWithoutDurableStateServesTheTableAlone(t *testing.T) {
	composed := composeResources("", router.New(), nil, func(error) {})
	if composed.table == nil {
		t.Fatal("no table")
	}
	if composed.book != nil {
		t.Fatal("a runtime with nowhere to keep leases opened a lease book")
	}
}

// With one, the book and its record live beside the table, and a runtime with
// no rights policy grants nothing: an absent policy is never permission.
func TestTheLeaseBookLivesInTheRuntimeState(t *testing.T) {
	state := t.TempDir()
	var reported []error
	composed := composeResources(state, router.New(), nil, func(err error) { reported = append(reported, err) })
	t.Cleanup(composed.close)
	if len(reported) != 0 {
		t.Fatalf("composing reported %v", reported)
	}
	if composed.book == nil {
		t.Fatal("no lease book")
	}
	for _, name := range []string{leaseBookFile, leaseAuditFile} {
		if got := filepath.Join(state, "resources", name); !strings.HasPrefix(got, state) {
			t.Fatalf("%s is outside the state directory", got)
		}
	}
	out := composed.book.Acquire(context.Background(), resourceservice.Subject{Program: `C:\x.exe`}, nil,
		instrument.Card0, 1<<30, 0)
	if out.Outcome.String() != "unavailable" {
		t.Fatalf("a runtime with no rights policy granted %s", out.Outcome)
	}
	if _, err := resourceservice.ReadAudit(filepath.Join(state, "resources", leaseAuditFile), 8); err != nil {
		t.Fatalf("the record could not be read: %v", err)
	}
}
