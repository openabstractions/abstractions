package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	"github.com/openabstractions/abstraction-storage/go/layout"
)

// A-SEC6 declarer ruling (FAC-R9): the model host's declarations keep
// product standing only when the Panel's registry client writes them. With
// --plan, `models host` and `models unhost` compute their registry edits as
// they always do and print them instead of writing them; the Panel applies
// them through its own client, in order. A declaration this command writes
// itself records the command line as its declarer and reads third_party.

// modelHostPlanStep is one registry edit: register or replace_registration a
// typed registration spec, or declare, replace or withdraw a declaration.
// spec and pinned are the generated codecs' own text.
type modelHostPlanStep struct {
	Op string `json:"op"`
	// Revision is the declarations revision this command would have passed;
	// the Panel passes it to its first edit and each edit's returned revision
	// to the next, as this command does.
	Revision string `json:"revision,omitempty"`
	// Expected is the registration revision a replace_registration names.
	Expected string `json:"expected,omitempty"`
	Name     string `json:"name,omitempty"`
	Spec     string `json:"spec,omitempty"`
	Pinned   string `json:"pinned,omitempty"`
}

type modelHostPlan struct {
	Steps []modelHostPlanStep `json:"steps"`
}

// write prints the plan, or returns the command's own refusal.
func (p *modelHostPlan) write(err error, output io.Writer) error {
	if err != nil {
		return err
	}
	return writeJSON(output, p)
}

// modelHostPlanLegacy records declaration edits and answers each applied at the
// revision it was given.
type modelHostPlanLegacy struct{ plan *modelHostPlan }

func (l modelHostPlanLegacy) record(op, revision, name string, d *wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	step := modelHostPlanStep{Op: op, Revision: revision, Name: name}
	if d != nil {
		raw, err := registrationEncoded(wire.EncodePinnedDeclaration, d)
		if err != nil {
			return wire.DeclarationChange{}, err
		}
		step.Pinned = strings.TrimSuffix(string(raw), "\n")
	}
	l.plan.Steps = append(l.plan.Steps, step)
	return wire.DeclarationChange{Outcome: wire.DeclarationEditOutcomeApplied, Revision: revision}, nil
}

func (l modelHostPlanLegacy) DeclarePinned(_ context.Context, revision string, d wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	return l.record("declare", revision, d.Declaration.Name, &d)
}

func (l modelHostPlanLegacy) ReplacePinned(_ context.Context, revision string, d wire.PinnedDeclaration) (wire.DeclarationChange, error) {
	return l.record("replace", revision, d.Declaration.Name, &d)
}

func (l modelHostPlanLegacy) Withdraw(_ context.Context, revision, name string) (wire.DeclarationChange, error) {
	return l.record("withdraw", revision, name, nil)
}

// modelHostPlanTyped reads registrations from the runtime and records typed
// writes.
type modelHostPlanTyped struct {
	real providerRegistrationEditor
	plan *modelHostPlan
}

func (t modelHostPlanTyped) Registrations(ctx context.Context, cursor string, limit int64) (registration.Page, error) {
	return t.real.Registrations(ctx, cursor, limit)
}

func (t modelHostPlanTyped) record(op, expected string, spec registration.RegistrationSpec) (registration.Change, error) {
	raw, err := registrationEncoded(registration.EncodeRegistrationSpec, &spec)
	if err != nil {
		return registration.Change{}, err
	}
	t.plan.Steps = append(t.plan.Steps, modelHostPlanStep{Op: op, Expected: expected, Spec: strings.TrimSuffix(string(raw), "\n")})
	return registration.Change{Outcome: registration.OutcomeApplied, Revision: expected}, nil
}

func (t modelHostPlanTyped) Register(_ context.Context, spec registration.RegistrationSpec) (registration.Change, error) {
	return t.record("register", "", spec)
}

func (t modelHostPlanTyped) Replace(_ context.Context, expected string, spec registration.RegistrationSpec) (registration.Change, error) {
	return t.record("replace_registration", expected, spec)
}

// plannedSibling and plannedRoots are the shipped defaults a stored model
// host declaration without product standing must match before a plan
// carries it forward; a test replaces them.
var (
	plannedSibling = siblingProgram
	plannedRoots   = modelHostShippedRoots
)

// modelHostShippedRoots are the folders OpenAbstractions' own model host
// files live in: its engines, model and state folders under the account's
// data, Setup's configuration and this command's own folder.
func modelHostShippedRoots() []string {
	var roots []string
	if data, err := layout.AccountData(); err == nil {
		roots = append(roots, filepath.Join(data, "OpenAbstractions"), filepath.Join(data, "OA"))
	}
	if config, err := layout.SetupConfig(); err == nil {
		roots = append(roots, config)
	}
	if exe, err := os.Executable(); err == nil {
		roots = append(roots, filepath.Dir(filepath.Clean(exe)))
	}
	return roots
}

// plannedFromAnotherProgram reports a stored modelhost or pytorch
// declaration a plan must not carry forward for the Panel to declare as its
// own (security check of A-SEC6, 2026-11-22): the runtime's Standings give
// it no product standing (shipped), and it is no declaration this command wrote with the
// shipped defaults: the sibling modelhostd or pytorchd, and every path it
// carries in OpenAbstractions' own folders. A 0.8.0 declaration this
// command wrote with those defaults stays upgradable.
func plannedFromAnotherProgram(state wire.DeclarationState, shipped bool) bool {
	if shipped {
		return false
	}
	stem := modelHostProgram
	if state.Declaration.Name == pytorchName {
		stem = pytorchProgram
	}
	sibling := plannedSibling(stem)
	if sibling == "" || !samePrograms(state.Declaration.Program, sibling) {
		return true
	}
	roots := plannedRoots()
	for _, argument := range state.Declaration.Arguments {
		value := argument
		if flag, rest, ok := strings.Cut(argument, "="); ok && strings.HasPrefix(flag, "-") {
			value = rest
		}
		if filepath.IsAbs(value) && !slices.ContainsFunc(roots, func(root string) bool { return pathWithin(value, root) }) {
			return true
		}
	}
	return false
}

// pathWithin reports whether path is root or below it, by name, ignoring
// case on Windows.
func pathWithin(path, root string) bool {
	if root == "" {
		return false
	}
	path, root = filepath.Clean(path), filepath.Clean(root)
	if runtime.GOOS == "windows" {
		path, root = strings.ToLower(path), strings.ToLower(root)
	}
	return path == root || strings.HasPrefix(path, strings.TrimSuffix(root, string(filepath.Separator))+string(filepath.Separator))
}
