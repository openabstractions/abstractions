//go:build !windows && !darwin

package setup

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-facade/go/client"
)

func UpdateEnrolledAgentImage(context.Context, *client.Machine, AgentParty, func(context.Context, string, string) error) (*AgentImage, bool, string, error) {
	return nil, false, "", errors.New("integration update is available on Windows")
}

func UpdateEnrolledAgentImageFrom(ctx context.Context, machine *client.Machine, party AgentParty, _ string, verify func(context.Context, string, string) error) (*AgentImage, bool, string, error) {
	return UpdateEnrolledAgentImage(ctx, machine, party, verify)
}

func RemoveAgentEnrollment(context.Context, *client.Machine, AgentParty, AgentEnrollmentCapture) error {
	return errors.New("native helper removal unavailable")
}
