package main

import (
	"context"
	"errors"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func preparePhysicalIntegrationImage(ctx context.Context, party setup.AgentParty) (installerIntegrationImage, error) {
	return preparePhysicalIntegrationImageWith(
		func() (installerIntegrationImage, error) {
			return setup.PrepareInstallerAgentImage(ctx, party)
		},
		func() (installerIntegrationImage, error) {
			return setup.ReplaceUnenrolledInstallerAgentImage(ctx, panelMachine(), party)
		},
		func() (installerIntegrationImage, error) {
			return setup.HoldEnrolledAgentImage(ctx, panelMachine(), party)
		})
}

func preparePhysicalIntegrationImageWith(prepare, replace, hold func() (installerIntegrationImage, error)) (installerIntegrationImage, error) {
	image, err := prepare()
	if err == nil {
		return image, nil
	}
	replacement, replaceErr := replace()
	if replaceErr == nil {
		return replacement, nil
	}
	kept, holdErr := hold()
	if holdErr == nil {
		return kept, nil
	}
	return nil, errors.Join(err, replaceErr, holdErr)
}

func prepareInstalledAgentInstallerImage(ctx context.Context, party setup.AgentParty) (*setup.AgentImage, error) {
	binding, err := panelMachine().ResolveService(ctx, "abstraction.facade/applications@3", facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, err
	}
	api := applications.NewApplicationsClient(binding.Transport().WithContext(ctx))
	application := nativecli.AgentApplicationForParty(string(party))
	// Finish a prior approved publication before reading its enrollment. The
	// same recovery owner is used at login; this installer selects only its app.
	if err := resumeAgentConnectionUpdates(func(request applications.AgentUpdateRequest) (applications.AgentUpdateResult, error) {
		if len(request.Members) != 1 || request.Members[0].Application != application {
			return applications.AgentUpdateResult{Outcome: applications.OutcomeUnknown}, nil
		}
		return api.FindAgentUpdate(request)
	}, api.CommitAgentUpdate, func(result applications.AgentUpdateResult) error {
		return (&servicePanel{}).readyClaudeConnectionUpdate(ctx, result)
	}, func(result applications.AgentUpdateResult) error {
		return postAgentConnectionCompletion(ctx, result)
	}); err != nil {
		return nil, err
	}
	image, err := setup.PrepareInstallerAgentImage(ctx, party)
	if err == nil {
		return image, nil
	}
	// Only the native-enrolled old image can enter this batch. Staging holds
	// both images and publication still waits for the person's existing Keep.
	if updateErr := runAgentConnectionUpdateBatchWithResult(ctx, party, nil); updateErr != nil {
		return nil, errors.Join(err, updateErr)
	}
	return setup.PrepareInstallerAgentImage(ctx, party)
}
