package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/serve/internal/runtimehost"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
)

const (
	hostTestStartupTimeout   = 10 * time.Second
	hostTestShutdownTimeout  = 3 * time.Second
	hostTestForceTimeout     = 2 * time.Second
	hostTestFilePollInterval = 20 * time.Millisecond
)

// TestMain doubles as the supervised runtime child the host tests launch.
func TestMain(m *testing.M) {
	if len(os.Args) == 6 && os.Args[1] == "serve" && os.Args[2] == "runtime" && os.Args[3] == "--supervised" {
		switch os.Args[4] {
		case "rm-test-child":
			fmt.Print("READY 1\n")
			if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
				os.Exit(3)
			}
			if err := os.WriteFile(os.Args[5], []byte("closed on EOF"), 0o600); err != nil {
				os.Exit(4)
			}
			os.Exit(0)
		case "restart-child":
			if err := os.WriteFile(filepath.Join(os.Args[5], fmt.Sprintf("child-%d", os.Getpid())), nil, 0o600); err != nil {
				os.Exit(4)
			}
			fmt.Print("READY 1\n")
			io.Copy(io.Discard, os.Stdin)
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func helperChildPlan(t *testing.T, mode, argument string, onReady func(runtimeChild)) hostPlan {
	return hostPlan{
		start: func(ctx context.Context) (runtimeChild, error) {
			h, err := runtimehost.Start(ctx, runtimehost.Options{Executable: os.Args[0], Args: []string{mode, argument}, StartupTimeout: hostTestStartupTimeout, ShutdownTimeout: hostTestShutdownTimeout, ForceTimeout: hostTestForceTimeout})
			if err != nil {
				return nil, err
			}
			if onReady != nil {
				onReady(h)
			}
			return h, nil
		},
	}
}

// TestHostRestartHelper is the host process of TestHostReplacesAKilledChild.
func TestHostRestartHelper(t *testing.T) {
	directory := os.Getenv("OA_HOST_RESTART_HELPER")
	if directory == "" {
		return
	}
	log := openHostLogAt(filepath.Join(directory, "host.log"))
	defer log.Close()
	plan := helperChildPlan(t, "restart-child", directory, nil)
	plan.logf = log.logf
	if err := runUserHost(context.Background(), plan, registerForRestart, log); err != nil {
		t.Fatal(err)
	}
}

func waitForFile(t *testing.T, directory string, within time.Duration, accept func(name string) bool) string {
	t.Helper()
	deadline := time.Now().Add(within)
	for {
		entries, _ := os.ReadDir(directory)
		for _, entry := range entries {
			if accept(entry.Name()) {
				return entry.Name()
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no matching file in %s within %s", directory, within)
		}
		time.Sleep(hostTestFilePollInterval)
	}
}

// A real host process registers for restart before its child is ready, and a
// killed child is replaced after the first backoff delay while the host
// process stays the same.
func TestHostReplacesAKilledChildAndRegistersForRestart(t *testing.T) {
	directory := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestHostRestartHelper$", "-test.timeout=60s")
	command.Env = append(os.Environ(), "OA_HOST_RESTART_HELPER="+directory)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: windows.CREATE_NO_WINDOW}
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	hostPID := command.Process.Pid
	exited := make(chan struct{})
	go func() { command.Wait(); close(exited) }()
	t.Cleanup(func() {
		command.Process.Kill()
		select {
		case <-exited:
		case <-time.After(5 * time.Second):
		}
	})
	first := waitForFile(t, directory, 15*time.Second, func(name string) bool { return strings.HasPrefix(name, "child-") })
	firstPID, _ := strconv.Atoi(strings.TrimPrefix(first, "child-"))

	host, err := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ|windows.SYNCHRONIZE, false, uint32(hostPID))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(host)
	line, flags, err := restartSettings(host)
	if err != nil {
		t.Fatal(err)
	}
	if line != hostRestartCommand || flags != restartNoCrash|restartNoHang|restartNoReboot {
		t.Fatalf("registered restart %q flags %#x", line, flags)
	}

	child, err := windows.OpenProcess(windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, uint32(firstPID))
	if err != nil {
		t.Fatal(err)
	}
	killed := time.Now()
	if err := windows.TerminateProcess(child, 9); err != nil {
		t.Fatal(err)
	}
	windows.CloseHandle(child)
	replacementDeadline := hostTestForceTimeout + hostRestartDelays[0] + hostTestStartupTimeout + hostTestFilePollInterval
	second := waitForFile(t, directory, replacementDeadline, func(name string) bool { return strings.HasPrefix(name, "child-") && name != first })
	replaced := time.Since(killed)
	if state, err := windows.WaitForSingleObject(host, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("host process %d ended when its child was killed", hostPID)
	}
	t.Logf("child %s replaced by %s after %s; host pid %d unchanged", first, second, replaced, hostPID)
	log, _ := os.ReadFile(filepath.Join(directory, "host.log"))
	if !bytes.Contains(log, []byte("restart 1 of 3 in 2s")) {
		t.Fatalf("host log does not record the restart:\n%s", log)
	}
	if replaced < hostRestartDelays[0] || replaced > replacementDeadline {
		t.Fatalf("replacement after %s, want the %s backoff within the %s cleanup, startup and observation bound", replaced, hostRestartDelays[0], replacementDeadline)
	}
}

// TestRestartManagerHelper is the host process a real RmShutdown ends.
func TestRestartManagerHelper(t *testing.T) {
	directory := os.Getenv("OA_HOST_RM_HELPER")
	if directory == "" {
		return
	}
	plan := helperChildPlan(t, "rm-test-child", filepath.Join(directory, "child-closed"), func(runtimeChild) {
		os.WriteFile(filepath.Join(directory, "ready"), nil, 0o600)
	})
	if err := runUserHost(context.Background(), plan, registerForRestart, &hostLog{}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(directory, "child-closed")); err != nil {
		t.Fatalf("contained child did not close normally: %v", err)
	}
	os.WriteFile(filepath.Join(directory, "parent-closed"), []byte("graceful"), 0o600)
}

func TestHostServiceReportsComponentFailureToTheSCM(t *testing.T) {
	statuses := make(chan svc.Status, 4)
	failure := errors.New("runtime failed 4 times in a row")
	specific, code := hostService{run: func(context.Context) error { return failure }}.Execute([]string{hostServiceName}, make(chan svc.ChangeRequest), statuses)
	close(statuses)
	var states []svc.State
	for s := range statuses {
		states = append(states, s.State)
	}
	if !specific || code == 0 || !reflect.DeepEqual(states, []svc.State{svc.StartPending, svc.Running}) {
		t.Fatalf("specific=%v code=%d states=%v", specific, code, states)
	}
}

func TestHostServiceRefusesDuringUpgradeAfterRunning(t *testing.T) {
	statuses := make(chan svc.Status, 4)
	ran := false
	h := hostService{guard: func() error { return errors.New("an upgrade of this installation is in progress") },
		run: func(context.Context) error { ran = true; return nil }}
	specific, code := h.Execute([]string{hostServiceName}, make(chan svc.ChangeRequest), statuses)
	close(statuses)
	var states []svc.State
	for s := range statuses {
		states = append(states, s.State)
	}
	if !specific || code != 1 || ran || !reflect.DeepEqual(states, []svc.State{svc.StartPending, svc.Running}) {
		t.Fatalf("specific=%v code=%d ran=%v states=%v", specific, code, ran, states)
	}
}

func TestHostServiceAnswersStopAndInterrogate(t *testing.T) {
	requests := make(chan svc.ChangeRequest)
	statuses := make(chan svc.Status)
	stopped := make(chan struct{})
	ended := make(chan [2]any, 1)
	h := hostService{run: func(ctx context.Context) error { <-ctx.Done(); close(stopped); return nil }}
	go func() {
		specific, code := h.Execute([]string{hostServiceName}, requests, statuses)
		ended <- [2]any{specific, code}
		close(statuses)
	}()
	if (<-statuses).State != svc.StartPending || (<-statuses).State != svc.Running {
		t.Fatal("startup statuses")
	}
	want := svc.Status{State: svc.Running, Accepts: svc.AcceptStop, CheckPoint: 7}
	requests <- svc.ChangeRequest{Cmd: svc.Interrogate, CurrentStatus: want}
	if got := <-statuses; got != want {
		t.Fatalf("interrogate answered %+v", got)
	}
	requests <- svc.ChangeRequest{Cmd: svc.Stop}
	if got := (<-statuses).State; got != svc.StopPending {
		t.Fatalf("status after stop %v", got)
	}
	select {
	case result := <-ended:
		if result[0].(bool) || result[1].(uint32) != 0 {
			t.Fatalf("stop result %v", result)
		}
	case <-time.After(2 * hostStopWait):
		t.Fatal("stop did not end Execute")
	}
	<-stopped
	for range statuses {
	}
}

func TestHostStartupDiagnosticsRecordTokenElevation(t *testing.T) {
	line := hostStartupDiagnostics("service")
	elevated := windows.GetCurrentProcessToken().IsElevated()
	for _, want := range []string{"mode=service", fmt.Sprintf("pid=%d", os.Getpid()), fmt.Sprintf("TokenElevation=%v", elevated), "TokenElevationType="} {
		if !strings.Contains(line, want) {
			t.Fatalf("diagnostics %q lack %q", line, want)
		}
	}
	if strings.Contains(line, "TokenElevationType=unknown") {
		t.Fatalf("elevation type unread: %q", line)
	}
}

func TestHostLogKeepsOneGeneration(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host", "host.log")
	log := openHostLogAt(path)
	log.logf("first")
	log.Close()
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), hostLogLimit+1), 0o600); err != nil {
		t.Fatal(err)
	}
	log = openHostLogAt(path)
	log.logf("after rotation")
	log.Close()
	current, _ := os.ReadFile(path)
	previous, err := os.Stat(path + ".1")
	if err != nil || previous.Size() != hostLogLimit+1 || !bytes.Contains(current, []byte("after rotation")) || len(current) > 200 {
		t.Fatalf("current=%q previous=%v err=%v", current, previous, err)
	}
}

