package main

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	host "github.com/openabstractions/abstraction-facade/go/runtime"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// Owner R16 review, 2026-11-09: an app that keeps running the previous
// connector reconnects under its moved file. A first-use question would name
// "oa-claude-integration.exe.<hex>.tmp.previous" and Allow would save a rule
// for a file the next sweep deletes. Its decisions read denied and nothing is
// asked; the current connector path still gets its question.
func TestMovedConnectorCopyIsDeniedWithoutQuestion(t *testing.T) {
	owner, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	folder := filepath.Join(t.TempDir(), "agent-integrations")
	moved := rwire.Subject{Account: owner.Uid, Program: filepath.Join(folder, "oa-claude-integration.exe."+strings.Repeat("d1", 32)+".tmp.previous")}
	current := rwire.Subject{Account: owner.Uid, Program: filepath.Join(folder, "oa-claude-integration.exe")}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	kind := "posix"
	if runtime.GOOS == "windows" {
		kind = "windows"
	}
	r := &runtimeRights{kind: kind, owner: owner.Uid, operators: []string{expectedSubjectProgram(executable)}}
	a, err := composeAsks(runtimeFlags{stateDir: t.TempDir(), endpoint: fullEndpoint(t, "moved-connector")}, r, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	if d := r.decide(context.Background(), moved, host.ModelLookupAction, "hf"); d.Outcome != rwire.DecisionOutcomeDenied {
		t.Fatalf("moved connector decision %v", d.Outcome)
	}
	if id := a.admitFirstUse(moved, nil, host.ModelLookupAction, "hf"); id != "" {
		t.Fatal("moved connector copy was asked about:", id)
	}
	if id := a.admitPairedInput(moved, nil, "input:audio"); id != "" {
		t.Fatal("moved connector copy was asked about its input:", id)
	}
	for _, key := range []string{firstUseKey(expectedSubjectProgram(moved.Program), host.ModelLookupAction, "hf")} {
		if _, outcome, err := a.book.ObserveApplication(a.scope, key); err != nil || outcome != "unknown" {
			t.Fatalf("question recorded for moved copy: %q %v", outcome, err)
		}
	}
	if id := a.admitFirstUse(current, nil, host.ModelLookupAction, "hf"); id == "" {
		t.Fatal("current connector path lost its first-use question")
	}
}
