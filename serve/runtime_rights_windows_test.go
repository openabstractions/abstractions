package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"golang.org/x/sys/windows"
)

const (
	sharingTestPath    = "OA_SHARING_TEST_PATH"
	sharingTestReady   = "OA_SHARING_TEST_READY"
	sharingTestRelease = "OA_SHARING_TEST_RELEASE"
)

func windowsFileIdentity(handle syscall.Handle) (string, error) {
	var info syscall.ByHandleFileInformation
	if err := syscall.GetFileInformationByHandle(handle, &info); err != nil {
		return "", err
	}
	return fmt.Sprintf("volume=%08x file=%08x%08x", info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow), nil
}

// holdPolicyForSharingTest runs in a separate test process, matching the
// production contention between a policy writer and the runtime reader.
func holdPolicyForSharingTest(t *testing.T) bool {
	path := os.Getenv(sharingTestPath)
	if path == "" {
		return false
	}
	name, err := windows.UTF16PtrFromString(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(handle)
	if err := os.WriteFile(os.Getenv(sharingTestReady), []byte("ready"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		if _, err := os.Stat(os.Getenv(sharingTestRelease)); err == nil {
			return true
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if time.Now().After(deadline) {
			t.Fatal("sharing-test holder timed out")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A policy file another handle holds without sharing keeps answering a sharing
// violation, which the file store retries for many seconds. The in-process
// decision gives up at its budget and reads unavailable; once the handle closes
// the next decision reads the file again.
func TestADecisionThatCannotBeReadInTimeIsUnavailable(t *testing.T) {
	if holdPolicyForSharingTest(t) {
		return
	}
	r := testRights(t, t.TempDir())
	rules := []installationRule{{host.ConfigEditAction, host.ConfigEditResource}}
	if err := r.install(nil, rules); err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: r.owner, Program: r.operators[0]}
	if d := r.decide(context.Background(), subject, host.ConfigEditAction, host.ConfigEditResource); d.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatalf("readable policy: %+v", d)
	}
	fixture := t.TempDir()
	ready := filepath.Join(fixture, "ready")
	release := filepath.Join(fixture, "release")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestADecisionThatCannotBeReadInTimeIsUnavailable$")
	command.Env = append(os.Environ(), sharingTestPath+"="+r.path, sharingTestReady+"="+ready, sharingTestRelease+"="+release)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	var waitErr error
	go func() {
		waitErr = command.Wait()
		close(done)
	}()
	released := false
	releaseHolder := func() error {
		if !released {
			if err := os.WriteFile(release, []byte("release"), 0o600); err != nil {
				return err
			}
			released = true
		}
		return nil
	}
	t.Cleanup(func() {
		_ = releaseHolder()
		select {
		case <-done:
		case <-ctx.Done():
		}
	})
	readyDeadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		select {
		case <-done:
			t.Fatalf("sharing-test holder exited before ready: %v\n%s", waitErr, output.String())
		default:
		}
		if time.Now().After(readyDeadline) {
			cancel()
			<-done
			t.Fatalf("sharing-test holder did not become ready\n%s", output.String())
		}
		time.Sleep(5 * time.Millisecond)
	}
	if probe, probeErr := os.Open(r.path); probeErr == nil {
		probeID, probeIDErr := windowsFileIdentity(syscall.Handle(probe.Fd()))
		probe.Close()
		t.Fatalf("child's exclusive handle did not deny a second read of %q (%v)", probeID, probeIDErr)
	} else if !errors.Is(probeErr, windows.ERROR_SHARING_VIOLATION) {
		t.Fatalf("exclusive handle precondition: second read answered %v, want sharing violation", probeErr)
	}
	start := time.Now()
	d := r.decide(context.Background(), subject, host.ConfigEditAction, host.ConfigEditResource)
	elapsed := time.Since(start)
	if err := releaseHolder(); err != nil {
		t.Fatalf("release sharing-test holder: %v", err)
	}
	<-done
	if waitErr != nil {
		t.Fatalf("sharing-test holder: %v\n%s", waitErr, output.String())
	}
	if d.Outcome != rwire.DecisionOutcomeUnavailable || elapsed > decisionBudget+time.Second {
		t.Fatalf("held policy file: %+v after %v", d, elapsed)
	}
	deadline := time.Now().Add(time.Minute)
	for {
		if d := r.decide(context.Background(), subject, host.ConfigEditAction, host.ConfigEditResource); d.Outcome == rwire.DecisionOutcomePermitted {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the released policy file never read again")
		}
		time.Sleep(100 * time.Millisecond)
	}
}