// The windowless link has no standard handles; its output goes to the host log.
func TestWindowlessOutputGoesToTheHostLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "host", "host.log")
	if file := outputWithoutConsole(os.Stdout, path); file != nil {
		file.Close()
		t.Fatal("a working stdout was redirected")
	}
	closed, err := os.CreateTemp(t.TempDir(), "stdout")
	if err != nil {
		t.Fatal(err)
	}
	closed.Close()
	file := outputWithoutConsole(closed, path)
	if file == nil {
		t.Fatal("an unusable stdout was not redirected")
	}
	fmt.Fprintln(file, "runtime ready")
	file.Close()
	if data, _ := os.ReadFile(path); string(data) != "runtime ready\n" {
		t.Fatalf("host log %q", data)
	}
}

func TestWindowlessImageIsRequired(t *testing.T) {
	root := t.TempDir()
	self := func() (string, error) { return filepath.Join(root, "openabstractions.exe"), nil }
	if _, err := windowlessImage(self, os.Stat); err == nil || !strings.Contains(err.Error(), windowlessName) {
		t.Fatalf("missing twin accepted: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, windowlessName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := windowlessImage(self, os.Stat); err == nil {
		t.Fatal("a directory accepted as the windowless image")
	}
	other := t.TempDir()
	twin := filepath.Join(other, windowlessName)
	os.WriteFile(twin, nil, 0o600)
	got, err := windowlessImage(func() (string, error) { return filepath.Join(other, "openabstractions.exe"), nil }, os.Stat)
	if err != nil || got != twin {
		t.Fatalf("twin %q err=%v", got, err)
	}
	if line := hostServiceCommand(`C:\Program Files\OpenAbstractions\tools\openabstractionsw.exe`); line != `"C:\Program Files\OpenAbstractions\tools\openabstractionsw.exe" serve host --service` {
		t.Fatalf("registered command %q", line)
	}
}
