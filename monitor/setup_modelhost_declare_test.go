package main

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
)

type fakeModelHostRegistry struct {
	edits     []string
	conflicts int
}

func (f *fakeModelHostRegistry) edit(op, revision string) fwire.DeclarationChange {
	f.edits = append(f.edits, op+"@"+revision)
	if f.conflicts > 0 {
		f.conflicts--
		return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeConflict}
	}
	return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeApplied, Revision: op + "-done"}
}
func (f *fakeModelHostRegistry) DeclarePinned(_ context.Context, revision string, d fwire.PinnedDeclaration) (fwire.DeclarationChange, error) {
	return f.edit("declare:"+d.Declaration.Name, revision), nil
}
func (f *fakeModelHostRegistry) ReplacePinned(_ context.Context, revision string, d fwire.PinnedDeclaration) (fwire.DeclarationChange, error) {
	return f.edit("replace:"+d.Declaration.Name, revision), nil
}
func (f *fakeModelHostRegistry) Withdraw(_ context.Context, revision, name string) (fwire.DeclarationChange, error) {
	return f.edit("withdraw:"+name, revision), nil
}
func (f *fakeModelHostRegistry) Register(_ context.Context, spec registration.RegistrationSpec) (registration.Change, error) {
	change := f.edit("register:"+spec.Providers[0].Name, "")
	if change.Outcome == fwire.DeclarationEditOutcomeConflict {
		return registration.Change{Outcome: registration.OutcomeConflict}, nil
	}
	return registration.Change{Outcome: registration.OutcomeApplied, Revision: change.Revision}, nil
}
func (f *fakeModelHostRegistry) Replace(_ context.Context, expected string, spec registration.RegistrationSpec) (registration.Change, error) {
	f.edits = append(f.edits, "replace_registration:"+spec.Providers[0].Name+"@"+expected)
	return registration.Change{Outcome: registration.OutcomeApplied, Revision: "replace_registration-done"}, nil
}

