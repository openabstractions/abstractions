//go:build !windows

package main

import (
	"errors"
	"os"
	"os/exec"
)

type commandChild struct{ cmd *exec.Cmd }

// startProviderChild starts program with env as its whole environment and
// output as its standard output and error; a nil output leaves both
// unconnected.
func startProviderChild(program string, args, env []string, output *os.File) (providerChild, error) {
	cmd := exec.Command(program, args...)
	cmd.Env = env
	if output != nil {
		cmd.Stdout, cmd.Stderr = output, output
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return &commandChild{cmd: cmd}, nil
}
func (p *commandChild) PID() int    { return p.cmd.Process.Pid }
func (p *commandChild) Stop() error { return p.cmd.Process.Kill() }
func (p *commandChild) Wait() (int, error) {
	err := p.cmd.Wait()
	if err == nil {
		return 0, nil
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		return exit.ExitCode(), nil
	}
	return 0, err
}
