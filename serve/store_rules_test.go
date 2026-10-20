package main

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-model/go/releasefiles"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// storeRulesFixture is cardRulesFixture whose rights also know
// inventory.provide, the action a store's acceptance rule names.
func storeRulesFixture(t *testing.T) (*runtimeRights, *runtimeProviders, *inferenceOperator) {
	t.Helper()
	r, p, o := cardRulesFixture(t)
	if err := r.install([]string{ActionProviderManage, ActionInventoryProvide}, nil); err != nil {
		t.Fatal(err)
	}
	return r, p, o
}

// installedLocalStores is the installation's local-stores declaration with
// its bare program resolved to a file under the test, as the runtime resolves
// it against the tools directory, added the way the runtime reads it: as a
// file, never through Declare.
func installedLocalStores(t *testing.T, r *runtimeRights, p *runtimeProviders) (string, []string) {
	t.Helper()
	raw, err := os.ReadFile(privateInput(t, "..", "installer", "declarations", "local-stores.json"))
	if err != nil {
		t.Fatal(err)
	}
	f := installedProviderFixture(t, raw)
	stores := f.Declaration.resources("store")
	if len(stores) == 0 {
		t.Fatalf("the installed local-stores declaration names no store: %+v", f.Declaration.Resources)
	}
	program := filepath.Join(t.TempDir(), "inventoryd.exe")
	f.Declaration.Program = program
	f.DeclaredBy, f.DeclaredAt = declaredByInstallation, time.Now().UnixMilli()
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	if change := p.add(revision, f); change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("add %+v", change)
	}
	return filepath.Clean(program), stores
}

// The installation's local-stores declaration never passes through Declare,
// which was the only writer of a store's inventory.provide rule; the runtime
// accepted none of its stores and the composed storage inventory, the
// router's held models, the Panel's Models page and Setup's lookup of a copy
// already on the machine were all empty on every installed machine
// (preview-1031l). The runtime's sweep of declarations writes the rule for
// each declared store, and the supervisor then accepts them.
func TestInstalledLocalStoresDeclarationGetsItsStoreRules(t *testing.T) {
	r, p, o := storeRulesFixture(t)
	program, stores := installedLocalStores(t, r, p)
	subject := rwire.Subject{Account: r.owner, Program: program}
	for _, store := range stores {
		if read := r.policy.ReadRule(subject, ActionInventoryProvide, ResourceStore(store)); read.Outcome != rwire.RuleReadOutcomeUnknown {
			t.Fatalf("a rule on store:%s before the sweep: %+v", store, read)
		}
	}
	p.decide = func(ctx context.Context, program, action, resource string) bool {
		return r.decide(ctx, rwire.Subject{Account: r.owner, Program: program}, action, resource).Outcome == rwire.DecisionOutcomePermitted
	}
	supervisor := p.sorted()[0]
	if accepted := supervisor.acceptStores(context.Background(), stores); len(accepted) != 0 {
		t.Fatalf("stores accepted before the sweep: %q", accepted)
	}

	writeStoreRules(o, p)()

	want := make([]string, 0, len(stores))
	for _, store := range stores {
		read := r.policy.ReadRule(subject, ActionInventoryProvide, ResourceStore(store))
		if read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit || read.Record.Why != providerAddWhy {
			t.Fatalf("the sweep wrote no inventory.provide on store:%s: %+v", store, read)
		}
		want = append(want, ResourceStore(store))
	}
	slices.Sort(want)
	if accepted := supervisor.acceptStores(context.Background(), stores); !slices.Equal(accepted, want) {
		t.Fatalf("accepted %q after the sweep, want %q", accepted, want)
	}
}

// A person's deny on one store stays as it is: the sweep writes only where no
// rule exists, and that store stays unaccepted.
func TestStoreRulesSweepKeepsAPersonsDeny(t *testing.T) {
	r, p, o := storeRulesFixture(t)
	program, stores := installedLocalStores(t, r, p)
	subject := rwire.Subject{Account: r.owner, Program: program}
	denied := stores[0]
	existing := r.policy.ReadRule(subject, ActionInventoryProvide, ResourceStore(denied))
	deny := false
	edit, err := r.policy.ChangeRule(existing.Revision, subject, ActionInventoryProvide, ResourceStore(denied),
		rights.RuleEdit{Permit: &deny, By: rwire.Subject{Account: r.owner, Program: r.operators[1]}, Why: "person"}, func() error { return nil })
	if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("deny %+v %v", edit, err)
	}

	writeStoreRules(o, p)()

	if read := r.policy.ReadRule(subject, ActionInventoryProvide, ResourceStore(denied)); read.Outcome != rwire.RuleReadOutcomeFound || read.Record.Rule.Permit {
		t.Fatalf("the sweep replaced the person's deny on store:%s: %+v", denied, read)
	}
	for _, store := range stores[1:] {
		if read := r.policy.ReadRule(subject, ActionInventoryProvide, ResourceStore(store)); read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit {
			t.Fatalf("no permit on store:%s: %+v", store, read)
		}
	}
}

