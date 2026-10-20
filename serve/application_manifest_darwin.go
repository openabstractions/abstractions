//go:build darwin && cgo

package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/fileid"
)

const applicationManifestSealedResource = "Resources/OA_APPLICATION_MANIFEST.json"

// The evidence authenticates captured bytes and native generations. Open file
// identities retain the locator association used for registration and launch.
type heldApplicationManifest struct {
	Bytes         []byte
	Code          identity.Code
	Proof         identity.Proof
	Signed        bool
	ImageSHA256   string
	ImageSize     int64
	Pinned        bool
	mu            sync.Mutex
	bundle, image fileid.Handle
	sealed        *identity.SealedResourceEvidence
	program, key  string
}

func (h *heldApplicationManifest) Close() error {
	if h == nil {
		return nil
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sealed == nil {
		return nil
	}
	err := errors.Join(h.sealed.Close(), h.image.Close(), h.bundle.Close())
	h.sealed = nil
	return err
}

func (h *heldApplicationManifest) Recheck(ctx context.Context) error {
	if h == nil {
		return errors.New("application manifest: file hold closed")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.sealed == nil {
		return errors.New("application manifest: file hold closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	for _, held := range []fileid.Handle{h.bundle, h.image} {
		if _, err := held.File().Stat(); err != nil {
			return err
		}
		named, err := fileid.Open(ctx, fileid.Self, held.FileID().Path(), fileid.Options{Links: fileid.RefuseLinks})
		if err != nil {
			return err
		}
		same := named.FileID().Equal(held.FileID())
		closeErr := named.Close()
		if !same {
			return errors.New("application manifest: locator no longer names held object")
		}
		if closeErr != nil {
			return closeErr
		}
	}
	// Active mapped-image generation is separately checked through the retained
	// started-process binding before every protected operation.
	return nil
}

func openSignedApplicationManifest(ctx context.Context, peer fileid.Peer, program string) (_ *heldApplicationManifest, err error) {
	image, err := fileid.Open(ctx, peer, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	h := &heldApplicationManifest{image: image, program: image.FileID().Path(), key: image.FileID().Key()}
	defer func() {
		if err != nil {
			err = errors.Join(err, h.image.Close(), h.bundle.Close())
			if h.sealed != nil {
				err = errors.Join(err, h.sealed.Close())
			}
		}
	}()
	macos := filepath.Dir(h.program)
	contents := filepath.Dir(macos)
	if filepath.Base(macos) != "MacOS" || filepath.Base(contents) != "Contents" {
		return nil, errors.New("application manifest: native bundle main executable required")
	}
	h.bundle, err = fileid.Open(ctx, peer, filepath.Dir(contents), fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		return nil, err
	}
	h.sealed, h.Code, h.Proof, err = identity.VerifySealedResource(ctx, h.bundle, h.image, applicationManifestSealedResource, applicationManifestMaxBytes)
	if err != nil {
		return nil, err
	}
	if !h.Code.Trusted || h.Proof != identity.ProofBound {
		return nil, errors.New("application manifest: program signature not trusted")
	}
	h.Signed = true
	h.Bytes, err = h.sealed.Bytes()
	if err != nil {
		return nil, err
	}
	if err = h.Recheck(ctx); err != nil {
		return nil, err
	}
	return h, nil
}

// macOS reads the declaration from a sealed bundle resource; an unsigned
// bundle has no such resource and is never pinned.
func applicationHeldPin(context.Context, *heldApplicationManifest) (string, int64, error) {
	return "", 0, errApplicationManifestUnsupported
}

// openHeldApplicationImage holds a program image for PROGRAM review. macOS
// reviews bundles through their sealed resource, so an image read is
// unsupported here.
func openHeldApplicationImage(context.Context, fileid.Peer, string) (*heldApplicationManifest, error) {
	return nil, errApplicationManifestUnsupported
}
