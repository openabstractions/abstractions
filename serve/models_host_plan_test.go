package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

// A-SEC6 declarer ruling (FAC-R9): `models host --plan` computes the model
// host's registry edits as the command always does and writes none of
// them; the plan carries each edit in the generated codecs' text, which the
// Panel applies through its own registry client.
func TestModelsHostPlanWritesNothingAndCarriesTheEdits(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	runtime := &modelHostOperatorFixture{page: registration.Page{Outcome: registration.OutcomePage}, outcome: registration.OutcomeApplied}
	plan := &modelHostPlan{Steps: []modelHostPlanStep{}}
	writer, err := newModelHostRegistryWriter(t.Context(), modelHostPlanLegacy{plan}, modelHostPlanTyped{runtime, plan})
	if err != nil {
		t.Fatal(err)
	}
	w := newWaiting(0)
	defer w.stop()
	settings := hostSettings{program: program, engine: filepath.Join(t.TempDir(), "llama-server.exe"), endpoint: "modelhost"}
	var out bytes.Buffer
	err = plan.write(declareHost(w, writer, wire.DeclarationList{Revision: "r1"}, settings, []hostedModel{{Name: "qwen3.6-35b-a3b", Store: "store", Object: "object"}},
		"models host", &bytes.Buffer{}, true, ""), &out)
	if err != nil {
		t.Fatal(err)
	}
	if runtime.writes != 0 {
		t.Fatalf("the plan wrote %d registrations", runtime.writes)
	}
	var printed modelHostPlan
	if err := json.Unmarshal(out.Bytes(), &printed); err != nil || len(printed.Steps) != 1 {
		t.Fatalf("plan %s: %v", out.String(), err)
	}
	step := printed.Steps[0]
	if step.Op != "register" {
		t.Fatalf("plan step %+v", step)
	}
	spec, err := registration.DecodeRegistrationSpec([]byte(step.Spec))
	if err != nil || len(spec.Providers) != 1 || spec.Providers[0].Name != modelHostName || !samePrograms(spec.Program, program) {
		t.Fatalf("planned registration %+v %v", spec, err)
	}

	// A declaration edit carries the pinned declaration's codec text.
	legacy := &modelHostPlan{Steps: []modelHostPlanStep{}}
	d := wire.PinnedDeclaration{Declaration: wire.Declaration{Name: modelHostName, Program: program, Arguments: []string{"serve"}, Endpoint: "modelhost",
		Transport: wire.DeclarationTransportNative, Contracts: []string{}, Resources: []string{}, Models: []string{}, Activation: wire.ActivationOnDemand}}
	if change, err := (modelHostPlanLegacy{legacy}).ReplacePinned(t.Context(), "r2", d); err != nil || change.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatal(change, err)
	}
	decoded, err := wire.DecodePinnedDeclaration([]byte(legacy.Steps[0].Pinned))
	if err != nil || decoded.Declaration.Name != modelHostName || legacy.Steps[0].Op != "replace" || legacy.Steps[0].Revision != "r2" {
		t.Fatalf("planned declaration %+v %v", legacy.Steps[0], err)
	}
}

// Security check of A-SEC6 (2026-11-22): a plan carries a stored modelhost
// or pytorch declaration forward only when the runtime lists it with
// product standing or it holds the shipped defaults: the sibling program
// and every path in OpenAbstractions' own folders. A 0.8.0 declaration this
// command wrote stays upgradable; another program's declaration, with its
// own program or an engine elsewhere, is refused.
func TestPlanRefusesAnotherProgramsModelHostDeclaration(t *testing.T) {
	tools, data, elsewhere := t.TempDir(), t.TempDir(), t.TempDir()
	savedSibling, savedRoots := plannedSibling, plannedRoots
	t.Cleanup(func() { plannedSibling, plannedRoots = savedSibling, savedRoots })
	plannedSibling = func(stem string) string { return filepath.Join(tools, stem+".exe") }
	plannedRoots = func() []string { return []string{tools, filepath.Join(data, "OpenAbstractions")} }
	engine := filepath.Join(data, "OpenAbstractions", "engines", "llama.cpp-b1", "llama-server.exe")
	type planned struct {
		state   wire.DeclarationState
		shipped bool
	}
	state := func(program string, standing wire.ProductStanding, arguments ...string) planned {
		return planned{wire.DeclarationState{Declaration: wire.Declaration{Name: modelHostName, Program: program, Arguments: append([]string{"serve", "--endpoint", "{endpoint}"}, arguments...)}}, standing == wire.ProductStandingProduct}
	}
	sibling := filepath.Join(tools, "modelhostd.exe")
	for name, row := range map[string]struct {
		state   planned
		refused bool
	}{
		"0.8.0 with shipped defaults":   {state(sibling, 0, "--engine", engine, "--runtime-program", filepath.Join(tools, "openabstractions.exe")), false},
		"third_party, shipped defaults": {state(sibling, wire.ProductStandingThirdParty, "--engine", engine), false},
		"product standing":              {state(filepath.Join(elsewhere, "modelhostd.exe"), wire.ProductStandingProduct, "--engine", filepath.Join(elsewhere, "x.exe")), false},
		"another program":               {state(filepath.Join(elsewhere, "modelhostd.exe"), wire.ProductStandingThirdParty, "--engine", engine), true},
		"an engine elsewhere":           {state(sibling, wire.ProductStandingThirdParty, "--engine", filepath.Join(elsewhere, "llama-server.exe")), true},
		"a joined path elsewhere":       {state(sibling, 0, "--engine-arg="+filepath.Join(elsewhere, "lib.dll")), true},
	} {
		if got := plannedFromAnotherProgram(row.state.state, row.state.shipped); got != row.refused {
			t.Errorf("%s: refused %v, want %v", name, got, row.refused)
		}
	}
	pytorch := state(filepath.Join(tools, "pytorchd.exe"), 0, "--torch-source", filepath.Join(elsewhere, "infer"))
	pytorch.state.Declaration.Name = pytorchName
	if !plannedFromAnotherProgram(pytorch.state, pytorch.shipped) {
		t.Error("a pytorch declaration with a torch source elsewhere was carried forward")
	}
}
