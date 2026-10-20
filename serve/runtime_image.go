package main

import (
	"context"
	"errors"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
)

// errRuntimeImageReplaced ends a supervised runtime whose own program file is
// no longer the program at the path it was started from. Every client pins
// the installed path and compares it with the server's image
// (listen.ServerExpectation, identity.SameSubjectProgram), so such a runtime
// is refused by all of them ("server program mismatch"). Windows Installer
// moves an in-use image aside when it removes or replaces it; a runtime that
// kept running across that served nobody (owner's PC, 2026-11-01: a removal
// at 12:23 moved the running 0.8.0 preview image to C:\Config.Msi, and the
// next preview's install put a new file at the path). Exiting lets the
// supervisor start the installed file.
var errRuntimeImageReplaced = errors.New("runtime: its program file was replaced or removed; the supervisor starts the installed program")

// runtimeImageCheckEvery is how often a supervised runtime compares its image
// with its path. The supervisor's restart limit (SUPERVISION.md: three
// restarts, then the host exits) bounds a runtime that keeps finding a
// mismatch.
var runtimeImageCheckEvery = 15 * time.Second

// runtimeImageWatch compares the file this process runs from (current, the
// operating system's present name for the image) with the path it was started
// from (launched), by the same rule clients apply to it.
type runtimeImageWatch struct {
	launched string
	current  func() (string, error)
	same     func(a, b string) bool
}

// replaced reports an image whose present name is another program than the
// launch path. A failed or empty read reports nothing: only a positive
// mismatch ends the runtime.
func (w runtimeImageWatch) replaced() bool {
	if w.launched == "" || w.current == nil {
		return false
	}
	now, err := w.current()
	if err != nil || now == "" {
		return false
	}
	same := w.same
	if same == nil {
		same = identity.SameSubjectProgram
	}
	return !same(now, w.launched)
}

// run checks every interval until ctx ends, and calls stop with
// errRuntimeImageReplaced at the first mismatch.
func (w runtimeImageWatch) run(ctx context.Context, every time.Duration, stop func(error)) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if w.replaced() {
				stop(errRuntimeImageReplaced)
				return
			}
		}
	}
}
