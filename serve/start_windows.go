package main

import (
	"context"
	"errors"
	"fmt"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"golang.org/x/sys/windows"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"
)

func startGuard() error {
	var elevated uint32
	var size uint32
	if err := windows.GetTokenInformation(windows.GetCurrentProcessToken(), windows.TokenElevation, (*byte)(unsafe.Pointer(&elevated)), 4, &size); err != nil {
		return fmt.Errorf("start token inspection: %w", err)
	}
	if elevated != 0 {
		return fmt.Errorf("start refused: run from the user's unelevated session")
	}
	return nil
}
func currentSupervisor() (string, error) { return bootstrap.SupervisorName() }
func activateInstalled(ctx context.Context) error {
	name, err := currentSupervisor()
	if err != nil {
		return err
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	sc := filepath.Join(system, "sc.exe")
	return activateWindows(ctx, name, sc, startCommand, os.Executable, os.Stat)
}
func activateWindows(ctx context.Context, name, sc string, run func(context.Context, string, ...string) error, executable func() (string, error), stat func(string) (os.FileInfo, error)) error {
	err := run(ctx, sc, "query", name)
	if err == nil {
		err = run(ctx, sc, "start", name)
		var exit *exec.ExitError
		if errors.As(err, &exit) && exit.ExitCode() == 1056 {
			return nil
		}
		return err
	}
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1060 {
		return err
	}
	self, err := executable()
	if err != nil {
		return err
	}
	sibling := filepath.Join(filepath.Dir(self), "jobdw.exe")
	info, err := stat(sibling)
	if err != nil {
		return fmt.Errorf("start absent: installed sibling jobdw.exe: %w", err)
	}
	if !filepath.IsAbs(sibling) || !info.Mode().IsRegular() {
		return fmt.Errorf("start absent: installed sibling jobdw.exe is not a regular absolute file")
	}
	return run(ctx, sibling, "start", "--runtime", "--require-unelevated")
}
func hideStartCommand(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }
