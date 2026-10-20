package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

type setupQuestionSession struct {
	after, occurrence string
	key               string
	declined          bool
	report            func(context.Context, applications.SetupPresenterReceipt) (applications.SetupStepReport, error)
	present           func(context.Context, string, map[string]string) (bool, error)
}

func (s *setupQuestionSession) ask(ctx context.Context, q setup.StepQuestion) (bool, error) {
	if s.report == nil || s.present == nil {
		return false, errors.New("setup: runtime question owner unavailable")
	}
	if _, err := asks.SetupQuestionPresentation(q.Facts); err != nil {
		return false, err
	}
	planned := maps.Clone(q.Facts)
	if s.key == "" {
		var key [24]byte
		if _, err := rand.Read(key[:]); err != nil {
			return false, err
		}
		s.key = hex.EncodeToString(key[:])
	}
	got, err := s.report(ctx, applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeStarted, Key: s.key, Request: s.after, Facts: planned})
	if err != nil {
		return false, err
	}
	if got.Outcome != applications.OutcomePage || got.Question == "" {
		return false, fmt.Errorf("setup question refused: %s", got.Reason)
	}
	s.occurrence = got.Question
	yes, err := s.present(ctx, got.Question, planned)
	if err == nil && !yes {
		s.declined = true
	}
	return yes, err
}

func (s *setupQuestionSession) completed(ctx context.Context) error {
	if s.occurrence == "" || s.declined {
		return nil
	}
	got, err := s.report(ctx, applications.SetupPresenterReceipt{Outcome: applications.SetupReceiptOutcomeCompleted, Occurrence: s.occurrence})
	if err != nil {
		return err
	}
	if got.Outcome != applications.OutcomePage || got.Question != "" {
		return fmt.Errorf("setup completion refused: %s", got.Reason)
	}
	return nil
}

// handSetupQuestion checks that the Book occurrence id carries exactly the
// planned facts, then has show present it and return the option the person
// chose ("" when nobody answered there). The presenter owns exact-occurrence
// rechecks, lock interruption and AnswerQuestionIfPending; the Asks catalogue
// renders the retained plan facts.
func handSetupQuestion(ctx context.Context, operatorFor func(context.Context) (presenterOperator, error), account, id string, planned map[string]string,
	show func(context.Context, presenterOperator, string) (string, error)) (bool, error) {
	operator, err := operatorFor(ctx)
	if err != nil {
		return false, err
	}
	records, err := operator.ListAllQuestionsContext(ctx)
	if err != nil {
		return false, err
	}
	var record *askclient.RecordMetadata
	for i := range records {
		if records[i].ID == id {
			record = &records[i]
			break
		}
	}
	if record == nil || record.Key != asks.SetupModelsKey {
		return false, errors.New("setup: question occurrence differs")
	}
	if err := checkSetupQuestionFacts(ctx, operator, account, *record, planned); err != nil {
		return false, err
	}
	option, err := show(ctx, operator, id)
	if err != nil {
		return false, err
	}
	if option == "" {
		current, err := readQuestionContent(ctx, operator, account, id)
		if err != nil {
			return false, err
		}
		read := current.Question.Details.Question.Question
		if read.Record == nil || read.Record.Option == "" {
			return false, errStepQuestionClosed
		}
		if read.Outcome != askclient.OperatorQuestionOutcomeFound ||
			read.AnsweredBy == nil || *read.AnsweredBy != askclient.AnswererOperator ||
			!sameQuestionContent(*record, *read.Record) {
			return false, errors.New("setup: retained answer differs")
		}
		if err := checkSetupQuestionContent(current, *record, planned); err != nil {
			return false, err
		}
		option = read.Record.Option
	}
	return option == "setup", nil
}

func checkSetupQuestionFacts(ctx context.Context, operator presenterOperator, account string, record askclient.RecordMetadata, planned map[string]string) error {
	content, err := readQuestionContent(ctx, operator, account, record.ID)
	if err != nil {
		return err
	}
	return checkSetupQuestionContent(content, record, planned)
}

func checkSetupQuestionContent(content askclient.QuestionContent, record askclient.RecordMetadata, planned map[string]string) error {
	if content.Content == nil {
		return errors.New("setup: retained facts absent")
	}
	retained := maps.Clone(content.Content.Slots)
	facts, err := asks.SetupFactsFromSlots(retained)
	if err != nil || facts.Occurrence == "" || facts.Occurrence != record.About {
		return errors.New("setup: retained occurrence differs")
	}
	retained["occurrence"] = ""
	if !maps.Equal(retained, planned) {
		return errors.New("setup: frozen plan facts changed")
	}
	return nil
}
