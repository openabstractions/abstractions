//go:build !windows

package main

import (
	"errors"
	"io"
)

// Only the Windows installer holds an upgrade exclusion; Linux and macOS
// packages hand the runtime to systemd and launchd.
func serviceCommand(args []string, _, diagnostics io.Writer) error {
	if len(args) == 0 {
		return commandMistake(diagnostics, "service: a command is required", "openabstractions service --help")
	}
	return &exitError{code: 2, err: errors.New("service begin-upgrade|end-upgrade|start|upgrade-check are Windows Installer actions")}
}
