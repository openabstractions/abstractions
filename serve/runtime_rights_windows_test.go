package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"
	"unsafe"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"golang.org/x/sys/windows"
)

func windowsTokenPrivileges(token windows.Token) ([]windows.LUIDAndAttributes, error) {
	var size uint32
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, nil, 0, &size); !errors.Is(err, windows.ERROR_INSUFFICIENT_BUFFER) {
		return nil, err
	}
	data := make([]byte, size)
	if err := windows.GetTokenInformation(token, windows.TokenPrivileges, &data[0], size, &size); err != nil {
		return nil, err
	}
	privileges := (*windows.Tokenprivileges)(unsafe.Pointer(&data[0])).AllPrivileges()
	return append([]windows.LUIDAndAttributes(nil), privileges...), nil
}

func disableWindowsReadBypassPrivileges(t *testing.T, token windows.Token) {
	t.Helper()
	privileges, err := windowsTokenPrivileges(token)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SeBackupPrivilege", "SeRestorePrivilege"} {
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
			t.Fatal(err)
		}
		for _, privilege := range privileges {
			if privilege.Luid != luid {
				continue
			}
			state := windows.Tokenprivileges{
				PrivilegeCount: 1,
				Privileges: [1]windows.LUIDAndAttributes{{
					Luid:       luid,
					Attributes: privilege.Attributes &^ windows.SE_PRIVILEGE_ENABLED,
				}},
			}
			if err := windows.AdjustTokenPrivileges(token, false, &state, 0, nil, nil); err != nil {
				t.Fatalf("disable %s: %v", name, err)
			}
		}
	}
	privileges, err = windowsTokenPrivileges(token)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SeBackupPrivilege", "SeRestorePrivilege"} {
		var luid windows.LUID
		if err := windows.LookupPrivilegeValue(nil, windows.StringToUTF16Ptr(name), &luid); err != nil {
			t.Fatal(err)
		}
		for _, privilege := range privileges {
			if privilege.Luid == luid && privilege.Attributes&windows.SE_PRIVILEGE_ENABLED != 0 {
				t.Fatalf("%s remained enabled on the test thread", name)
			}
		}
	}
}

func withoutWindowsReadBypassPrivileges(t *testing.T, call func()) {
	t.Helper()
	runtime.LockOSThread()
	if err := windows.ImpersonateSelf(windows.SecurityImpersonation); err != nil {
		runtime.UnlockOSThread()
		t.Fatal(err)
	}
	defer func() {
		if err := windows.RevertToSelf(); err != nil {
			t.Errorf("revert thread impersonation: %v", err)
			return
		}
		runtime.UnlockOSThread()
	}()
	var token windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_ADJUST_PRIVILEGES|windows.TOKEN_QUERY, false, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	disableWindowsReadBypassPrivileges(t, token)
	call()
}

// A policy file whose ACL denies reads keeps answering access denied. The
// policy store retries that transient writer-facing error until its read budget,
// and the runtime fails the decision closed. Resetting the ACL restores the
// exact policy bytes and the next decision.
func TestADecisionThatCannotBeReadInTimeIsUnavailable(t *testing.T) {
	r := testRights(t, t.TempDir())
	rules := []installationRule{{host.ConfigEditAction, host.ConfigEditResource}}
	if err := r.install(nil, rules); err != nil {
		t.Fatal(err)
	}
	subject := rwire.Subject{Account: r.owner, Program: r.operators[0]}
	if d := r.decide(context.Background(), subject, host.ConfigEditAction, host.ConfigEditResource); d.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatalf("readable policy: %+v", d)
	}
	wantPolicy, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("icacls", r.path, "/deny", "*S-1-1-0:(RD)").CombinedOutput(); err != nil {
		t.Fatalf("icacls deny: %v\n%s", err, out)
	}
	denied := true
	reset := func() {
		if !denied {
			return
		}
		out, err := exec.Command("icacls", r.path, "/reset").CombinedOutput()
		if err != nil {
			t.Fatalf("icacls reset: %v\n%s", err, out)
		}
		denied = false
	}
	t.Cleanup(reset)
	withoutWindowsReadBypassPrivileges(t, func() {
		if _, err := os.Lstat(r.path); err != nil {
			t.Fatalf("read-denied policy metadata: %v", err)
		}
		if f, err := os.Open(r.path); err == nil {
			f.Close()
			t.Fatal("read-denied policy opened")
		} else if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			t.Fatalf("read-denied policy answered %v, want access denied", err)
		}

		start := time.Now()
		checkErr := r.policy.CheckState(context.Background())
		checkElapsed := time.Since(start)
		if !errors.Is(checkErr, context.DeadlineExceeded) {
			t.Fatalf("CheckState = %v after %v, want deadline exceeded", checkErr, checkElapsed)
		}
		if checkElapsed < decisionBudget-100*time.Millisecond || checkElapsed > decisionBudget+time.Second {
			t.Fatalf("CheckState returned after %v, want the %v read budget", checkElapsed, decisionBudget)
		}

		start = time.Now()
		d := r.decide(context.Background(), subject, host.ConfigEditAction, host.ConfigEditResource)
		decisionElapsed := time.Since(start)
		if d.Outcome != rwire.DecisionOutcomeUnavailable {
			t.Fatalf("read-denied policy decision: %+v after %v", d, decisionElapsed)
		}
		if decisionElapsed < decisionBudget-100*time.Millisecond || decisionElapsed > decisionBudget+time.Second {
			t.Fatalf("decision returned after %v, want the %v read budget", decisionElapsed, decisionBudget)
		}
	})

	reset()
	gotPolicy, err := os.ReadFile(r.path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotPolicy, wantPolicy) {
		t.Fatal("ACL reset changed the policy bytes")
	}
	if d := r.decide(context.Background(), subject, host.ConfigEditAction, host.ConfigEditResource); d.Outcome != rwire.DecisionOutcomePermitted {
		t.Fatalf("restored policy: %+v", d)
	}
}
