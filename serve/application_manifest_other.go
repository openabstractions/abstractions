//go:build !windows && (!darwin || !cgo)

package main

import (
	"context"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

type heldApplicationManifest struct {
	Bytes       []byte
	Code        identity.Code
	Proof       identity.Proof
	Signed      bool
	ImageSHA256 string
	ImageSize   int64
	Pinned      bool
}

func (*heldApplicationManifest) Close() error { return nil }
func (*heldApplicationManifest) Recheck(context.Context) error {
	return errApplicationManifestUnsupported
}
func openSignedApplicationManifest(context.Context, fileid.Peer, string) (*heldApplicationManifest, error) {
	return nil, errApplicationManifestUnsupported
}
func applicationHeldPin(context.Context, *heldApplicationManifest) (string, int64, error) {
	return "", 0, errApplicationManifestUnsupported
}

// openHeldApplicationImage holds a program image for PROGRAM review, which
// this platform does not run.
func openHeldApplicationImage(context.Context, fileid.Peer, string) (*heldApplicationManifest, error) {
	return nil, errApplicationManifestUnsupported
}
