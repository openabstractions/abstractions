//go:build !windows

package main

import (
	"github.com/openabstractions/abstraction-provider-kit/go/sandbox"
	"io"
)

func startOpenCodeSession(options openCodeSessionOptions, output, diagnostics io.Writer) error {
	return sandbox.ErrUnsupported
}
