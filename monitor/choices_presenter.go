package main

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// This is the ordinary branch of the existing native presenter. The public
// operator owns attribution, authority and terminal outcomes (ASK-O9).
// Application labels have no permission effect and never enter grants.
type presenterChoiceOperator interface {
	ListAllChoicesContext(context.Context) ([]askclient.ChoiceRecord, error)
	ReadChoiceContext(context.Context, string) (askclient.ChoiceRead, error)
	AnswerChoiceContext(context.Context, string, string) (askclient.ChoiceDecision, error)
	DismissChoiceContext(context.Context, string) (askclient.ChoiceDecision, error)
}

func defaultPresenterChoices(ctx context.Context) (presenterChoiceOperator, error) {
	return panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

func (p *questionPresenter) choiceOperator(ctx context.Context) (presenterChoiceOperator, error) {
	if p.choices != nil {
		return p.choices(ctx)
	}
	return defaultPresenterChoices(ctx)
}

func ordinaryUnsupported(err error) bool {
	var service *askclient.ServiceError
	return errors.As(err, &service) && service.Code == askclient.ServiceErrorCodeUnknownMethod
}

func (p *questionPresenter) choiceTime() time.Time {
	if p.choiceNow != nil {
		return p.choiceNow()
	}
	return time.Now()
}

func choicePendingAt(record askclient.ChoiceRecord, now time.Time) bool {
	return record.State == askclient.ChoiceStatePending && record.Answer == nil &&
		now.UnixMilli() < record.Request.PendingUntilMs && now.UnixMilli() < record.Request.RequestRef.ReplayUntilMs
}

// Every field exposed by the service is frozen. Display projections and option
// labels never substitute for the original request or recorded attribution.
func sameChoiceContent(a, b askclient.ChoiceRecord) bool {
	return a.ID == b.ID && a.Asker == b.Asker && a.AskedMs == b.AskedMs &&
		a.Request.RequestRef == b.Request.RequestRef && a.Request.Title == b.Request.Title &&
		a.Request.PendingUntilMs == b.Request.PendingUntilMs && slices.Equal(a.Request.Options, b.Request.Options) &&
		a.Publisher == b.Publisher && a.PublisherTrusted == b.PublisherTrusted &&
		a.ProgramDescription == b.ProgramDescription && a.ProgramOriginalName == b.ProgramOriginalName
}

func cloneChoiceRecord(record askclient.ChoiceRecord) askclient.ChoiceRecord {
	record.Request.Options = slices.Clone(record.Request.Options)
	if record.Answer != nil {
		answer := *record.Answer
		record.Answer = &answer
	}
	return record
}

type choicePresentResult struct {
	Outcome         presentOutcome
	Decision        askclient.ChoiceDecision
	Err             error
	Unsupported     bool
	ClosedElsewhere bool
}

func (p *questionPresenter) readShownChoice(ctx context.Context, shown askclient.ChoiceRecord) (askclient.ChoiceRecord, choicePresentResult) {
	ctx, cancel := context.WithTimeout(ctx, p.callBudget())
	defer cancel()
	operator, err := p.choiceOperator(ctx)
	if err != nil {
		return askclient.ChoiceRecord{}, choicePresentResult{Outcome: presentUnavailable, Err: err}
	}
	read, err := operator.ReadChoiceContext(ctx, shown.ID)
	if err != nil {
		return askclient.ChoiceRecord{}, choicePresentResult{Outcome: presentUnavailable, Err: err, Unsupported: ordinaryUnsupported(err)}
	}
	if read.Outcome == askclient.OperatorQuestionOutcomeUnknown {
		return askclient.ChoiceRecord{}, choicePresentResult{Outcome: presentStale, ClosedElsewhere: true}
	}
	if read.Outcome != askclient.OperatorQuestionOutcomeFound || read.Record == nil {
		return askclient.ChoiceRecord{}, choicePresentResult{Outcome: presentUnavailable}
	}
	if !asks.ValidChoiceRecord(*read.Record) {
		return askclient.ChoiceRecord{}, choicePresentResult{Outcome: presentUnavailable, Err: errors.New("invalid ordinary record")}
	}
	if !choicePendingAt(*read.Record, p.choiceTime()) || !sameChoiceContent(shown, *read.Record) {
		return askclient.ChoiceRecord{}, choicePresentResult{Outcome: presentStale, ClosedElsewhere: true}
	}
	return cloneChoiceRecord(*read.Record), choicePresentResult{Outcome: presentAnswered}
}

func (p *questionPresenter) presentChoice(ctx context.Context, listed askclient.ChoiceRecord) choicePresentResult {
	if !p.session.Unlocked() {
		return choicePresentResult{Outcome: presentDeferred}
	}
	revision := p.session.Revision()
	shown, checked := p.readShownChoice(ctx, listed)
	if checked.Outcome != presentAnswered {
		return checked
	}
	if p.session.Revision() != revision || !p.session.Unlocked() {
		return choicePresentResult{Outcome: presentDeferred}
	}
	watchCtx, stop := context.WithCancel(ctx)
	var watching sync.WaitGroup
	var stale bool
	var mu sync.Mutex
	watching.Add(1)
	go func() {
		defer watching.Done()
		if p.watchChoice(watchCtx, shown) {
			mu.Lock()
			stale = true
			mu.Unlock()
			p.surface.Dismiss()
		}
	}()
	occurrence, err := choiceOccurrence(shown, trayChoiceView(shown, machineProgramTitler(ctx)))
	if err != nil {
		stop()
		watching.Wait()
		return choicePresentResult{Outcome: presentFailed, Err: err}
	}
	option, err := p.surface.Ask(occurrence)
	stop()
	watching.Wait()
	if p.session.Revision() != revision || !p.session.Unlocked() || errors.Is(err, errPromptNotShown) {
		return choicePresentResult{Outcome: presentDeferred}
	}
	if err != nil {
		return choicePresentResult{Outcome: presentFailed, Err: err}
	}
	mu.Lock()
	interrupted := stale
	mu.Unlock()
	if interrupted {
		return choicePresentResult{Outcome: presentStale, ClosedElsewhere: true}
	}
	if option != "" && !slices.ContainsFunc(shown.Request.Options, func(c askclient.ChoiceOption) bool { return c.ID == option }) {
		return choicePresentResult{Outcome: presentStale}
	}
	return p.decideChoice(ctx, shown, option, revision)
}

func (p *questionPresenter) decideChoice(ctx context.Context, shown askclient.ChoiceRecord, option string, revision uint64) choicePresentResult {
	ctx, cancel := context.WithTimeout(ctx, p.callBudget())
	defer cancel()
	if _, checked := p.readShownChoice(ctx, shown); checked.Outcome != presentAnswered {
		return checked
	}
	operator, err := p.choiceOperator(ctx)
	if err != nil {
		return choicePresentResult{Outcome: presentUnavailable, Err: err}
	}
	if p.session.Revision() != revision || !p.session.Unlocked() {
		return choicePresentResult{Outcome: presentDeferred}
	}
	if !choicePendingAt(shown, p.choiceTime()) {
		return choicePresentResult{Outcome: presentStale}
	}
	var decision askclient.ChoiceDecision
	if option == "" {
		decision, err = operator.DismissChoiceContext(ctx, shown.ID)
	} else {
		decision, err = operator.AnswerChoiceContext(ctx, shown.ID, option)
	}
	return choicePresentResult{Outcome: presentAnswered, Decision: decision, Err: err, Unsupported: ordinaryUnsupported(err)}
}

func (p *questionPresenter) watchChoice(ctx context.Context, shown askclient.ChoiceRecord) bool {
	every := p.watchEvery
	if every <= 0 {
		every = trayPollInterval
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()
	deadline := time.NewTimer(max(time.Duration(shown.Request.PendingUntilMs-p.choiceTime().UnixMilli())*time.Millisecond, 0))
	defer deadline.Stop()
	for {
		select {
		case <-ctx.Done():
			return false
		case <-deadline.C:
			return true
		case <-ticker.C:
		}
		if !choicePendingAt(shown, p.choiceTime()) {
			return true
		}
		_, checked := p.readShownChoice(ctx, shown)
		if ctx.Err() == nil && checked.Outcome == presentStale {
			return true
		}
	}
}

func trayChoiceView(record askclient.ChoiceRecord, names *programTitler) trayQuestionPresentation {
	publisher := trayPublisher{Trusted: record.PublisherTrusted, Subject: record.Publisher,
		Description: record.ProgramDescription, OriginalName: record.ProgramOriginalName, Names: names, Prompt: true}
	// Architect popup ruling 2026-11-10, row 19: the app's own question as
	// the heading, "From <App>." and the inline publisher line as the body.
	content := "From " + programDisplayName(publisher, record.Asker) + "."
	if line := promptPublisherSummary(publisher); line != "" {
		content += "\n" + line
	}
	details := []string{promptPublisherDetail(publisher), "Closing this window dismisses the request without choosing.", "", "Request: " + record.ID, "Requester recorded by the service: " + record.Asker,
		"Title supplied by the app: " + record.Request.Title, "Key: " + record.Request.RequestRef.Key,
		fmt.Sprintf("Asked (Unix ms): %d", record.AskedMs),
		fmt.Sprintf("Pending until (Unix ms): %d", record.Request.PendingUntilMs),
		fmt.Sprintf("Replay until (Unix ms): %d", record.Request.RequestRef.ReplayUntilMs),
		"Recorded publisher: " + record.Publisher, fmt.Sprintf("Publisher trusted: %t", record.PublisherTrusted),
		"Recorded description: " + record.ProgramDescription, "Recorded original name: " + record.ProgramOriginalName}
	choices := make([]trayQuestionChoice, len(record.Request.Options))
	for i, option := range record.Request.Options {
		choices[i] = trayQuestionChoice{Label: option.Label, Option: option.ID}
		details = append(details, "Option "+option.ID+": "+option.Label)
	}
	return trayQuestionPresentation{presentation: asks.PresentationForApplicationChoice(), Title: popupTitle, Instruction: record.Request.Title,
		Content: content, Details: strings.Join(details, "\n"), Choices: choices, AppChoice: true,
		Level: promptLevelFor(promptEvidence{Program: record.Asker, Publisher: publisher})}
}

// Separate typed listing keeps old operators and permission record shapes
// compatible. Only an explicit unknown_method means unsupported ordinary UI.
func listPresenterChoices(ctx context.Context, operator presenterChoiceOperator) ([]askclient.ChoiceRecord, error) {
	return presenterList(operator.ListAllChoicesContext(ctx))
}

func pollChoices(ctx context.Context, p *questionPresenter, announcer *trayAnnouncer) ([]askclient.ChoiceRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, trayPollBudget)
	defer cancel()
	operator, err := p.choiceOperator(ctx)
	if err != nil {
		return nil, err
	}
	records, err := listPresenterChoices(ctx, operator)
	if err != nil {
		return nil, err
	}
	if announcer.choicesSeen == nil {
		announcer.choicesSeen = map[string]bool{}
	}
	var fresh []askclient.ChoiceRecord
	for _, record := range records {
		if !choicePendingAt(record, p.choiceTime()) || announcer.choicesSeen[record.ID] {
			continue
		}
		announcer.choicesSeen[record.ID] = true
		fresh = append(fresh, cloneChoiceRecord(record))
	}
	return fresh, nil
}
