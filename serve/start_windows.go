package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"golang.org/x/sys/windows"
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
	return activateWindows(ctx, windowsActivation{
		instance: name, sc: filepath.Join(system, "sc.exe"),
		run: startCommand, launch: launchDetached, executable: os.Executable, stat: os.Stat, refusal: refuseDuringUpgrade,
		profile: bootstrap.CurrentProfileView, shellLaunch: launchThroughShell,
	})
}

type windowsActivation struct {
	instance, sc string
	run          func(context.Context, string, ...string) error
	launch       func(image string, args ...string) error
	executable   func() (string, error)
	stat         func(string) (os.FileInfo, error)
	refusal      func() error
	// profile probes the caller's profile view before a per-user launch; nil
	// reads real.
	profile func() (bootstrap.ProfileView, error)
	// shellLaunch starts the host through this session's desktop shell for a
	// caller whose profile view is virtualized.
	shellLaunch func(image, dir string, args ...string) error
}

// activateWindows starts this session's host. A machine installation's
// per-session service instance is started through the SCM when this sign-in
// has one. Otherwise, per-user or in the session that installed for everyone,
// the windowless image beside this program is launched detached as
// `serve host`. The upgrade exclusion refuses first, with exit status 3.
func activateWindows(ctx context.Context, a windowsActivation) error {
	twin, err := windowlessImage(a.executable, a.stat)
	if err != nil {
		return fmt.Errorf("start absent: %w", err)
	}
	if err := a.refusal(); err != nil {
		return err
	}
	err = a.run(ctx, a.sc, "query", a.instance)
	if err == nil {
		err = a.run(ctx, a.sc, "start", a.instance)
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
	if a.profile != nil {
		view, err := a.profile()
		if err != nil {
			return refuseVirtualizedProfile("start", a.profile)
		}
		if view.Virtualized {
			// A host launched from here would inherit the package's private
			// AppData. The desktop shell starts it with the real profile.
			if a.shellLaunch == nil {
				return refuseVirtualizedProfile("start", a.profile)
			}
			err := a.shellLaunch(twin, filepath.Dir(twin), "serve", "host")
			if errors.Is(err, errNoShell) {
				return &exitError{code: exitVirtualizedProfile, err: fmt.Errorf("start: virtualized_caller_no_shell: this process sees package %s's private copy of AppData, and no desktop shell of this account can start the runtime: %w", view.Family, err)}
			}
			return err
		}
	}
	return a.launch(twin, "serve", "host")
}

func hideStartCommand(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true} }

const (
	detachedProcess        = 0x00000008
	createNewProcessGroup  = 0x00000200
	createBreakawayFromJob = 0x01000000
)

// launchDetached starts the host outside the caller's console and process
// group, and outside the caller's job object when that job allows breakaway:
// an application's kill-on-close job must not take the shared host with it.
// It does not wait; `start` observes readiness through the resolver.
func launchDetached(image string, args ...string) error {
	flags := uint32(detachedProcess | createNewProcessGroup | windows.CREATE_NO_WINDOW)
	err := startDetached(image, args, flags|createBreakawayFromJob)
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		err = startDetached(image, args, flags)
	}
	if err != nil {
		return fmt.Errorf("activation refused: launch %s %v: %w", image, args, err)
	}
	return nil
}

func startDetached(image string, args []string, flags uint32) error {
	cmd := exec.Command(image, args...)
	cmd.Dir = filepath.Dir(image)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: flags}
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
