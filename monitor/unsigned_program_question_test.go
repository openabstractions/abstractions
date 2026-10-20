package main

import (
	"strings"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

const unsignedFixtureProgram = `C:\Users\person\OAComfy\launch-helper.exe`

func unsignedKeepFixture() (askclient.RecordMetadata, *askclient.CatalogueContent) {
	record := askclient.RecordMetadata{ID: "keep-q", Key: asks.SelfRegisteredKey, About: unsignedFixtureProgram,
		Text: unsignedFixtureProgram + " was added to OpenAbstractions as ComfyUI. Keep it?", Options: []string{"keep", "remove"}}
	return record, &askclient.CatalogueContent{Slots: map[string]string{"program": unsignedFixtureProgram, "title": "ComfyUI"}}
}

func unsignedReviewFixture(t *testing.T) (askclient.RecordMetadata, *askclient.CatalogueContent) {
	t.Helper()
	slots, err := asks.ProgramReviewSlots(asks.ProgramReviewFacts{Targets: []asks.ProgramReviewTarget{{Program: unsignedFixtureProgram, Revision: "current",
		FileKey: "native-file", Image: strings.Repeat("a", 64), Spec: strings.Repeat("b", 64), Cause: asks.ProgramReviewUnsignedChanged, Supported: true, Title: "ComfyUI"}}})
	if err != nil {
		t.Fatal(err)
	}
	_, instruction, _, err := asks.ProgramReviewPresentation(slots)
	if err != nil {
		t.Fatal(err)
	}
	return askclient.RecordMetadata{ID: "review-q", Key: asks.ProgramReviewKey, About: slots["occurrence"], Text: instruction, Options: asks.ProgramReviewOptions(slots)},
		&askclient.CatalogueContent{Slots: slots}
}

func choiceLabels(view trayQuestionPresentation) string {
	labels := []string{}
	for _, choice := range view.Choices {
		labels = append(labels, choice.Option+"="+choice.Label)
	}
	return strings.Join(labels, ",")
}

// unsignedKeepWhy is the Details line that says why the signature matters.
const unsignedKeepWhy = "This program is unsigned. Allow it only if you installed it yourself.\nWindows cannot confirm who made this program: it carries no signature Windows trusts. A signature names the publisher and shows the file is unchanged since its release; without one, only the person who installed it can vouch for it."

func TestUnsignedProgramKeepQuestionWords(t *testing.T) {
	record, retained := unsignedKeepFixture()
	view := trayQuestionViewForContent(record, trayPublisher{}, nil, "", retained)
	if view.Title != "OpenAbstractions" || view.Instruction != "Let ComfyUI work with OpenAbstractions?" ||
		view.Content != "" ||
		view.Details != "Program: "+unsignedFixtureProgram+"\n"+unsignedKeepWhy || choiceLabels(view) != "keep=Allow,remove=Don't allow" {
		t.Fatalf("%+v", view)
	}
	if view.Permission == nil || view.Permission.Question != view.Instruction || view.Permission.Scope != view.Content || view.Permission.Publisher != "" {
		t.Fatalf("consent card: %+v", view.Permission)
	}
	if _, err := selectNativeQuestionPresentation(view); err != nil {
		t.Fatal(err)
	}
	signed := trayQuestionViewForContent(record, trayPublisher{Trusted: true, Subject: "Example Publisher"}, nil, "", retained)
	if signed.Instruction != "Let ComfyUI work with OpenAbstractions?" || strings.Contains(signed.Details, unsignedKeepWhy) {
		t.Fatal("a signed program's Keep question took the unsigned words", signed.Instruction)
	}
}

func TestUnsignedProgramChangedReviewWords(t *testing.T) {
	record, retained := unsignedReviewFixture(t)
	view := trayQuestionViewForContent(record, trayPublisher{}, nil, "", retained)
	// Architect popup ruling 2026-11-10, row 11.
	if view.Title != "OpenAbstractions" || view.Instruction != "Use the changed ComfyUI?" || view.Content != "Use it only if you updated it yourself." ||
		!strings.HasPrefix(view.Details, "Program: "+unsignedFixtureProgram+"\nCurrent file SHA-256: ") || choiceLabels(view) != "keep=Use it,not_now=Not now" || view.Default != "not_now" || view.Dismiss != "not_now" {
		t.Fatalf("%+v", view)
	}
	if strings.Contains(view.Instruction+view.Content, strings.Repeat("a", 64)) {
		t.Fatal("the review's visible words show the image hash")
	}
	if _, err := selectNativeQuestionPresentation(view); err != nil {
		t.Fatal(err)
	}
}

func TestUnsignedProgramAnswerNotices(t *testing.T) {
	keep, keepContent := unsignedKeepFixture()
	review, reviewContent := unsignedReviewFixture(t)
	for _, test := range []struct {
		record  askclient.RecordMetadata
		content *askclient.CatalogueContent
		option  string
		want    string
	}{
		{keep, keepContent, "keep", "ComfyUI stays in OpenAbstractions."},
		{keep, keepContent, "remove", "ComfyUI was removed from OpenAbstractions."},
		{review, reviewContent, "keep", "ComfyUI will keep working."},
		{review, reviewContent, "not_now", "ComfyUI is paused until you review it."},
	} {
		if got, ok := unsignedAnswerNotice(test.record, trayPublisher{}, test.content, test.option); !ok || got != test.want {
			t.Fatalf("%s %s: %q", test.record.Key, test.option, got)
		}
	}
	if _, ok := unsignedAnswerNotice(keep, trayPublisher{Trusted: true}, keepContent, "keep"); ok {
		t.Fatal("a signed program's Keep posted the unsigned notice")
	}
	if _, ok := unsignedAnswerNotice(askclient.RecordMetadata{Key: asks.FirstUseKey}, trayPublisher{}, &askclient.CatalogueContent{}, "allow"); ok {
		t.Fatal("a first-use answer posted an unsigned notice")
	}
}

func TestUnsignedProgramKeepQuestionPathTitleNamesStem(t *testing.T) {
	record, retained := unsignedKeepFixture()
	retained.Slots["title"] = unsignedFixtureProgram
	view := trayQuestionViewForContent(record, trayPublisher{}, nil, "", retained)
	if view.Instruction != "Let launch-helper work with OpenAbstractions?" ||
		view.Content != "" ||
		view.Details != "Program: "+unsignedFixtureProgram+"\n"+unsignedKeepWhy || view.Dismiss != trayNotNow || choiceLabels(view) != "keep=Allow,remove=Don't allow" {
		t.Fatalf("%+v", view)
	}
}

// The header follows Windows' UAC banners: blue for a verified publisher,
// amber (yellow) for an unverified one, on every question popup.
func TestKeepQuestionHeaderFollowsVerification(t *testing.T) {
	record, retained := unsignedKeepFixture()
	if view := trayQuestionViewForContent(record, trayPublisher{}, nil, "", retained); view.Level != promptLevelYellow {
		t.Fatal("unverified", view.Level)
	}
	if view := trayQuestionViewForContent(record, trayPublisher{Trusted: true, Subject: "Example Publisher"}, nil, "", retained); view.Level != promptLevelBlue {
		t.Fatal("verified", view.Level)
	}
}
