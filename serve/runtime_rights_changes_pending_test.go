package main

import (
	"fmt"
	"path/filepath"
	"testing"

	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Security review 2026-11-22, LOW: one program holds at most four pending
// requests; a fifth is refused and asks nothing.
func TestChangeRequestsPerRequesterAreBounded(t *testing.T) {
	c, r := changeGateUnit(t, true)
	if err := r.policy.RegisterAction("example.app/use"); err != nil {
		t.Fatal(err)
	}
	by := r.self()
	for i := range maxPendingPerRequester + 1 {
		rule := rwire.PolicyRule{Subject: rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), "app.exe")}, Action: "example.app/use", Resource: fmt.Sprint(i), Permit: true}
		req, outcome := c.admitWith(by, rwire.PolicyChange{Kind: rwire.ChangeKindSetRule, Rule: &rule}, nil)
		if i < maxPendingPerRequester && (req == nil || outcome != rwire.ChangeRequestOutcomePending) {
			t.Fatalf("request %d: %v", i, outcome)
		}
		if i == maxPendingPerRequester && (req != nil || outcome != rwire.ChangeRequestOutcomeUnavailable) {
			t.Fatalf("a fifth pending request was admitted: %v", outcome)
		}
	}
}

// Security review 2026-11-22, F6: the whole account holds at most eight
// pending requests, whichever programs asked.
func TestChangeRequestsPerAccountAreBounded(t *testing.T) {
	c, r := changeGateUnit(t, true)
	if err := r.policy.RegisterAction("example.app/use"); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "app.exe")
	for i := range maxPendingPerAccount + 1 {
		by := rwire.Subject{Account: r.owner, Program: filepath.Join(t.TempDir(), fmt.Sprintf("requester%d.exe", i/2))}
		rule := rwire.PolicyRule{Subject: rwire.Subject{Account: r.owner, Program: target}, Action: "example.app/use", Resource: fmt.Sprint(i), Permit: true}
		req, outcome := c.admitWith(by, rwire.PolicyChange{Kind: rwire.ChangeKindSetRule, Rule: &rule}, nil)
		if i < maxPendingPerAccount && (req == nil || outcome != rwire.ChangeRequestOutcomePending) {
			t.Fatalf("request %d: %v", i, outcome)
		}
		if i == maxPendingPerAccount && (req != nil || outcome != rwire.ChangeRequestOutcomeUnavailable) {
			t.Fatalf("a ninth pending request was admitted: %v", outcome)
		}
	}
}
