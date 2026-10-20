//go:build windows

package main

import (
	"encoding/json"
	"os"
	"testing"
)

func TestNativePairingWindowsProjection(t *testing.T) {
	record := nativePairingFixture(t)
	view := trayQuestionViewForAsker(record, trayPublisher{}, nil, "")
	prompt, err := trayWindowsQuestionPrompt(view)
	if err != nil {
		t.Fatal(err)
	}
	if !prompt.Permission || !prompt.PairingPrompt || prompt.AppChoice || prompt.PermissionView == nil {
		t.Fatal("reserved pairing did not select the native permission renderer")
	}
	if prompt.Title != "OpenAbstractions" || prompt.Instruction != "Pair these computers?" || prompt.PermissionView.Question != view.Instruction || prompt.Content != view.Content || prompt.PermissionView.Scope != view.Content {
		t.Fatal("Windows pairing heading/body lost the frozen text")
	}
	if prompt.PermissionView.Requester != "" || prompt.PermissionView.Publisher != "" || prompt.PermissionView.RequesterProgram != "" || prompt.PermissionView.Warning == nil || *prompt.PermissionView.Warning != "" || prompt.Footer != "" || prompt.Details != view.Details {
		t.Fatal("Windows pairing invented identity or changed provenance/dismissal")
	}
	if prompt.Default != "refuse" || len(prompt.Choices) != 2 || prompt.Choices[0].Value != "once" || prompt.Choices[0].Label != "Pair with these permissions" || prompt.Choices[1].Value != "refuse" || prompt.Choices[1].Label != "Cancel" {
		t.Fatal("Windows pairing changed the decision mapping")
	}
	// An opt-in inert capture receives this exact production mapping. The
	// separate private screenshot fixture closes it without an answer.
	if path := os.Getenv("OA_PAIR_NATIVE1614_PROMPT"); path != "" {
		data, err := json.Marshal(prompt)
		if err == nil {
			err = os.WriteFile(path, data, 0o600)
		}
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestNativePairingWindowsProjectionRejectsTextAndFlagForgery(t *testing.T) {
	record := nativePairingFixture(t)
	record.Key = "ordinary.question"
	view := trayQuestionViewForAsker(record, trayPublisher{}, nil, "")
	view.PairingPrompt = true
	prompt, err := trayWindowsQuestionPrompt(view)
	if err != nil {
		t.Fatal(err)
	}
	if prompt.Permission || prompt.PairingPrompt || prompt.PermissionView != nil {
		t.Fatal("ordinary text or an injected display flag selected permission appearance")
	}
	view.presentation = nativeQuestionConsent
	view.Permission = &trayPermissionPresentation{}
	prompt, err = trayWindowsQuestionPrompt(view)
	if err != nil {
		t.Fatal(err)
	}
	if !prompt.Permission || prompt.PairingPrompt {
		t.Fatal("typed permission took a forged pairing display flag")
	}
	view.AppChoice = true
	view.presentation = nativeQuestionAppChoice
	view.Permission = nil
	prompt, err = trayWindowsQuestionPrompt(view)
	if err != nil {
		t.Fatal(err)
	}
	if prompt.PairingPrompt || prompt.Permission && !prompt.AppChoice {
		t.Fatal("app-owned choice became eligible for the Windows permission selector")
	}
}
