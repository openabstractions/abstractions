package main

import (
	"context"
	"io"
	"os"
	"os/user"
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
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// A models host declaration names the card its engine loads weights into
// beside its profiles (facade CONTRACT.md FAC-R3): the accelerator it holds is
// what it declares, not an inference from its contracts or models.
func TestModelsHostDeclaresTheCardItLoadsInto(t *testing.T) {
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
	if err := declareHost(w, registry, list.Revision, settings, nil, "models host", io.Discard, false, "hosting"); err != nil {
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
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	startInferenceRuntime(t, options)

	call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	machine := client.New(options.endpoint)
	registry, err := machine.ResolveRegistry(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	list, err := registry.Declarations(call)
	if err != nil {
		t.Fatal(err)
	}

	holder := filepath.Join(t.TempDir(), "card-holder")
	holding := wire.Declaration{Name: "card-holder", Program: holder, Arguments: []string{}, Endpoint: "card-holder",
		Transport: wire.DeclarationTransportNative, Contracts: []string{inference.Contract}, Resources: []string{"card:0"},
		Activation: wire.ActivationAttach}
	change, err := registry.Declare(call, list.Revision, holding)
	if err != nil || change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("declaring card-holder: %+v %v", change, err)
	}

	list, err = registry.Declarations(call)
	if err != nil {
		t.Fatal(err)
	}
	bystander := filepath.Join(t.TempDir(), "no-resource")
	naming := wire.Declaration{Name: "no-resource", Program: bystander, Arguments: []string{}, Endpoint: "no-resource",
		Transport: wire.DeclarationTransportNative, Contracts: []string{inference.Contract}, Activation: wire.ActivationAttach}
	change, err = registry.Declare(call, list.Revision, naming)
	if err != nil || change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("declaring no-resource: %+v %v", change, err)
	}

	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	operator, err := machine.ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	held := rights.Subject{Account: account.Uid, Program: filepath.Clean(holder)}
	read, err := operator.ReadRuleContext(call, held, resourceservice.ActionHold, instrument.Card0)
	if err != nil || read.Outcome.String() != "found" || !read.Record.Rule.Permit || read.Record.Why != providerAddWhy {
		t.Fatalf("abstraction.resource/hold for the declared card: %+v %v", read, err)
	}

	unheld := rights.Subject{Account: account.Uid, Program: filepath.Clean(bystander)}
	read, err = operator.ReadRuleContext(call, unheld, resourceservice.ActionHold, instrument.Card0)
	if err != nil || read.Outcome.String() != "unknown" {
		t.Fatalf("a declaration naming no resource wrote %+v %v", read, err)
	}
}
