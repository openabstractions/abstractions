package main

import (
	"slices"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func TestComfyStarterSelectionRequiresExactLocalPinnedCandidate(t *testing.T) {
	good := &iclient.SelectionPolicyV2{Name: comfyImagePolicyName, Candidates: []iclient.SelectionCandidateV2{{
		Model: setup.BF16ModelID, Placement: iwire.CandidatePlacementLocal, Provider: "lemonade-image",
	}}}
	if !exactComfyImageCandidate(good, "lemonade-image") {
		t.Fatal("the exact reviewed local candidate was rejected")
	}
	// The picture chooser points the policy at another model Setup stages.
	offered := &iclient.SelectionPolicyV2{Name: comfyImagePolicyName, Candidates: []iclient.SelectionCandidateV2{{
		Model: "user.Z-Image-Turbo", Placement: iwire.CandidatePlacementLocal, Provider: "lemonade-image",
	}}}
	if !exactComfyImageCandidate(offered, "lemonade-image") {
		t.Fatal("an offered picture model was rejected")
	}
	tests := []struct {
		name     string
		policy   *iclient.SelectionPolicyV2
		provider string
	}{
		{"different policy name", &iclient.SelectionPolicyV2{Name: "other", Candidates: good.Candidates}, "lemonade-image"},
		{"different model", &iclient.SelectionPolicyV2{Name: comfyImagePolicyName, Candidates: []iclient.SelectionCandidateV2{{Model: "other", Placement: iwire.CandidatePlacementLocal, Provider: "lemonade-image"}}}, "lemonade-image"},
		{"hosted placement", &iclient.SelectionPolicyV2{Name: comfyImagePolicyName, Candidates: []iclient.SelectionCandidateV2{{Model: setup.BF16ModelID, Placement: iwire.CandidatePlacementHosted, Provider: "lemonade-image"}}}, "lemonade-image"},
		{"different provider generation", good, "replaced-provider"},
		{"extra fallback", &iclient.SelectionPolicyV2{Name: comfyImagePolicyName, Candidates: append(append([]iclient.SelectionCandidateV2{}, good.Candidates...), good.Candidates[0])}, "lemonade-image"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if exactComfyImageCandidate(test.policy, test.provider) {
				t.Fatal("a non-exact selection was accepted")
			}
		})
	}
}

func TestOnlyInstalledIdleOrReadyImageDeclarationCanBackSetupPin(t *testing.T) {
	good := fwire.DeclarationState{DeclaredBy: "installation", Role: fwire.DeclarationRoleProvider,
		Declaration: fwire.Declaration{Name: "lemonade-image", Role: fwire.DeclarationRoleProvider, Activation: fwire.ActivationOnDemand,
			Contracts: []string{"abstraction.inference/image@1"}, Resources: []string{"profile:image"}, Models: []string{setup.BF16ModelID}},
		Readiness: fwire.DeclarationReadinessIdle}
	list := fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Declarations: []fwire.DeclarationState{good}}
	if err := validateLemonadeImageDeclarationList(list, "lemonade-image"); err != nil {
		t.Fatalf("valid installed declaration rejected: %v", err)
	}
	// The installed declaration also names the speech profiles' models, each
	// for its own profile (ROUTE-D9).
	speech := good
	speech.Declaration.Contracts = []string{"abstraction.inference/image@1", "abstraction.inference/transcription@1", "abstraction.inference/speech@1"}
	speech.Declaration.Resources = []string{"profile:image", "profile:transcription", "profile:speech"}
	speech.Declaration.Models = []string{"image:" + setup.BF16ModelID, "transcription:" + setup.SpeechTranscriptionModelID, "speech:" + setup.SpeechVoiceModelID}
	if err := validateLemonadeImageDeclarationList(fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Declarations: []fwire.DeclarationState{speech}}, "lemonade-image"); err != nil {
		t.Fatalf("installed declaration with speech rejected: %v", err)
	}
	// An offered picture model Setup staged follows the starter, for image.
	offered := speech
	offered.Declaration.Models = append(slices.Clone(speech.Declaration.Models), "image:user.Z-Image-Turbo")
	if err := validateLemonadeImageDeclarationList(fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Declarations: []fwire.DeclarationState{offered}}, "lemonade-image"); err != nil {
		t.Fatalf("installed declaration with an offered picture model rejected: %v", err)
	}
	scoped := []string{"image:" + setup.BF16ModelID}
	for _, mutate := range []struct {
		name   string
		change func(*fwire.DeclarationState)
	}{
		{"operator declared", func(s *fwire.DeclarationState) { s.DeclaredBy = "operator" }},
		{"wrong activation", func(s *fwire.DeclarationState) { s.Declaration.Activation = fwire.ActivationAttach }},
		{"extra model", func(s *fwire.DeclarationState) { s.Declaration.Models = append(s.Declaration.Models, "other") }},
		{"no starter model", func(s *fwire.DeclarationState) { s.Declaration.Models = []string{"image:user.Z-Image-Turbo"} }},
		{"offered model for speech", func(s *fwire.DeclarationState) {
			s.Declaration.Models = append(slices.Clone(scoped), "speech:user.Z-Image-Turbo")
		}},
		// Speech models without their profiles serve every profile the
		// declaration lists, image included.
		{"unscoped speech models", func(s *fwire.DeclarationState) {
			s.Declaration.Models = []string{setup.BF16ModelID, setup.SpeechTranscriptionModelID, setup.SpeechVoiceModelID}
		}},
		{"speech model for image", func(s *fwire.DeclarationState) {
			s.Declaration.Models = append(slices.Clone(scoped), "image:"+setup.SpeechVoiceModelID)
		}},
		{"model after speech", func(s *fwire.DeclarationState) {
			s.Declaration.Models = append(slices.Clone(scoped), "transcription:"+setup.SpeechTranscriptionModelID, "speech:"+setup.SpeechVoiceModelID, "image:other")
		}},
		{"speech model twice", func(s *fwire.DeclarationState) {
			s.Declaration.Models = append(slices.Clone(scoped), "speech:"+setup.SpeechVoiceModelID, "speech:"+setup.SpeechVoiceModelID)
		}},
		{"speech model first", func(s *fwire.DeclarationState) {
			s.Declaration.Models = []string{"speech:" + setup.SpeechVoiceModelID, "image:" + setup.BF16ModelID}
		}},
		// The flat picture-only form is the earlier installation's; with
		// speech profiles listed it would serve the picture model for speech.
		{"flat picture model with speech profiles", func(s *fwire.DeclarationState) {
			s.Declaration.Resources = []string{"profile:image", "profile:transcription", "profile:speech"}
		}},
		{"wrong contract", func(s *fwire.DeclarationState) { s.Declaration.Contracts = nil }},
		{"not ready", func(s *fwire.DeclarationState) { s.Readiness = fwire.DeclarationReadinessNotReady }},
	} {
		t.Run(mutate.name, func(t *testing.T) {
			bad := good
			mutate.change(&bad)
			if err := validateLemonadeImageDeclarationList(fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Declarations: []fwire.DeclarationState{bad}}, "lemonade-image"); err == nil {
				t.Fatal("unsafe provider declaration accepted")
			}
		})
	}
}
