package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	"github.com/openabstractions/abstractions/serve/internal/runtimehost"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

// `openabstractions serve host` owns the Windows runtime lifetime in both
// install scopes. Per-user, a Startup shortcut and `openabstractions start` run
// it; it holds a hidden session window for Restart Manager shutdown and
// registers for restart, so an upgrade that ends it gets it back. Machine
// scope registers `openabstractionsw.exe serve host --service` as the
// per-user service template; the SCM restarts the host and the host restarts
// its runtime child. The child is `openabstractions.exe serve runtime
// --supervised` in a kill-on-close job object.

const (
	hostServiceName    = "OpenAbstractionsSupervisor"
	hostServiceDisplay = "Abstraction supervisor"
	windowlessName     = "openabstractionsw.exe"

	// The SCM kills a service that has not answered a stop within its wait
	// hint. The child's graceful and forced budgets fit inside it.
	hostStopWait = 10 * time.Second

	restartNoCrash  = 0x1
	restartNoHang   = 0x2
	restartNoReboot = 0x8
	// hostRestartFlags restarts the host only after an update ended it.
	hostRestartFlags = restartNoCrash | restartNoHang | restartNoReboot
	// hostRestartCommand is what Windows appends to the image path.
	hostRestartCommand = "serve host"
)

var (
	kernel32                       = windows.NewLazySystemDLL("kernel32.dll")
	registerApplicationRestart     = kernel32.NewProc("RegisterApplicationRestart")
	getApplicationRestartSettings  = kernel32.NewProc("GetApplicationRestartSettings")
	unregisterApplicationRestartFn = kernel32.NewProc("UnregisterApplicationRestart")
)

func serveHost(args []string) error {
	flags := flag.NewFlagSet("host", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	service := flags.Bool("service", false, "run under the service control manager; the registered per-user service template passes this")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), `Usage: openabstractions serve host [--service]

Hosts the installed runtime for this user: starts "openabstractions.exe serve
runtime --supervised" beside this program, restarts it after failure (2s, 10s,
30s, then exits with failure) and ends it at sign-out or when Windows Installer
replaces the installation. Refuses with exit status 3 while an upgrade of this
installation is in progress, and with exit status 4 when this process sees a
packaged app's private copy of AppData. Diagnostics go to
%LOCALAPPDATA%\openabstractions\host\host.log.`)
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return &exitError{code: 2, err: errors.New("serve host takes no arguments")}
	}
	if !*service {
		// Refused before the host log is opened, so a contained host writes nothing.
		if err := refuseVirtualizedProfile("serve host", bootstrap.CurrentProfileView); err != nil {
			return err
		}
	}
	diagnostics := openHostLog()
	defer diagnostics.Close()
	mode := "user"
	if *service {
		mode = "service"
	}
	diagnostics.logf("%s", hostStartupDiagnostics(mode))
	if *service {
		return runHostService(diagnostics)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return runUserHost(ctx, installedHostPlan(diagnostics), registerForRestart, diagnostics)
}

// runUserHost refuses during an upgrade, registers for restart before the
// runtime is ready, and supervises until the session or a Restart Manager
// shutdown ends it.
func runUserHost(ctx context.Context, plan hostPlan, register func(string, uint32) error, diagnostics *hostLog) error {
	if plan.guard != nil {
		if err := plan.guard(); err != nil {
			diagnostics.logf("refused: %v", err)
			return err
		}
	}
	return withSessionShutdown(ctx, func(ctx context.Context) error {
		if err := register(hostRestartCommand, hostRestartFlags); err != nil {
			diagnostics.logf("restart registration failed: %v", err)
			return fmt.Errorf("serve host: register for restart: %w", err)
		}
		err := plan.run(ctx)
		if err != nil {
			diagnostics.logf("host ends with failure: %v", err)
		} else {
			diagnostics.logf("host stopped")
		}
		return err
	})
}

func installedHostPlan(diagnostics *hostLog) hostPlan {
	return hostPlan{
		start:  startInstalledRuntime,
		served: runtimeServed,
		guard:  hostUpgradeGuard,
		logf:   diagnostics.logf,
	}
}

