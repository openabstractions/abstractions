package main

import (
	"context"
	"errors"
	"path/filepath"

	identity "github.com/openabstractions/abstraction-identity"
)

// The signed DATA reader supplies these values from its still-live hold.
// Its native held type remains owned by application_manifest_windows.go.
type ownedApplicationImage struct {
	ownedApplicationNativeImage
	program string
	code    identity.Code
	proof   identity.Proof
	// pinned is an unsigned image whose held SHA-256 and size matched the
	// registration the person kept, under this same hold.
	pinned  bool
	recheck func(context.Context) error
	close   func() error
}

func (image ownedApplicationImage) admit(ctx context.Context) error {
	if image.recheck == nil || image.close == nil || !(image.code.Trusted && image.proof == identity.ProofBound || image.pinned) || image.code.SharedInterpreter || !filepath.IsAbs(image.program) || filepath.Clean(image.program) != image.program {
		return errors.New("application host: no held trusted own image")
	}
	if err := image.recheck(ctx); err != nil {
		return err
	}
	// VerifyFile's Code does not populate script-host facts. Use their existing
	// owner against this still-held image rather than trusting zero flags.
	if identity.SharedInterpreterProgram(image.program, image.code) {
		return errors.New("application host: shared interpreter is not an own host")
	}
	return ctx.Err()
}
