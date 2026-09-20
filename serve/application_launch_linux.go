//go:build linux

package main

import (
	"errors"
	"os"
	"os/exec"
)

func launchApplication(program string, arguments []string) error {
	if os.Geteuid() == 0 || os.Getenv("XDG_SESSION_ID") == "" || os.Getenv("XDG_RUNTIME_DIR") == "" || os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		return errors.New("application activation requires an identified graphical user session")
	}
	command := exec.Command(program, arguments...)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
