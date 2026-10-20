package main

import (
	"context"
	"strings"
	"testing"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

// No popup heading or identity row says "(not registered)": a program no
// title names reads as its file stem (owner, popup wording 2026-11-10). The
// Panel's own pages keep their wording.
func TestPopupNamesAnUnregisteredProgramByItsStem(t *testing.T) {
	opencode := nativePresenterProgram("opencode", "opencode.exe")
	check := func(name string, view trayQuestionPresentation) {
		t.Helper()
		visible := []string{view.Instruction, view.Content}
		if view.Permission != nil {
			visible = append(visible, view.Permission.Question, view.Permission.Requester, view.Permission.Content, view.Permission.Publisher)
		}
		for _, text := range visible {
			if strings.Contains(text, "(not registered)") {
				t.Fatalf("%s shows %q", name, text)
			}
		}
	}

	record := firstUseFixture("first-use", opencode, "abstraction.inference/complete", "host:llama")
	permission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "account", Program: opencode, Action: "abstraction.inference/complete", Resource: "host:llama"}}}
	firstUse := trayQuestionViewForPermission(record, trayPublisher{}.forPrompt(nil), permission)
	check("first use", firstUse)
	if firstUse.Instruction != "Allow opencode to use AI models?" || firstUse.Permission.Requester != "opencode" {
		t.Fatalf("first use heading %q requester %q", firstUse.Instruction, firstUse.Permission.Requester)
	}

	spendRecord := firstUseFixture("spend", opencode, "abstraction.credentials/apply", "credential:oa-panel-deepseek")
	spendPermission := &askclient.PermissionRequest{Targets: []askclient.PermissionTarget{{Account: "account", Program: opencode, Action: "abstraction.credentials/apply", Resource: "credential:oa-panel-deepseek"}}}
	spend := firstSpendPrompt(context.Background(), trayQuestionViewForPermission(spendRecord, trayPublisher{}.forPrompt(nil), spendPermission), spendPermission,
		func(context.Context, string) (spendLimit, bool) {
			return spendLimit{Label: "DeepSeek", Micros: 5_000_000}, true
		})
	check("spend", spend)
	if spend.Instruction != "Allow opencode to use your DeepSeek account?" {
		t.Fatalf("spend heading %q", spend.Instruction)
	}

	typedRecord, typed := typedNativeFixture(t, false, false)
	check("typed", trayTypedPermissionView(typedRecord, typed, nil))
	if got := permissionRequesterTitle(nil, typed); strings.Contains(got, "(not registered)") || got == "" {
		t.Fatalf("typed requester %q", got)
	}

	choice := choiceFixture()
	choice.PublisherTrusted, choice.Publisher, choice.ProgramDescription = false, "", ""
	choice.Asker = `C:\Tools\fixture-app.exe`
	check("app choice", trayChoiceView(choice, nil))

	if got := senderName(askclient.NotificationRecord{Program: opencode}, nil); got != "opencode" {
		t.Fatalf("notification sender %q", got)
	}
	// The Panel's pages still say a program is not registered.
	if got := programDisplayName(trayPublisher{}, opencode); got != "opencode (not registered)" {
		t.Fatalf("panel name %q", got)
	}
}
