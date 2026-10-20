package main

import (
	"context"
	"slices"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
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
		if !asks.AutomaticNativeQuestion(record.Key) || record.Option != "" || a.seen[record.ID] {
			continue
		}
		a.seen[record.ID] = true
		fresh = append(fresh, record)
	}
	return fresh
}

// resolveTrayOperator resolves the asks operator a tray poll lists through;
// tests replace it with an operator over a fake frame server.
var resolveTrayOperator = func(ctx context.Context, machine *facade.Machine) (*askclient.Operator, error) {
	return machine.ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
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
	operator, err := resolveTrayOperator(ctx, machine)
	if err != nil {
		return nil, nil, true
	}
	// The one ASK-O2 reader: any list refusal, such as unavailable,
	// forbidden, stalled or a gap twice, is unavailable, never an empty book.
	records, err := listPresenterQuestions(ctx, operator)
	if err != nil {
		return nil, nil, true
	}
	typed, typedUnavailable := pollPermissionRecords(ctx, machine)
	return announcer.update(orderTrayQuestions(records, typed)), operator, typedUnavailable
}

// orderTrayQuestions merges the two operator routes by the Book's Asked time.
// Equal times keep their existing source order. Unreadable times sort last.
func orderTrayQuestions(records, typed []askclient.RecordMetadata) []askclient.RecordMetadata {
	records = append(slices.Clone(records), typed...)
	slices.SortStableFunc(records, func(a, b askclient.RecordMetadata) int {
		at, aErr := time.Parse(time.RFC3339Nano, a.Asked)
		bt, bErr := time.Parse(time.RFC3339Nano, b.Asked)
		switch {
		case aErr != nil && bErr != nil:
			return 0
		case aErr != nil:
			return 1
		case bErr != nil:
			return -1
		default:
			return at.Compare(bt)
		}
	})
	return records
}