// A-SEC6 declarer ruling (FAC-R9): Setup's model host edits run the command
// with --plan and the Panel writes each edit through its own registry
// client, in order, chaining revisions as the command does. A conflict at
// the first edit plans again once; nothing the command printed is written
// by the command itself.
func TestModelHostDeclareWritesThePlanThroughThePanelsRegistry(t *testing.T) {
	root := t.TempDir()
	tools := filepath.Join(root, "OA", "tools")
	// OA's programs carry .exe on Windows and no suffix elsewhere.
	exe := func(stem string) string {
		if runtime.GOOS == "windows" {
			return stem + ".exe"
		}
		return stem
	}
	pinned := func(name string) string {
		program := map[string]string{"modelhost": filepath.Join(tools, exe("modelhostd")), "pytorch": filepath.Join(tools, exe("pytorchd"))}[name]
		if program == "" {
			program = filepath.Join(tools, exe("lemonaded"))
		}
		raw := fwire.EncodePinnedDeclaration(&fwire.PinnedDeclaration{Declaration: fwire.Declaration{Name: name, Program: program,
			Arguments: []string{"serve"}, Endpoint: name, Transport: fwire.DeclarationTransportNative, Contracts: []string{}, Resources: []string{},
			Models: []string{}, Activation: fwire.ActivationOnDemand}})
		return strings.TrimSuffix(string(raw), "\n")
	}
	plan, err := json.Marshal(modelHostPlan{Steps: []modelHostPlanStep{
		{Op: "replace", Revision: "read-1", Pinned: pinned("pytorch")},
		{Op: "declare", Revision: "ignored", Pinned: pinned("modelhost")},
		{Op: "withdraw", Revision: "ignored", Name: "pytorch"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	savedFolder := modelHostPlanFolder
	t.Cleanup(func() { modelHostPlanFolder = savedFolder })
	modelHostPlanFolder = func() string { return tools }
	registry := &fakeModelHostRegistry{conflicts: 1}
	saved := modelHostPanelRegistry
	t.Cleanup(func() { modelHostPanelRegistry = saved })
	modelHostPanelRegistry = func(context.Context) (modelHostRegistry, error) { return registry, nil }
	p := newTestPanel(t)
	var commands [][]string
	p.modelHostOA = func(_ context.Context, args ...string) ([]byte, error) {
		commands = append(commands, args)
		return plan, nil
	}
	if err := p.modelHostDeclare(context.Background(), "models", "host", "store/object", "--json"); err != nil {
		t.Fatal(err)
	}
	if len(commands) != 2 || !slices.Contains(commands[0], "--plan") {
		t.Fatalf("commands %v", commands)
	}
	want := []string{"replace:pytorch@read-1", "replace:pytorch@read-1", "declare:modelhost@replace:pytorch-done", "withdraw:pytorch@declare:modelhost-done"}
	if !slices.Equal(registry.edits, want) {
		t.Fatalf("the Panel wrote %v, want %v", registry.edits, want)
	}

	// A plan that names another provider writes nothing.
	registry.edits = nil
	other, err := json.Marshal(modelHostPlan{Steps: []modelHostPlanStep{{Op: "declare", Revision: "r", Pinned: pinned("lemonade-image")}}})
	if err != nil {
		t.Fatal(err)
	}
	p.modelHostOA = func(context.Context, ...string) ([]byte, error) { return other, nil }
	if err := p.modelHostDeclare(context.Background(), "models", "host", "store/object"); err == nil || len(registry.edits) != 0 {
		t.Fatalf("a plan naming lemonade-image: %v, wrote %v", err, registry.edits)
	}
	p.modelHostOA = func(context.Context, ...string) ([]byte, error) { return plan, nil }

	// A plan whose program sits outside this Panel's folder, or is no
	// program the table lists for the name, writes nothing.
	for _, program := range []string{filepath.Join(root, "Elsewhere", exe("modelhostd")), filepath.Join(tools, exe("lemonaded"))} {
		registry.edits = nil
		raw := fwire.EncodePinnedDeclaration(&fwire.PinnedDeclaration{Declaration: fwire.Declaration{Name: "modelhost", Program: program,
			Arguments: []string{"serve"}, Endpoint: "modelhost", Transport: fwire.DeclarationTransportNative, Contracts: []string{}, Resources: []string{},
			Models: []string{}, Activation: fwire.ActivationOnDemand}})
		foreign, err := json.Marshal(modelHostPlan{Steps: []modelHostPlanStep{{Op: "replace", Revision: "r", Pinned: strings.TrimSuffix(string(raw), "\n")}}})
		if err != nil {
			t.Fatal(err)
		}
		p.modelHostOA = func(context.Context, ...string) ([]byte, error) { return foreign, nil }
		if err := p.modelHostDeclare(context.Background(), "models", "host", "store/object"); err == nil || len(registry.edits) != 0 {
			t.Fatalf("a plan naming %s: %v, wrote %v", program, err, registry.edits)
		}
	}
	p.modelHostOA = func(context.Context, ...string) ([]byte, error) { return plan, nil }

	// A refused later edit stops the plan and names the refusal.
	registry.edits = nil
	failing := &failingModelHostRegistry{fakeModelHostRegistry: registry}
	modelHostPanelRegistry = func(context.Context) (modelHostRegistry, error) { return failing, nil }
	if err := p.modelHostDeclare(context.Background(), "models", "host", "store/object"); err == nil || errors.Is(err, errModelHostPlanConflict) {
		t.Fatalf("a refused withdraw: %v", err)
	}
}

type failingModelHostRegistry struct{ *fakeModelHostRegistry }

func (f *failingModelHostRegistry) Withdraw(context.Context, string, string) (fwire.DeclarationChange, error) {
	return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeForbidden}, nil
}
