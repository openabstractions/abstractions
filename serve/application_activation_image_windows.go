package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func holdActivationImage(ctx context.Context, image applicationAgentImage) (activationImageHold, error) {
	held, err := openHeldApplicationImage(ctx, fileid.Self, image.Program)
	if err != nil {
		return nil, err
	}
	if held.key != image.Key {
		return nil, errors.Join(errors.New("activation: native image replaced"), held.Close())
	}
	return held, nil
}
