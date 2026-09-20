package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"golang.org/x/sys/windows"
)

// A policy file another handle holds without sharing keeps answering a sharing
// violation, which the file store retries for many seconds. The in-process
// decision gives up at its budget and reads unavailable; once the handle closes
// the next decision reads the file again.
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
	name, err := windows.UTF16PtrFromString(filepath.Clean(r.path))
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(name, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	d := r.decide(context.Background(), subject, host.ConfigEditAction, host.ConfigEditResource)
	elapsed := time.Since(start)
	windows.CloseHandle(handle)
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
