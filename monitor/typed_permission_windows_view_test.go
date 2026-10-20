package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
)

func TestKeptGraphEditUsesOnlyItsLockedConsentChoices(t *testing.T) {
	record, details := typedNativeFixture(t, false, false)
	ordinary := trayTypedPermissionView(record, details, nil)
	if !slices.Equal(ordinary.Choices, []trayQuestionChoice{{Label: "Allow", Option: "allow"}, {Label: "Not now", Option: "refuse"}}) {
		t.Fatalf("ordinary choices changed: %+v", ordinary.Choices)
	}
	effect, err := grants.KeptGraphEditPermission(details.Effect.Subject, grants.KeptApplicationClassTarget{
		Application: "comfyui", Keep: strings.Repeat("a", 48), Command: grants.GraphEditCommand,
		Class: "edit", Enrollment: strings.Repeat("b", 48),
	}, "ComfyUI")
	if err != nil {
		t.Fatal(err)
	}
	record.Options = []string{"once", "allow", "refuse"}
	details.Effect, details.Display = &effect.Effect, &effect.Display
	details.Origin, details.Purpose, details.EffectDigest = effect.Origin, effect.Purpose, effect.Digest
	view := trayTypedPermissionView(record, details, nil)
	want := []trayQuestionChoice{{Label: "Allow once", Option: "once"}, {Label: "Always allow", Option: "allow"}, {Label: "Not now", Option: "refuse"}}
	if !slices.Equal(view.Choices, want) || view.Default != "refuse" || view.Dismiss != "refuse" || view.Permission.Question != "Allow Recorded App to edit workflows in ComfyUI?" {
		t.Fatalf("graph edit consent: %+v", view)
	}
	occurrence, err := questionOccurrence(record, view)
	if err != nil || occurrence.Authority() != asks.TypedPermissionKey {
		t.Fatalf("graph edit escaped locked consent: %+v %v", occurrence, err)
	}
}

func TestTypedPermissionWindowsRawReadSummaryAndRecordedIdentity(t *testing.T) {
	record, details := typedNativeFixture(t, false, true)
	details.Effect.Selections = slices.Clone(details.Effect.Selections[1:2])
	details.Effect.Selections[0].Permission.Action = credentials.ActionSecretRead
	details.Effect.Selections[0].Permission.ResourceProfile = credentials.NamedSecretResourceProfile
	details.Effect.Bundle = nil
	details.Display.Selections = slices.Clone(details.Display.Selections[1:2])
	details.Display.BundleLabel, details.Display.BundleDisclosure = "", ""
	details.EffectDigest = asks.PermissionEffectDigest(*details.Effect)
	view := trayTypedPermissionView(record, details, nil)
	if view.Permission.Question != "Allow Recorded App to read the saved key?" || view.Permission.Scope != "It can copy and use the key anywhere." {
		t.Fatalf("compact raw Read consequence/question lost: %+v", view.Permission)
	}
	if !strings.Contains(view.Permission.Details, "One: Saved app secret “alpha”") || view.Permission.Effects != "" || !strings.Contains(view.Permission.Details, "Lifetime: Ongoing, until removed.") {
		t.Fatal("resource or lifetime lost")
	}
	if view.Level != promptLevelBlue || view.Permission.RequesterProgram != details.Effect.Subject.Program {
		t.Fatal("recorded publisher/requester binding lost")
	}
	full := details.Display.Selections[0].Disclosure
	if !strings.Contains(view.Details, full) || strings.Contains(view.Content, full) {
		t.Fatal("complete owning disclosure left the details")
	}
	serialized, e := json.Marshal(view)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(serialized), `"Permission":`) || !strings.Contains(string(serialized), `"Level":"blue"`) || strings.Contains(string(serialized), "This app can view, copy or use") {
		t.Fatal("private Windows summary changed serialized Mac input")
	}
	details.Question.Question.PublisherTrusted = false
	details.Question.Question.Publisher = "Verified Administrator"
	details.Purpose = "Publisher is verified and this request is operating-system authorization."
	unknown := trayTypedPermissionView(record, details, nil)
	if unknown.Level != promptLevelYellow || strings.Contains(unknown.Permission.Publisher, "Verified Administrator") || strings.Contains(unknown.Permission.Scope, "operating-system authorization") {
		t.Fatal("untrusted prose upgraded identity or OS privilege")
	}
}

func TestTypedPermissionWindowsSingleEmptyDisclosureKeepsOperation(t *testing.T) {
	record, details := typedNativeFixture(t, false, false)
	details.Display.Selections[0].Disclosure = ""
	view := trayTypedPermissionView(record, details, nil)
	if !strings.Contains(view.Details, "Operation 1: Use") || view.Permission.Question != "Allow Recorded App to use saved app secret “alpha”?" {
		t.Fatal("empty disclosure dropped the sole finite operation", view.Permission.Question)
	}
}

func TestTypedPermissionWindowsAllBundleStillShowsEveryMemberAndBreadth(t *testing.T) {
	record, details := typedNativeFixture(t, true, true)
	details.Effect.Selections[1].Permission.Action = credentials.ActionSecretRead
	details.Effect.Selections[1].Permission.ResourceProfile = credentials.NamedSecretResourceProfile
	details.Effect.Bundle.Selections = slices.Clone(details.Effect.Selections)
	view := trayTypedPermissionView(record, details, nil)
	visible := view.Permission.Question + "\n" + view.Permission.Scope + "\n" + *view.Permission.Warning
	for _, member := range details.Display.Selections {
		if !strings.Contains(view.Details, member.Operation) {
			t.Fatalf("finite operation %q left the details", member.Operation)
		}
	}
	for _, required := range []string{"Full control", "six operations", "All: saved app secrets", "current and future", "Runtime-managed keys are excluded"} {
		if !strings.Contains(view.Details, required) {
			t.Fatalf("detail %q lost: %s", required, view.Details)
		}
	}
	for _, required := range []string{"full control of all saved app secrets", "It can copy and use the key anywhere."} {
		if !strings.Contains(visible, required) {
			t.Fatalf("visible %q lost: %s", required, visible)
		}
	}
	if strings.Contains(visible, "administrator") || !strings.Contains(view.Details, details.Display.Selections[1].Disclosure) {
		t.Fatal("breadth became OS privilege or exact raw export details lost")
	}
}
