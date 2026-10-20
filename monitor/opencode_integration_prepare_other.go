//go:build !windows

package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func preparePhysicalIntegrationImage(ctx context.Context, party setup.AgentParty) (installerIntegrationImage, error) {
	image, err := setup.PrepareInstallerAgentImage(ctx, party)
	if err == nil {
		return image, nil
	}
	kept, holdErr := setup.HoldEnrolledAgentImage(ctx, panelMachine(), party)
	if holdErr != nil {
		return nil, errors.Join(err, holdErr)
	}
	return kept, nil
}

func prepareInstalledAgentInstallerImage(ctx context.Context, party setup.AgentParty) (*setup.AgentImage, error) {
	return setup.PrepareInstallerAgentImage(ctx, party)
}
