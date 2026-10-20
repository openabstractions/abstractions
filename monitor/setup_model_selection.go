package main

import (
	"context"
	"errors"
	"fmt"
	"slices"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

const comfyImagePolicyName = "oa-image-default"

func (p *servicePanel) selectedComfyImagePolicy(ctx context.Context) (bool, error) {
	p.comfyPathsMu.Lock()
	paths := p.comfyPaths
	p.comfyPathsMu.Unlock()
	if paths == nil {
		// Status polling never rehashes the installed portable application.
		// The next explicit setup action can reopen and verify its receipt.
		return false, nil
	}
	if err := paths.VerifyImagePolicy(); err != nil {
		return false, nil
	}
	providerID, err := setup.LemonadeProviderID(ctx)
	if err != nil {
		return false, err
	}
	if err := p.validateLemonadeImageDeclaration(ctx, providerID); err != nil {
		return false, err
	}
	operator, err := panelMachine().ResolveInferenceSelectionOperatorV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return false, err
	}
	read, err := operator.Read(ctx, paths.CallerProgram(), comfyImagePolicyName)
	if err != nil {
		return false, err
	}
	if read.Outcome != iwire.SelectionReadOutcomeFound || read.Policy == nil {
		return false, nil
	}
	return exactComfyImageCandidate(read.Policy, providerID), nil
}

func exactComfyImageCandidate(policy *iclient.SelectionPolicyV2, providerID string) bool {
	if policy == nil || policy.Name != comfyImagePolicyName || len(policy.Candidates) != 1 {
		return false
	}
	candidate := policy.Candidates[0]
	return slices.Contains(setup.PictureModelIDs(), candidate.Model) && candidate.Placement == iwire.CandidatePlacementLocal &&
		candidate.Credential == "" && candidate.Provider == providerID
}

func (p *servicePanel) validateLemonadeImageDeclaration(ctx context.Context, providerID string) error {
	registry, err := panelMachine().ResolveRegistry(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	list, err := registry.Declarations(ctx)
	if err != nil {
		return err
	}
	return validateLemonadeImageDeclarationList(list, providerID)
}

func validateLemonadeImageDeclarationList(list fwire.DeclarationList, providerID string) error {
	if list.Outcome != fwire.DeclarationListOutcomePage {
		return fmt.Errorf("provider declarations are %s", list.Outcome.String())
	}
	for _, item := range list.Declarations {
		d := item.Declaration
		if d.Name != providerID {
			continue
		}
		if item.DeclaredBy != "installation" || item.Role != fwire.DeclarationRoleProvider || d.Role != fwire.DeclarationRoleProvider || d.Activation != fwire.ActivationOnDemand ||
			!slices.Contains(d.Contracts, "abstraction.inference/image@1") || !reviewedLemonadeModels(d) {
			return errors.New("installed picture provider declaration does not match the reviewed model plan")
		}
		if item.Readiness != fwire.DeclarationReadinessReady && item.Readiness != fwire.DeclarationReadinessIdle {
			if item.Why != "" {
				return fmt.Errorf("installed picture provider is %s: %s", item.Readiness.String(), item.Why)
			}
			return fmt.Errorf("installed picture provider is %s", item.Readiness.String())
		}
		return nil
	}
	return errors.New("installed picture provider declaration is not visible to the public registry")
}

// reviewedLemonadeModels reports whether d's models are the reviewed starter
// picture model for image followed by none, some or all of the other picture
// models Setup stages, for image, and the reviewed speech profiles' models,
// each once and each for its own profile only (router CONTRACT.md ROUTE-D9).
// The starter model alone, with no profile, is the picture-only declaration
// earlier installations wrote, accepted only while d lists profile:image
// alone: an unscoped entry serves every listed profile.
func reviewedLemonadeModels(d fwire.Declaration) bool {
	models := d.Models
	if len(models) == 1 && models[0] == setup.BF16ModelID {
		return slices.Equal(d.Resources, []string{"profile:image"})
	}
	if len(models) == 0 || models[0] != "image:"+setup.BF16ModelID {
		return false
	}
	known := []string{"transcription:" + setup.SpeechTranscriptionModelID, "speech:" + setup.SpeechVoiceModelID}
	for _, id := range setup.PictureModelIDs()[1:] {
		known = append(known, "image:"+id)
	}
	for i, model := range models[1:] {
		if !slices.Contains(known, model) || slices.Contains(models[1:i+1], model) {
			return false
		}
	}
	return true
}

// ensureComfyImagePolicy performs an inspect-before-write, empty-revision
// create, public re-read, then app-owned recipe write. A conflicting policy is
// preserved and reported to the operator.
func (p *servicePanel) ensureComfyImagePolicy(ctx context.Context, paths setup.ComfyPaths) error {
	providerID, err := setup.LemonadeProviderID(ctx)
	if err != nil {
		return err
	}
	if err := p.validateLemonadeImageDeclaration(ctx, providerID); err != nil {
		return err
	}
	operator, err := panelMachine().ResolveInferenceSelectionOperatorV2(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return err
	}
	read, err := operator.Read(ctx, paths.CallerProgram(), comfyImagePolicyName)
	if err != nil {
		return err
	}
	if read.Outcome == iwire.SelectionReadOutcomeFound {
		if !exactComfyImageCandidate(read.Policy, providerID) {
			return errors.New("an existing ComfyUI image policy differs from the reviewed starter model; it was preserved")
		}
	} else if read.Outcome == iwire.SelectionReadOutcomeUnknown {
		change, err := operator.Put(ctx, paths.CallerProgram(), "", iclient.SelectionPolicyV2{Name: comfyImagePolicyName, Candidates: []iclient.SelectionCandidateV2{{
			Model: setup.BF16ModelID, Placement: iwire.CandidatePlacementLocal, Provider: providerID,
		}}})
		if err != nil {
			return err
		}
		if change.Outcome != iwire.EditOutcomeApplied {
			return fmt.Errorf("picture policy was not created (%s); reload and review the current policy", change.Outcome.String())
		}
	} else {
		return fmt.Errorf("picture policy cannot be read (%s); no policy was changed", read.Outcome.String())
	}
	confirmed, err := operator.Read(ctx, paths.CallerProgram(), comfyImagePolicyName)
	if err != nil {
		return err
	}
	if confirmed.Outcome != iwire.SelectionReadOutcomeFound || !exactComfyImageCandidate(confirmed.Policy, providerID) {
		return errors.New("picture policy could not be confirmed after setup; the app recipe was not written")
	}
	if err := paths.WriteImagePolicy(); err != nil {
		return err
	}
	return paths.VerifyImagePolicy()
}
