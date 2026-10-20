package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

func sendSessionMessage(w *sessionWindow, message, wp uintptr) (uintptr, error) {
	// These owned test windows can be descheduled before GetMessage. Keep the
	// fixed deadline without asking Windows to abort on its hang heuristic.
	const smtoBlock = 0x0001
	var result uintptr
	ok, _, err := user32.NewProc("SendMessageTimeoutW").Call(w.handle, message, wp, 1,
		smtoBlock, 3000, uintptr(unsafe.Pointer(&result)))
	if ok == 0 {
		if errors.Is(err, syscall.Errno(0)) {
			return 0, fmt.Errorf("session message %#x failed without a Windows error", message)
		}
		return 0, fmt.Errorf("session message %#x: %w", message, err)
	}
	return result, nil
}

func TestSessionMessagesOnlyStopConfirmedSessionAndAwaitCompletion(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	completed := make(chan struct{})
	w, err := startSessionWindow(cancel, completed, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
	}()
	if result, err := sendSessionMessage(w, wmQueryEndSession, 0); err != nil || result != 1 {
		t.Fatalf("session query result = %d, error = %v", result, err)
	}
	if _, err := sendSessionMessage(w, wmEndSession, 0); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() != nil {
		t.Fatal("query or canceled session stopped application")
	}
	sent := make(chan error, 1)
	go func() {
		_, err := sendSessionMessage(w, wmEndSession, 1)
		sent <- err
	}()
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("confirmed end did not cancel")
	}
	select {
	case err := <-sent:
		t.Fatalf("end-session returned before completion: %v", err)
	default:
	}
	close(completed)
	select {
	case err := <-sent:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("completed shutdown not acknowledged")
	}
}

func TestSessionShutdownWaitIsBounded(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := startSessionWindow(cancel, make(chan struct{}), 30*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sendSessionMessage(w, wmEndSession, 1); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil {
		t.Fatal("timeout path did not cancel")
	}
	if err := w.Close(); !errors.Is(err, errSessionShutdownTimeout) {
		t.Fatalf("shutdown timeout=%v", err)
	}
}

func TestSessionWindowDestructionCancelsLifetime(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	w, err := startSessionWindow(cancel, make(chan struct{}), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := sendSessionMessage(w, wmDisposeSessionWindow, 0); err != nil {
		t.Fatal(err)
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("window termination left runtime lifetime active")
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionMessageLoopExitCancelsAndPreservesError(t *testing.T) {
	loopFailure := errors.New("injected GetMessage failure")
	for _, result := range []int32{0, -1} {
		t.Run(fmt.Sprint(result), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			release := make(chan struct{})
			w, err := startSessionWindowWithMessages(cancel, make(chan struct{}), time.Second,
				func(*windowMessage) (int32, error) { <-release; return result, loopFailure })
			if err != nil {
				t.Fatal(err)
			}
			close(release)
			select {
			case <-w.done:
			case <-time.After(time.Second):
				t.Fatal("message loop did not terminate")
			}
			if ctx.Err() == nil {
				t.Fatal("loop termination did not cancel runtime")
			}
			for i := 0; i < 2; i++ {
				err := w.Close()
				if result == -1 && !errors.Is(err, loopFailure) {
					t.Fatalf("loop error lost: %v", err)
				}
				if result == 0 && err != nil {
					t.Fatalf("normal loop exit: %v", err)
				}
			}
		})
	}
}

type rmUniqueProcess struct {
	PID     uint32
	Created windows.Filetime
}

func restartManagerShutdown(t *testing.T, handle windows.Handle, process rmUniqueProcess) error {
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		return err
	}
	if created != process.Created {
		return errors.New("stale process identity")
	}
	t.Helper()
	dll := windows.NewLazySystemDLL("rstrtmgr.dll")
	var session uint32
	var key [33]uint16
	call := func(name string, args ...uintptr) {
		t.Helper()
		code, _, _ := dll.NewProc(name).Call(args...)
		if code != 0 {
			t.Fatalf("%s: %d", name, code)
		}
	}
	call("RmStartSession", uintptr(unsafe.Pointer(&session)), 0, uintptr(unsafe.Pointer(&key[0])))
	defer dll.NewProc("RmEndSession").Call(uintptr(session))
	call("RmRegisterResources", uintptr(session), 0, 0, 1, uintptr(unsafe.Pointer(&process)), 0, 0)
	call("RmShutdown", uintptr(session), 0, 0)
	return nil
}

func TestRestartManagerTargetsExactInertProcessAndClosesContainedChild(t *testing.T) {
	directory := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestRestartManagerHelper$", "-test.timeout=50s")
	command.Env = append(os.Environ(), "OA_HOST_RM_HELPER="+directory)
	command.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: 0x08000000}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	exitedProcess := make(chan struct{})
	go func() { done <- command.Wait(); close(exitedProcess) }()
	t.Cleanup(func() {
		command.Process.Kill()
		select {
		case <-exitedProcess:
		case <-time.After(3 * time.Second):
		}
	})
	handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, uint32(command.Process.Pid))
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	var created, exited, kernel, user windows.Filetime
	if err := windows.GetProcessTimes(handle, &created, &exited, &kernel, &user); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(filepath.Join(directory, "ready")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("inert child readiness timeout")
		}
		time.Sleep(20 * time.Millisecond)
	}
	stale := created
	stale.HighDateTime--
	if err := restartManagerShutdown(t, handle, rmUniqueProcess{PID: uint32(command.Process.Pid), Created: stale}); err == nil {
		t.Fatal("stale target accepted")
	}
	if state, err := windows.WaitForSingleObject(handle, 0); err != nil || state != uint32(windows.WAIT_TIMEOUT) {
		t.Fatalf("stale identity affected live child: %v %v", state, err)
	}
	if err := restartManagerShutdown(t, handle, rmUniqueProcess{PID: uint32(command.Process.Pid), Created: created}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Restart Manager did not stop helper")
	}
	for _, name := range []string{"child-closed", "parent-closed"} {
		if _, err := os.Stat(filepath.Join(directory, name)); err != nil {
			t.Fatalf("missing graceful marker %s: %v", name, err)
		}
	}
}
