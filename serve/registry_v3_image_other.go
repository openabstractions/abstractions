//go:build !windows

package main

import (
	"context"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func openRegistryProgramImage(context.Context, fileid.Peer, string) (*heldApplicationManifest, error) {
	return nil, errApplicationManifestUnsupported
}

func observeRegistryLegacyImage(program string) (string, identity.Code, identity.Proof, error) {
	key, err := fileid.FileKey(program)
	if err != nil {
		return "", identity.Code{}, 0, err
	}
	code, proof, err := identity.VerifyFile(program, nil)
	return key, code, proof, err
}
