package main

import (
	"reflect"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

func reservedConsentFixtures() []askclient.RecordMetadata {
	program := `C:\Apps\agent.exe`
	return []askclient.RecordMetadata{
		{ID: "agent-question", Key: asks.AgentEnrollmentKey, About: program, Text: "Keep Codex integration as your assistant? Each access permission is reviewed separately.", Options: []string{"keep", "refuse"}},
		{ID: "registered-question", Key: asks.SelfRegisteredKey, About: program, Text: program + " added itself to OpenAbstractions as Codex integration. Keep it?", Options: []string{"keep", "remove"}},
		{ID: "command-question", Key: asks.ApplicationCommandOnceKey, About: program, Text: "Open this document? Agent requests this once. Command open. Request ABC expires soon.", Options: []string{"once", "refuse"}},
		{ID: "system-question", Key: asks.SystemSecurityLogKey, About: "ABC", Text: "Agent wants to read one page of the Windows Security log. Windows asks for administrator approval next.", Options: []string{"once", "refuse"}},
	}
}

func TestReservedConsentsSelectExistingNativePresentation(t *testing.T) {
	publisher := trayPublisher{Trusted: true, Subject: "Recorded publisher"}
	for _, record := range reservedConsentFixtures() {
		t.Run(record.Key, func(t *testing.T) {
			view := trayQuestionViewForAsker(record, publisher, nil, "")
			if view.Permission == nil || view.AppChoice || view.presentation == nativeQuestionPairing || view.PermissionContent != view.Content {
				t.Fatal("reserved consent did not select existing native presentation", view)
			}
			if view.Permission.Content != view.Content || view.Permission.Details != view.Details || view.Permission.Footer != view.Footer || view.Permission.Question != view.Instruction || view.Permission.Scope != view.Content {
				t.Fatal("presentation changed recorded consent words")
			}
			if !strings.Contains(view.Details, "Publisher: "+publisher.Subject+" (verified)") || view.Permission.Publisher != "" || !strings.Contains(view.Details, record.ID) {
				t.Fatal("recorded publisher or exact occurrence lost")
			}
			if view.Default != trayDefaultChoice(record.Options) {
				t.Fatal("safe default changed")
			}
			options := []string{}
			for _, choice := range view.Choices {
				options = append(options, choice.Option)
			}
			if !reflect.DeepEqual(options, record.Options) {
				t.Fatal("options changed", options)
			}
			bridge, frames := nativeBridgeFixture(t)
			bridge.session(true)
			done := make(chan string, 1)
			go func() { option, _ := bridge.Ask(testOccurrence(view)); done <- option }()
			frame := nextNativeFrame(t, frames, "question")
			if frame.Prompt == nil || !frame.Prompt.PermissionPrompt || frame.Prompt.PairingPrompt || frame.Prompt.Content != view.Content || frame.Prompt.Default != view.Default {
				t.Fatal("native bridge lost consent semantics", frame)
			}
			if err := bridge.accept(nativeAsksReply{Kind: "close", ID: frame.ID}, nil); err != nil {
				t.Fatal(err)
			}
			if option := <-done; option != "" {
				t.Fatal("closing consent became an answer", option)
			}
		})
	}
	ordinary := reservedConsentFixtures()[0]
	ordinary.Key = "application.ordinary_question"
	if view := trayQuestionViewForAsker(ordinary, publisher, nil, ""); view.Permission != nil || view.PermissionContent != "" {
		t.Fatal("ordinary application prose selected native consent")
	}
	unverified := trayQuestionViewForAsker(reservedConsentFixtures()[0], trayPublisher{Subject: "unverified claim"}, nil, "")
	if unverified.Permission == nil {
		t.Fatal("reserved consent missing for unverified publisher")
	}
	if strings.Contains(unverified.Permission.Publisher, "unverified claim") {
		t.Fatal("unverified publisher text became trusted")
	}
}
