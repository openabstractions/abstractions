package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

func activationConfigFixture(t *testing.T) (*selfRegisteredRun, applications.ActivationConfiguration, applications.AgentEnrollmentResult) {
	t.Helper()
	run := agent1478Fixture(t)
	agent1478Register(t, run, "config-agent", run.f.answerer.Program)
	agent1478Set(t, run, "config-agent", "", true)
	q := agent1478Question(t, run, "config-agent")
	answer(t, run.f, q.ID, "keep")
	agent1478Wait(t, run, "config-agent", "enrolled")
	before := run.d.currentAgentEnrollment(run.ctx, "config-agent")
	descriptor, _, err := run.d.agentSnapshot(run.ctx, "config-agent")
	if err != nil {
		t.Fatal(err)
	}
	return run, applications.ActivationConfiguration{Expected: descriptor, Activation: &wire.ApplicationActivationRecipe{Arguments: []string{"fixed"}, Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 1000}}, before
}

func TestActivationConfigurationPreservesKeepAndConditionalReplay(t *testing.T) {
	run, request, before := activationConfigFixture(t)
	question := agent1478Question(t, run, "config-agent")
	for i := 0; i < 2; i++ {
		if out := run.d.setActivation(run.ctx, run.f.panel, request, nil); out.Outcome != wire.ApplicationOutcomeApplied {
			t.Fatal(out)
		}
	}
	if after := run.d.currentAgentEnrollment(run.ctx, "config-agent"); !reflect.DeepEqual(before, after) {
		t.Fatal(before, after)
	}
	if after := agent1478Question(t, run, "config-agent"); !reflect.DeepEqual(question, after) {
		t.Fatal("person answer changed")
	}
	stale := request
	stale.Expected.Title = "changed"
	if out := run.d.setActivation(run.ctx, run.f.panel, stale, nil); out.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal(out)
	}
	current, _, err := run.d.agentSnapshot(run.ctx, "config-agent")
	if err != nil {
		t.Fatal(err)
	}
	if out := run.d.setActivation(run.ctx, run.f.panel, applications.ActivationConfiguration{Expected: current}, nil); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	current, _, _ = run.d.agentSnapshot(run.ctx, "config-agent")
	if current.Activation != nil {
		t.Fatal("disable retained activation")
	}
	if after := run.d.currentAgentEnrollment(run.ctx, "config-agent"); !reflect.DeepEqual(before, after) {
		t.Fatal(before, after)
	}
}

func TestActivationConfigurationPartialWriteRestartExactRecovery(t *testing.T) {
	for _, stage := range []string{"prepared", "descriptor"} {
		t.Run(stage, func(t *testing.T) {
			run, request, before := activationConfigFixture(t)
			run.d.activationConfigFault = func(at string) error {
				if at == stage {
					return errors.New("interrupted")
				}
				return nil
			}
			if out := run.d.setActivation(run.ctx, run.f.panel, request, nil); out.Outcome != wire.ApplicationOutcomeUnavailable {
				t.Fatal(out)
			}
			if current := run.d.currentAgentEnrollment(run.ctx, "config-agent"); current.Enrollment != nil && current.Enrollment.State == applications.AgentEnrollmentStateEnrolled {
				t.Fatal("partial edit retained class")
			}
			run.start(t)
			agent1478Attach(t, run)
			wrong := request
			copyRecipe := *request.Activation
			copyRecipe.Arguments = []string{"other"}
			wrong.Activation = &copyRecipe
			if out := run.d.setActivation(run.ctx, run.f.panel, wrong, nil); out.Outcome != wire.ApplicationOutcomeConflict {
				t.Fatal("different edit reconciled marker", out)
			}
			if out := run.d.setActivation(run.ctx, run.f.panel, request, nil); out.Outcome != wire.ApplicationOutcomeApplied {
				t.Fatal(out)
			}
			if current := run.d.currentAgentEnrollment(run.ctx, "config-agent"); !reflect.DeepEqual(before, current) {
				t.Fatal(before, current)
			}
		})
	}
}

func TestActivationConfigurationAuthorityPendingAndNativeRefusals(t *testing.T) {
	for _, mode := range []string{"non_operator", "wrong_account", "revoked_manage", "pending", "replaced", "native_unknown", "caller_changed"} {
		t.Run(mode, func(t *testing.T) {
			run, request, _ := activationConfigFixture(t)
			subject := run.f.panel
			guard := func() error { return nil }
			switch mode {
			case "non_operator":
				subject = run.f.cli
			case "wrong_account":
				subject.Account = "foreign"
			case "revoked_manage":
				run.d.decide = func(context.Context, rights.Subject, string, string) (bool, error) { return false, nil }
			case "pending":
				current := run.d.currentAgentEnrollment(run.ctx, "config-agent")
				revoked := agent1478Set(t, run, "config-agent", current.Enrollment.Revision, false)
				agent1478Set(t, run, "config-agent", revoked.Revision, true)
			case "replaced":
				read := run.d.agentImage
				run.d.agentImage = func(ctx context.Context, program, binding string) (applicationAgentImage, error) {
					image, err := read(ctx, program, binding)
					image.Key = "replacement"
					return image, err
				}
			case "native_unknown":
				run.d.agentImage = func(context.Context, string, string) (applicationAgentImage, error) {
					return applicationAgentImage{}, errors.New("unknown")
				}
			case "caller_changed":
				guard = func() error { return errors.New("changed") }
			}
			if out := run.d.setActivation(run.ctx, subject, request, guard); out.Outcome == wire.ApplicationOutcomeApplied {
				t.Fatal(mode, out)
			}
		})
	}
}

func TestActivationConfigurationRefusesSignedManifestAndInvalidRecipe(t *testing.T) {
	run, request, _ := activationConfigFixture(t)
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	registration1459Publish(t, run.d, "signed-agent", run.f.comfy.Program, "Signed fixture", "operator", "")
	descriptor, _, err := run.d.agentSnapshot(run.ctx, "signed-agent")
	if err != nil {
		t.Fatal(err)
	}
	if out := run.d.setActivation(run.ctx, run.f.panel, applications.ActivationConfiguration{Expected: descriptor, Activation: request.Activation}, nil); out.Outcome != wire.ApplicationOutcomeForbidden || out.Reason != "signed_manifest" {
		t.Fatal(out)
	}
	bad := request
	recipe := *request.Activation
	recipe.ReadinessTimeoutMs = 0
	bad.Activation = &recipe
	if out := run.d.setActivation(run.ctx, run.f.panel, bad, nil); out.Outcome != wire.ApplicationOutcomeInvalid {
		t.Fatal(out)
	}
	if out := run.d.setActivation(run.ctx, run.f.panel, request, nil); out.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(out)
	}
	if projected := run.d.descriptors["signed-agent"]; projected.Name != "signed-agent" || projected.Title != "Signed fixture" {
		t.Fatal("legacy edit hid unrelated signed-manifest projection", projected)
	}
}
