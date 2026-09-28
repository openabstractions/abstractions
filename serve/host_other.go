//go:build !windows

package main

import (
	"errors"
	"io"
)

// systemd and launchd run `openabstractions serve runtime` directly with their
// own restart policy; the host is the Windows lifetime owner.
func serveHost([]string) error {
	return &exitError{code: 2, err: errors.New("serve supervisor is the Windows runtime supervisor; on this platform the installed service manager runs serve runtime directly")}
}

func hostCommand(args []string, _, diagnostics io.Writer) error {
	if len(args) == 0 {
		return commandMistake(diagnostics, "host: a command is required", "openabstractions host --help")
	}
	return &exitError{code: 2, err: errors.New("host register|unregister writes the Windows per-user service template; this platform registers its runtime through its own service manager")}
}
