package main

import (
	"strings"
	"testing"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
)

func TestCredentialDefinitionProjectsSameNamedSlotDisclosureToNativeAndWeb(t *testing.T) {
	resource := credentials.ResourceFor("demo-secret")
	record := firstUseFixture("definition-view", `C:\Apps\demo.exe`, credentials.ActionApply, resource)
	// Current structured permission data is authoritative even when display prose
	// contains unrelated words. No English reader selects this target.
	record.Text = "unrelated display text"
	permission := askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "test-account", Program: record.About, Action: credentials.ActionApply, Resource: resource}}}
	prompt := trayQuestionViewForPermission(record, trayPublisher{}, &permission)
	if prompt.Permission == nil || !strings.Contains(prompt.Permission.Question, "credential “demo-secret”") {
		t.Fatal("native named Use target missing")
	}
	disclosure := grants.FirstUseDisclosure(credentials.ActionApply, resource)
	for _, text := range []string{prompt.Content, prompt.Permission.Content, prompt.Permission.Scope, firstUseSentence(trayPublisher{}, record.About, credentials.ActionApply, resource)} {
		if !strings.Contains(text, disclosure) {
			t.Fatal("native/web definition disclosure diverged")
		}
	}
	for _, text := range []string{prompt.Details, prompt.Permission.Details} {
		if !strings.Contains(text, grants.FirstUseDefinitionDetails(credentials.ActionApply, resource)) {
			t.Fatal("complete named-slot detail missing")
		}
	}
	for _, technical := range []string{"account-owned", "designated service", "freezes no current"} {
		if strings.Contains(prompt.Permission.Scope, technical) {
			t.Fatal("main surface contains technical definition prose")
		}
	}
	if strings.Contains(prompt.Permission.Scope, "Applies to this app") {
		t.Fatal("named exact target retains generic scope boilerplate")
	}
	if !strings.Contains(prompt.Permission.Effects, "ongoing access") || !strings.Contains(prompt.Permission.Effects, "ongoing block") {
		t.Fatal("recorded rule effects missing")
	}
	for _, target := range []string{"Account: test-account", "Action: " + credentials.ActionApply, "Resource: " + resource} {
		if !strings.Contains(prompt.Permission.Details, target) {
			t.Fatal("exact structured details missing")
		}
	}
}
