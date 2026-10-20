package main

import (
	"encoding/json"
	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"maps"
	"os"
	"strings"
	"testing"
)

type setupRouteWordingFixture struct {
	Name        string            `json:"name"`
	Facts       map[string]string `json:"facts"`
	Title       string            `json:"title"`
	Instruction string            `json:"instruction"`
	Details     string            `json:"details"`
}

func setupRouteWordingFixtures(t *testing.T) []setupRouteWordingFixture {
	t.Helper()
	raw, err := os.ReadFile("internal/setup/testdata/setup_question_wording.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []setupRouteWordingFixture
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	if len(fixtures) != 10 {
		t.Fatalf("wording cases=%d", len(fixtures))
	}
	return fixtures
}
func setupRouteWordingView(t *testing.T, f setupRouteWordingFixture) trayQuestionPresentation {
	t.Helper()
	slots := maps.Clone(f.Facts)
	slots["occurrence"] = strings.Repeat("a", 48)
	record := askclient.RecordMetadata{ID: "setup-occurrence", Key: asks.SetupModelsKey, About: slots["occurrence"], Text: f.Instruction, Options: []string{"setup", "not_now"}}
	return trayQuestionViewForContent(record, trayPublisher{}, nil, "", &askclient.CatalogueContent{Slots: slots})
}
func catalogueNativeQuestionFixture(t *testing.T, question asks.Question) trayQuestionPresentation {
	t.Helper()
	if question.Key == asks.ProgramReviewKey {
		return programReviewNativeFixture(t, "shipped:unrecorded", true)
	}
	if question.Key == asks.SetupModelsKey {
		return setupRouteWordingView(t, setupRouteWordingFixtures(t)[0])
	}
	record := askclient.RecordMetadata{ID: "catalogue-occurrence", Key: question.Key, Text: question.Text, About: "recorded target"}
	for _, option := range question.Options {
		record.Options = append(record.Options, option.Name)
	}
	return trayQuestionViewForAsker(record, trayPublisher{}, nil, "")
}

func programReviewNativeFixture(t *testing.T, cause string, supported bool) trayQuestionPresentation {
	t.Helper()
	slots, err := asks.ProgramReviewSlots(asks.ProgramReviewFacts{Targets: []asks.ProgramReviewTarget{{Program: `C:\fixture\provider.exe`, Revision: "current", FileKey: "fixture-native-file", Image: strings.Repeat("a", 64), Spec: strings.Repeat("b", 64), Cause: cause, Supported: supported}}})
	if err != nil {
		t.Fatal(err)
	}
	_, instruction, _, err := asks.ProgramReviewPresentation(slots)
	if err != nil {
		t.Fatal(err)
	}
	record := askclient.RecordMetadata{ID: "group", Key: asks.ProgramReviewKey, About: slots["occurrence"], Text: instruction, Options: asks.ProgramReviewOptions(slots)}
	return trayQuestionViewForContent(record, trayPublisher{}, nil, "", &askclient.CatalogueContent{Slots: slots})
}

func TestProgramReviewNativeOptionsAndFiles(t *testing.T) {
	for _, test := range []struct {
		cause     string
		supported bool
		options   string
	}{{"shipped:unrecorded", true, "keep,not_now"}, {"shipped:hash-mismatch", true, "keep,repair,not_now"}, {"identity:unavailable", false, "not_now"}} {
		view := programReviewNativeFixture(t, test.cause, test.supported)
		options := []string{}
		for _, choice := range view.Choices {
			options = append(options, choice.Option)
			if choice.Option == "keep" && choice.Label != "Use it" || choice.Option == "repair" && choice.Label != "Repair" {
				t.Fatal(choice)
			}
		}
		if strings.Join(options, ",") != test.options || view.Default != "not_now" || view.Title != "OpenAbstractions" || view.Instruction != "Use these changed programs?" ||
			view.Content != "Use them only if you updated them yourself." || !strings.Contains(view.Details, `C:\fixture\provider.exe`) {
			t.Fatal(view)
		}
	}
}

// Exercise the actual catalogue producer and bridge, rather than a second
// hand-maintained list of privileged keys or an isolated renderer fixture.
func TestQuestionPresentationCatalogueToNativeBridge(t *testing.T) {
	for _, question := range asks.Questions {
		t.Run(question.Key, func(t *testing.T) {
			view := catalogueNativeQuestionFixture(t, question)
			selection, err := selectNativeQuestionPresentation(view)
			if err != nil || selection.consent != asks.RuntimeOnlyKey(question.Key) {
				t.Fatalf("catalogue producer missing route: %+v %v", selection, err)
			}
			if view.presentation != asks.PresentationForQuestion(question.Key) {
				t.Fatal("Asks family lost")
			}
			bridge, frames := nativeBridgeFixture(t)
			bridge.session(true)
			done := make(chan error, 1)
			go func() { _, err := bridge.Ask(testOccurrence(view)); done <- err }()
			frame := nextNativeFrame(t, frames, "question")
			if frame.Prompt.PermissionPrompt != asks.RuntimeOnlyKey(question.Key) || frame.Prompt.PairingPrompt != (view.presentation == nativeQuestionPairing) {
				t.Fatal("native bridge changed declared family")
			}
			if err := bridge.accept(nativeAsksReply{Kind: "close", ID: frame.ID}, nil); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSetupQuestionFrozenWordingThroughNativeBridge(t *testing.T) {
	for _, fixture := range setupRouteWordingFixtures(t) {
		t.Run(fixture.Name, func(t *testing.T) {
			view := setupRouteWordingView(t, fixture)
			bridge, frames := nativeBridgeFixture(t)
			bridge.session(true)
			done := make(chan error, 1)
			go func() { _, err := bridge.Ask(testOccurrence(view)); done <- err }()
			frame := nextNativeFrame(t, frames, "question")
			if frame.Prompt == nil || !frame.Prompt.PermissionPrompt || frame.Prompt.PairingPrompt || frame.Prompt.Title != "OpenAbstractions" || frame.Prompt.Instruction != fixture.Title || frame.Prompt.Content != fixture.Instruction || frame.Prompt.Details != fixture.Details {
				t.Fatal("shared bridge changed frozen wording or family", frame.Prompt)
			}
			if err := bridge.accept(nativeAsksReply{Kind: "close", ID: frame.ID}, nil); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
		})
	}
	// Metadata alone supplies neither the retained disclosures nor a family
	// fallback through which the person could answer a coarser question.
	view := trayQuestionViewForAsker(askclient.RecordMetadata{Key: asks.SetupModelsKey, Options: []string{"setup", "not_now"}}, trayPublisher{}, nil, "")
	if _, err := selectNativeQuestionPresentation(view); err == nil {
		t.Fatal("Setup without retained content selected a native family")
	}
}

func TestQuestionPresentationRejectsMissingAndContradictoryFamily(t *testing.T) {
	for _, view := range []trayQuestionPresentation{
		{},
		{presentation: nativeQuestionConsent},
		{presentation: nativeQuestionGeneric, Permission: &trayPermissionPresentation{}},
		{presentation: nativeQuestionAppChoice, Permission: &trayPermissionPresentation{}},
	} {
		if _, err := selectNativeQuestionPresentation(view); err == nil {
			t.Fatal("invalid family accepted")
		}
		bridge, _ := nativeBridgeFixture(t)
		bridge.session(true)
		if _, err := bridge.Ask(testOccurrence(view)); err == nil {
			t.Fatal("bridge accepted invalid family")
		}
	}
}
