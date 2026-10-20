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
	"testing"
	"time"

	"github.com/openabstractions/abstractions/serve/internal/runtimehost"
	"golang.org/x/sys/windows"
)

// The supervised owned-launch test (FAC-P5, backlog/APP-LAUNCH-JOB.md) runs
// this test binary in two more TestMain modes (host_windows_test.go):
//
//	serve runtime --supervised owned-launch-child <dir>
//	    the runtime the runtime host supervises: it performs the owned
//	    launch of the test app, writes "pid <n>" or "error <text>" to
//	    <dir>/launch, then reports READY 1 and runs until its stdin closes
//	serve runtime --supervised owned-app <dir>
//	    the owned test app: it writes its pid to <dir>/app-ready and sleeps
//	    until it is ended
//
// An installed build launched every owned app inside the service's job,
// which refused breakaway, and every Open failed with "Access is denied".

// ownedLaunchSupervisedChild is the owned-launch-child mode.
func ownedLaunchSupervisedChild(dir string) {
	report := func(line string) { _ = os.WriteFile(filepath.Join(dir, "launch"), []byte(line), 0o600) }
	self, err := os.Executable()
	if err != nil {
		report("error " + err.Error())
		os.Exit(4)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		report("error " + err.Error())
		os.Exit(4)
	}
	var session uint32
	if err := windows.ProcessIdToSessionId(uint32(os.Getpid()), &session); err != nil {
		report("error " + err.Error())
		os.Exit(4)
	}
	image := ownedApplicationImage{program: self, pinned: true, recheck: func(context.Context) error { return nil }, close: func() error { return nil }}
	binding := ownedApplicationBinding{application: "fixture", account: user.User.Sid.String(), desktop: "windows:" + strconv.FormatUint(uint64(session), 10),
		runtimeEpoch: "epoch", manifestRevision: "revision", imageCode: "code"}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	s, err := startOwnedApplication(ctx, image, binding, []string{"serve", "runtime", "--supervised", "owned-app", dir}, func(context.Context, ownedApplicationBinding) error { return nil })
	cancel()
	if err != nil {
		report("error " + err.Error())
	} else {
		report("pid " + strconv.Itoa(s.bindingValue().pid))
	}
	fmt.Print("READY 1\n")
	io.Copy(io.Discard, os.Stdin)
	os.Exit(0)
}

// ownedAppTestChild is the owned-app mode.
func ownedAppTestChild(dir string) {
	_, carried := os.LookupEnv("ABSTRACTION_OPENED_FOR")
	_ = os.WriteFile(filepath.Join(dir, "opened-for"), []byte(strconv.FormatBool(carried)), 0o600)
	_ = os.WriteFile(filepath.Join(dir, "app-ready"), []byte(strconv.Itoa(os.Getpid())), 0o600)
	for {
		time.Sleep(time.Hour)
	}
}

func waitTestFile(t *testing.T, path string) string {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); time.Sleep(hostTestFilePollInterval) {
		if data, err := os.ReadFile(path); err == nil && len(data) > 0 {
			return string(data)
		}
	}
	t.Fatalf("%s was never written", path)
	return ""
}

// A runtime supervised through the runtime host's own launch performs an
// owned launch; the app reaches ready and outlives the runtime's stop.
// requireOwnedLaunchBreakawayHere skips when this test process's own job
// refuses breakaway, as under a runner that holds its tests in such a job:
// the runtime host's job would then sit inside it, and the owned launch could
// not leave it.
func requireOwnedLaunchBreakawayHere(t *testing.T) {
	t.Helper()
	cmd := exec.Command(filepath.Join(os.Getenv("SystemRoot"), "System32", "whoami.exe"))
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_BREAKAWAY_FROM_JOB | windows.CREATE_SUSPENDED}
	if err := cmd.Start(); err != nil {
		t.Skipf("this test process cannot break away from its own job: %v", err)
	}
	//unchecked: the suspended probe is only ended
	_ = cmd.Process.Kill()
	//unchecked: its exit status is the kill's
	_ = cmd.Wait()
}

func TestSupervisedRuntimeOwnedLaunchOutlivesTheRuntime(t *testing.T) {
	requireOwnedLaunchBreakawayHere(t)
	dir := t.TempDir()
	host, err := runtimehost.Start(context.Background(), runtimehost.Options{Executable: os.Args[0], Args: []string{"owned-launch-child", dir},
		StartupTimeout: hostTestStartupTimeout, ShutdownTimeout: hostTestShutdownTimeout, ForceTimeout: hostTestForceTimeout})
	if err != nil {
		t.Fatal(err)
	}
	stopped := false
	t.Cleanup(func() {
		if !stopped {
			host.Close()
			host.Wait()
		}
	})
	launch := waitTestFile(t, filepath.Join(dir, "launch"))
	if detail, failed := strings.CutPrefix(launch, "error "); failed {
		if strings.Contains(detail, "virtualized") {
			t.Skipf("this test runs in a packaged app's virtualized profile, where no owned launch is allowed: %s", detail)
		}
		t.Fatalf("the supervised runtime's owned launch failed: %s", detail)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(waitTestFile(t, filepath.Join(dir, "app-ready"))))
	if err != nil || "pid "+strconv.Itoa(pid) != launch {
		t.Fatalf("the launched app %q reported %d: %v", launch, pid, err)
	}
	// The host learns nothing of who opened it: the runtime keeps that in its
	// own log (ruling 2026-11-12 point 3, application_owned_start_record.go).
	if carried := waitTestFile(t, filepath.Join(dir, "opened-for")); carried != "false" {
		t.Fatal("the owned host's environment names the app that opened it")
	}
	app, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.TerminateProcess(app, 1); windows.CloseHandle(app) })
	closeErr := host.Close()
	waitErr := host.Wait()
	stopped = true
	if closeErr != nil && !errors.Is(closeErr, runtimehost.ErrForced) {
		t.Fatalf("runtime stop: %v", closeErr)
	}
	_ = waitErr
	if event, err := windows.WaitForSingleObject(app, 500); err != nil || event != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("the owned app ended with the runtime: event %#x %v", event, err)
	}
}
