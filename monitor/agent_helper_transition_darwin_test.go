package main

import (
	"context"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/fileid"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDarwinVersionedHelperEnrollmentCollisionPreservesPriorProgram(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "oa-claude-integration-"+strings.Repeat("a", 64))
	foreign := filepath.Join(root, "oa-claude-integration-"+strings.Repeat("a", 32)+strings.Repeat("b", 32))
	for _, path := range []string{program, foreign} {
		if err := os.WriteFile(path, []byte("inert image"), 0700); err != nil {
			t.Fatal(err)
		}
	}
	id, err := fileid.Resolve(context.Background(), fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	image := &claudePanelFixtureImage{program: program, key: id.Key()}
	name := agentImageApplication(image, claudeIntegrationApplication)
	if len(name) > 64 || name != claudeIntegrationApplication+"-"+strings.Repeat("a", 32) {
		t.Fatal(name)
	}
	registered := false
	sets := 0
	entry := applications.AgentEnrollment{Application: name, Program: program, Revision: "fresh", State: applications.AgentEnrollmentStateNone}
	native := claudeContextEnrollmentClient{read: func(_ context.Context, application string) (applications.AgentEnrollmentResult, error) {
		if application != name {
			t.Fatal(application)
		}
		if !registered {
			return applications.AgentEnrollmentResult{Outcome: applications.OutcomeUnknown}, nil
		}
		return applications.AgentEnrollmentResult{Outcome: applications.OutcomePage, Enrollment: &entry}, nil
	}, set: func(request applications.AgentEnrollmentRequest) (applications.AgentEnrollmentResult, error) {
		sets++
		if request.Application != name || request.ExpectedRevision != "fresh" {
			t.Fatal(request)
		}
		pending := entry
		pending.State = applications.AgentEnrollmentStatePending
		return applications.AgentEnrollmentResult{Outcome: applications.OutcomeApplied, Enrollment: &pending}, nil
	}}
	register := func(_ context.Context, d wire.ApplicationDescriptor) (wire.ApplicationChange, error) {
		if d.Name != name || d.Program != program {
			t.Fatal(d)
		}
		if err := client.ValidApplicationDescriptor(d); err != nil {
			t.Fatal(err)
		}
		registered = true
		return wire.ApplicationChange{Outcome: wire.ApplicationOutcomeApplied}, nil
	}
	if state, err := requestClaudeEnrollment(context.Background(), image, native, register); err != nil || state != "pending" || sets != 1 {
		t.Fatal(state, err, sets)
	}
	entry.Program = foreign
	entry.State = applications.AgentEnrollmentStateEnrolled
	if _, err := requestClaudeEnrollment(context.Background(), image, native, register); err == nil || sets != 1 {
		t.Fatal("colliding label admitted", err, sets)
	}
}

func TestDarwinStartupSelectsExactSavedVersionProgram(t *testing.T) {
	name := claudeIntegrationApplication + "-" + strings.Repeat("a", 32)
	program := "/owned/oa-claude-integration-" + strings.Repeat("a", 64)
	page := wire.ApplicationPage{Outcome: wire.ApplicationOutcomePage, Applications: []wire.ApplicationEntry{
		{Scope: wire.ScopeLocal, Descriptor: wire.ApplicationDescriptor{Name: claudeIntegrationApplication, Program: "/owned/old"}},
		{Scope: wire.ScopeLocal, Descriptor: wire.ApplicationDescriptor{Name: name, Program: program}},
	}}
	got, err := claudeStartupDescriptorSelected(page, name, program)
	if err != nil || got.Name != name || got.Program != program {
		t.Fatal(got, err)
	}
	if _, err := claudeStartupDescriptorSelected(page, name, program+"-changed"); err == nil {
		t.Fatal("stale program accepted")
	}
	page.Applications = append(page.Applications, page.Applications[1])
	if _, err := claudeStartupDescriptorSelected(page, name, program); err == nil {
		t.Fatal("ambiguous descriptor accepted")
	}
}
