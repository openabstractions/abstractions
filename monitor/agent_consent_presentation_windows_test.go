package main

import "testing"

func TestReservedConsentWindowsProjectionSelectsConsentAndChimeOwner(t *testing.T) {
	for _, record := range reservedConsentFixtures() {
		view := trayQuestionViewForAsker(record, trayPublisher{}, nil, "")
		prompt, err := trayWindowsQuestionPrompt(view)
		if err != nil {
			t.Fatal(err)
		}
		if !prompt.Permission || prompt.AppChoice || prompt.PairingPrompt || prompt.PermissionView == nil {
			t.Fatal("existing renderer/chime permission marker missing", record.Key)
		}
		if prompt.Default != view.Default || prompt.Content != view.Content || prompt.PermissionView.Scope != view.Content || prompt.Details != view.Details {
			t.Fatal("native projection changed consent", record.Key)
		}
	}
}