func startInstalledRuntime(ctx context.Context) (runtimeChild, error) {
	return runtimehost.Start(ctx, runtimehost.Options{StartupTimeout: 20 * time.Second, ShutdownTimeout: 5 * time.Second, ForceTimeout: 2 * time.Second})
}

// runtimeServed asks the default resolver endpoint whether anything answers.
// Any reply, a refusal included, means the endpoint already has its listener.
func runtimeServed(ctx context.Context) bool {
	endpoint, err := resolution.CheckedDefaultEndpoint()
	if err != nil {
		return false
	}
	probe, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	contracts := runtimeContracts()
	if len(contracts) == 0 {
		return false
	}
	_, err = resolution.NewClient(endpoint, time.Second).Resolve(probe, wire.ResolveRequest{Capability: contracts[0][0], Contracts: []string{contracts[0][1]}, Scope: wire.ScopeLocal})
	var service *wire.ServiceError
	return err == nil || errors.As(err, &service)
}

func registerForRestart(command string, flags uint32) error {
	line, err := windows.UTF16PtrFromString(command)
	if err != nil {
		return err
	}
	if err := registerApplicationRestart.Find(); err != nil {
		return err
	}
	hr, _, _ := registerApplicationRestart.Call(uintptr(unsafe.Pointer(line)), uintptr(flags))
	if hr != 0 {
		return fmt.Errorf("RegisterApplicationRestart: HRESULT %#x", uint32(hr))
	}
	return nil
}

func unregisterForRestart() error {
	hr, _, _ := unregisterApplicationRestartFn.Call()
	if hr != 0 {
		return fmt.Errorf("UnregisterApplicationRestart: HRESULT %#x", uint32(hr))
	}
	return nil
}

// restartSettings is GetApplicationRestartSettings for process: the registered
// command line and flags.
func restartSettings(process windows.Handle) (string, uint32, error) {
	buf := make([]uint16, 32768)
	size := uint32(len(buf))
	var flags uint32
	hr, _, _ := getApplicationRestartSettings.Call(uintptr(process), uintptr(unsafe.Pointer(&buf[0])), uintptr(unsafe.Pointer(&size)), uintptr(unsafe.Pointer(&flags)))
	if hr != 0 {
		return "", 0, fmt.Errorf("GetApplicationRestartSettings: HRESULT %#x", uint32(hr))
	}
	return windows.UTF16ToString(buf[:size]), flags, nil
}

// hostStartupDiagnostics names the process and its token. Whether an
// administrator's per-user service instance runs with a full or a filtered
// token is unmeasured; the machine qualification reads this line to answer it.
func hostStartupDiagnostics(mode string) string {
	image, err := os.Executable()
	if err != nil {
		image = "unknown (" + err.Error() + ")"
	}
	elevated, elevationType := tokenElevation(windows.GetCurrentProcessToken())
	return fmt.Sprintf("serve host start: mode=%s pid=%d image=%s TokenElevation=%s TokenElevationType=%s", mode, os.Getpid(), image, elevated, elevationType)
}

func tokenElevation(token windows.Token) (string, string) {
	var elevation, kind uint32
	var size uint32
	elevated := "unknown"
	if err := windows.GetTokenInformation(token, windows.TokenElevation, (*byte)(unsafe.Pointer(&elevation)), 4, &size); err == nil {
		elevated = fmt.Sprint(elevation != 0)
	} else {
		elevated += " (" + err.Error() + ")"
	}
	kindName := "unknown"
	if err := windows.GetTokenInformation(token, windows.TokenElevationType, (*byte)(unsafe.Pointer(&kind)), 4, &size); err == nil {
		switch kind {
		case 1:
			kindName = "default"
		case 2:
			kindName = "full"
		case 3:
			kindName = "limited"
		default:
			kindName = fmt.Sprint(kind)
		}
	} else {
		kindName += " (" + err.Error() + ")"
	}
	return elevated, kindName
}

