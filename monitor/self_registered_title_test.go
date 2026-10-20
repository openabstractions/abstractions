package main

import (
	"path/filepath"
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/probe"
	"github.com/openabstractions/abstraction-identity/listen"
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
	titles := titleHolders(resources, newProgramTitler([]fwire.ApplicationEntry{self}))
	if got := titles[program]; got != "Editor (added itself)" {
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

// The runtime's question about an app that added itself, asked only after
// the person acts on it in Apps, shows Keep and Remove, names no default
// (closing answers nothing) and names the app by its title. The program's
// path and the publisher bound from the registration are in the details
// (architect ruling 2026-11-10, rows 9 and 10).
func TestTheSelfRegisteredQuestionPrompt(t *testing.T) {
	program := filepath.Join(t.TempDir(), "editor.exe")
	record := selfRegisteredRecord(t, program, "Editor")
	record.Text = "Updated catalogue wording without an embedded registration tuple."
	content := &askclient.CatalogueContent{Slots: map[string]string{"program": program, "title": "Editor"}}
	signed := trayPublisher{Subject: "Example Software", Trusted: true, Description: "Example Editor", OriginalName: "editor.exe"}
	view := trayQuestionViewForContent(record, signed.forPrompt(nil), nil, "", content)
	var labels []string
	for _, choice := range view.Choices {
		labels = append(labels, choice.Label)
	}
	if strings.Join(labels, "|") != "Allow|Don't allow" || view.Default != "" {
		t.Fatalf("choices %v default %q", labels, view.Default)
	}
	if view.Title != "OpenAbstractions" || view.Instruction != "Let Editor work with OpenAbstractions?" || view.Content != "" || view.Dismiss != trayNotNow {
		t.Fatalf("prompt %+v", view)
	}
	for _, line := range []string{"Program: " + program, "Publisher: Example Software (signature verified at registration).",
		"Description: Example Editor", "Original file name: editor.exe"} {
		if !strings.Contains(view.Details, line) {
			t.Errorf("details lack %q:\n%s", line, view.Details)
		}
	}
	if view.Permission == nil || view.Permission.Publisher != "Publisher: Example Software (signature verified at registration)." {
		t.Fatalf("consent publisher invents verification timing: %+v", view.Permission)
	}
	if strings.Contains(view.Content, program) {
		t.Fatal("the question shows the program's path outside the details")
	}
	// An unsigned program is asked the same plain question: its path, the
	// unsigned note and why the signature matters are the details.
	unsigned := trayQuestionViewForContent(record, trayPublisher{}, nil, "", content)
	if unsigned.Instruction != "Let Editor work with OpenAbstractions?" || unsigned.Content != "" ||
		unsigned.Details != "Program: "+program+"\n"+unsignedKeepWhy || strings.Contains(unsigned.Details, "signature verified") {
		t.Fatalf("unsigned prompt: %+v", unsigned)
	}
	if unsigned.Permission == nil || unsigned.Permission.Publisher != "" {
		t.Fatalf("unsigned consent publisher: %+v", unsigned.Permission)
	}
	// The header follows the publisher: blue when verified, amber when not.
	if view.Level != promptLevelBlue || unsigned.Level != promptLevelYellow {
		t.Fatalf("levels %q and %q, want blue and yellow", view.Level, unsigned.Level)
	}
	// The app's own questions keep the level its evidence gives; its title
	// selects none.
	own := askclient.RecordMetadata{ID: "q2", Key: "download.reach", Asker: "editor.exe", Text: "Editor (added itself) wants to fetch from example.org", Options: []string{"allow", "refuse"}}
	if got := trayQuestionViewForAsker(own, trayPublisher{}, nil, program).Level; got != promptLevelYellow {
		t.Fatalf("an unsigned app's own question reads %q", got)
	}
	if got := trayQuestionViewForAsker(own, signed, nil, program).Level; got != promptLevelBlue {
		t.Fatalf("a signed app's own question reads %q", got)
	}
}

// Review round 2, D: a record that names an asker program is an
// application's own question, and keeps the generic wording even with the
// self-registration key.
func TestTheSelfRegisteredWordingNeedsTheRuntimeAsAsker(t *testing.T) {
	// This rendering fixture opens no file; keep its catalogue slot below
	// the declared bound regardless of the test runner's TEMP prefix.
	program := `C:\Apps\editor.exe`
	record := selfRegisteredRecord(t, program, "OpenAbstractions Helper")
	view := trayQuestionViewForContent(record, trayPublisher{}, nil, program, &askclient.CatalogueContent{Slots: map[string]string{"program": program, "title": "OpenAbstractions Helper"}})
	if view.Instruction != record.Text || view.Content != "" || strings.Contains(view.Footer, "Keep confirms this registration") {
		t.Fatalf("an application's question took the runtime's wording: %+v", view)
	}
}

func TestTheSelfRegisteredLegacyTextDoesNotSupplyStructuredContent(t *testing.T) {
	program := `C:\Apps\editor.exe`
	record := selfRegisteredRecord(t, program, "Editor")
	record.Text = program + " added itself to OpenAbstractions as Editor. Keep it?"
	for _, content := range []*askclient.CatalogueContent{nil, {Slots: map[string]string{"program": `C:\Other\editor.exe`, "title": "Editor"}}} {
		view := trayQuestionViewForContent(record, trayPublisher{}, nil, "", content)
		if view.Instruction != record.Text || view.Content != "" || strings.Contains(view.Footer, "Keep confirms this registration") {
			t.Fatalf("missing or mismatched content inferred a registration: %+v", view)
		}
	}
}

func TestTheSelfRegisteredPopupAndQuestionsShareRecordedSlots(t *testing.T) {
	book, _, _ := panelFirstUseRuntime(t, "abstraction.model/lookup")
	program := `C:\Apps\editor.exe`
	question, _, err := book.AskApplicationFrom("runtime-scope", "OpenAbstractions", "registration-content", asks.SelfRegisteredKey,
		map[string]string{"program": program, "title": "Recorded Editor"}, listen.Seen{}, asks.Publisher{})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(question.Text, "added itself") {
		t.Fatal("the actual retained PROGRAM question invents its registrar", question.Text)
	}
	response := panelRequest(t, (&servicePanel{}).handler("test-key"), "/questions?cursor=", nil)
	page := decodePanel[questionPage](t, response.Code, response.Body.Bytes())
	want := "Recorded Editor (editor) was added to OpenAbstractions. Keep it?"
	if page.Sentences[question.ID] != want {
		t.Fatalf("question sentence %q, want %q", page.Sentences[question.ID], want)
	}
	for _, record := range page.Records {
		if record.ID != question.ID {
			continue
		}
		surface := &fakeSurface{}
		presenter := questionPresenter{session: unlockedSession(), surface: surface, operator: defaultPresenterOperator, account: probe.Self().Account}
		result := presenter.present(t.Context(), record)
		// The recorded registration names no trusted publisher: the popup
		// asks in the unsigned Keep words from the same recorded slots.
		if result.Outcome != presentClosed || len(surface.shown) != 1 ||
			surface.shown[0].Content != "" {
			t.Fatalf("popup %+v, shown %+v", result, surface.shown)
		}
		shown := surface.shown[0]
		if shown.Instruction != "Let Recorded Editor work with OpenAbstractions?" || shown.Details != "Program: "+program+"\n"+unsignedKeepWhy || strings.Contains(shown.Details, "app added itself") {
			t.Fatalf("shared native PROGRAM rendering invents provenance/effects: %+v", shown)
		}
		return
	}
	t.Fatal("registration missing from questions page")
}
