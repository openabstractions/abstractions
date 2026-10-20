//go:build !windows

package main

import (
	"context"
	"errors"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// Legacy platforms retain their existing controller update until their batch
// owns staging, publication and companion readiness.
func (p *servicePanel) updateAgentHelperFrom(ctx context.Context, party setup.AgentParty, program string, verify func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
	image, changed, backup, err := setup.UpdateEnrolledAgentImageFrom(ctx, panelMachine(), party, program, verify)
	if err != nil {
		return nil, changed, backup, err
	}
	if image == nil {
		return nil, changed, backup, errors.New("updated connection image unavailable")
	}
	return image, changed, backup, nil
}
