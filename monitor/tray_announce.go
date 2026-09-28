package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// trayDisplayName names the program in its own notifications.
const trayDisplayName = "Abstraction Panel"

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
	seen map[string]bool
}

func newTrayAnnouncer() *trayAnnouncer { return &trayAnnouncer{seen: map[string]bool{}} }

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
// unreachable is ordinary, not exceptional.
func pollTray(ctx context.Context, machine *facade.Machine, announcer *trayAnnouncer) (fresh []askclient.RecordMetadata, unavailable bool) {
	ctx, cancel := context.WithTimeout(ctx, trayPollBudget)
	defer cancel()
	operator, err := machine.ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, true
	}
	records, err := listQuestions(ctx, operator)
	if err != nil {
		return nil, true
	}
	return announcer.update(records), false
}

// trayNotificationText renders one pending record as a notification title
// and body: the asking program's display name, and the action waiting on it.
// A question outside the runtime's own first-use catalog falls back to the
// question's own rendered sentence under this program's name.
func trayNotificationText(record askclient.RecordMetadata) (title, body string) {
	if program, action, resource, ok := asks.FirstUse(record.Key, record.About, record.Text); ok {
		return trayProgramDisplayName(program), fmt.Sprintf("wants to %s on %s. Allow it in the %s.", action, resource, trayDisplayName)
	}
	return trayDisplayName, record.Text
}

// trayProgramDisplayName takes the file name off a program path recorded by
// either convention, since the record names a Windows program (the asking
// application this tray watches for) whether this tray build runs on Windows
// or, as in its cross-platform tests, on Linux. A plain filepath.Base would
// only split on the host's own separator and leave a foreign-separated path
// whole, the way abstraction-resource/go/instrument's fdinfo.go already
// treats a recorded program path.
func trayProgramDisplayName(program string) string {
	if idx := strings.LastIndexAny(program, "/\\"); idx >= 0 && idx+1 < len(program) {
		return program[idx+1:]
	}
	return program
}
