package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

// `openabstractions host register|unregister` writes and removes the machine
// scope's per-user service template. Windows clones a SERVICE_USER_OWN_PROCESS
// template into every interactive session as <name>_<luid>, running as that
// person with no password stored, and the recovery policy restarts a failed
// host. Registration needs an administrator token once; the installer's
// machine-scope custom actions run these commands.

// SERVICE_USER_OWN_PROCESS. sc.exe offers it as `type= userown`; x/sys does not
// name it. VISION.md 2026-09-10 "Host S is unnecessary" measured the SCM
// accepting it for a third-party binary.
const serviceUserOwnProcess = 0x50

const hostUsage = `Usage: openabstractions host register|unregister

  register    write the per-user service template OpenAbstractionsSupervisor:
              image openabstractionsw.exe beside this program, "serve host
              --service", automatic start at each sign-in, restart after 3s,
              10s, then every 30s. Rewrites an existing registration.
  unregister  stop and delete every per-session instance and the template,
              confirming each process exit within one 60-second budget.

Both need an administrator token. A failure is also appended to
%ProgramData%\abstraction\upgrade-v1\installer-actions.txt, because Windows
Installer discards the output of the custom actions that run them.
`

func hostCommand(args []string, output, diagnostics io.Writer) error {
	if len(args) == 1 && isHelp(args[0]) {
		_, err := io.WriteString(output, hostUsage)
		return err
	}
	if len(args) != 1 || (args[0] != "register" && args[0] != "unregister") {
		io.WriteString(diagnostics, hostUsage)
		return &exitError{code: 2, err: errors.New("host: give register or unregister")}
	}
	registrar := systemHostRegistrar(output)
	var err error
	if args[0] == "register" {
		err = registrar.register()
	} else {
		err = registrar.unregister()
	}
	if err != nil {
		recordInstallerFailure(installerActionsPath, "machine", "host "+args[0], err, time.Now())
	}
	return err
}

type hostRegistrar struct {
	name, display string
	image         func() (string, error)
	manager       func(access uint32) (windows.Handle, error)
	output        io.Writer
	budget        time.Duration
}

func systemHostRegistrar(output io.Writer) hostRegistrar {
	return hostRegistrar{
		name:    hostServiceName,
		display: hostServiceDisplay,
		image:   func() (string, error) { return windowlessImage(os.Executable, os.Stat) },
		manager: func(access uint32) (windows.Handle, error) { return windows.OpenSCManager(nil, nil, access) },
		output:  output,
		budget:  time.Minute,
	}
}

// hostServiceCommand is the registered image path and arguments.
func hostServiceCommand(image string) string {
	return windows.EscapeArg(image) + " serve host --service"
}

// register refuses a missing windowless image before it opens the SCM, so the
// refusal is what an unprivileged run reports.
func (r hostRegistrar) register() error {
	exe, err := r.image()
	if err != nil {
		return err
	}
	m, err := r.manager(windows.SC_MANAGER_CONNECT | windows.SC_MANAGER_CREATE_SERVICE)
	if err != nil {
		return fmt.Errorf("registering the per-user service template needs an administrator token: %w", err)
	}
	defer windows.CloseServiceHandle(m)
	name, err := windows.UTF16PtrFromString(r.name)
	if err != nil {
		return err
	}
	display, err := windows.UTF16PtrFromString(r.display)
	if err != nil {
		return err
	}
	binary, err := windows.UTF16PtrFromString(hostServiceCommand(exe))
	if err != nil {
		return err
	}
	// No account and no password: a per-user service runs as whoever signs in.
	h, existed, err := registrationHandle(
		func(access uint32) (windows.Handle, error) {
			return windows.CreateService(m, name, display, access,
				serviceUserOwnProcess, windows.SERVICE_AUTO_START, windows.SERVICE_ERROR_NORMAL,
				binary, nil, nil, nil, nil, nil)
		},
		func(access uint32) (windows.Handle, error) { return windows.OpenService(m, name, access) },
	)
	if err != nil {
		return fmt.Errorf("registering %s: %w", r.name, err)
	}
	defer windows.CloseServiceHandle(h)
	// A repair or upgrade rewrites a registration an older package made, which
	// named jobdw.exe.
	if existed {
		if err := windows.ChangeServiceConfig(h, serviceUserOwnProcess, windows.SERVICE_AUTO_START,
			windows.SERVICE_ERROR_NORMAL, binary, nil, nil, nil, nil, nil, display); err != nil {
			return fmt.Errorf("rewriting %s, which was already registered: %w", r.name, err)
		}
	}
	if err := setRecovery(h, r.name); err != nil {
		return fmt.Errorf("recovery policy for %s: %w", r.name, err)
	}
	_, err = fmt.Fprintf(r.output, "registered %s -> %s\n", r.name, hostServiceCommand(exe))
	return err
}

