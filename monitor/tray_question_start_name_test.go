package main

import (
	"strings"
	"testing"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/grants"
)

// Review 31 (1): the start permit's question names the receiver by its
// registered display name, filled in when the popup is shown, and says the
// receiver works also while the person is away. Without a registered title it
// falls back to the registered name.
func TestStartPermissionNamesReceiverByDisplayName(t *testing.T) {
	sender := `C:\apps\claude.exe`
	resource := "app:codex/start:" + strings.Repeat("b", 64)
	record := firstUseFixture("start-display", sender, grants.ApplicationMessageStartAction, resource)
	permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "account", Program: sender, Action: grants.ApplicationMessageStartAction, Resource: resource}}}
	names := newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{Name: "codex", Title: "Codex", Program: `C:\apps\codex.exe`}}})
	view := trayQuestionViewForPermission(record, trayPublisher{Names: names}, permission)
	if view.Permission == nil {
		t.Fatal(view)
	}
	shown := view.Content + view.Permission.Content + view.Permission.Instruction
	if view.Permission.Instruction != "Allow claude (not registered) to give Codex tasks in your Codex chats?" || strings.Contains(shown, "“codex”") {
		t.Fatal("display name not filled in", shown)
	}
	plain := trayQuestionViewForPermission(record, trayPublisher{}, permission)
	if plain.Permission.Instruction != "Allow claude (not registered) to give “codex” tasks in your “codex” chats?" {
		t.Fatal("registered-name fallback", plain.Permission.Instruction)
	}
}

// The start permit is bound to its body sentence (serve's
// messageStartWording): the popup shows it, and its details say the receiver
// reads any of its conversations, this direction only.
func TestStartPermissionShowsItsBoundSentence(t *testing.T) {
	sender := `C:\apps\claude.exe`
	resource := "app:codex/start:" + strings.Repeat("b", 64)
	record := firstUseFixture("start-body", sender, grants.ApplicationMessageStartAction, resource)
	permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "account", Program: sender, Action: grants.ApplicationMessageStartAction, Resource: resource}}}
	names := newProgramTitler([]fwire.ApplicationEntry{{Scope: fwire.ScopeLocal, Descriptor: fwire.ApplicationDescriptor{Name: "codex", Title: "Codex", Program: `C:\apps\codex.exe`}}})
	view := trayQuestionViewForPermission(record, trayPublisher{}.forPrompt(names), permission)
	if view.Instruction != "Allow claude to give Codex tasks in your Codex chats?" || view.Permission.Requester != "claude" {
		t.Fatalf("heading %q requester %q", view.Instruction, view.Permission.Requester)
	}
	if body := grants.MessageStartBody("Codex"); view.Permission.Scope != body || !strings.HasPrefix(view.Content, body) {
		t.Fatalf("scope %q content %q", view.Permission.Scope, view.Content)
	}
	if !strings.Contains(view.Details, grants.MessageStartDetails("Codex")) {
		t.Fatalf("details %q", view.Details)
	}
}
