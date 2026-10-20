package main

import (
	"errors"
	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

// Native question family is a semantic projection decision. Platform adapters
// render this selection; optional display payloads and app wording select none.
type nativeQuestionFamily = asks.PresentationFamily

const (
	nativeQuestionInvalid   = asks.PresentationInvalid
	nativeQuestionGeneric   = asks.PresentationQuestion
	nativeQuestionAppChoice = asks.PresentationApplicationChoice
	nativeQuestionConsent   = asks.PresentationConsent
	nativeQuestionPairing   = asks.PresentationPairingConsent
)

func catalogueQuestionFamily(key string) nativeQuestionFamily {
	return asks.PresentationForQuestion(key)
}

// Retained catalogue bodies enter the same family selection as every native
// question. Missing content leaves no prompt or options to present.
func retainedCatalogueQuestionView(record askclient.RecordMetadata, publisher trayPublisher, retained *askclient.CatalogueContent) (trayQuestionPresentation, bool) {
	if record.Key == asks.ProgramReviewKey {
		if retained == nil || catalogueQuestionFamily(record.Key) != nativeQuestionConsent {
			return trayQuestionPresentation{}, true
		}
		facts, err := asks.ProgramReviewFactsFromSlots(retained.Slots)
		if err != nil || len(facts.Targets) == 0 || retained.Slots["occurrence"] != record.About {
			return trayQuestionPresentation{}, true
		}
		title, instruction, details, err := asks.ProgramReviewPresentation(retained.Slots)
		if err != nil || instruction != record.Text {
			return trayQuestionPresentation{}, true
		}
		options := asks.ProgramReviewOptions(retained.Slots)
		if len(options) != len(record.Options) {
			return trayQuestionPresentation{}, true
		}
		for i := range options {
			if options[i] != record.Options[i] {
				return trayQuestionPresentation{}, true
			}
		}
		choices := questionChoices(options)
		_, unsigned := asks.ProgramReviewUnsigned(retained.Slots)
		// Architect popup ruling 2026-11-10, rows 11 and 12: Use it or Use
		// them, Repair, Not now; the question is the heading, the one
		// sentence its body, the file facts behind Show details.
		use := "Use them"
		if unsigned || len(facts.Targets) == 1 {
			use = "Use it"
		}
		ordered := []trayQuestionChoice{}
		for _, want := range []string{"keep", "repair", "not_now"} {
			for _, choice := range choices {
				if choice.Option != want {
					continue
				}
				switch want {
				case "keep":
					choice.Label = use
				case "repair":
					choice.Label = "Repair"
				}
				ordered = append(ordered, choice)
			}
		}
		view := consentDisplayView(trayQuestionPresentation{presentation: nativeQuestionConsent, Title: popupTitle, Instruction: title, Content: instruction, Details: details + "\n" + promptCloseDetail, Choices: ordered, Default: "not_now", Dismiss: "not_now", Level: promptLevelBlue}, publisher)
		view.Permission.Publisher = ""
		return view, true
	}
	if record.Key != asks.SetupModelsKey {
		return trayQuestionPresentation{}, false
	}
	if retained == nil || catalogueQuestionFamily(record.Key) != nativeQuestionConsent {
		return trayQuestionPresentation{}, true
	}
	facts, err := asks.SetupFactsFromSlots(retained.Slots)
	if err != nil || facts.Occurrence == "" || facts.Occurrence != record.About {
		return trayQuestionPresentation{}, true
	}
	body, err := asks.SetupQuestionPresentation(retained.Slots)
	if err != nil || body.Instruction != record.Text {
		return trayQuestionPresentation{}, true
	}
	// Architect popup ruling 2026-11-10, row 13: the heading is the Setup
	// question, the body its one sentence; Set up is the default because the
	// person started Setup.
	return consentDisplayView(trayQuestionPresentation{presentation: catalogueQuestionFamily(record.Key), Title: popupTitle, Instruction: body.Title, Content: body.Instruction, Details: body.Details, Choices: questionChoices(record.Options), Default: "setup", Dismiss: "not_now", Level: promptLevelBlue}, publisher), true
}

type nativeQuestionSelection struct {
	choice, consent, pairing bool
	card                     *trayPermissionPresentation
}

func selectNativeQuestionPresentation(prompt trayQuestionPresentation) (nativeQuestionSelection, error) {
	switch prompt.presentation {
	case nativeQuestionGeneric, nativeQuestionAppChoice:
		if prompt.Permission != nil {
			return nativeQuestionSelection{}, errors.New("native question: non-consent has a consent display payload")
		}
		return nativeQuestionSelection{choice: prompt.presentation == nativeQuestionAppChoice}, nil
	case nativeQuestionConsent, nativeQuestionPairing:
		if prompt.Permission == nil {
			return nativeQuestionSelection{}, errors.New("native question: consent display payload is missing")
		}
		return nativeQuestionSelection{consent: true, pairing: prompt.presentation == nativeQuestionPairing, card: prompt.Permission}, nil
	default:
		return nativeQuestionSelection{}, errors.New("native question: presentation family is missing or invalid")
	}
}
