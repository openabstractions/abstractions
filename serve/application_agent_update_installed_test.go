package main

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"os"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askwire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
)

// agentPreviewImage is an unsigned native helper image. A nil release builds
// it the way preview installers do, without an OA_AGENT_RELEASE resource.
func agentPreviewImage(release []byte, payload string) []byte {
	if release == nil {
		return append(manifestPE([]byte(payload), true), payload...)
	}
	image := manifestPE(release, true)
	binary.LittleEndian.PutUint16(image[600:], uint16(len(agentReleaseResourceName)))
	for i, c := range agentReleaseResourceName {
		binary.LittleEndian.PutUint16(image[602+i*2:], uint16(c))
	}
	return append(image, payload...)
}

// installedConnectorFixture is the owner's state: unsigned preview helpers,
// Keeps recorded before release facts and companions, and an installed
// tools copy the staged copies equal. trusted stands for the admin-only
// install folder; false keeps the real owner-and-access check.
func installedConnectorFixture(t *testing.T, trusted bool) (*selfRegisteredRun, applications.AgentUpdateRequest, []byte) {
	t.Helper()
	run, request := agentUpdateFixtureWithImage(t, func(run *selfRegisteredRun) {
		run.d.agentMaintenance = true
		if trusted {
			run.d.agentInstalledTrusted = func(string) error { return nil }
		}
	})
	for _, m := range request.Members {
		old := m.StagedProgram[:len(m.StagedProgram)-len(".staged")]
		writeAgentImage(t, old, agentPreviewImage(nil, "kept "+m.Application))
	}
	next := agentPreviewImage(nil, "preview two")
	stageInstalledCopy(t, request, next)
	forgetKeepFacts(t, run, request)
	return run, request, next
}

func writeAgentImage(t *testing.T, path string, image []byte) {
	t.Helper()
	if err := os.WriteFile(path, image, 0600); err != nil {
		t.Fatal(err)
	}
}

func stageInstalledCopy(t *testing.T, request applications.AgentUpdateRequest, image []byte) {
	t.Helper()
	writeAgentImage(t, request.SourceProgram, image)
	for _, m := range request.Members {
		writeAgentImage(t, m.StagedProgram, image)
	}
}

// forgetKeepFacts writes the owner's marks: no Keep facts, and companions
// stored as JSON null by earlier updates that recorded none.
func forgetKeepFacts(t *testing.T, run *selfRegisteredRun, request applications.AgentUpdateRequest) {
	t.Helper()
	if !run.d.lock(run.ctx) {
		t.Fatal("directory lock")
	}
	err := run.d.withRegistrationStorageLocked(run.ctx, func(legacy map[string]wire.ApplicationDescriptor) error {
		marks, base, err := run.d.readSelfMarksAtDescriptorLocked(run.ctx)
		if err != nil {
			return err
		}
		for _, m := range request.Members {
			mark := marks[m.Application]
			if mark.Agent == nil {
				return errors.New("fixture member not kept")
			}
			a := *mark.Agent
			a.KeptRelease, a.Companions = nil, json.RawMessage("null")
			mark.Agent = &a
			marks[m.Application] = mark
		}
		run.d.selfBase = base
		return run.d.persistUnconfirmedAtDescriptorLocked(run.ctx, marks, legacy)
	})
	run.d.unlock()
	if err != nil {
		t.Fatal(err)
	}
}

func agentQuestionCount(t *testing.T, run *selfRegisteredRun) int {
	t.Helper()
	history, _, err := run.f.a.book.ApplicationHistory()
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range history {
		if r.Key == asks.AgentUpdateKey {
			n++
		}
	}
	return n
}

func nextRevisions(t *testing.T, run *selfRegisteredRun, request *applications.AgentUpdateRequest) {
	t.Helper()
	for i, m := range request.Members {
		current := run.d.currentAgentEnrollment(run.ctx, m.Application)
		if current.Enrollment == nil || current.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
			t.Fatal("connection lost its Keep", current)
		}
		request.Members[i].ExpectedRevision = current.Enrollment.Revision
	}
}

