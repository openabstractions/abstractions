//go:build darwin && cgo

package main

import identity "github.com/openabstractions/abstraction-identity"

type ownedApplicationNativeImage struct {
	sealed *identity.SealedResourceEvidence
}

func (h *heldApplicationManifest) ownedImage() ownedApplicationImage {
	if h == nil {
		return ownedApplicationImage{}
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return ownedApplicationImage{program: h.program, code: h.Code, proof: h.Proof, recheck: h.Recheck, close: h.Close, ownedApplicationNativeImage: ownedApplicationNativeImage{sealed: h.sealed}}
}
