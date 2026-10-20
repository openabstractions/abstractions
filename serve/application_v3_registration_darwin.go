//go:build darwin && cgo

package main

func applicationV3HeldKey(h *heldApplicationManifest) string {
	if h == nil {
		return ""
	}
	return h.key
}

func applicationV3HeldProgram(h *heldApplicationManifest) string {
	if h == nil {
		return ""
	}
	return h.program
}
