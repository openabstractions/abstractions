//go:build !windows

package main

// ownRuntimeImageWatch: the installed supervisor and its image check are
// Windows-only (SUPERVISION.md).
func ownRuntimeImageWatch() (runtimeImageWatch, bool) { return runtimeImageWatch{}, false }
