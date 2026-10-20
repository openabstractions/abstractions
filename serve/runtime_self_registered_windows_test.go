//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	identity "github.com/openabstractions/abstraction-identity"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// The question names the program the platform proved for the registering
// caller, never the descriptor's own spelling of it: a descriptor that
// reaches the program through a junction still shows the bound path, and
// Remove blocks that bound program.
func TestSelfRegistrationQuestionShowsTheBoundProgram(t *testing.T) {
	run := newSelfRegisteredRun(t)
	f := run.f
	root := t.TempDir()
	real := filepath.Join(root, "real")
	if err := os.Mkdir(real, 0o700); err != nil {
		t.Fatal(err)
	}
	bound := filepath.Join(real, "editor.exe")
	if err := os.WriteFile(bound, []byte("editor"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "link")
	junction(t, link, real)
	caller := rwire.Subject{Account: f.answerer.Account, Program: identity.NormalizeSubjectProgram(bound)}
	editor := wire.ApplicationDescriptor{Name: "editor", Program: filepath.Join(link, "editor.exe"), Title: "Editor", StartGuidance: "Open"}
	if r := run.d.register(run.ctx, caller, editor); r.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal("registering through a junction", r)
	}
	v, _, ok := run.d.selfEntry("editor")
	if !ok || !strings.Contains(v.Program, `\link\`) {
		t.Fatalf("the descriptor keeps its own spelling: %+v", v)
	}
	q := selfRegisteredQuestion(t, f, run.d, "editor")
	if q.About != caller.Program || !strings.HasPrefix(q.Text, caller.Program+" added itself") {
		t.Fatalf("the question names %q, not the bound program %q", q.About, caller.Program)
	}
	answer(t, f, q.ID, "remove")
	waitDirectory(t, run.d, func() bool { registered, _ := unconfirmedNow(run.d, "editor"); return !registered })
	read := f.a.rights.policy.ReadRule(caller, ActionApplicationRegisterSelf, "account")
	if read.Record == nil || read.Record.Rule.Permit || read.Record.Rule.Subject.Program != caller.Program {
		t.Fatalf("the Block is not on the bound program: %+v", read)
	}
}
