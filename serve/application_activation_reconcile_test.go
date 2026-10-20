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

func interruptedActivation(t *testing.T, stage string) (*selfRegisteredRun, applications.ActivationConfiguration) {
	t.Helper()
	run, request, _ := activationConfigFixture(t)
	run.d.activationConfigFault = func(at string) error {
		if at == stage {
			return errors.New("interrupted")
		}
		return nil
	}
	if got := run.d.setActivation(run.ctx, run.f.panel, request, nil); got.Outcome != wire.ApplicationOutcomeUnavailable {
		t.Fatal(got)
	}
	run.d.activationConfigFault = nil
	return run, request
}

func TestReconcileActivationNoPendingOrStaleMakesNoEdit(t *testing.T) {
	run, request, _ := activationConfigFixture(t)
	if got := run.d.reconcileActivation(run.ctx, run.f.panel, request.Expected, nil); got.Outcome != wire.ApplicationOutcomeConflict || got.Reason != "activation_not_pending" {
		t.Fatal(got)
	}
	actual, _, err := run.d.agentSnapshot(run.ctx, request.Expected.Name)
	if err != nil || !reflect.DeepEqual(actual, request.Expected) {
		t.Fatal(actual, err)
	}
	stale := actual
	stale.Title = "stale"
	if got := run.d.reconcileActivation(run.ctx, run.f.panel, stale, nil); got.Outcome != wire.ApplicationOutcomeConflict {
		t.Fatal(got)
	}
	missing := actual
	missing.Name = "missing"
	if got := run.d.reconcileActivation(run.ctx, run.f.panel, missing, nil); got.Outcome != wire.ApplicationOutcomeUnknown {
		t.Fatal(got)
	}
}

func TestReconcileActivationRefusesSignedManifestAndPendingEnrollment(t *testing.T) {
	run, request, before := activationConfigFixture(t)
	revoked := agent1478Set(t, run, request.Expected.Name, before.Enrollment.Revision, false)
	agent1478Set(t, run, request.Expected.Name, revoked.Revision, true)
	if got := run.d.reconcileActivation(run.ctx, run.f.panel, request.Expected, nil); got.Outcome == wire.ApplicationOutcomeApplied {
		t.Fatal("pending enrollment admitted recovery", got)
	}
	if err := run.d.attachApplicationV3(run.ctx, registration1459Decoder); err != nil {
		t.Fatal(err)
	}
	registration1459Publish(t, run.d, "signed-recovery", run.f.comfy.Program, "Signed fixture", "operator", "")
	descriptor, _, err := run.d.agentSnapshot(run.ctx, "signed-recovery")
	if err != nil {
		t.Fatal(err)
	}
	if got := run.d.reconcileActivation(run.ctx, run.f.panel, descriptor, nil); got.Outcome != wire.ApplicationOutcomeForbidden || got.Reason != "signed_manifest" {
		t.Fatal(got)
	}
}

func TestReconcileActivationAuthorityAndNativeRefusals(t *testing.T) {
	for _, mode := range []string{"non_operator", "wrong_account", "revoked_manage", "revoked_manage_after_capture", "replaced", "native_unknown", "caller_changed", "keep_missing", "wrong_expected"} {
		t.Run(mode, func(t *testing.T) {
			run, request := interruptedActivation(t, "prepared")
			subject := run.f.panel
			expected := request.Expected
			guard := func() error { return nil }
			switch mode {
			case "non_operator":
				subject = run.f.cli
			case "wrong_account":
				subject.Account = "foreign"
			case "revoked_manage":
				run.d.decide = func(context.Context, rights.Subject, string, string) (bool, error) { return false, nil }
			case "revoked_manage_after_capture":
				checks := 0
				guard = func() error {
					checks++
					if checks == 2 {
						run.d.decide = func(context.Context, rights.Subject, string, string) (bool, error) { return false, nil }
					}
					return nil
				}
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
			case "keep_missing":
				run.d.mu.Lock()
				marks, _, err := run.d.readSelfMarksAtDescriptorLocked(run.ctx)
				run.d.mu.Unlock()
				if err != nil {
					t.Fatal(err)
				}
				if err := run.f.a.book.Forget(marks[expected.Name].Question); err != nil {
					t.Fatal(err)
				}
			case "wrong_expected":
				expected.Title = "changed"
			}
			if got := run.d.reconcileActivation(run.ctx, subject, expected, guard); got.Outcome == wire.ApplicationOutcomeApplied {
				t.Fatal(mode, got)
			}
			current, _, err := run.d.agentSnapshot(run.ctx, request.Expected.Name)
			if err != nil || !reflect.DeepEqual(current, request.Expected) {
				t.Fatal("refusal changed descriptor", current, err)
			}
		})
	}
}

func TestReconcileActivationClearedOrReplacedMarkerCannotBeRecreated(t *testing.T) {
	for _, mode := range []string{"cleared", "replaced"} {
		t.Run(mode, func(t *testing.T) {
			run, request := interruptedActivation(t, "prepared")
			checks := 0
			guard := func() error {
				checks++
				if checks != 2 {
					return nil
				}
				if got := run.d.setActivation(run.ctx, run.f.panel, request, nil); got.Outcome != wire.ApplicationOutcomeApplied {
					t.Fatal(got)
				}
				after := request.Expected
				after.Activation = request.Activation
				if got := run.d.setActivation(run.ctx, run.f.panel, applications.ActivationConfiguration{Expected: after, Activation: request.Expected.Activation}, nil); got.Outcome != wire.ApplicationOutcomeApplied {
					t.Fatal(got)
				}
				if mode == "replaced" {
					other := request
					recipe := *request.Activation
					recipe.Arguments = []string{"new-intent"}
					other.Activation = &recipe
					run.d.activationConfigFault = func(stage string) error {
						if stage == "prepared" {
							return errors.New("interrupted replacement")
						}
						return nil
					}
					if got := run.d.setActivation(run.ctx, run.f.panel, other, nil); got.Outcome != wire.ApplicationOutcomeUnavailable {
						t.Fatal(got)
					}
					run.d.activationConfigFault = nil
				}
				return nil
			}
			if got := run.d.reconcileActivation(run.ctx, run.f.panel, request.Expected, guard); got.Outcome != wire.ApplicationOutcomeConflict {
				t.Fatal("captured recovery recreated old intent", got)
			}
			current, _, err := run.d.agentSnapshot(run.ctx, request.Expected.Name)
			if err != nil || !reflect.DeepEqual(current, request.Expected) {
				t.Fatal(current, err)
			}
			run.d.mu.Lock()
			marks, _, err := run.d.readSelfMarksAtDescriptorLocked(run.ctx)
			run.d.mu.Unlock()
			if err != nil {
				t.Fatal(err)
			}
			pending := marks[request.Expected.Name].Agent.Activation
			if mode == "cleared" && pending != nil || mode == "replaced" && (pending == nil || pending.After.Activation.Arguments[0] != "new-intent") {
				t.Fatalf("race changed WAL: %+v", pending)
			}
		})
	}
}
