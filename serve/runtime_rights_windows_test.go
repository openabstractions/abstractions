package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"
	"time"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"golang.org/x/sys/windows"
)

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
