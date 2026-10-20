//go:build !windows

package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func runAgentConnectionUpdateBatch(context.Context) error { return nil }

func runAgentConnectionUpdateBatchFor(context.Context, setup.AgentParty) (agentHelperUpdateResult, error) {
	return agentHelperUpdateResult{}, errors.New("connection update batch unavailable on this platform")
}

func runAgentConnectionUpdateBatchReviewFor(context.Context, setup.AgentParty, applications.AgentUpdateRequest) (agentHelperUpdateResult, error) {
	return agentHelperUpdateResult{}, errors.New("connection update batch unavailable on this platform")
}
