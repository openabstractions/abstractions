package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// cardRulesFixture is a runtime's rights, provider declarations and operator,
// with no sweep watching the declarations: each test drives one writer alone.
func cardRulesFixture(t *testing.T) (*runtimeRights, *runtimeProviders, *inferenceOperator) {
	t.Helper()
	r := testRights(t, t.TempDir())
	if err := r.install([]string{ActionProviderManage, resourceservice.ActionHold},
		[]installationRule{{ActionProviderManage, "account"}}); err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"),
		supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}),
		report: func(err error) { t.Errorf("provider: %v", err) }}
	o := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: r}, providers: p,
		report: func(err error) { t.Errorf("operator: %v", err) }}
	return r, p, o
}

// cardDeclaration is a native provider that names card:0.
func cardDeclaration(program string) wire.Declaration {
	return wire.Declaration{Name: "card-holder", Program: program, Arguments: []string{}, Endpoint: "card-holder",
		Transport: wire.DeclarationTransportNative, Contracts: []string{inference.Contract}, Resources: []string{"card:0"},
		Activation: wire.ActivationAttach}
}

// requireCardRules reads hold on card:0 and on awake for program.
func requireCardRules(t *testing.T, r *runtimeRights, program, writer string) {
	t.Helper()
	for _, resource := range []string{"card:0", resourceservice.ResourceAwake} {
		read := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: program}, resourceservice.ActionHold, resource)
		if read.Outcome != rwire.RuleReadOutcomeFound || !read.Record.Rule.Permit || read.Record.Why != providerAddWhy {
			t.Fatalf("%s wrote no hold on %s for the card holder: %+v", writer, resource, read)
		}
	}
}

// FAC-R3, RES-A1: a card holder's rules, hold on the card and on awake, have
// one writer (inferenceOperator.permitCardRules). A provider's Declare and
// the runtime's sweep of declarations each write both through it; each is
// driven alone here, so either writing only the card fails.
func TestCardRulesHaveOneWriterForDeclareAndTheSweep(t *testing.T) {
	t.Run("declare", func(t *testing.T) {
		r, p, o := cardRulesFixture(t)
		registry := &runtimeRegistry{providers: p, operator: o}
		_, revision, err := p.read()
		if err != nil {
			t.Fatal(err)
		}
		program := filepath.Join(t.TempDir(), "card-holder")
		caller := inference.Subject{Account: r.owner, Program: r.operators[0]}
		if change := registry.Declare(context.Background(), caller, revision, cardDeclaration(program)); change.Outcome != wire.DeclarationEditOutcomeApplied {
			t.Fatalf("declare %+v", change)
		}
		requireCardRules(t, r, filepath.Clean(program), "Declare")
	})
	t.Run("sweep", func(t *testing.T) {
		r, p, o := cardRulesFixture(t)
		_, revision, err := p.read()
		if err != nil {
			t.Fatal(err)
		}
		// A declaration the runtime finds when it opens: written as a file,
		// never through Declare, so Declare's rules are not written.
		program := filepath.Join(t.TempDir(), "card-holder")
		d := declarationOf(cardDeclaration(program))
		if change := p.add(revision, providerFile{Version: providerFileVersion, Declaration: d, DeclaredBy: r.operators[0], DeclaredAt: time.Now().UnixMilli()}); change.Outcome != wire.DeclarationEditOutcomeApplied {
			t.Fatalf("add %+v", change)
		}
		for _, resource := range []string{"card:0", resourceservice.ResourceAwake} {
			if read := r.policy.ReadRule(rwire.Subject{Account: r.owner, Program: filepath.Clean(program)}, resourceservice.ActionHold, resource); read.Outcome != rwire.RuleReadOutcomeUnknown {
				t.Fatalf("a rule on %s before the sweep: %+v", resource, read)
			}
		}
		writeCardHoldRules(o, p)()
		requireCardRules(t, r, filepath.Clean(program), "the sweep")
	})
}
