package main

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestUpgradeCensusOwnedStartupTerminationMetadata(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "openabstractions.exe")
	if err := os.WriteFile(path, data, 0700); err != nil {
		t.Fatal(err)
	}
	ops := systemUpgradeCensusOps()
	pendingFailures, signaledFailures := 0, 0
	for i := 0; i < 16; i++ {
		cmd := exec.Command(path, "serve", "runtime", "--supervised", "rm-test-child", filepath.Join(t.TempDir(), "eof"))
		cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_SUSPENDED}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		pid := uint32(cmd.Process.Pid)
		query, err := ops.open(pid, windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE)
		if err != nil {
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal(err)
		}
		// Only this suspended test child is terminated. Its query handle remains
		// retained across asynchronous termination and the real image API reads.
		if _, err := ops.image(query); err != nil {
			windows.CloseHandle(query)
			cmd.Process.Kill()
			cmd.Wait()
			t.Fatal("suspended startup image", err)
		}
		if err := cmd.Process.Kill(); err != nil {
			windows.CloseHandle(query)
			cmd.Wait()
			t.Fatal(err)
		}
		_, imageErr := ops.image(query)
		exited, waitErr := ops.exited(query)
		if imageErr != nil {
			if exited {
				signaledFailures++
			} else {
				pendingFailures++
			}
			t.Logf("owned termination pid=%d image_error=%v signaled=%t wait_error=%v", pid, imageErr, exited, waitErr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err = awaitUpgradeMetadata(ctx, query, ops, func() error { _, err := ops.image(query); return err })
		cancel()
		windows.CloseHandle(query)
		cmd.Wait()
		if err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("owned native startup/termination metadata failures: pending=%d signaled=%d; historical SYSTEM candidate ownership remains unknown", pendingFailures, signaledFailures)
}

// Only copied test children started here are enumerated or have a process ACL
// changed. The installed runtime and all other processes are outside this test.
func TestUpgradeCensusOwnedProtectedForeignAndInstalledImages(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	tools := filepath.Join(root, "tools")
	if err := os.Mkdir(tools, 0700); err != nil {
		t.Fatal(err)
	}
	owned := filepath.Join(tools, "openabstractionsw.exe")
	foreign := filepath.Join(t.TempDir(), "openabstractionsw.exe")
	for _, path := range []string{owned, foreign} {
		if err := os.WriteFile(path, data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	start := func(path string) (*exec.Cmd, io.WriteCloser) {
		cmd := exec.Command(path, "serve", "runtime", "--supervised", "rm-test-child", filepath.Join(t.TempDir(), "eof"))
		in, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			in.Close()
			done := make(chan error, 1)
			go func() { done <- cmd.Wait() }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				cmd.Process.Kill()
				<-done
			}
		})
		ready := make(chan string, 1)
		go func() { line, _ := bufio.NewReader(out).ReadString('\n'); ready <- line }()
		select {
		case line := <-ready:
			if line != "READY 1\n" {
				t.Fatal(line)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("owned fixture did not become ready")
		}
		return cmd, in
	}
	for _, path := range []string{foreign, owned} {
		cmd, _ := start(path)
		pid := uint32(cmd.Process.Pid)
		h, err := windows.OpenProcess(windows.READ_CONTROL|windows.WRITE_DAC|windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
		if err != nil {
			t.Fatal(err)
		}
		original, err := windows.GetSecurityInfo(h, windows.SE_KERNEL_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			windows.CloseHandle(h)
			t.Fatal(err)
		}
		retained := h
		t.Cleanup(func() {
			if retained != 0 {
				windows.SetKernelObjectSecurity(retained, windows.DACL_SECURITY_INFORMATION, original)
				windows.CloseHandle(retained)
			}
		})
		user, err := windows.GetCurrentProcessToken().GetTokenUser()
		if err != nil {
			t.Fatal(err)
		}
		sid := user.User.Sid.String()
		sd, err := windows.SecurityDescriptorFromString("D:P(D;;0x1;;;" + sid + ")(A;;0x101400;;;" + sid + ")")
		if err != nil {
			t.Fatal(err)
		}
		if err := windows.SetKernelObjectSecurity(h, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, sd); err != nil {
			t.Fatal(err)
		}
		denied, openErr := windows.OpenProcess(windows.PROCESS_TERMINATE, false, pid)
		if openErr == nil {
			windows.CloseHandle(denied)
			t.Fatal("owned ACL fixture did not deny stop rights")
		}
		if !errors.Is(openErr, windows.ERROR_ACCESS_DENIED) {
			t.Fatal(openErr)
		}
		ops := systemUpgradeCensusOps()
		ops.list = func() ([]upgradeCandidate, error) {
			return []upgradeCandidate{{PID: pid, Name: "openabstractionsw.exe"}}, nil
		}
		var report bytes.Buffer
		err = inspectUpgradeCensus([]string{root}, ops, &report)
		if path == foreign {
			if err != nil || !strings.Contains(report.String(), "unrelated-file") {
				t.Fatal(err, report.String())
			}
		} else {
			if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || !strings.Contains(err.Error(), "open-stop-rights") || !strings.Contains(err.Error(), strconv.Quote(path)) {
				t.Fatal(err, report.String())
			}
		}
		// Restore only the test child's ACL using the handle retained before
		// protection. It remains running: diagnostic admission never closes it.
		if err := windows.SetKernelObjectSecurity(h, windows.DACL_SECURITY_INFORMATION, original); err != nil {
			t.Fatal(err)
		}
		windows.CloseHandle(h)
		retained = 0
		if path == owned {
			if err := inspectUpgradeCensus([]string{root}, ops, io.Discard); err != nil {
				t.Fatal(err)
			}
		}
		query, err := windows.OpenProcess(windows.SYNCHRONIZE, false, pid)
		if err != nil {
			t.Fatal(err)
		}
		status, err := windows.WaitForSingleObject(query, 0)
		windows.CloseHandle(query)
		if err != nil || status != uint32(windows.WAIT_TIMEOUT) {
			t.Fatal("inspection stopped owned fixture", status, err)
		}
	}
	cmd, in := start(owned)
	pid := uint32(cmd.Process.Pid)
	ops := systemUpgradeCensusOps()
	ops.list = func() ([]upgradeCandidate, error) {
		return []upgradeCandidate{{PID: pid, Name: "openabstractionsw.exe"}}, nil
	}
	image := ops.image
	ops.image = func(handle windows.Handle) (string, error) {
		in.Close()
		state, err := windows.WaitForSingleObject(handle, 10000)
		if err != nil || state != windows.WAIT_OBJECT_0 {
			t.Fatal("owned exiting fixture did not exit", state, err)
		}
		return image(handle)
	}
	open := ops.open
	ops.open = func(pid, access uint32) (windows.Handle, error) {
		if access&windows.PROCESS_TERMINATE != 0 {
			t.Fatal("exited fixture requested stop rights")
		}
		return open(pid, access)
	}
	var report bytes.Buffer
	if err := inspectUpgradeCensus([]string{root}, ops, &report); err != nil || !strings.Contains(report.String(), "exited-") {
		t.Fatal(err, report.String())
	}
}

func TestUpgradeExclusionRetainedReadersAndReadonlyTargetFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "upgrade-v1", "exclusion.json")
	if err := writeExclusionFile(path, "user", []byte("old")); err != nil {
		t.Fatal(err)
	}
	legacy, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	legacyTemporary := filepath.Join(filepath.Dir(path), "legacy-replacement.tmp")
	if err := os.WriteFile(legacyTemporary, []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	from, _ := windows.UTF16PtrFromString(legacyTemporary)
	to, _ := windows.UTF16PtrFromString(path)
	err = windows.MoveFileEx(from, to, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
	legacy.Close()
	os.Remove(legacyTemporary)
	if err == nil || (!errors.Is(err, windows.ERROR_ACCESS_DENIED) && !errors.Is(err, windows.ERROR_SHARING_VIOLATION)) {
		t.Fatal("legacy retained-reader counterexample", err)
	}
	t.Logf("legacy retained reader atomic replacement: %v", err)
	legacy, err = os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	err = writeExclusionFile(path, "user", []byte("rooted-new"))
	legacyBytes, readErr := io.ReadAll(legacy)
	legacy.Close()
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || readErr != nil || string(legacyBytes) != "old" || !strings.Contains(err.Error(), "stage=replace-record") {
		t.Fatal("retained historical reader must fail closed without a retry", err, readErr, string(legacyBytes))
	}
	current, err := readExclusionFile(path)
	if err != nil || string(current) != "old" {
		t.Fatal(string(current), err)
	}
	if err := writeExclusionFile(path, "user", []byte("old")); err != nil {
		t.Fatal(err)
	}
	old, err := openExclusionRead(path)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	second, err := openExclusionRead(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	if err := writeExclusionFile(path, "user", []byte("new")); err != nil {
		t.Fatal(err)
	}
	oldBytes, err := io.ReadAll(old.File())
	if err != nil || string(oldBytes) != "old" {
		t.Fatal(string(oldBytes), err)
	}
	secondBytes, err := io.ReadAll(second.File())
	if err != nil || string(secondBytes) != "old" {
		t.Fatal(string(secondBytes), err)
	}
	newBytes, err := readExclusionFile(path)
	if err != nil || string(newBytes) != "new" {
		t.Fatal(string(newBytes), err)
	}
	if err := os.Chmod(path, 0400); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(path, 0600)
	err = writeExclusionFile(path, "user", []byte("not-authorized"))
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) || !strings.Contains(err.Error(), "stage=replace-record") {
		t.Fatal("readonly target must retain a named refusal", err)
	}
	newBytes, err = readExclusionFile(path)
	if err != nil || string(newBytes) != "new" {
		t.Fatal("failed replace changed record", string(newBytes), err)
	}
}

func TestUpgradeExclusionReplacementRetainsPrivateOwnerAndRefusesLinkedRoot(t *testing.T) {
	directory := t.TempDir()
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	sid := user.User.Sid.String()
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + sid + ")(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)")
	if err != nil {
		t.Fatal(err)
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		t.Fatal(err)
	}
	if err := windows.SetNamedSecurityInfo(directory, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, dacl, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "exclusion.json")
	for _, data := range []string{"old", "new"} {
		if err := writeExclusionFile(path, "user", []byte(data)); err != nil {
			t.Fatal(err)
		}
		owner, err := pathOwner(path)
		if err != nil || !owner.Equals(user.User.Sid) {
			t.Fatal(owner, err)
		}
		security, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION)
		if err != nil {
			t.Fatal(err)
		}
		if text := security.String(); strings.Contains(text, ";;;WD)") || strings.Contains(text, ";;;AU)") {
			t.Fatal("replacement lost inherited private ACL", text)
		}
		if err := trustedExclusionFile(path, "user"); err != nil {
			t.Fatal(err)
		}
		if !administrativeSID(owner) && trustedExclusionFile(path, "machine") == nil {
			t.Fatal("ordinary user owner accepted for machine exclusion")
		}
	}
	link := filepath.Join(t.TempDir(), "linked")
	junction(t, link, directory)
	if _, err := readExclusionFile(filepath.Join(link, "exclusion.json")); err == nil {
		t.Fatal("linked storage root read")
	}
	if err := writeExclusionFile(filepath.Join(link, "exclusion.json"), "user", []byte("foreign")); err == nil {
		t.Fatal("linked storage root written")
	}
	data, err := os.ReadFile(path)
	if err != nil || string(data) != "new" {
		t.Fatal("linked refusal changed target", string(data), err)
	}
}
