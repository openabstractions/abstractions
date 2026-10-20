package setup

import (
	"context"
	"github.com/openabstractions/abstraction-facade/go/client"
)

func UpdateEnrolledAgentImageFrom(ctx context.Context, machine *client.Machine, party AgentParty, _ string, verify func(context.Context, string, string) error) (*AgentImage, bool, string, error) {
	return UpdateEnrolledAgentImage(ctx, machine, party, verify)
}
