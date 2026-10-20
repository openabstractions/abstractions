//go:build darwin && cgo

package main

import (
	"context"
	"errors"

	"github.com/openabstractions/abstraction-identity/fileid"
)

func openMessageStartImage(ctx context.Context, request applicationMessageStartRequest) (ownedApplicationImage, error) {
	// The current Darwin process matcher requires the existing sealed bundle
	// resource/generation evidence. A standalone helper supplies no substitute.
	held, err := openSignedApplicationManifest(ctx, fileid.Self, request.Program)
	if err != nil {
		return ownedApplicationImage{}, err
	}
	if held.key != request.ProgramKey || !samePrograms(held.program, request.Program) {
		return ownedApplicationImage{}, errors.Join(errors.New("message start: held image changed"), held.Close())
	}
	return held.ownedImage(), nil
}
