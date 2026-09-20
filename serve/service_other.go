//go:build !windows

package main

import (
	"errors"
	"io"
)

// Only the Windows installer holds an upgrade exclusion; Linux and macOS
// packages hand the runtime to systemd and launchd.
func serviceCommand(_ []string, _, _ io.Writer) error {
	return &exitError{code: 2, err: errors.New("service begin-upgrade|end-upgrade|start|upgrade-check are Windows Installer actions")}
}
