//go:build windows

package main

func applicationV3HeldKey(held *heldApplicationManifest) string { return held.key }

func applicationV3HeldProgram(held *heldApplicationManifest) string { return held.program }
