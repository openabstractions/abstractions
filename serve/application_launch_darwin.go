//go:build darwin

package main

import (
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
)

func launchApplication(program string, arguments []string) error {
	uid, err := exec.Command("/bin/launchctl", "manageruid").Output()
	if err != nil || strings.TrimSpace(string(uid)) != strconv.Itoa(os.Getuid()) {
		return errors.New("application activation requires this user's launchd session")
	}
	name, err := exec.Command("/bin/launchctl", "managername").Output()
	if err != nil || strings.TrimSpace(string(name)) != "Aqua" {
		return errors.New("application activation requires this user's Aqua session")
	}
	command := exec.Command(program, arguments...)
	if err := command.Start(); err != nil {
		return err
	}
	go func() { _ = command.Wait() }()
	return nil
}
