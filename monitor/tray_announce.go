package main

import (
	"context"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// trayDisplayName names the program in its own dialog titles.
const trayDisplayName = "OpenAbstractions Panel"

// trayPollInterval and trayPollBudget bound how often and how long the tray
// checks the question book for new pending questions.
const (
	trayPollInterval = 3 * time.Second
	trayPollBudget   = 2 * time.Second
)

// trayAnnouncer remembers, for the life of the tray process, which questions
// it has already announced. An id it has announced once is never announced
// again, whether the question is later answered, retired, or (were an id
// ever reused, which the runtime does not do) pending again.
type trayAnnouncer struct {
	seen        map[string]bool
	choicesSeen map[string]bool
}

func newTrayAnnouncer() *trayAnnouncer {
	return &trayAnnouncer{seen: map[string]bool{}, choicesSeen: map[string]bool{}}
}

// forget lets a question dismissed because Windows locked the session be
// announced again after the person returns to the interactive desktop.
func (a *trayAnnouncer) forget(id string) { delete(a.seen, id) }

// update takes one ListQuestions reading, pending and answered records alike,
// and returns the pending records not yet announced, marking them announced.
// "Empty option means pending" (asks.thrift RecordMetadata).
func (a *trayAnnouncer) update(records []askclient.RecordMetadata) []askclient.RecordMetadata {
	var fresh []askclient.RecordMetadata
	for _, record := range records {
		if record.Option != "" || a.seen[record.ID] {
			continue
		}
		a.seen[record.ID] = true
		fresh = append(fresh, record)
	}
	return fresh
}

// listQuestions reads every page of the current book within ctx. A page
// bound defends against a misbehaving operator looping this forever; the
// book a tray polls holds far fewer records in practice.
func listQuestions(ctx context.Context, operator *askclient.Operator) ([]askclient.RecordMetadata, error) {
	var records []askclient.RecordMetadata
	cursor := ""
	for page := 0; page < 64; page++ {
		result, err := operator.ListQuestionsContext(ctx, cursor, 64)
		if err != nil {
			return nil, err
		}
		records = append(records, result.Records...)
		if result.Complete {
			return records, nil
		}
		cursor = result.Next
	}
	return records, nil
}

// pollTray runs one poll: resolve the asks operator through machine, list
// the current book within trayPollBudget, and report the pending questions
// newly seen. unavailable reports whether the runtime could not be reached
// this poll — not an error to the caller, since a runtime briefly
// unreachable is ordinary, not exceptional. The operator is the one the poll
// resolved, for reads about the fresh questions.
func pollTray(ctx context.Context, machine *facade.Machine, announcer *trayAnnouncer) (fresh []askclient.RecordMetadata, operator *askclient.Operator, unavailable bool) {
	ctx, cancel := context.WithTimeout(ctx, trayPollBudget)
	defer cancel()
	operator, err := machine.ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, nil, true
	}
	records, err := listQuestions(ctx, operator)
	if err != nil {
		return nil, nil, true
	}
	typed, typedUnavailable := pollPermissionRecords(ctx, machine)
	records = append(records, typed...)
	return announcer.update(records), operator, typedUnavailable
}
