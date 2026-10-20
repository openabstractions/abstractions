package main

import (
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

// The inert display fixture uses the runtime's existing question declaration.
// It creates no Book occurrence, permission request, pairing or rights edit.
func nativePairingFixture(t *testing.T) askclient.RecordMetadata {
	t.Helper()
	question, ok := asks.Find(asks.PairingKey)
	if !ok {
		t.Fatal("runtime pairing question is absent")
	}
	text, err := question.Render(asks.Ask{Asker: "openabstractions", Slots: map[string]string{
		"check": "123456", "peer": strings.Repeat("a", 64), "binding": "Studio workstation",
		"given":               "Completion and embedding requests through selected model server studio.",
		"givenmore":           "Names/details of ALL model servers registered for that user account; OA connections needed for those operations.",
		"received":            "Completion and embedding requests through selected model server laptop.",
		"receivedmore":        "Names/details of ALL model servers registered for that user account; OA connections needed for those operations.",
		"givenreplacement":    "Replacing the model server registered under studio keeps this permission.",
		"receivedreplacement": "Replacing the model server registered under laptop keeps this permission.",
		"givensource":         "studio at localhost:18080", "receivedsource": "laptop at localhost:18081",
		"code": "0123456789abcdef", "until": "2026-10-26T15:30:00Z",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return askclient.RecordMetadata{ID: "owned-pairing-fixture", Key: asks.PairingKey,
		Asker: "openabstractions", Asked: "2026-10-26T15:00:00Z", Text: text, Options: question.OptionNames()}
}

func TestNativePairingProjectionUsesConsentAppearanceAndFullFrozenText(t *testing.T) {
	record := nativePairingFixture(t)
	program := `C:\OpenAbstractions\openabstractions.exe`
	view := trayQuestionViewForAsker(record, trayPublisher{}, nil, program)
	if view.Permission == nil || view.AppChoice {
		t.Fatalf("reserved pairing did not select native consent appearance: %+v", view)
	}
	if view.Title != "OpenAbstractions" || view.Instruction != "Pair these computers?" || view.Permission.Question != view.Instruction {
		t.Fatalf("native pairing heading differs across renderers: %+v", view)
	}
	body, rest, _, canonical := pairingDisplayText(record.Text)
	if !canonical || view.Content != body || view.PermissionContent != body || view.Permission.Content != body || view.Permission.Scope != body || !strings.Contains(view.Details, record.Text) {
		t.Fatal("native pairing omitted visible scope or the exact frozen agreement")
	}
	human, _, _ := strings.Cut(record.Text, " Details: Verified peer key ")
	if strings.ReplaceAll(body, "\n\n", " ")+" "+rest != strings.TrimPrefix(human, "Pair these computers? ") {
		t.Fatal("display formatting changed a comparison, scope or lifetime term")
	}
	for _, technical := range []string{strings.Repeat("a", 64), "localhost:18080", "0123456789abcdef", "2026-10-26T15:30:00Z"} {
		if strings.Contains(body, technical) || !strings.Contains(view.Details, technical) {
			t.Fatalf("technical provenance was lost or stayed in the human body: %q", technical)
		}
	}
	if strings.Contains(body, "Permissions stay until removed") || !strings.HasPrefix(rest, "Permissions stay until removed in the Panel.") || !strings.Contains(view.Details, rest) {
		t.Fatal("the agreement's lifetime and approval terms left the details or stayed visible")
	}
	if view.Permission.Requester != "" || view.Permission.Publisher != "" || view.Permission.RequesterProgram != "" || view.Permission.Warning == nil || *view.Permission.Warning != "" {
		t.Fatalf("pairing invented a requester/publisher/icon or replaced its footer: %+v", view.Permission)
	}
	if view.Default != "refuse" || len(view.Choices) != 2 || view.Choices[0] != (trayQuestionChoice{"Pair with these permissions", "once"}) || view.Choices[1] != (trayQuestionChoice{"Cancel", "refuse"}) {
		t.Fatalf("pairing choices or safe default changed: %+v", view)
	}
	if view.Level != promptLevelFor(promptEvidence{Program: program}) || view.Footer != "" || !strings.Contains(view.Details, "Closing this question leaves it unanswered") || view.Permission.Details != view.Details {
		t.Fatal("pairing header, dismissal or technical provenance changed")
	}
	for _, exact := range []string{program, record.ID, record.Asked} {
		if !strings.Contains(view.Details, exact) {
			t.Fatalf("technical provenance omits %q", exact)
		}
	}
}

func TestNativePairingUnknownTemplateKeepsWholeBodyAndOriginal(t *testing.T) {
	canonical := nativePairingFixture(t)
	for _, text := range []string{
		"Unrecognized pairing wording: retain everything.",
		strings.Replace(canonical.Text, " Details: Verified peer key ", " Unknown details: ", 1),
		strings.Replace(canonical.Text, " Permissions stay until removed in the Panel.", " Unknown lifetime.", 1),
		canonical.Text + " Details: Verified peer key duplicated.",
		strings.Replace(canonical.Text, ". Local binding ", ". Unknown binding ", 1),
	} {
		record := canonical
		record.Text = text
		view := trayQuestionViewForAsker(record, trayPublisher{}, nil, "")
		if view.Content != text || view.PermissionContent != text || view.Permission == nil || view.Permission.Scope != text || !strings.Contains(view.Details, text) {
			t.Fatalf("unrecognized template lost its full fallback: %+v", view)
		}
	}
}

func TestNativePairingAppearanceRequiresReservedKey(t *testing.T) {
	record := nativePairingFixture(t)
	for _, key := range []string{"ordinary.question", asks.PairingKey + ".forged", ""} {
		record.Key = key
		view := trayQuestionViewForAsker(record, trayPublisher{}, nil, "")
		if view.Permission != nil || view.PermissionContent != "" || view.PermissionPrompt || view.AppChoice {
			t.Fatalf("text/options selected consent appearance for %q: %+v", key, view)
		}
		if view.Instruction != record.Text || view.Content != "" || view.Title != "OpenAbstractions" {
			t.Fatal("ordinary question content or presentation changed")
		}
	}
}

func TestNativePairingMacBridgeKeepsTrustedDisplayAndDistinctDismissal(t *testing.T) {
	pairing := trayQuestionViewForAsker(nativePairingFixture(t), trayPublisher{}, nil, "")
	ordinary := trayQuestionPresentation{presentation: nativeQuestionGeneric, Title: "Ordinary", Content: pairing.Content,
		Choices: pairing.Choices, Default: "refuse", PairingPrompt: true, PermissionPrompt: true}
	typed := ordinary
	typed.presentation = nativeQuestionConsent
	typed.Permission = &trayPermissionPresentation{}
	appChoice := pairing
	appChoice.AppChoice = true
	appChoice.presentation = nativeQuestionAppChoice
	appChoice.Permission = nil
	appChoice.PairingPrompt = true
	stripped := pairing
	stripped.Permission = nil
	stripped.PairingPrompt = true
	if _, err := selectNativeQuestionPresentation(stripped); err == nil {
		t.Fatal("missing consent payload accepted")
	}
	for _, test := range []struct {
		name       string
		view       trayQuestionPresentation
		pairing    bool
		permission bool
		close      bool
	}{
		{"pairing Cancel refuses", pairing, true, true, false},
		{"pairing Close stays unanswered", pairing, true, true, true},
		{"ordinary injected flags", ordinary, false, false, true},
		{"typed permission injected pairing flag", typed, false, true, true},
		{"app-owned choice", appChoice, false, false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			bridge, frames := nativeBridgeFixture(t)
			bridge.session(true)
			type result struct {
				option string
				err    error
			}
			done := make(chan result, 1)
			go func() { option, err := bridge.Ask(testOccurrence(test.view)); done <- result{option, err} }()
			frame := nextNativeFrame(t, frames, "question")
			if frame.Prompt == nil || frame.Prompt.PairingPrompt != test.pairing || frame.Prompt.PermissionPrompt != test.permission || frame.Prompt.IconProgram != "" {
				t.Fatalf("Mac display trust or icon differs: %+v", frame.Prompt)
			}
			if test.pairing && (frame.Prompt.Title != pairing.Title || frame.Prompt.Instruction != pairing.Instruction || frame.Prompt.Content != pairing.Content || frame.Prompt.PermissionContent != pairing.Content || frame.Prompt.Default != "refuse") {
				t.Fatal("Mac bridge changed the heading, full agreement or safe default")
			}
			reply := nativeAsksReply{Kind: "answer", ID: frame.ID, Option: "refuse"}
			if test.close {
				reply.Kind, reply.Option = "close", ""
			}
			if err := bridge.accept(reply, nil); err != nil {
				t.Fatal(err)
			}
			got := <-done
			if got.err != nil || got.option != reply.Option {
				t.Fatalf("refusal and unanswered dismissal merged: %+v", got)
			}
		})
	}
}