// runHostService is what the SCM starts. A per-user service instance has
// ServicesPipeTimeout to reach the dispatcher; the dispatcher is never skipped.
func runHostService(diagnostics *hostLog) error {
	plan := installedHostPlan(diagnostics)
	err := svc.Run(hostServiceName, hostService{run: plan.run, guard: plan.guard, logf: diagnostics.logf})
	if errors.Is(err, windows.ERROR_FAILED_SERVICE_CONTROLLER_CONNECT) {
		return errors.New("serve host --service is how the service manager starts the host; run serve host without --service here")
	}
	return err
}

type hostService struct {
	run   func(context.Context) error
	guard func() error
	logf  func(string, ...any)
}

func (h hostService) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	logf := h.logf
	if logf == nil {
		logf = func(string, ...any) {}
	}
	s <- svc.Status{State: svc.StartPending}
	// A refusal after Running is a service failure, which SCM recovery retries
	// after the installer releases the exclusion. A start failure is not.
	if h.guard != nil {
		if err := h.guard(); err != nil {
			s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
			logf("refused: %v", err)
			return true, 1
		}
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- h.run(ctx) }()
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}
	for {
		select {
		case err := <-done:
			return hostServiceResult(err, logf)
		case c := <-r:
			switch c.Cmd {
			case svc.Interrogate:
				s <- c.CurrentStatus
			case svc.Stop, svc.Shutdown:
				s <- svc.Status{State: svc.StopPending, WaitHint: uint32(hostStopWait / time.Millisecond)}
				stop()
				select {
				case err := <-done:
					return hostServiceResult(err, logf)
				case <-time.After(hostStopWait):
					return false, 0
				}
			}
		}
	}
}

// hostServiceResult reports a host that ended while the service was meant to
// run as a service-specific error, so recovery restarts it.
func hostServiceResult(err error, logf func(string, ...any)) (bool, uint32) {
	if err != nil {
		logf("service ends with failure: %v", err)
		return true, 1
	}
	return false, 0
}

// windowlessImage is the image registered and launched detached. It is never
// the console image: a per-user service instance or a Startup shortcut given a
// console image opens a console window at every sign-in.
func windowlessImage(executable func() (string, error), stat func(string) (os.FileInfo, error)) (string, error) {
	exe, err := executable()
	if err != nil {
		return "", err
	}
	w := filepath.Join(filepath.Dir(exe), windowlessName)
	info, err := stat(w)
	if err != nil {
		return "", fmt.Errorf("%s is not beside %s: a console image registered or launched as the host opens a window at every sign-in, so it is refused: %w", windowlessName, filepath.Base(exe), err)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("%s beside %s is not a regular file", windowlessName, filepath.Base(exe))
	}
	return w, nil
}

// hostLog appends diagnostics for a host that may have no console.
type hostLog struct {
	file io.WriteCloser
}

const hostLogLimit = 1 << 20

func hostLogPath() (string, error) {
	base, err := windows.KnownFolderPath(windows.FOLDERID_LocalAppData, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "openabstractions", "host", "host.log"), nil
}

// openHostLog opens the diagnostics file, keeping one previous generation past
// hostLogLimit. A log that cannot be opened writes nowhere; diagnostics never
// decide whether the host runs.
func openHostLog() *hostLog {
	path, err := hostLogPath()
	if err != nil {
		return &hostLog{}
	}
	return openHostLogAt(path)
}

func openHostLogAt(path string) *hostLog {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return &hostLog{}
	}
	if info, err := os.Stat(path); err == nil && info.Size() > hostLogLimit {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return &hostLog{}
	}
	return &hostLog{file: f}
}

func (l *hostLog) logf(format string, args ...any) {
	if l == nil || l.file == nil {
		return
	}
	_, _ = fmt.Fprintf(l.file, "%s [%d] %s\n", time.Now().UTC().Format(time.RFC3339Nano), os.Getpid(), fmt.Sprintf(format, args...))
}

func (l *hostLog) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	return l.file.Close()
}
