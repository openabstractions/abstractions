package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unsafe"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"golang.org/x/sys/windows"

	"github.com/openabstractions/abstraction-facade/go-core/childproc"
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

func supervisorForActivation(lookup func() (string, error)) (string, string, error) {
	name, err := lookup()
	if err == nil || err == os.ErrNotExist {
		return name, "", nil
	}
	// Bootstrap remains strict. Only its native SCM query refusal selects the
	// existing owned host; a denied context registry read still refuses.
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) && strings.HasPrefix(err.Error(), "current supervisor instance query: ") {
		return "", "per-user service lookup denied (Windows error 5)", nil
	}
	return "", "", err
}

func activateInstalled(ctx context.Context) error {
	name, reason, err := supervisorForActivation(currentSupervisor)
	if err != nil {
		return err
	}
	system, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	return activateWindows(ctx, windowsActivation{
		instance: name, sc: filepath.Join(system, "sc.exe"), fallbackReason: reason,
		run: startCommand, launch: launchDetached, executable: os.Executable, stat: os.Stat, refusal: refuseDuringUpgrade,
		profile: bootstrap.CurrentProfileView, shellLaunch: launchThroughShell,
		report: func(reason string) { fmt.Fprintln(os.Stderr, "start: "+reason+"; activating the owned per-user host") },
	})
}

// activateInstalledPresenter uses the installed selection and the existing
// account-bound desktop shell. The presenter's session singleton handles
// duplicate activation; a runtime-only package has no presenter to dispatch.
func activateInstalledPresenter(ctx context.Context, output io.Writer) error {
	selection, err := bootstrap.SelectInstalled(ctx)
	if err != nil {
		return fmt.Errorf("native presenter activation refused: %w", err)
	}
	if selection.EndpointFromEnvironment {
		return errors.New("native presenter activation refuses an endpoint override")
	}
	return activateWindowsPresenter(selection.Server.Program, os.Executable, os.Lstat, launchThroughShell, output)
}

func activateWindowsPresenter(selected string, executable func() (string, error), stat func(string) (os.FileInfo, error), launch func(string, string, ...string) error, output io.Writer) error {
	self, err := executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(self)
	server := filepath.Join(dir, "openabstractions.exe")
	if !samePrograms(selected, server) {
		return errors.New("native presenter activation refused: this executable differs from the selected installation")
	}
	panel := filepath.Join(dir, presenterImage)
	info, err := stat(panel)
	if errors.Is(err, os.ErrNotExist) {
		return printPresenterStatus(output, "native presenter absent from this installation")
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("native presenter activation refused: installed presenter is not a regular file")
	}
	if err := launch(panel, dir, presenterArguments); err != nil {
		return fmt.Errorf("native presenter delivery deferred: %w", err)
	}
	return printPresenterStatus(output, "native presenter dispatched to this account's desktop")
}

func printPresenterStatus(output io.Writer, line string) error {
	err := printLine(output, line)
	// A windowless installer child can have an invalid stdout handle.
	if errors.Is(err, windows.ERROR_INVALID_HANDLE) {
		return nil
	}
	return err
}

type windowsActivation struct {
	instance, sc   string
	fallbackReason string
	run            func(context.Context, string, ...string) error
	launch         func(image string, args ...string) error
	executable     func() (string, error)
	stat           func(string) (os.FileInfo, error)
	refusal        func() error
	// profile probes the caller's profile view before a per-user launch; nil
	// reads real.
	profile func() (bootstrap.ProfileView, error)
	// shellLaunch starts the host through this session's desktop shell for a
	// caller whose profile view is virtualized.
	shellLaunch func(image, dir string, args ...string) error
	report      func(string)
}

// activateWindows starts this session's host. A machine installation's
// per-session service instance is started through the SCM when this sign-in
// has one. Otherwise, per-user or in the session that installed for everyone,
// the windowless image beside this program is launched detached as
// `serve supervisor`. The upgrade exclusion refuses first, with exit status 3.
func activateWindows(ctx context.Context, a windowsActivation) error {
	twin, err := windowlessImage(a.executable, a.stat)
	if err != nil {
		return fmt.Errorf("start absent: %w", err)
	}
	if err := a.refusal(); err != nil {
		return err
	}
	reason := "User Manager context is absent"
	if a.fallbackReason != "" {
		reason = a.fallbackReason
	}
	if a.instance != "" {
		reason = "this session's per-user service is absent"
		err = a.run(ctx, a.sc, "query", a.instance)
		if err == nil {
			err = a.run(ctx, a.sc, "start", a.instance)
			var exit *exec.ExitError
			if errors.As(err, &exit) && exit.ExitCode() == 1056 {
				return nil
			}
			if !serviceAccessDenied(err) {
				return err
			}
			reason = "per-user service start denied (Windows error 5)"
		} else if serviceAccessDenied(err) {
			reason = "per-user service query denied (Windows error 5)"
		} else {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1060 {
				return err
			}
		}
	}
	if a.report != nil {
		a.report(reason)
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
			err := a.shellLaunch(twin, filepath.Dir(twin), "serve", "supervisor")
			if errors.Is(err, errNoShell) {
				return &exitError{code: exitVirtualizedProfile, err: fmt.Errorf("start: virtualized_caller_no_shell: this process sees package %s's private copy of AppData, and no desktop shell of this account can start the runtime: %w", view.Family, err)}
			}
			return err
		}
	}
	return a.launch(twin, "serve", "supervisor")
}

func serviceAccessDenied(err error) bool {
	var exit *exec.ExitError
	return errors.Is(err, windows.ERROR_ACCESS_DENIED) || (errors.As(err, &exit) && exit.ExitCode() == 5)
}

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
	flags := uint32(detachedProcess | createNewProcessGroup)
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
	cmd := childproc.Hidden(exec.Command(image, args...))
	cmd.Dir = filepath.Dir(image)
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= flags
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}
