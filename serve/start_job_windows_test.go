package main

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// A runtime started by an application whose kill-on-close job forbids
// breakaway outlives that application (Codex closed and took the runtime with
// it, 2026-11-22). The starter runs launchDetached inside such a job; closing
// the job kills the starter, and the launched stand-in keeps running.
func TestLaunchDetachedOutlivesStarterKillOnCloseJob(t *testing.T) {
	if err := shellIsThisAccount(new(shellFrame)); errors.Is(err, errNoShell) {
		t.Skip("no unelevated desktop shell of this account in this session:", err)
	}
	marker := filepath.Join(t.TempDir(), "runtime-standin")

	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	jobOpen := true
	defer func() {
		if jobOpen {
			windows.CloseHandle(job)
		}
	}()
	var limits windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation, uintptr(unsafe.Pointer(&limits)), uint32(unsafe.Sizeof(limits))); err != nil {
		t.Fatal(err)
	}

	starter := exec.Command(os.Args[0], "-test.run=^TestRuntimeStarterHelperProcess$", marker)
	starter.Env = append(os.Environ(), "OA_START_HELPER=starter")
	release, err := starter.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	output, err := starter.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := starter.Start(); err != nil {
		t.Fatal(err)
	}
	defer starter.Process.Kill()
	process, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(starter.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	err = windows.AssignProcessToJobObject(job, process)
	windows.CloseHandle(process)
	if err != nil {
		t.Fatal(err)
	}
	// The starter calls launchDetached only once it is inside the job.
	if _, err := release.Write([]byte{'\n'}); err != nil {
		t.Fatal(err)
	}

	lines := bufio.NewScanner(output)
	var report []string
	for lines.Scan() {
		report = append(report, lines.Text())
		if lines.Text() == "launched" || strings.HasPrefix(lines.Text(), "error ") {
			break
		}
	}
	if !contains(report, "breakaway denied") || !contains(report, "launched") {
		t.Fatalf("the starter did not launch from a job that forbids breakaway: %q", report)
	}

	pid := readStandInPID(t, marker)
	standIn, err := windows.OpenProcess(windows.SYNCHRONIZE|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE, false, pid)
	if err != nil {
		t.Fatal("the launched stand-in is gone before the starter closed:", err)
	}
	defer func() {
		windows.TerminateProcess(standIn, 0)
		windows.CloseHandle(standIn)
	}()

	// The starter closes: its kill-on-close job ends every process in it.
	windows.CloseHandle(job)
	jobOpen = false
	starter.Wait()
	if event, _ := windows.WaitForSingleObject(standIn, 2000); event == windows.WAIT_OBJECT_0 {
		t.Fatal("the runtime stand-in died with its starter's job")
	}
}

func readStandInPID(t *testing.T, marker string) uint32 {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for {
		data, err := os.ReadFile(marker)
		if err == nil && len(data) > 0 {
			pid, err := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 32)
			if err != nil {
				t.Fatal(err)
			}
			return uint32(pid)
		}
		if time.Now().After(deadline) {
			t.Fatal("the launched stand-in never started")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestRuntimeStarterHelperProcess is the application that starts the runtime:
// once inside its job it runs the production launch and reports whether the
// job refused breakaway.
func TestRuntimeStarterHelperProcess(t *testing.T) {
	if os.Getenv("OA_START_HELPER") != "starter" || len(os.Args) < 3 {
		return
	}
	marker := os.Args[len(os.Args)-1]
	bufio.NewReader(os.Stdin).ReadString('\n')
	start := func(image string, args []string, flags uint32) error {
		err := startDetached(image, args, flags)
		if flags&createBreakawayFromJob != 0 && errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			os.Stdout.WriteString("breakaway denied\n")
		}
		return err
	}
	if err := launchOutsideCallerJob(os.Args[0], []string{runtimeStandInArg, marker}, start, launchThroughShell, os.Stderr); err != nil {
		os.Stdout.WriteString("error " + err.Error() + "\n")
		os.Exit(1)
	}
	os.Stdout.WriteString("launched\n")
	time.Sleep(time.Minute)
	os.Exit(0)
}

const runtimeStandInArg = "runtime-standin"

// runtimeStandIn stands in for `serve supervisor`; TestMain runs it before the
// test person folders, since the desktop shell starts it with the shell's own
// environment.
func runtimeStandIn(marker string) {
	os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600)
	time.Sleep(time.Minute)
	os.Exit(0)
}
