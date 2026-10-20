//go:build windows

package main

import (
	asks "github.com/openabstractions/abstraction-asks/go"
	"testing"
)

func TestQuestionPresentationCatalogueToWindows(t *testing.T) {
	for _, question := range asks.Questions {
		t.Run(question.Key, func(t *testing.T) {
			view := catalogueNativeQuestionFixture(t, question)
			native, err := trayWindowsQuestionPrompt(view)
			if err != nil || native.Permission != asks.RuntimeOnlyKey(question.Key) || native.AppChoice || native.PairingPrompt != (asks.PresentationForQuestion(question.Key) == asks.PresentationPairingConsent) {
				t.Fatalf("catalogue route lost: %+v %v", native, err)
			}
		})
	}
	if _, err := trayWindowsQuestionPrompt(trayQuestionPresentation{}); err == nil {
		t.Fatal("missing family accepted")
	}
	record, details := typedNativeFixture(t, false, true)
	if native, err := trayWindowsQuestionPrompt(trayTypedPermissionView(record, details, nil)); err != nil || !native.Permission || native.PairingPrompt {
		t.Fatalf("typed permission route lost: %+v %v", native, err)
	}
}

func TestSetupQuestionFrozenWordingThroughWindows(t *testing.T) {
	for _, fixture := range setupRouteWordingFixtures(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			native, err := trayWindowsQuestionPrompt(setupRouteWordingView(t, fixture))
			if err != nil || !native.Permission || native.AppChoice || native.PairingPrompt || native.PermissionView == nil || native.Title != "OpenAbstractions" || native.Instruction != fixture.Title || native.Content != fixture.Instruction || native.Details != fixture.Details {
				t.Fatal("Windows changed frozen wording or family", native, err)
			}
		})
	}
}
