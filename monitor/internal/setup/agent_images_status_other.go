//go:build !windows && !darwin

package setup

import (
	"context"
	"github.com/openabstractions/abstraction-facade/go/client"
)

func AgentImageCurrency(context.Context, *client.Machine, AgentParty, string, string) string {
	return "unknown"
}

func EnrolledAgentImageUpdate(context.Context, *client.Machine, AgentParty) (string, string) {
	return "absent", ""
}