// catalogRights is a runtime's rights after installation with the composed
// storage inventory, and the Panel, the operator program Setup runs in.
// before runs between registering the actions and writing the installation
// rules, as a person's earlier edit would.
func catalogRights(t *testing.T, before func(r *runtimeRights, panel rwire.Subject)) (*runtimeRights, rwire.Subject) {
	t.Helper()
	r := testRights(t, t.TempDir())
	actions := []string{contentReadAction, ActionInventoryRead}
	if err := r.install(actions, nil); err != nil {
		t.Fatal(err)
	}
	panel := rwire.Subject{Account: r.owner, Program: r.operators[1]}
	if before != nil {
		before(r, panel)
	}
	permit := storageservice.Policy(func(context.Context, *identity.Peer, string) error { return nil })
	if err := r.install(actions, installationRules(host.Options{StorageInventoryPolicy: permit}, nil)); err != nil {
		t.Fatal(err)
	}
	return r, panel
}

func catalogDigests() []string {
	var out []string
	for _, d := range releasefiles.ModelDigests() {
		out = append(out, "sha256:"+d)
	}
	return out
}

// The installed layout, starting with no rule, ends with the Panel holding
// content.read on exactly the model digests Setup's reviewed catalog pins:
// the composed inventory shows Setup a hashed copy of each, and nothing else
// a store holds.
func TestInstallationLetsOperatorsReadExactlyTheCatalogDigests(t *testing.T) {
	r, panel := catalogRights(t, nil)
	want := catalogDigests()
	if len(want) == 0 {
		t.Fatalf("catalog digests %q", want)
	}
	snapshot, err := r.policy.OperatorSnapshot()
	if err != nil {
		t.Fatal(err)
	}
	var written []string
	for _, rule := range snapshot.Rules {
		if rule.Subject == panel && rule.Action == contentReadAction {
			if !rule.Permit {
				t.Fatalf("a deny on %s", rule.Resource)
			}
			written = append(written, rule.Resource)
		}
	}
	slices.Sort(written)
	if !slices.Equal(written, want) {
		t.Fatalf("the Panel reads %q, want exactly %q", written, want)
	}
	for _, digest := range want {
		if d := r.decide(context.Background(), panel, contentReadAction, digest); d.Outcome != rwire.DecisionOutcomePermitted {
			t.Fatalf("content.read on %s: %s", digest, d.Outcome)
		}
	}
}

// A digest the catalog does not pin stays unreadable to the Panel.
func TestADigestOutsideTheCatalogStaysNotGranted(t *testing.T) {
	r, panel := catalogRights(t, nil)
	other := "sha256:" + strings.Repeat("0", 64)
	if slices.Contains(catalogDigests(), other) {
		t.Fatal("the probe digest is in the catalog")
	}
	if d := r.decide(context.Background(), panel, contentReadAction, other); d.Outcome != rwire.DecisionOutcomeNotGranted {
		t.Fatalf("content.read on a digest outside the catalog: %s", d.Outcome)
	}
}

// A person's deny on a catalog digest survives installation.
func TestInstallationKeepsAPersonsDenyOnACatalogDigest(t *testing.T) {
	denied := catalogDigests()[0]
	r, panel := catalogRights(t, func(r *runtimeRights, panel rwire.Subject) {
		existing := r.policy.ReadRule(panel, contentReadAction, denied)
		deny := false
		edit, err := r.policy.ChangeRule(existing.Revision, panel, contentReadAction, denied,
			rights.RuleEdit{Permit: &deny, By: panel, Why: "person"}, func() error { return nil })
		if err != nil || edit.Outcome != rwire.PolicyEditOutcomeApplied {
			t.Fatalf("deny %+v %v", edit, err)
		}
	})
	if read := r.policy.ReadRule(panel, contentReadAction, denied); read.Outcome != rwire.RuleReadOutcomeFound || read.Record.Rule.Permit {
		t.Fatalf("installation replaced the person's deny: %+v", read)
	}
	if d := r.decide(context.Background(), panel, contentReadAction, denied); d.Outcome == rwire.DecisionOutcomePermitted {
		t.Fatalf("a denied catalog digest is readable")
	}
}

// Operators hold abstraction.storage/inventory.read on the composed inventory
// by installation, as they hold the router's inventory.read: Setup in the
// Panel finds a model this machine already holds through it. Without the
// composed inventory there is no such rule.
func TestOperatorsReadTheStorageInventoryByInstallation(t *testing.T) {
	read := installationRule{ActionInventoryRead, storageservice.InventoryResource}
	permit := storageservice.Policy(func(context.Context, *identity.Peer, string) error { return nil })
	with := installationRules(host.Options{StorageInventoryPolicy: permit}, nil)
	if !slices.Contains(with, read) {
		t.Fatalf("installation rules with the composed inventory lack %+v: %+v", read, with)
	}
	if without := installationRules(host.Options{}, nil); slices.Contains(without, read) {
		t.Fatalf("installation rules without the composed inventory carry %+v", read)
	}
}
