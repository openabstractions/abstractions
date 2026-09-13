package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

func startGuard() error {
	if os.Geteuid() == 0 {
		return fmt.Errorf("start refused: use the installed user's unelevated session")
	}
	return nil
}
func activateInstalled(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return activateLinux(ctx, home, os.Stat, startCommand)
}
func hideStartCommand(*exec.Cmd) {}

func activateLinux(ctx context.Context, home string, stat func(string) (os.FileInfo, error), run func(context.Context, string, ...string) error) error {
	unit := filepath.Join(home, ".config", "systemd", "user", "abstraction-runtime.service")
	info, err := stat(unit)
	if err != nil {
		return fmt.Errorf("start absent: installed user runtime unit: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("start absent: runtime unit is not a regular file")
	}
	return run(ctx, "/usr/bin/systemctl", "--user", "start", "abstraction-runtime.service")
}
