//go:build !windows && (!darwin || !cgo)

package main

func applicationV3HeldKey(*heldApplicationManifest) string { return "" }

func applicationV3HeldProgram(*heldApplicationManifest) string { return "" }
