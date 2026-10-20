//go:build windows

package win

import (
	"testing"

	"golang.org/x/sys/windows"
)

func TestPairingPermissionDismissalIsDistinctAndUnanswered(t *testing.T) {
	pairing := QuestionPrompt{Permission: true, PairingPrompt: true, Default: "refuse",
		Choices: []QuestionChoice{{"Pair with these permissions", "once"}, {"Cancel", "refuse"}}}
	version := &windows.OsVersionInfoEx{MajorVersion: 10, BuildNumber: 22621, ProductType: 1}
	if !modernPermissionFor(pairing, version) || permissionNeedsDismissButton(pairing, false, false) {
		t.Fatal("trusted pairing did not keep consent appearance with just its two answers")
	}
	flags, common, def := questionPresentationFlags(pairing)
	if flags&tdfAllowDialogCancellation == 0 || common != 0 || def != firstQuestionButton+1 || permissionDefaultButton(pairing, false) != firstQuestionButton+1 {
		t.Fatal("fallback dismissal or safe refusal default changed")
	}
	if permissionChoiceLabel(pairing.Choices[1], permissionConsentPair(pairing.Choices)) != "Cancel" || pairing.Choices[1].Value != "refuse" {
		t.Fatal("actual Cancel stopped being the refusing answer")
	}
	for _, id := range []int32{idcCancel, idcClose} {
		if !questionClickAllowed(false, uintptr(id)) || id >= firstQuestionButton {
			t.Fatal("unanswered close became an answer option")
		}
	}
	for _, prompt := range []QuestionPrompt{
		{Permission: true}, {PairingPrompt: true}, {},
	} {
		_, common, _ := questionPresentationFlags(prompt)
		if !permissionNeedsDismissButton(prompt, false, false) || common != tdcbfCancelButton {
			t.Fatal("ordinary or typed permission presentation changed")
		}
	}
	// A choice that already declines takes Escape and the close box, and no
	// separate Cancel is drawn ("Connect an app": Allow, Not now). A result
	// has OK alone (owner's PC, 2026-11-01: Allow / Not now / Cancel, and
	// OK / Cancel on "Nothing was changed.").
	connect := QuestionPrompt{Title: "Connect an app", Dismiss: "not_now",
		Choices: []QuestionChoice{{"Allow", "allow"}, {"Not now", "not_now"}}}
	if permissionNeedsDismissButton(connect, false, false) {
		t.Fatal("Not now and Cancel both drawn")
	}
	if _, common, def := questionPresentationFlags(connect); common != 0 || def != firstQuestionButton+1 {
		t.Fatalf("fallback dialog: common buttons %#x, default %d; want none and Not now", common, def)
	}
	if permissionNeedsDismissButton(QuestionPrompt{Choices: []QuestionChoice{{"OK", "acknowledged"}}}, false, true) {
		t.Fatal("a result drew Cancel beside OK")
	}
	app := pairing
	app.AppChoice = true
	if modernPermissionFor(app, version) {
		t.Fatal("app-owned choice selected native permission appearance")
	}
}
