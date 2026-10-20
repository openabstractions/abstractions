package main

import (
	"context"
	"errors"

	"github.com/openabstractions/abstraction-identity/fileid"
)

func openMessageStartImage(ctx context.Context, request applicationMessageStartRequest) (ownedApplicationImage, error) {
	held, err := openTrustedApplicationImage(ctx, fileid.Self, request.Program)
	if err != nil {
		return ownedApplicationImage{}, err
	}
	if held.key != request.ProgramKey || !samePrograms(held.program, request.Program) {
		return ownedApplicationImage{}, errors.Join(errors.New("message start: held image changed"), held.Close())
	}
	return held.ownedImage(), nil
}
