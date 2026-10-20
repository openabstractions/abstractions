package main

import (
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// An entry its own program registered reads "<title> (added itself)" in Apps,
// on its page, in card holders and in prompts until the person keeps it; a
// title that reads as OpenAbstractions reads as the program's stem.
func TestSelfRegisteredEntriesSayTheyAddedThemselves(t *testing.T) {
	program := filepath.Join(t.TempDir(), "editor.exe")
	self := fwire.ApplicationEntry{Scope: fwire.ScopeLocal, SelfRegistered: true,
		Descriptor: fwire.ApplicationDescriptor{Name: "editor", Program: program, Title: "Editor"}}
	if got := appEntryTitle(self); got != "Editor (added itself)" {
		t.Fatalf("Apps title %q", got)
	}
	kept := self
	kept.SelfRegistered = false
	if got := appEntryTitle(kept); got != "Editor" {
		t.Fatalf("kept title %q", got)
	}
	reserved := self
	reserved.Descriptor.Title = "OpenAbstractions Helper"
	if got := appEntryTitle(reserved); got != "editor (registered app) (added itself)" {
		t.Fatalf("reserved title %q", got)
	}

	// A prompt naming the app (the native first-use sentence) uses the same name.
	titler := newProgramTitler([]fwire.ApplicationEntry{self})
	if got := firstUseSentence(trayPublisher{Names: titler}, program, "abstraction.model/lookup", "hf"); !strings.HasPrefix(got, "Editor (added itself) wants to ") {
		t.Fatalf("prompt %q", got)
	}
	// A card holder row too.
	resources := []cardResourceState{{Holders: []cardHolder{{Program: program}}}}
	titleHolders(resources, []fwire.ApplicationEntry{self})
	if got := resources[0].Holders[0].Title; got != "Editor (added itself)" {
		t.Fatalf("card holder %q", got)
	}
}

// selfRegisteredRecord is the runtime's question about program, rendered from
// the catalogue as the runtime admits it.
func selfRegisteredRecord(t *testing.T, program, title string) askclient.RecordMetadata {
	t.Helper()
	question, ok := asks.Find(asks.SelfRegisteredKey)
	if !ok {
		t.Fatal("no catalogue question")
	}
	text, err := question.Render(asks.Ask{Key: asks.SelfRegisteredKey, Slots: map[string]string{"program": program, "title": title}})
	if err != nil {
		t.Fatal(err)
	}
	return askclient.RecordMetadata{ID: "q1", Key: asks.SelfRegisteredKey, Asker: "openabstractions", About: program,
		Text: text, Options: question.OptionNames(), Asked: "2026-10-07T10:00:00Z"}
}

// The runtime's question about an app that added itself shows Keep and Remove,
// names no default (closing answers nothing), and says what each does. It
// names the app by its title and the program by its stem; the program's path
// and the publisher bound from the registration are in the details (review
// round 2, B).
func TestTheSelfRegisteredQuestionPrompt(t *testing.T) {
	program := filepath.Join(t.TempDir(), "editor.exe")
	record := selfRegisteredRecord(t, program, "Editor")
	signed := trayPublisher{Subject: "Example Software", Trusted: true, Description: "Example Editor", OriginalName: "editor.exe"}
	view := trayQuestionViewForAsker(record, signed.forPrompt(nil), nil, "")
	var labels []string
	for _, choice := range view.Choices {
		labels = append(labels, choice.Label)
	}
	if strings.Join(labels, "|") != "Keep|Remove" || view.Default != "" {
		t.Fatalf("choices %v default %q", labels, view.Default)
	}
	if view.Instruction != "An app added itself" || view.Content != "Editor (editor) added itself to OpenAbstractions. Keep it?" ||
		!strings.Contains(view.Footer, "Closing this window leaves it unconfirmed") {
		t.Fatalf("prompt %+v", view)
	}
	for _, line := range []string{"Program: " + program, "Publisher: Example Software (signature verified when the app added itself).",
		"Description: Example Editor", "Original file name: editor.exe", "Question: q1"} {
		if !strings.Contains(view.Details, line) {
			t.Errorf("details lack %q:\n%s", line, view.Details)
		}
	}
	if strings.Contains(view.Content, program) {
		t.Fatal("the question shows the program's path outside the details")
	}
	unsigned := trayQuestionViewForAsker(record, trayPublisher{}, nil, "")
	if !strings.Contains(unsigned.Details, "Publisher: not verified.") || !strings.Contains(unsigned.Details, "Program: "+program) {
		t.Fatalf("unsigned details:\n%s", unsigned.Details)
	}
	// OpenAbstractions asks this in its own words: blue, signed or not.
	if view.Level != promptLevelBlue || unsigned.Level != promptLevelBlue {
		t.Fatalf("levels %q and %q, want blue", view.Level, unsigned.Level)
	}
	// The app's own questions keep the level its evidence gives; its title
	// selects none.
	own := askclient.RecordMetadata{ID: "q2", Key: "download.reach", Asker: "editor.exe", Text: "Editor (added itself) wants to fetch from example.org", Options: []string{"allow", "refuse"}}
	if got := trayQuestionViewForAsker(own, trayPublisher{}, nil, program).Level; got != promptLevelYellow {
		t.Fatalf("an unsigned app's own question reads %q", got)
	}
	if got := trayQuestionViewForAsker(own, signed, nil, program).Level; got != promptLevelGray {
		t.Fatalf("a signed app's own question reads %q", got)
	}
}

// Review round 2, D: a record that names an asker program is an
// application's own question, and keeps the generic wording even with the
// self-registration key.
func TestTheSelfRegisteredWordingNeedsTheRuntimeAsAsker(t *testing.T) {
	program := filepath.Join(t.TempDir(), "editor.exe")
	record := selfRegisteredRecord(t, program, "OpenAbstractions Helper")
	view := trayQuestionViewForAsker(record, trayPublisher{}, nil, program)
	if view.Instruction == "An app added itself" || view.Content != record.Text || strings.Contains(view.Footer, "Keep confirms it") {
		t.Fatalf("an application's question took the runtime's wording: %+v", view)
	}
}
