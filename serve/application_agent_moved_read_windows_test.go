package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// movedCaller renames the file at image into an agent-integrations folder
// under Setup's moved-copy name and returns the caller Windows then names: a
// helper still running that image after its connector update.
func movedCaller(t *testing.T, run *selfRegisteredRun, image string, n int) rights.Subject {
	t.Helper()
	dir := filepath.Join(filepath.Dir(image), "agent-integrations")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	moved := filepath.Join(dir, "oa-claude-integration.exe."+strings.Repeat(string(rune('a'+n)), 16)+".tmp.previous")
	if err := os.Rename(image, moved); err != nil {
		t.Fatal(err)
	}
	caller := rights.Subject{Account: run.f.panel.Account, Program: moved}
	if !movedConnectorCaller(caller.Program) {
		t.Fatal("fixture path is not a moved connector", moved)
	}
	return caller
}

// agentUpdateRound prepares, approves, publishes and commits one connector
// update of request's members from a source with content, and returns the
// path each member's previous image was moved to.
func agentUpdateRound(t *testing.T, run *selfRegisteredRun, request applications.AgentUpdateRequest, suffix string) map[string]string {
	t.Helper()
	pending := prepareAgentUpdateFixture(t, run, request)
	answer(t, run.f, pending.Question, "update")
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Reason != "update_ready" {
		t.Fatal("publication authority", got)
	}
	previous := map[string]string{}
	for _, m := range request.Members {
		target := strings.TrimSuffix(m.StagedProgram, suffix)
		if err := os.Rename(target, m.StagedProgram+".previous"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(m.StagedProgram, target); err != nil {
			t.Fatal(err)
		}
		previous[m.Application] = m.StagedProgram + ".previous"
	}
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, pending.Intent); got.Outcome != applications.OutcomeApplied || got.Reason != "updated" {
		t.Fatal("commit", got)
	}
	return previous
}

func readsEnrolled(t *testing.T, run *selfRegisteredRun, caller rights.Subject, name string) bool {
	t.Helper()
	got := run.d.readAgentEnrollment(run.ctx, caller, name)
	if got.Outcome == applications.OutcomeForbidden && got.Enrollment == nil {
		return false
	}
	current := run.d.currentAgentEnrollment(run.ctx, name)
	if got.Outcome != applications.OutcomePage || got.Enrollment == nil || current.Enrollment == nil || *got.Enrollment != *current.Enrollment || got.Enrollment.Program == "" {
		t.Fatalf("moved read answered other than the current enrollment: %+v", got)
	}
	return true
}

