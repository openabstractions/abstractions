//go:build windows

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
)

// Setup's early stop (bundle.wxs BeginCoreUpgrade) closes the Panel, its
// background presenter and the runtime before Windows Installer replaces
// their files, under the upgrade exclusion. A copy started after that stop,
// from Start, a sign-in entry or a notification click, held its own program
// file again, and Windows listed it in the files-in-use question the early
// stop exists to avoid. The runtime already refuses to start then; the Panel
// and the presenter now do the same: while the installation's own CLI answers
// `service upgrade-check` with status 3, they exit 3 before opening anything.
// A missing CLI, a development build and any other answer start normally.

const upgradeInProgressExit = 3

func exitDuringUpgrade() {
	if upgradeReplacingThisInstallation(os.Executable, runUpgradeCheck) {
		fmt.Fprintln(os.Stderr, "OpenAbstractions is being updated; open it again when the update finishes")
		os.Exit(upgradeInProgressExit)
	}
}

func upgradeReplacingThisInstallation(executable func() (string, error), check func(cli string) (int, error)) bool {
	self, err := executable()
	if err != nil {
		return false
	}
	cli := filepath.Join(filepath.Dir(self), "openabstractions.exe")
	if info, err := os.Stat(cli); err != nil || !info.Mode().IsRegular() {
		return false
	}
	code, err := check(cli)
	return err == nil && code == upgradeInProgressExit
}

func runUpgradeCheck(cli string) (int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := childproc.Hidden(exec.CommandContext(ctx, cli, "service", "upgrade-check")).Run()
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	if err != nil {
		return 0, err
	}
	return 0, nil
}
