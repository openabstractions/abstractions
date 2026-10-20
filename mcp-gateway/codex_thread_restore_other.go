//go:build !windows

package main

import "errors"

// Lock holders are read on Windows only; elsewhere the helper waits for host
// metadata as before.
func codexParentLoadedThreads(string) ([]string, error) {
	return nil, errors.New("Codex loaded threads unavailable on this platform")
}
