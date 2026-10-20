//go:build !windows && (!darwin || !cgo)

package main

type ownedApplicationNativeImage struct{}

func (h *heldApplicationManifest) ownedImage() ownedApplicationImage { return ownedApplicationImage{} }
