package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	facadeclient "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// A-SEC6 declarer ruling (FAC-R9): Setup declares modelhost and pytorch
// through the Panel's registry client before it starts the model host, and
// the component table lists oa:panel as their declarer. `openabstractions
// models host` and `models unhost` compute the edits (--plan) and this Panel
// writes them, so the runtime records the Panel as their declarer. Every
// later re-declaration Setup makes, a repair or a restart included, goes
// through here too.

// modelHostPlanStep and modelHostPlan are the command's --plan output
// (serve models_host_plan.go).
type modelHostPlanStep struct {
	Op       string `json:"op"`
	Revision string `json:"revision,omitempty"`
	Expected string `json:"expected,omitempty"`
	Name     string `json:"name,omitempty"`
	Spec     string `json:"spec,omitempty"`
	Pinned   string `json:"pinned,omitempty"`
}

type modelHostPlan struct {
	Steps []modelHostPlanStep `json:"steps"`
}

// modelHostRegistry is the Panel's own registry clients: the declarations
// (registry@2) and the typed registrations (registry@3).
type modelHostRegistry interface {
	DeclarePinned(context.Context, string, fwire.PinnedDeclaration) (fwire.DeclarationChange, error)
	ReplacePinned(context.Context, string, fwire.PinnedDeclaration) (fwire.DeclarationChange, error)
	Withdraw(context.Context, string, string) (fwire.DeclarationChange, error)
	Register(context.Context, registration.RegistrationSpec) (registration.Change, error)
	Replace(context.Context, string, registration.RegistrationSpec) (registration.Change, error)
}

type panelModelHostRegistry struct {
	*facadeclient.RegistryV2
	typed *facadeclient.RegistryV3
}

func (r panelModelHostRegistry) Register(ctx context.Context, spec registration.RegistrationSpec) (registration.Change, error) {
	return r.typed.Register(ctx, spec)
}

func (r panelModelHostRegistry) Replace(ctx context.Context, expected string, spec registration.RegistrationSpec) (registration.Change, error) {
	return r.typed.Replace(ctx, expected, spec)
}

// modelHostPanelRegistry resolves this Panel's registry clients; a test
// replaces it.
var modelHostPanelRegistry = func(ctx context.Context) (modelHostRegistry, error) {
	machine := panelMachine()
	legacy, err := machine.ResolveRegistryV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	typed, err := machine.ResolveRegistryV3(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	return panelModelHostRegistry{RegistryV2: legacy, typed: typed}, nil
}

// errModelHostPlanConflict is a registry that changed between the
// command's read and the Panel's first edit.
var errModelHostPlanConflict = errors.New("the registry changed while the model host's declaration was planned")

// modelHostDeclare runs one models host or models unhost command with
// --plan and writes its edits through this Panel's registry clients. A
// conflict at the first edit plans again once, as the command itself does.
func (p *servicePanel) modelHostDeclare(ctx context.Context, args ...string) error {
	var err error
	for attempt := 0; attempt < 2; attempt++ {
		var raw []byte
		raw, err = p.modelHostOpenAbstractions(ctx, append(slices.Clone(args), "--plan")...)
		if err != nil {
			return err
		}
		var plan modelHostPlan
		if err = json.Unmarshal(raw, &plan); err != nil {
			return fmt.Errorf("the model host's plan could not be read: %w", err)
		}
		if len(plan.Steps) == 0 {
			// What is declared already says this.
			return nil
		}
		registry, resolveErr := modelHostPanelRegistry(ctx)
		if resolveErr != nil {
			return resolveErr
		}
		if err = applyModelHostPlan(ctx, registry, plan); !errors.Is(err, errModelHostPlanConflict) {
			return err
		}
	}
	return err
}

// modelHostPlanNames are the only names a model host plan may write: the
// Panel lends its declarer standing to nothing else.
var modelHostPlanNames = []string{product.ModelHost, product.DiffusersModelsHost}

// modelHostPlanFolder is the folder of this Panel's own programs; a test
// replaces it.
var modelHostPlanFolder = func() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	return filepath.Dir(filepath.Clean(exe))
}