// CONNECTOR-HANDOVER-AUTH, amended ruling: a helper still running the image
// the person kept before its application's latest committed update may read
// that application's current enrollment, program path included, and nothing
// else. Any other key, a later commit and a revoke refuse it.
func TestMovedConnectorReadsOnlyItsKeptApplicationEnrollment(t *testing.T) {
	run, request := agentUpdateFixture(t)
	first := agentUpdateRound(t, run, request, ".staged")
	claude, codex := "batch-claude", "batch-codex"
	caller := movedCaller(t, run, first[claude], 0)

	if !readsEnrolled(t, run, caller, claude) {
		t.Fatal("the kept image could not read its own enrollment")
	}
	if readsEnrolled(t, run, caller, codex) {
		t.Fatal("the kept image read another application's enrollment")
	}
	// Every other call stays denied.
	if out := run.d.authorized(run.ctx, caller, ActionApplicationRead, "app:"+claude); out != wire.ApplicationOutcomeForbidden {
		t.Fatal("a moved connector was authorized", out)
	}
	if got := run.d.announce(run.ctx, caller, wire.ApplicationPresence{Application: claude, LeaseMs: 30000, Contexts: []wire.ApplicationContext{{Name: "native-moved"}}}); got.Outcome == wire.ApplicationOutcomeApplied {
		t.Fatal("a moved connector announced", got)
	}
	current := run.d.currentAgentEnrollment(run.ctx, claude)
	if got := run.d.setAgentEnrollment(run.ctx, caller, applications.AgentEnrollmentRequest{Application: claude, ExpectedRevision: current.Enrollment.Revision, Enabled: false}); got.Outcome == applications.OutcomeApplied {
		t.Fatal("a moved connector changed its enrollment", got)
	}
	if got := run.d.findAgentUpdate(run.ctx, caller, applications.AgentUpdateRequest{Members: []applications.AgentUpdateMember{{Application: claude}}}); got.Outcome == applications.OutcomeApplied || got.Intent != "" {
		t.Fatal("a moved connector read an update", got)
	}
	if got := run.d.readAgentEnrollment(run.ctx, rights.Subject{Account: caller.Account, Program: caller.Program}, "unregistered"); got.Outcome != applications.OutcomeForbidden {
		t.Fatal("a moved connector read an unregistered name", got)
	}

	// A hard link to the kept moved file, under a moved name, is refused.
	linked := filepath.Join(filepath.Dir(caller.Program), "oa-claude-integration.exe."+strings.Repeat("z", 16)+".tmp.previous")
	if err := os.Link(caller.Program, linked); err != nil {
		t.Fatal(err)
	}
	if readsEnrolled(t, run, rights.Subject{Account: caller.Account, Program: linked}, claude) {
		t.Fatal("a hard link to the kept image read the enrollment")
	}
	if readsEnrolled(t, run, caller, claude) {
		t.Fatal("the kept image read while it had a second link")
	}
	if err := os.Remove(linked); err != nil {
		t.Fatal(err)
	}
	if !readsEnrolled(t, run, caller, claude) {
		t.Fatal("the kept image could not read after its extra link was removed")
	}

	// Another moved image, which the person never kept for this application.
	other := filepath.Join(filepath.Dir(first[codex]), "unkept.exe")
	if err := os.WriteFile(other, []byte("never kept"), 0o600); err != nil {
		t.Fatal(err)
	}
	if stranger := movedCaller(t, run, other, 1); readsEnrolled(t, run, stranger, claude) {
		t.Fatal("an image with another key read the enrollment")
	}

	// A second commit replaces the kept key with the image it replaced.
	source := filepath.Join(filepath.Dir(request.SourceProgram), "installed-next.exe")
	if err := os.WriteFile(source, []byte("newer installed gateway"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstSource := request.SourceProgram
	run.d.agentExcluded = func(_ context.Context, program, _ string) (bool, error) {
		if same, err := identity.SameFile(program, firstSource); same || err != nil {
			return same, err
		}
		return identity.SameFile(program, source)
	}
	next := applications.AgentUpdateRequest{SourceProgram: source}
	for _, m := range request.Members {
		target := strings.TrimSuffix(m.StagedProgram, ".staged")
		stage := target + ".next"
		if err := os.WriteFile(stage, []byte("newer installed gateway"), 0o600); err != nil {
			t.Fatal(err)
		}
		revision := run.d.currentAgentEnrollment(run.ctx, m.Application).Enrollment.Revision
		next.Members = append(next.Members, applications.AgentUpdateMember{Application: m.Application, ExpectedRevision: revision, StagedProgram: stage})
	}
	second := agentUpdateRound(t, run, next, ".next")
	if readsEnrolled(t, run, caller, claude) {
		t.Fatal("the earlier kept image read after a second commit")
	}
	latest := movedCaller(t, run, second[claude], 2)
	if !readsEnrolled(t, run, latest, claude) {
		t.Fatal("the latest kept image could not read its enrollment")
	}

	// A revoke clears the kept key.
	current = run.d.currentAgentEnrollment(run.ctx, claude)
	if got := run.d.setAgentEnrollment(run.ctx, run.f.panel, applications.AgentEnrollmentRequest{Application: claude, ExpectedRevision: current.Enrollment.Revision, Enabled: false}); got.Outcome != applications.OutcomeApplied {
		t.Fatal("revoke", got)
	}
	if readsEnrolled(t, run, latest, claude) {
		t.Fatal("the kept image read after a revoke")
	}
}
