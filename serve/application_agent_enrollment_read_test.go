package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	identity "github.com/openabstractions/abstraction-identity"
)

func TestApplicationAgentEnrollmentReadOwnAndGrantedOtherDisclosure(t *testing.T) {
	run := agent1478Fixture(t)
	agent1478Register(t, run, "agent", run.f.answerer.Program)
	view := agent1478Set(t, run, "agent", "", true)
	answer(t, run.f, agent1478Question(t, run, "agent").ID, "keep")
	agent1478Wait(t, run, "agent", "enrolled")
	own := run.d.readAgentEnrollment(run.ctx, run.f.answerer, "agent")
	if own.Outcome != applications.OutcomePage || own.Enrollment == nil || !identity.SameSubjectProgram(own.Enrollment.Program, run.f.answerer.Program) || own.Enrollment.State != applications.AgentEnrollmentStateEnrolled || own.Enrollment.Revision != view.Revision {
		t.Fatal(own)
	}
	other := run.f.comfy
	if result := run.d.readAgentEnrollment(run.ctx, other, "agent"); result.Outcome != applications.OutcomeForbidden || result.Enrollment != nil {
		t.Fatal("ungranted other read", result)
	}
	if err := run.f.a.rights.policy.RegisterAction(ActionApplicationRead); err != nil {
		t.Fatal(err)
	}
	if err := run.f.a.rights.policy.Set(other, ActionApplicationRead, "app:agent", true); err != nil {
		t.Fatal(err)
	}
	result := run.d.readAgentEnrollment(run.ctx, other, "agent")
	if result.Outcome != applications.OutcomePage || result.Enrollment == nil || result.Enrollment.Program != "" || result.Enrollment.State != own.Enrollment.State || result.Enrollment.Revision != own.Enrollment.Revision {
		t.Fatal("other disclosure", result)
	}
	if result := run.d.readAgentEnrollment(run.ctx, run.f.panel, "agent"); result.Enrollment == nil || !identity.SameSubjectProgram(result.Enrollment.Program, run.f.answerer.Program) {
		t.Fatal("operator path lost", result)
	}
	if err := run.f.a.rights.policy.Set(other, ActionApplicationRead, "app:agent", false); err != nil {
		t.Fatal(err)
	}
	if result := run.d.readAgentEnrollment(run.ctx, other, "agent"); result.Outcome != applications.OutcomeForbidden || result.Enrollment != nil {
		t.Fatal("revoked other read", result)
	}
	// The shared gateway's native subject receives no authority from its host.
	agent1478Register(t, run, "gateway", run.f.cli.Program)
	if result := run.d.readAgentEnrollment(run.ctx, run.f.cli, "gateway"); result.Enrollment == nil || !identity.SameSubjectProgram(result.Enrollment.Program, run.f.cli.Program) {
		t.Fatal(result)
	}
	if result := run.d.readAgentEnrollment(run.ctx, run.f.cli, "agent"); result.Outcome != applications.OutcomeForbidden || result.Enrollment != nil {
		t.Fatal("gateway exceeded own scope", result)
	}
}

func TestApplicationAgentEnrollmentReadDoesNotAdmitOperatorWrites(t *testing.T) {
	run := agent1478Fixture(t)
	agent1478Register(t, run, "agent", run.f.answerer.Program)
	// A person-grantable manage rule does not put an agent on the native
	// presenter's fixed operator image list.
	if err := run.f.a.rights.policy.Set(run.f.answerer, ActionApplicationManage, "account", true); err != nil {
		t.Fatal(err)
	}
	if err := run.f.a.rights.policy.RegisterAction(ActionApplicationRead); err != nil {
		t.Fatal(err)
	}
	if err := run.f.a.rights.policy.Set(run.f.answerer, ActionApplicationRead, "app:agent", true); err != nil {
		t.Fatal(err)
	}
	if result := run.d.setAgentEnrollment(run.ctx, run.f.answerer, applications.AgentEnrollmentRequest{Application: "agent", Enabled: true}); result.Outcome != applications.OutcomeForbidden {
		t.Fatal(result)
	}
	if result := run.d.prepareAgentUpdate(run.ctx, run.f.answerer, applications.AgentUpdateRequest{}); result.Outcome != applications.OutcomeForbidden {
		t.Fatal(result)
	}
	if result := run.d.findAgentUpdate(run.ctx, run.f.answerer, applications.AgentUpdateRequest{}); result.Outcome != applications.OutcomeForbidden {
		t.Fatal(result)
	}
	if result := run.d.commitAgentUpdate(run.ctx, run.f.answerer, "intent"); result.Outcome != applications.OutcomeForbidden {
		t.Fatal(result)
	}
}

// CONNECTOR-HANDOVER-AUTH: after Setup publishes over a running connector,
// Windows names that helper by its moved image, and every new connection it
// opens reaches the runtime under that path. Such a caller reads its
// enrollment only with the key of the image the person kept before that
// application's latest committed update
// (TestMovedConnectorReadsOnlyItsKeptApplicationEnrollment answers one). With
// no committed update its read is refused, and an application.read grant
// changes nothing: every rights decision for it stays denied.
func TestApplicationAgentEnrollmentReadRefusesMovedConnectorCaller(t *testing.T) {
	run := agent1478Fixture(t)
	agent1478Register(t, run, "agent", run.f.answerer.Program)
	agent1478Set(t, run, "agent", "", true)
	answer(t, run.f, agent1478Question(t, run, "agent").ID, "keep")
	agent1478Wait(t, run, "agent", "enrolled")
	moved := run.f.answerer
	moved.Program = filepath.Join(filepath.Dir(run.f.answerer.Program), "agent-integrations", "oa-claude-integration.exe."+strings.Repeat("ab", 32)+".tmp.previous")
	if !movedConnectorCaller(moved.Program) {
		t.Fatal("fixture path is not a moved connector")
	}
	// A real moved file: its key is not a kept image's.
	if err := os.MkdirAll(filepath.Dir(moved.Program), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(moved.Program, []byte("moved copy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if result := run.d.readAgentEnrollment(run.ctx, moved, "agent"); result.Outcome != applications.OutcomeForbidden || result.Enrollment != nil {
		t.Fatal("a moved connector read its enrollment", result)
	}
	if err := run.f.a.rights.policy.RegisterAction(ActionApplicationRead); err != nil {
		t.Fatal(err)
	}
	if err := run.f.a.rights.policy.Set(moved, ActionApplicationRead, "app:agent", true); err != nil {
		t.Fatal(err)
	}
	if result := run.d.readAgentEnrollment(run.ctx, moved, "agent"); result.Outcome != applications.OutcomeForbidden || result.Enrollment != nil {
		t.Fatal("a grant admitted a moved connector's read", result)
	}
	// The same read from the enrolled path itself answers enrolled.
	if own := run.d.readAgentEnrollment(run.ctx, run.f.answerer, "agent"); own.Outcome != applications.OutcomePage || own.Enrollment == nil || own.Enrollment.State != applications.AgentEnrollmentStateEnrolled {
		t.Fatal(own)
	}
}
