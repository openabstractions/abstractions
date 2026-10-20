//go:build windows

package main

import (
	"context"
	"errors"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

// Reuse the existing no-write/no-delete-shared image custody owner. Signature
// verification and captured FileKey therefore describe this same held image.
func openRegistryProgramImage(ctx context.Context, peer fileid.Peer, program string) (_ *heldApplicationManifest, err error) {
	held, err := openHeldApplicationImage(ctx, peer, program)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, held.Close())
		}
	}()
	held.Code, held.Proof, err = identity.VerifyFile(applicationV3HeldProgram(held), nil)
	if err != nil {
		return nil, err
	}
	if !registrationProgramCodeAccepted(held.Code, held.Proof) {
		return nil, errors.New("registration image: unsupported native trust verdict")
	}
	if err = held.Recheck(ctx); err != nil {
		return nil, err
	}
	return held, nil
}

func observeRegistryLegacyImage(program string) (key string, code identity.Code, proof identity.Proof, err error) {
	held, err := openHeldApplicationImage(context.Background(), fileid.Self, program)
	if err != nil {
		return "", code, proof, err
	}
	defer func() { err = errors.Join(err, held.Close()) }()
	code, proof, err = identity.VerifyFile(applicationV3HeldProgram(held), nil)
	if recheck := held.Recheck(context.Background()); recheck != nil {
		return "", code, proof, recheck
	}
	return applicationV3HeldKey(held), code, proof, err
}
