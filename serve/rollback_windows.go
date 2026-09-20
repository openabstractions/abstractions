package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A failed upgrade rolls the files and registration of the previous version
// back. The rollback action releases the upgrade exclusion and runs the
// previous version's own activation, so its runtime answers again before the
// next sign-in. Every activation is idempotent: it is correct whether or not
// the previous runtime was running, and whether or not Restart Manager
// restarts it as well. Nothing records what was stopped.

const userRestartBudget = 30 * time.Second

type activationRunner func(image string, args []string) error

// userActivation is the command a related per-user product's Startup
// activation runs. A product that ships openabstractionsw.exe (0.1.8 and
// later, and any local build of this layout whatever its version) is started
// with `openabstractionsw.exe start`. Earlier products started jobdw.exe:
// 0.1.5 and before with "start", 0.1.6 and 0.1.7 with "start --runtime".
func userActivation(root, version string, exists func(string) bool) (string, []string, error) {
	if twin := filepath.Join(root, "tools", windowlessName); exists(twin) {
		return twin, []string{"start"}, nil
	}
	parts := strings.Split(version, ".")
	if len(parts) < 3 {
		return "", nil, fmt.Errorf("related product version %q is not MAJOR.MINOR.PATCH", version)
	}
	var numbers [3]int
	for i := range numbers {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return "", nil, fmt.Errorf("related product version %q is not MAJOR.MINOR.PATCH", version)
		}
		numbers[i] = n
	}
	image := filepath.Join(root, "tools", "jobdw.exe")
	if numbers[0] == 0 && (numbers[1] == 0 || (numbers[1] == 1 && numbers[2] <= 5)) {
		return image, []string{"start"}, nil
	}
	return image, []string{"start", "--runtime"}, nil
}

// restartUserPredecessors runs every related product's activation and reports
// each product it cannot start. A failure to start one product does not skip
// the others.
func restartUserPredecessors(list string, info productInfoFunc, exists func(string) bool, run activationRunner) (int, []string, error) {
	products, err := relatedProducts(list, info)
	if err != nil {
		return 0, nil, err
	}
	var notes []string
	var failures []error
	started := 0
	for _, p := range products {
		if p.location == "" {
			notes = append(notes, fmt.Sprintf("related product %s %s records no install location; its runtime was not started", p.code, p.version))
			continue
		}
		root, err := userInstallFolder(p.location)
		if err != nil {
			failures = append(failures, fmt.Errorf("related product %s: %w", p.code, err))
			continue
		}
		image, args, err := userActivation(root, p.version, exists)
		if err != nil {
			failures = append(failures, fmt.Errorf("related product %s: %w", p.code, err))
			continue
		}
		if !exists(image) {
			notes = append(notes, fmt.Sprintf("related product %s %s has no %s; its runtime was not started", p.code, p.version, image))
			continue
		}
		if err := run(image, args); err != nil {
			failures = append(failures, fmt.Errorf("start related product %s %s with %s %s: %w", p.code, p.version, image, strings.Join(args, " "), err))
			continue
		}
		started++
	}
	return started, notes, errors.Join(failures...)
}

// serviceStartUser releases the user upgrade exclusion, then runs each related
// product's activation. A note that cannot be written is joined into the
// result and the activation still runs.
func serviceStartUser(related string, output io.Writer) error {
	var result error
	for _, note := range systemExclusionEnv().release("user") {
		result = errors.Join(result, printLine(output, note))
	}
	started, notes, err := restartUserPredecessors(related, msiProductInfo, regularFile, runActivation)
	for _, note := range notes {
		result = errors.Join(result, printLine(output, note))
	}
	result = errors.Join(result, printLine(output, fmt.Sprintf("started the runtime of %d related per-user product(s)", started)))
	return errors.Join(err, result)
}

// instanceControl is what the machine rollback asks of the SCM.
type instanceControl struct {
	list  func() ([]string, error)
	state func(name string) (uint32, error)
	start func(name string) error
}

// startStoppedInstances starts every per-session host instance that is not
// running. Instances exist only for signed-in sessions; the SCM recovery
// policy already restarts one that fails, so this covers an instance the
// transaction left stopped.
func startStoppedInstances(control instanceControl) (int, []string, error) {
	names, err := control.list()
	if err != nil {
		return 0, nil, fmt.Errorf("enumerate host service instances: %w", err)
	}
	var notes []string
	var failures []error
	started := 0
	for _, name := range names {
		state, err := control.state(name)
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			notes = append(notes, fmt.Sprintf("host service instance %s ended before it could be started", name))
			continue
		}
		if err != nil {
			failures = append(failures, fmt.Errorf("query host service instance %s: %w", name, err))
			continue
		}
		if state != windows.SERVICE_STOPPED {
			continue
		}
		err = control.start(name)
		switch {
		case err == nil, errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING):
			started++
		case errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST):
			notes = append(notes, fmt.Sprintf("host service instance %s ended before it could be started", name))
		default:
			failures = append(failures, fmt.Errorf("start host service instance %s: %w", name, err))
		}
	}
	return started, notes, errors.Join(failures...)
}

// serviceStartMachine releases the machine upgrade exclusion and starts the
// stopped per-session instances of the restored registration.
func serviceStartMachine(output io.Writer) error {
	var result error
	for _, note := range systemExclusionEnv().release("machine") {
		result = errors.Join(result, printLine(output, note))
	}
	m, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT|windows.SC_MANAGER_ENUMERATE_SERVICE)
	if err != nil {
		return errors.Join(fmt.Errorf("open the service manager: %w", err), result)
	}
	defer windows.CloseServiceHandle(m)
	started, notes, err := startStoppedInstances(instanceControl{
		list:  func() ([]string, error) { return instances(m, hostServiceName) },
		state: func(name string) (uint32, error) { return serviceState(m, name) },
		start: func(name string) error { return startService(m, name) },
	})
	for _, note := range notes {
		result = errors.Join(result, printLine(output, note))
	}
	result = errors.Join(result, printLine(output, fmt.Sprintf("started %d stopped host service instance(s)", started)))
	return errors.Join(err, result)
}

func serviceState(m windows.Handle, name string) (uint32, error) {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return 0, err
	}
	h, err := windows.OpenService(m, p, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return 0, err
	}
	defer windows.CloseServiceHandle(h)
	var status windows.SERVICE_STATUS_PROCESS
	var needed uint32
	if err := windows.QueryServiceStatusEx(h, windows.SC_STATUS_PROCESS_INFO, (*byte)(unsafe.Pointer(&status)), uint32(unsafe.Sizeof(status)), &needed); err != nil {
		return 0, err
	}
	return status.CurrentState, nil
}

func startService(m windows.Handle, name string) error {
	p, err := windows.UTF16PtrFromString(name)
	if err != nil {
		return err
	}
	h, err := windows.OpenService(m, p, windows.SERVICE_START)
	if err != nil {
		return err
	}
	defer windows.CloseServiceHandle(h)
	return windows.StartService(h, 0, nil)
}

func printLine(output io.Writer, line string) error {
	_, err := fmt.Fprintln(output, line)
	return err
}

func regularFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular()
}

// runActivation waits for the activation command, which starts a detached host
// and exits. No output pipes are inherited, so the wait ends with the command
// rather than with the host it started.
func runActivation(image string, args []string) error {
	ctx, cancel := context.WithTimeout(context.Background(), userRestartBudget)
	defer cancel()
	command := exec.CommandContext(ctx, image, args...)
	command.Dir = filepath.Dir(image)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	return command.Run()
}
