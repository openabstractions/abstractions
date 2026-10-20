package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
)

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
	if view.Permission.Question != "Allow this app to read the secret value?" || !strings.Contains(view.Permission.Scope, "This app can view, copy or use the secret value anywhere.") {
		t.Fatalf("compact raw Read consequence/question lost: %+v", view.Permission)
	}
	if !strings.Contains(view.Permission.Scope, "One: Saved app secret “alpha”") || view.Permission.Effects != "Ongoing access until removed." {
		t.Fatal("resource or lifetime lost")
	}
	if view.Level != promptLevelGray || view.Permission.RequesterProgram != details.Effect.Subject.Program {
		t.Fatal("recorded publisher/requester binding lost")
	}
	full := details.Display.Selections[0].Disclosure
	if !strings.Contains(view.Details, full) || !strings.Contains(view.Content, full) {
		t.Fatal("complete owning disclosure or existing Mac content changed")
	}
	serialized, e := json.Marshal(view)
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(serialized), view.Permission.Question) || !strings.Contains(string(serialized), `"Level":"gray"`) || strings.Contains(string(serialized), "This app can view, copy or use") {
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
	if !strings.Contains(view.Permission.Scope, "Use") || !strings.Contains(view.Permission.Question, "use") {
		t.Fatal("empty disclosure dropped the sole finite operation")
	}
}

func TestTypedPermissionWindowsAllBundleStillShowsEveryMemberAndBreadth(t *testing.T) {
	record, details := typedNativeFixture(t, true, true)
	details.Effect.Selections[1].Permission.Action = credentials.ActionSecretRead
	details.Effect.Selections[1].Permission.ResourceProfile = credentials.NamedSecretResourceProfile
	details.Effect.Bundle.Selections = slices.Clone(details.Effect.Selections)
	view := trayTypedPermissionView(record, details, nil)
	visible := view.Permission.Scope + "\n" + *view.Permission.Warning
	for _, member := range details.Display.Selections {
		if !strings.Contains(visible, member.Operation) {
			t.Fatalf("finite operation %q hidden", member.Operation)
		}
	}
	for _, required := range []string{"Full control", "six operations", "All: saved app secrets", "current and future", "Runtime-managed keys are excluded", "view, copy or use the secret value anywhere"} {
		if !strings.Contains(visible, required) {
			t.Fatalf("default visible %q lost: %s", required, visible)
		}
	}
	if strings.Contains(visible, "administrator") || !strings.Contains(view.Details, details.Display.Selections[1].Disclosure) {
		t.Fatal("breadth became OS privilege or exact raw export details lost")
	}
}
