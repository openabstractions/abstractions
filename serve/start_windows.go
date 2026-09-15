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
	"time"
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
	if err := checkUpgrade(ctx, run, sibling, "start"); err != nil {
		return err
	}
	err = run(ctx, sc, "query", name)
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
	return run(ctx, sibling, "start", "--runtime", "--require-unelevated")
}
func hideStartCommand(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

// checkUpgrade asks the installed jobd whether an installer holds the upgrade
// exclusion for this installation. jobd owns that decision; its exit status 3
// means an upgrade is in progress.
func checkUpgrade(ctx context.Context, run func(context.Context, string, ...string) error, sibling, verb string) error {
	err := run(ctx, sibling, "service", "upgrade-check")
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == exitUpgradeInProgress {
		return &exitError{code: exitUpgradeInProgress, err: fmt.Errorf("%s refused: an upgrade of this installation is in progress; %s again when it finishes", verb, verb)}
	}
	return err
}

func jobdUpgradeGuard(ctx context.Context) error {
	return installedJobdGuard(ctx, startCommand, os.Executable, os.Stat)
}

// installedJobdGuard applies the upgrade exclusion to `serve jobd` run from an
// installation. A program with no installed jobdw.exe beside it belongs to no
// installation an installer can replace, and runs unchecked.
func installedJobdGuard(ctx context.Context, run func(context.Context, string, ...string) error, executable func() (string, error), stat func(string) (os.FileInfo, error)) error {
	self, err := executable()
	if err != nil {
		return fmt.Errorf("serve jobd cannot check the upgrade exclusion without this program's path: %w", err)
	}
	sibling := filepath.Join(filepath.Dir(self), "jobdw.exe")
	info, err := stat(sibling)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("serve jobd cannot inspect installed sibling jobdw.exe: %w", err)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("serve jobd refused: installed sibling jobdw.exe is not a regular file")
	}
	check, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	return checkUpgrade(check, run, sibling, "serve jobd")
}
