package main

import (
	"strings"
	"testing"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
)

// AB-1m: the person allows one direction between two assistants on this
// computer in the native permission prompt. It names the sending program and
// the receiving assistant, states what the receiver may then do, offers
// Never allow, and its default choice grants nothing.
func TestMessageDirectionPermissionPrompt(t *testing.T) {
	program := `C:\apps\claude\oa-claude.exe`
	for _, c := range []struct{ action, resource, request string }{
		{grants.ApplicationMessageAction, "app:codex", "send messages to “codex”"},
		{grants.ApplicationMessageStartAction, "app:codex/start:" + strings.Repeat("b", 64), "give “codex” tasks in your “codex” chats"},
	} {
		record := firstUseFixture("direction", program, c.action, c.resource)
		permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "account", Program: program, Action: c.action, Resource: c.resource}}}
		view := trayQuestionViewForPermission(record, trayPublisher{}, permission)
		if view.Permission == nil || view.Permission.Instruction != "Allow oa-claude (not registered) to "+c.request+"?" {
			t.Fatalf("direction prompt: %+v", view.Permission)
		}
		if view.Default != "refuse" || !hasTrayOption(record.Options, "never") || !strings.Contains(view.Details, "Never allow adds an ongoing block") {
			t.Fatalf("direction prompt choices: %+v", view)
		}
		if view.Permission.Effects != "" || !strings.Contains(view.Permission.Details, "Allow adds an ongoing permission") {
			t.Fatalf("direction prompt effects: %q", view.Permission.Effects)
		}
		for _, exact := range []string{program, c.action, c.resource} {
			if !strings.Contains(view.Details, exact) {
				t.Fatal("direction prompt lost its exact target", exact)
			}
		}
	}
}
