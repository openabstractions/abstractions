//go:build !windows && !darwin

package setup

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/client"
)

type EnrolledAgentImage struct{ *AgentImage }

func HoldEnrolledAgentImage(context.Context, *client.Machine, AgentParty) (*EnrolledAgentImage, error) {
	return nil, errors.New("integration management is unavailable on this platform")
}

func HoldEnrolledAgentImageFrom(ctx context.Context, machine *client.Machine, party AgentParty, _ string) (*EnrolledAgentImage, error) {
	return HoldEnrolledAgentImage(ctx, machine, party)
}
