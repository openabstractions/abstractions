package main

import (
	"context"
	"errors"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func (p *servicePanel) updateAgentHelperFrom(context.Context, setup.AgentParty, string, func(context.Context, string, string) error) (claudePanelImage, bool, string, error) {
	return nil, false, "", errors.New("Windows connection updates require the native batch")
}
