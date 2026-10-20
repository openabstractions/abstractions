package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	"github.com/openabstractions/abstraction-resource/go/instrument"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// A models host declaration names the card its engine loads weights into
// beside its profiles (facade CONTRACT.md FAC-R3): the accelerator it holds is
// what it declares, not an inference from its contracts or models.
func TestModelsHostDeclaresTheCardItLoadsInto(t *testing.T) {
	requireNativeModelHostRegistration(t)
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)

	w := newWaiting(30 * time.Second)
	defer w.stop()
	call, done := w.call()
	registry, err := client.New(options.endpoint).ResolveRegistry(call, client.Requirements{})
	done()
	if err != nil {
		t.Fatal(err)
	}
	call, done = w.call()
	list, err := registry.Declarations(call)
	done()
	if err != nil {
		t.Fatal(err)
	}

	programDir := filepath.Join(t.TempDir(), "model host program")
	if err := os.MkdirAll(programDir, 0o700); err != nil {
		t.Fatal(err)
	}
	program := filepath.Clean(copyTestBinaryTo(t, programDir, modelHostProgram))
	requestedProgram := program
	if short, hasAlias := shortSubjectProgramAlias(t, program); hasAlias {
		requestedProgram = short
	}
	settings := hostSettings{program: requestedProgram, endpoint: "modelhost-declares-card", engine: "llama-server", resource: "resource-endpoint"}
	if err := declareHost(w, pinnedModelHostEditor(t, w, options), list, settings, []hostedModel{{Name: "tiny", Store: "fake", Object: "tiny"}}, "models host", io.Discard, false, "hosting"); err != nil {
		t.Fatalf("declareHost: %v", err)
	}

	call, done = w.call()
	after, err := registry.Declarations(call)
	done()
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, d := range after.Declarations {
		if d.Declaration.Name != modelHostName {
			continue
		}
		found = true
		if d.Declaration.Program != identity.NormalizeSubjectProgram(program) {
			t.Fatalf("model host declaration program = %q, want canonical path %q", d.Declaration.Program, program)
		}
		if !slices.Contains(d.Declaration.Resources, instrument.Card0) || !slices.Contains(d.Declaration.Resources, "profile:chat") {
			t.Fatalf("the model host declares %v, wants %s and profile:chat among its resources", d.Declaration.Resources, instrument.Card0)
		}
		if !slices.Contains(d.Declaration.Arguments, argRuntimeProgram) {
			t.Fatalf("the on-demand model host has no runtime designation in %v", d.Declaration.Arguments)
		}
	}
	if !found {
		t.Fatal("the model host declaration is not listed")
	}
}

// The capability that owns card:<n> writes abstraction.resource/hold for the
// declared program at Declare, with why "provider add", the same way Declare
// writes a store's inventory.provide; a declaration naming no resource gets
// no rule (facade CONTRACT.md FAC-R3).
func TestADeclarationNamingACardGetsTheHoldRuleAndOneNamingNoneGetsNone(t *testing.T) {
	call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	// Exercise the declaration/rule owner without starting unrelated pairing
	// reconciliation. Native registry dispatch has its own transport controls.
	r := testRights(t, t.TempDir())
	if err := r.install([]string{ActionProviderManage, resourceservice.ActionHold},
		[]installationRule{{ActionProviderManage, "account"}}); err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"),
		supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}), report: func(err error) { t.Errorf("provider: %v", err) }}
	o := &inferenceOperator{credentials: &runtimeCredentials{runtimeRights: r}, providers: p, report: func(err error) { t.Errorf("operator: %v", err) }}
	registry := &runtimeRegistry{providers: p, operator: o}
	caller := inference.Subject{Account: r.owner, Program: r.operators[0]}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}

	holder := filepath.Join(t.TempDir(), "card-holder")
	holding := wire.Declaration{Name: "card-holder", Program: holder, Arguments: []string{}, Endpoint: "card-holder",
		Transport: wire.DeclarationTransportNative, Contracts: []string{inference.Contract}, Resources: []string{"card:0"},
		Activation: wire.ActivationAttach}
	change := registry.Declare(call, caller, revision, holding)
	if change.Outcome != wire.DeclarationEditOutcomeApplied || change.Reason != "" {
		t.Fatalf("declaring card-holder: %+v", change)
	}

	_, revision, err = p.read()
	if err != nil {
		t.Fatal(err)
	}
	bystander := filepath.Join(t.TempDir(), "no-resource")
	naming := wire.Declaration{Name: "no-resource", Program: bystander, Arguments: []string{}, Endpoint: "no-resource",
		Transport: wire.DeclarationTransportNative, Contracts: []string{inference.Contract}, Activation: wire.ActivationAttach}
	change = registry.Declare(call, caller, revision, naming)
	if change.Outcome != wire.DeclarationEditOutcomeApplied || change.Reason != "" {
		t.Fatalf("declaring no-resource: %+v", change)
	}

	held := rwire.Subject{Account: r.owner, Program: filepath.Clean(holder)}
	read := r.policy.ReadRule(held, resourceservice.ActionHold, instrument.Card0)
	if read.Outcome.String() != "found" || !read.Record.Rule.Permit || read.Record.Why != providerAddWhy {
		t.Fatalf("abstraction.resource/hold for the declared card: %+v", read)
	}
	// A card holder runs GPU work and holds the wake lease while it does
	// (resource CONTRACT.md RES-G5, RES-A1).
	read = r.policy.ReadRule(held, resourceservice.ActionHold, resourceservice.ResourceAwake)
	if read.Outcome.String() != "found" || !read.Record.Rule.Permit || read.Record.Why != providerAddWhy {
		t.Fatalf("abstraction.resource/hold on awake for the card holder: %+v", read)
	}

	unheld := rwire.Subject{Account: r.owner, Program: filepath.Clean(bystander)}
	for _, resource := range []string{instrument.Card0, resourceservice.ResourceAwake} {
		read = r.policy.ReadRule(unheld, resourceservice.ActionHold, resource)
		if read.Outcome.String() != "unknown" {
			t.Fatalf("a declaration naming no resource wrote hold on %s: %+v", resource, read)
		}
	}
}
