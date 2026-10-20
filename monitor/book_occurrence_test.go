package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

// testOccurrence stands a view in for a Book occurrence in tests of the
// native surfaces' rendering.
func testOccurrence(view trayQuestionPresentation) bookOccurrence {
	return bookOccurrence{id: "test-occurrence", view: view}
}

// Only a Book occurrence reaches the native question surface: a view that
// names no Book id, or whose family differs from the one Asks assigns its
// recorded key, is refused.
func TestBookOccurrenceBindsIdAndFamily(t *testing.T) {
	record := askclient.RecordMetadata{ID: "q-1", Key: asks.AgentUpdateKey, Text: "Update the Claude integration connections? Request 0123456789abcdef."}
	consent := trayQuestionPresentation{presentation: nativeQuestionConsent, Instruction: "Update OpenAbstractions in Claude?"}
	if occurrence, err := questionOccurrence(record, consent); err != nil || occurrence.ID() != "q-1" || occurrence.Presentation().Instruction != consent.Instruction {
		t.Fatalf("Book occurrence refused: %+v %v", occurrence, err)
	}
	ordinary := consent
	ordinary.presentation = nativeQuestionGeneric
	free := record
	free.ID = ""
	for name, c := range map[string]struct {
		record askclient.RecordMetadata
		view   trayQuestionPresentation
	}{
		"local words with no Book id":    {free, consent},
		"a family the key does not have": {record, ordinary},
	} {
		if _, err := questionOccurrence(c.record, c.view); !errors.Is(err, errNotBookOccurrence) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	choice := askclient.ChoiceRecord{ID: "c-1"}
	if _, err := choiceOccurrence(choice, trayQuestionPresentation{presentation: nativeQuestionConsent}); !errors.Is(err, errNotBookOccurrence) {
		t.Errorf("an application choice took the consent style: %v", err)
	}
	if _, err := choiceOccurrence(choice, trayQuestionPresentation{presentation: nativeQuestionAppChoice}); err != nil {
		t.Errorf("an application choice was refused: %v", err)
	}
}

// The presenter surface has one entry type, and only the two constructors in
// book_occurrence.go build it. Panel code holds no local confirmation in the
// native question style and calls no report dialog.
func TestNativeQuestionStyleHasNoLocalCaller(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		if name != "book_occurrence.go" && strings.Contains(text, "bookOccurrence{") {
			t.Errorf("%s builds a bookOccurrence outside its constructors", name)
		}
		for _, banned := range []string{"PresentationForLocalConfirmation", "ShowQuestionResult", "showFailureReport", "askPersonalPermissionReview"} {
			if strings.Contains(text, banned) {
				t.Errorf("%s still uses %s", name, banned)
			}
		}
	}
}

// Authority-granting questions carry their Book key to the native prompt,
// which then counts only the person's own mouse and key presses (POPUP-STYLE
// Q14 part D). An application's free-text question and its choices do not.
func TestBookOccurrenceMarksAuthorityGrantingQuestions(t *testing.T) {
	granting := []string{asks.FirstUseKey, asks.TypedPermissionKey, asks.PairingKey, asks.SetupModelsKey,
		asks.AgentEnrollmentKey, asks.AgentUpdateKey, asks.SelfRegisteredKey, asks.ProgramReviewKey,
		asks.ApplicationCommandOnceKey, asks.SystemSecurityLogKey, "rights.register", "download.reach", "content.replace"}
	for _, key := range granting {
		record := askclient.RecordMetadata{ID: "q-" + key, Key: key}
		occurrence, err := questionOccurrence(record, trayQuestionPresentation{presentation: catalogueQuestionFamily(key)})
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if occurrence.Authority() != key {
			t.Errorf("%s is not marked authority-granting: %q", key, occurrence.Authority())
		}
	}
	free := askclient.RecordMetadata{ID: "q-free", Key: "example.app.question"}
	if occurrence, err := questionOccurrence(free, trayQuestionPresentation{presentation: catalogueQuestionFamily(free.Key)}); err != nil || occurrence.Authority() != "" {
		t.Fatalf("an application's own question is marked authority-granting: %q %v", occurrence.Authority(), err)
	}
	choice, err := choiceOccurrence(askclient.ChoiceRecord{ID: "c-1"}, trayQuestionPresentation{presentation: nativeQuestionAppChoice})
	if err != nil || choice.Authority() != "" {
		t.Fatalf("an application's choice is marked authority-granting: %q %v", choice.Authority(), err)
	}
}
