package main

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// Security review 2026-11-22: the installed Panel's developer page only reads,
// whichever runtime it is pointed at (an explicit -runtime-endpoint
// included). Every method that is no read is refused before any connection:
// rights edits, change requests, answers, and the content removal, archive
// restore and Connect records a fixed list missed.
func TestLoopbackPanelOnlyReads(t *testing.T) {
	saved, savedExplicit := installedPanelSelf, panelExplicit
	t.Cleanup(func() { installedPanelSelf, panelExplicit = saved, savedExplicit; listen.GuardOutgoingFrames(nil) })
	installedPanelSelf = func(string) bool { return true }
	panelExplicit = &explicitRuntime{Endpoint: "elsewhere"}
	guardLoopbackPage(false)
	endpoint := filepath.Join(t.TempDir(), "absent")
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	rights := rightsclient.NewOperator(endpoint)
	rule := rwire.PolicyRule{Subject: rwire.Subject{Account: "a", Program: filepath.Join(t.TempDir(), "x", "app.exe")}, Action: "a/b", Resource: "r", Permit: true}
	if _, err := rights.SetRuleContext(ctx, "rev", rule); !errors.Is(err, errLoopbackPersonOnly) {
		t.Fatalf("SetRule: %v", err)
	}
	if _, err := rights.RequestChangeContext(ctx, rwire.PolicyChange{Kind: rwire.ChangeKindSetRule, Rule: &rule}, 0); !errors.Is(err, errLoopbackPersonOnly) {
		t.Fatalf("RequestChange: %v", err)
	}
	if _, err := askclient.NewOperator(endpoint).AnswerQuestionIfPendingContext(ctx, "q1", "allow"); !errors.Is(err, errLoopbackPersonOnly) {
		t.Fatalf("answer: %v", err)
	}
	for _, method := range []string{"Remove", "Commit", "Apply", "Store", "ExpectConnect", "Register", "Answer", "Submit", "Write", ""} {
		if err := loopbackFrameGuard([]byte(`{"version":1,"service":"x","method":"` + method + `"}`)); !errors.Is(err, errLoopbackPersonOnly) {
			t.Fatalf("%q was sent", method)
		}
	}
	if err := loopbackFrameGuard([]byte("not json")); !errors.Is(err, errLoopbackPersonOnly) {
		t.Fatal("an unreadable frame was sent")
	}
	if _, err := rights.ListPolicyContext(ctx, "", 1); errors.Is(err, errLoopbackPersonOnly) {
		t.Fatal("a read was refused")
	}

	// The Panel window, and a copy that is not the installed Panel, are
	// unaffected.
	listen.GuardOutgoingFrames(nil)
	guardLoopbackPage(true)
	if _, err := rights.SetRuleContext(ctx, "rev", rule); errors.Is(err, errLoopbackPersonOnly) {
		t.Fatal("the native window was guarded")
	}
	installedPanelSelf = func(string) bool { return false }
	guardLoopbackPage(false)
	if _, err := rights.SetRuleContext(ctx, "rev", rule); errors.Is(err, errLoopbackPersonOnly) {
		t.Fatal("a copy of the Panel was guarded")
	}
}