// registrationHandle acquires the rights needed by both configuration and
// restart recovery, including on repair. ChangeServiceConfig2 requires
// SERVICE_START when the recovery actions contain SC_ACTION_RESTART:
// https://learn.microsoft.com/en-us/windows/win32/api/winsvc/nf-winsvc-changeserviceconfig2w
func registrationHandle(create, open func(uint32) (windows.Handle, error)) (windows.Handle, bool, error) {
	access := uint32(windows.SERVICE_CHANGE_CONFIG | windows.SERVICE_START)
	h, err := create(access)
	existed := errors.Is(err, windows.ERROR_SERVICE_EXISTS)
	if existed {
		h, err = open(access)
	}
	return h, existed, err
}

// setRecovery configures restart delays of 3s, 10s, then 30s. The SCM repeats
// the final action on later failures; the count resets after an hour without
// one. https://learn.microsoft.com/en-us/windows/win32/api/winsvc/ns-winsvc-service_failure_actionsw
func setRecovery(h windows.Handle, name string) error {
	s := &mgr.Service{Name: name, Handle: h}
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 3 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, uint32(time.Hour/time.Second)); err != nil {
		return err
	}
	// Without this the actions fire only when the process dies without
	// reporting SERVICE_STOPPED; a host that reports failure after Running is
	// the case recovery exists for.
	return s.SetRecoveryActionsOnNonCrashFailures(true)
}

// unregister stops and deletes every instance, then the template. Nothing
// registered is not a failure.
func (r hostRegistrar) unregister() error {
	ctx, cancel := context.WithTimeout(context.Background(), r.budget)
	defer cancel()
	m, err := r.manager(windows.SC_MANAGER_CONNECT | windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return fmt.Errorf("removing the per-user service template needs an administrator token: %w", err)
	}
	defer windows.CloseServiceHandle(m)
	gone, err := removeServices(ctx, r.name, func() ([]string, error) { return instances(m, r.name) }, func(ctx context.Context, name string) (bool, error) {
		return deleteService(ctx, m, name)
	})
	if err != nil {
		return err
	}
	if gone == 0 {
		_, err = fmt.Fprintf(r.output, "no %s registration was present\n", r.name)
		return err
	}
	_, err = fmt.Fprintf(r.output, "removed %d %s registration(s)\n", gone, r.name)
	return err
}

// removeServices removes every listed instance and then the template within
// the shared budget, and refuses an instance that appeared meanwhile.
func removeServices(ctx context.Context, template string, list func() ([]string, error), remove func(context.Context, string) (bool, error)) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	names, err := list()
	if err != nil {
		return 0, fmt.Errorf("cannot enumerate host service instances: %w", err)
	}
	gone := 0
	for _, name := range append(names, template) {
		if err := ctx.Err(); err != nil {
			return gone, err
		}
		removed, err := remove(ctx, name)
		if err != nil {
			return gone, fmt.Errorf("host service %s removal failed: %w", name, err)
		}
		if removed {
			gone++
		}
	}
	if err := ctx.Err(); err != nil {
		return gone, err
	}
	after, err := list()
	if err != nil {
		return gone, fmt.Errorf("cannot verify the host service instance set after removal: %w", err)
	}
	known := map[string]bool{}
	for _, name := range names {
		known[name] = true
	}
	for _, name := range after {
		if !known[name] {
			return gone, fmt.Errorf("new host service instance appeared during removal: %s", name)
		}
	}
	return gone, nil
}

// instances names the per-session clones <template>_<luid>. Deleting the
// template leaves them, so each is removed by name.
func instances(m windows.Handle, template string) ([]string, error) {
	all, err := (&mgr.Mgr{Handle: m}).ListServices()
	if err != nil {
		return nil, err
	}
	var out []string
	for _, n := range all {
		if strings.HasPrefix(n, template+"_") {
			out = append(out, n)
		}
	}
	return out, nil
}