// modelHostPlanProgram reports a program the Panel declares as its own
// model host provider: one the component table lists for the name, directly
// in this Panel's own folder (security check of A-SEC6, 2026-11-22).
func modelHostPlanProgram(program, name string) bool {
	folder := modelHostPlanFolder()
	return folder != "" && product.ShipsProvider(program, name) && product.InFolder(program, folder)
}

// applyModelHostPlan makes the plan's edits in order. The first edit takes
// the revision the command read and each later one the revision the edit
// before it returned, as the command's own run does.
func applyModelHostPlan(ctx context.Context, registry modelHostRegistry, plan modelHostPlan) error {
	revision := ""
	for i, step := range plan.Steps {
		if i == 0 {
			revision = step.Revision
		}
		var change fwire.DeclarationChange
		var err error
		switch step.Op {
		case "declare", "replace":
			d, decodeErr := fwire.DecodePinnedDeclaration([]byte(step.Pinned))
			if decodeErr != nil {
				return fmt.Errorf("the model host's plan names an unreadable declaration: %w", decodeErr)
			}
			if !slices.Contains(modelHostPlanNames, d.Declaration.Name) {
				return fmt.Errorf("the model host's plan names another provider, %q", d.Declaration.Name)
			}
			if !modelHostPlanProgram(d.Declaration.Program, d.Declaration.Name) {
				return fmt.Errorf("the model host's plan names a program outside this Panel's folder for %q", d.Declaration.Name)
			}
			if step.Op == "declare" {
				change, err = registry.DeclarePinned(ctx, revision, *d)
			} else {
				change, err = registry.ReplacePinned(ctx, revision, *d)
			}
		case "withdraw":
			if !slices.Contains(modelHostPlanNames, step.Name) {
				return fmt.Errorf("the model host's plan names another provider, %q", step.Name)
			}
			change, err = registry.Withdraw(ctx, revision, step.Name)
		case "register", "replace_registration":
			spec, decodeErr := registration.DecodeRegistrationSpec([]byte(step.Spec))
			if decodeErr != nil {
				return fmt.Errorf("the model host's plan names an unreadable registration: %w", decodeErr)
			}
			if len(spec.Providers) == 0 || slices.ContainsFunc(spec.Providers, func(f registration.ProviderFacet) bool { return !slices.Contains(modelHostPlanNames, f.Name) }) ||
				len(spec.Servers)+len(spec.Remotes)+len(spec.Applications) != 0 {
				return errors.New("the model host's plan registers another provider")
			}
			for _, facet := range spec.Providers {
				if !modelHostPlanProgram(spec.Program, facet.Name) {
					return fmt.Errorf("the model host's plan names a program outside this Panel's folder for %q", facet.Name)
				}
			}
			var typed registration.Change
			if step.Op == "register" {
				typed, err = registry.Register(ctx, *spec)
			} else {
				typed, err = registry.Replace(ctx, step.Expected, *spec)
			}
			change.Revision, change.Reason = typed.Revision, typed.Reason
			switch typed.Outcome {
			case registration.OutcomeApplied:
				change.Outcome = fwire.DeclarationEditOutcomeApplied
			case registration.OutcomeConflict:
				change.Outcome = fwire.DeclarationEditOutcomeConflict
			default:
				change.Outcome = fwire.DeclarationEditOutcomeInvalid
				if err == nil {
					err = fmt.Errorf("registration: %s: %s", typed.Outcome.String(), typed.Reason)
				}
			}
		default:
			return fmt.Errorf("the model host's plan names an unknown edit %q", step.Op)
		}
		if err != nil {
			return err
		}
		if change.Outcome == fwire.DeclarationEditOutcomeConflict && i == 0 {
			return errModelHostPlanConflict
		}
		if change.Outcome != fwire.DeclarationEditOutcomeApplied {
			return fmt.Errorf("the model host's declaration was not written: %s %s", change.Outcome.String(), change.Reason)
		}
		revision = change.Revision
	}
	return nil
}