// The owner installs preview after preview. The first update in this state
// came from a user-writable copy and was asked; the person answered Update.
// The next preview's installed copy updates with no question, posts one
// notice with Undo, and Undo puts the kept image back.
func TestAgentUpdateInstalledPreviewUpdatesWithoutQuestion(t *testing.T) {
	run, request, first := installedConnectorFixture(t, false)
	asked := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if asked.Reason != "permission_pending" || asked.Question == "" {
		t.Fatal("unproven source did not ask", asked)
	}
	answer(t, run.f, asked.Question, "update")
	publishAgentUpdateFixture(t, request)
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, asked.Intent); got.Reason != "updated" {
		t.Fatal(got)
	}
	nextRevisions(t, run, &request)
	forgetKeepFacts(t, run, request)
	for _, m := range request.Members {
		mark := run.d.unconfirmed[m.Application]
		if mark.Agent.UpdateProof != asked.Intent || mark.Agent.KeptRelease != nil {
			t.Fatal("fixture is not the owner's state", mark.Agent)
		}
	}

	run.d.agentInstalledTrusted = func(string) error { return nil }
	stageInstalledCopy(t, request, agentPreviewImage(nil, "preview three"))
	questions := agentQuestionCount(t, run)
	ready := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if ready.Outcome != applications.OutcomeApplied || ready.Reason != "update_ready" || ready.Question != "" {
		t.Fatal("installed preview asked", ready)
	}
	if agentQuestionCount(t, run) != questions {
		t.Fatal("installed preview created a question")
	}
	publishAgentUpdateFixture(t, request)
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, ready.Intent); got.Reason != "updated" {
		t.Fatal(got)
	}
	run.d.agentCompanionProducer.(*agentCompanionFixture).current = true
	done := run.d.commitAgentUpdate(run.ctx, run.f.panel, ready.Intent)
	if done.Reason != "updated" || done.Phase == nil || *done.Phase != applications.AgentUpdatePhaseReady {
		t.Fatal("installed preview did not finish", done)
	}
	for _, m := range request.Members {
		mark := run.d.unconfirmed[m.Application]
		if mark.Agent.KeptRelease == nil || mark.Agent.KeptRelease.Current.Origin != agentReleaseOriginInstalled || mark.Question != asked.Question {
			t.Fatal("update lost the person's Keep or its new facts", mark.Agent)
		}
	}
	notices, _, err := run.f.a.book.NotificationHistory()
	if err != nil || len(notices) != 1 || len(notices[0].Request.Actions) != 1 || notices[0].Request.Actions[0].ID != "undo" {
		t.Fatal("no Activity notice with Undo", notices, err)
	}

	acted, err := run.f.a.book.DecideNotification(notices[0].ID, asks.NotificationAct, "undo", run.f.panel.Program, nil)
	if err != nil || acted.Outcome != askwire.NotificationDecisionOutcomeActed {
		t.Fatal(acted, err)
	}
	child := run.d.prepareAgentUpdate(run.ctx, run.f.panel, applications.AgentUpdateRequest{UndoOf: ready.Intent})
	if child.Outcome != applications.OutcomeApplied || child.Reason != "update_ready" || child.Question != "" {
		t.Fatal("Undo unavailable", child)
	}
	programs := map[string]string{}
	for _, entry := range done.Enrollments {
		programs[entry.Application] = entry.Program
	}
	for _, stage := range child.StagedImages {
		program := programs[stage.Application]
		if err := os.Rename(program, stage.Program+".previous"); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(stage.Program, program); err != nil {
			t.Fatal(err)
		}
	}
	restored := run.d.commitAgentUpdate(run.ctx, run.f.panel, child.Intent)
	if restored.Reason != "updated" || restored.Phase == nil || *restored.Phase != applications.AgentUpdatePhaseReady {
		t.Fatal("Undo did not finish", restored)
	}
	for _, entry := range restored.Enrollments {
		got, err := os.ReadFile(entry.Program)
		if err != nil || !bytes.Equal(got, first) {
			t.Fatal("Undo did not restore the kept image", entry.Application, err)
		}
		if entry.State != applications.AgentEnrollmentStateEnrolled {
			t.Fatal("Undo lost the Keep", entry)
		}
	}
	if agentQuestionCount(t, run) != questions {
		t.Fatal("Undo created a question")
	}
}