func deleteService(ctx context.Context, m windows.Handle, name string) (bool, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return false, err
	}
	h, err := windows.OpenService(m, p, windows.SERVICE_STOP|windows.SERVICE_QUERY_STATUS|windows.DELETE)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return false, nil
		}
		return false, err
	}
	defer windows.CloseServiceHandle(h)
	return stopAndDeleteService(ctx, removalOps{
		query: func() (windows.SERVICE_STATUS_PROCESS, error) {
			var status windows.SERVICE_STATUS_PROCESS
			var needed uint32
			err := windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&status)), uint32(unsafe.Sizeof(status)), &needed)
			return status, err
		},
		stop: func() error {
			var status windows.SERVICE_STATUS
			return windows.ControlService(h, windows.SERVICE_CONTROL_STOP, &status)
		},
		pin: func(pid uint32) (removalProcess, error) {
			h, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
			if err != nil {
				return nil, err
			}
			return retainedServiceProcess{h}, nil
		},
		remove: func() error { return windows.DeleteService(h) },
	})
}

type removalProcess interface {
	exited() (bool, error)
	close() error
}

type retainedServiceProcess struct{ handle windows.Handle }

func (p retainedServiceProcess) close() error { return windows.CloseHandle(p.handle) }
func (p retainedServiceProcess) exited() (bool, error) {
	state, err := windows.WaitForSingleObject(p.handle, 0)
	if err != nil {
		return false, err
	}
	switch state {
	case windows.WAIT_OBJECT_0:
		return true, nil
	case uint32(windows.WAIT_TIMEOUT):
		return false, nil
	default:
		return false, fmt.Errorf("unexpected process wait state %d", state)
	}
}

type removalOps struct {
	query func() (windows.SERVICE_STATUS_PROCESS, error)
	stop  func() error
	pin   func(uint32) (removalProcess, error)
	// remove, when set, deletes the service after its process exit is confirmed.
	remove func() error
}

// stopAndDeleteService pins the process the SCM names and requeries before
// requesting a stop, keeps that handle until both STOPPED and process exit are
// observed, and deletes nothing after the shared deadline.
func stopAndDeleteService(ctx context.Context, ops removalOps) (bool, error) {
	var process removalProcess
	var pid uint32
	defer func() {
		if process != nil {
			process.close()
		}
	}()
	stopSent := false
	observedActive := false
	for {
		if err := ctx.Err(); err != nil {
			return false, err
		}
		status, err := ops.query()
		if err != nil {
			return false, err
		}
		if err = ctx.Err(); err != nil {
			return false, err
		}
		if status.CurrentState == windows.SERVICE_STOPPED {
			if process == nil && observedActive {
				return false, errors.New("stopped host service has no retained process identity to confirm exit")
			}
			if process != nil {
				exited, err := process.exited()
				if err != nil {
					return false, err
				}
				if !exited {
					if err = waitRemoval(ctx); err != nil {
						return false, err
					}
					continue
				}
			}
			if err = ctx.Err(); err != nil {
				return false, err
			}
			if ops.remove != nil {
				if err = ops.remove(); err != nil {
					return false, err
				}
			}
			return true, nil
		}
		observedActive = true
		if status.ServiceType&windows.SERVICE_WIN32_OWN_PROCESS == 0 || status.ServiceType&windows.SERVICE_WIN32_SHARE_PROCESS != 0 {
			return false, errors.New("refusing process-exit assumptions for a shared or unsupported service type")
		}
		if process == nil {
			// The SCM does not guarantee a valid PID while start or stop is pending.
			if status.CurrentState == windows.SERVICE_START_PENDING || status.CurrentState == windows.SERVICE_STOP_PENDING {
				if err = waitRemoval(ctx); err != nil {
					return false, err
				}
				continue
			}
			if status.ProcessId == 0 {
				return false, errors.New("active host service has no process identity")
			}
			pid = status.ProcessId
			process, err = ops.pin(pid)
			if err != nil {
				return false, fmt.Errorf("retain host service process: %w", err)
			}
			confirmed, err := ops.query()
			if err != nil {
				return false, err
			}
			if confirmed.ProcessId != pid || confirmed.CurrentState == windows.SERVICE_STOPPED {
				return false, errors.New("host service process changed while retaining identity")
			}
			continue
		}
		if status.ProcessId != 0 && status.ProcessId != pid {
			return false, errors.New("host service process changed during removal")
		}
		if !stopSent && status.CurrentState != windows.SERVICE_STOP_PENDING {
			if err = ctx.Err(); err != nil {
				return false, err
			}
			err = ops.stop()
			if err != nil && !errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
				return false, fmt.Errorf("stop host service: %w", err)
			}
			stopSent = true
		}
		if err = waitRemoval(ctx); err != nil {
			return false, err
		}
	}
}

func waitRemoval(ctx context.Context) error {
	timer := time.NewTimer(25 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
