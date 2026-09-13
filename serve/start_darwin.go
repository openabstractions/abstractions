package main

import (
	"context"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"

	"errors"
	"fmt"

	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const runtimeAgent = "com.openabstractions.jobd" // Installed label retained across upgrades.
func startGuard() error {
	if os.Geteuid() == 0 {
		return errors.New("start refused: use the installed user's graphical session")
	}
	return nil
}
func hideStartCommand(*exec.Cmd) {}
func activateInstalled(ctx context.Context) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	return activateDarwin(ctx, home, os.Getuid(), os.ReadFile, func(ctx context.Context, args ...string) (string, error) {
		cmd := exec.CommandContext(ctx, "/bin/launchctl", args...)
		cmd.WaitDelay = 10 * time.Millisecond
		out, err := cmd.Output()
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		if err != nil {
			return "", fmt.Errorf("launchctl %v: %w", args, err)
		}
		return string(out), nil
	})
}
func activateDarwin(ctx context.Context, home string, uid int, read func(string) ([]byte, error), run func(context.Context, ...string) (string, error)) error {
	plist := filepath.Join(home, "Library", "LaunchAgents", runtimeAgent+".plist")
	raw, err := read(plist)
	if err != nil {
		return fmt.Errorf("start absent: installed runtime LaunchAgent: %w", err)
	}
	if err := validateRuntimeAgent(raw, filepath.Join(home, ".local", "bin", "openabstractions")); err != nil {
		return err
	}
	owner, err := run(ctx, "manageruid")
	if err != nil {
		return err
	}
	name, err := run(ctx, "managername")
	if err != nil {
		return err
	}
	if strings.TrimSpace(owner) != strconv.Itoa(uid) || strings.TrimSpace(name) != "Aqua" {
		return errors.New("start refused: run in this user's graphical login session")
	}
	jobs, err := run(ctx, "list")
	if err != nil {
		return err
	}
	present, err := runtimeAgentListed(jobs)
	if err != nil {
		return err
	}
	domain := "gui/" + strconv.Itoa(uid)
	if !present {
		_, err = run(ctx, "bootstrap", domain, plist)
		return err
	}
	_, err = run(ctx, "kickstart", domain+"/"+runtimeAgent)
	return err // No -k: preserve a running instance.
}
func runtimeAgentListed(jobs string) (bool, error) {
	lines := strings.Split(strings.TrimSpace(jobs), "\n")
	if len(lines) == 0 || strings.Join(strings.Fields(lines[0]), " ") != "PID Status Label" {
		return false, errors.New("start: unrecognized launchctl list output")
	}
	found := false
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < 3 {
			return false, errors.New("start: malformed launchctl entry")
		}
		if f[0] != "-" {
			if n, e := strconv.Atoi(f[0]); e != nil || n < 0 {
				return false, errors.New("start: invalid launchctl PID")
			}
		}
		if _, e := strconv.Atoi(f[1]); e != nil {
			return false, errors.New("start: invalid launchctl status")
		}
		if len(f) == 3 && f[2] == runtimeAgent {
			found = true
		}
	}
	return found, nil
}

// Read only the actual root dictionary. Unknown values are skipped as complete
// elements, so nested identity keys cannot replace top-level launch settings.
func validateRuntimeAgent(raw []byte, executable string) error {
	return bootstrap.ValidateRuntimeAgent(raw, executable)
}