func TestAgentUpdateInstalledPreviewAsksOnceWhenUnproven(t *testing.T) {
	widened := func(t *testing.T, run *selfRegisteredRun, request applications.AgentUpdateRequest) {
		stageInstalledCopy(t, request, agentPreviewImage(agentReleaseFixture(t, nil), "preview with a tool"))
	}
	for name, c := range map[string]struct {
		trusted bool
		change  func(*testing.T, *selfRegisteredRun, applications.AgentUpdateRequest)
	}{
		"widened":       {true, widened},
		"user-writable": {false, nil},
		"malformed": {true, func(t *testing.T, _ *selfRegisteredRun, request applications.AgentUpdateRequest) {
			stageInstalledCopy(t, request, agentPreviewImage([]byte(`{"version":1}`), "preview"))
		}},
	} {
		t.Run(name, func(t *testing.T) {
			run, request, _ := installedConnectorFixture(t, c.trusted)
			if c.change != nil {
				c.change(t, run, request)
			}
			questions := agentQuestionCount(t, run)
			got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
			if got.Outcome != applications.OutcomeApplied || got.Reason != "permission_pending" || got.Question == "" {
				t.Fatal("update did not ask", got)
			}
			again := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
			if again.Question != got.Question || agentQuestionCount(t, run) != questions+1 {
				t.Fatal("update asked more than once", again)
			}
			if commit := run.d.commitAgentUpdate(run.ctx, run.f.panel, got.Intent); commit.Reason != "permission_pending" {
				t.Fatal("unanswered update applied", commit)
			}
		})
	}
}

// A shrinking declaration from the installed copy is non-widening.
func TestAgentUpdateInstalledPreviewShrinkingDeclarationIsSilent(t *testing.T) {
	run, request, _ := installedConnectorFixture(t, true)
	for _, m := range request.Members {
		old := m.StagedProgram[:len(m.StagedProgram)-len(".staged")]
		writeAgentImage(t, old, agentPreviewImage(agentReleaseFixture(t, nil), "kept with a tool "+m.Application))
	}
	stageInstalledCopy(t, request, agentPreviewImage(agentReleaseFixture(t, func(d map[string]any) { d["tools"] = []any{} }), "preview without tools"))
	got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if got.Reason != "update_ready" || got.Question != "" {
		t.Fatal("shrinking installed preview asked", got)
	}
}

// Bytes that differ from the installed copy never update a connection.
func TestAgentUpdateInstalledPreviewRefusesBytesOtherThanTheInstalledCopy(t *testing.T) {
	run, request, _ := installedConnectorFixture(t, true)
	writeAgentImage(t, request.Members[0].StagedProgram, agentPreviewImage(nil, "planted"))
	got := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if got.Outcome != applications.OutcomeConflict || got.Reason != "staged_image" || got.Intent != "" {
		t.Fatal("staged bytes other than the installed copy were accepted", got)
	}

	run, request, _ = installedConnectorFixture(t, true)
	ready := run.d.prepareAgentUpdate(run.ctx, run.f.panel, request)
	if ready.Reason != "update_ready" {
		t.Fatal(ready)
	}
	// The installed copy changes between Prepare and Commit.
	writeAgentImage(t, request.SourceProgram, agentPreviewImage(nil, "another install"))
	publishAgentUpdateFixture(t, request)
	if got := run.d.commitAgentUpdate(run.ctx, run.f.panel, ready.Intent); got.Reason == "updated" || got.Reason == "update_ready" {
		t.Fatal("changed installed copy still committed", got)
	}
	for _, m := range request.Members {
		if mark := run.d.unconfirmed[m.Application]; mark.RegistrationRevision != m.ExpectedRevision {
			t.Fatal("connection moved without proof", m.Application)
		}
	}
}
