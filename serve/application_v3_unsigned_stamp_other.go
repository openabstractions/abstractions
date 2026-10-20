//go:build !windows

package main

// Only Windows pins unsigned images; elsewhere no image is refused by stamp.
func applicationHeldStamp(*heldApplicationManifest) string { return "" }
