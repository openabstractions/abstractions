//go:build !windows && !darwin

package setup

import (
	"context"
	"errors"
)

func HoldAgentImageForReview(context.Context, AgentParty) (*AgentImage, error) {
	return nil, errors.New("native integration review is unavailable on this platform")
}
