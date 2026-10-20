package main

import (
	"errors"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
)

// bookOccurrence is one retained Asks Book occurrence and the display the
// presenter projected from its retained content. It is the only value the
// native question surface accepts (presenterSurface.Ask): the native question
// style renders only Book occurrences, so OpenAbstractions' own
// confirmations, reports and failures cannot reach it (abstraction-asks
// CONTRACT). They go to the Panel window or to a notification.
//
// Only questionOccurrence and choiceOccurrence construct one, and each binds
// the presentation family to the occurrence's recorded key: a view cannot
// choose the consent style for itself.
type bookOccurrence struct {
	id   string
	view trayQuestionPresentation
	// authority is the Book key of an authority-granting question, or empty.
	authority string
}

var errNotBookOccurrence = errors.New("native question: only an Asks Book occurrence is presented")

// questionOccurrence binds a question or typed permission read from the Book
// to its view. The view's family is the one Asks assigns the recorded key.
func questionOccurrence(record askclient.RecordMetadata, view trayQuestionPresentation) (bookOccurrence, error) {
	if !questionWord(record.ID) || view.presentation != catalogueQuestionFamily(record.Key) {
		return bookOccurrence{}, errNotBookOccurrence
	}
	occurrence := bookOccurrence{id: record.ID, view: view}
	if authorityGranting(record.Key) {
		occurrence.authority = record.Key
	}
	return occurrence, nil
}

// authorityGranting reports whether an answer to the question under key can
// grant authority: a consent or pairing question (permissions, program
// trust, Setup, enrollment, updates, command once, pairing), or a catalogue
// question with an option that says yes. Its native prompt then counts only
// the person's own mouse or key presses (POPUP-STYLE Q14 part D). An
// application's free-text question and its choices grant nothing.
func authorityGranting(key string) bool {
	switch catalogueQuestionFamily(key) {
	case nativeQuestionConsent, nativeQuestionPairing:
		return true
	case nativeQuestionGeneric:
		if question, ok := asks.Find(key); ok {
			for _, option := range question.Options {
				if option.Yes {
					return true
				}
			}
		}
	}
	return false
}

// choiceOccurrence binds an application choice read from the Book to its
// view, which keeps the application-choice family.
func choiceOccurrence(record askclient.ChoiceRecord, view trayQuestionPresentation) (bookOccurrence, error) {
	if !questionWord(record.ID) || view.presentation != nativeQuestionAppChoice {
		return bookOccurrence{}, errNotBookOccurrence
	}
	return bookOccurrence{id: record.ID, view: view}, nil
}

// ID is the Book id of the occurrence.
func (o bookOccurrence) ID() string { return o.id }

// Authority is the Book key of an authority-granting question, or empty for
// a question or choice whose answer grants nothing.
func (o bookOccurrence) Authority() string { return o.authority }

// Presentation is the projected display.
func (o bookOccurrence) Presentation() trayQuestionPresentation { return o.view }
