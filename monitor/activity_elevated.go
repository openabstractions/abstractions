package main

import (
	"net/http"
	"time"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
)

// The Activity timeline's administrator lines (rights RIGHTS-R27, owner
// 2026-11-11): the runtime writes one record to its own log the first time
// in a runtime session a program running as administrator on this account is
// admitted without a question (serve runtime_elevated.go). Each is one row,
// "<App> ran as administrator; OpenAbstractions didn't ask". The runtime's
// history is read like the message audit, through the runtime program the
// installation expects (messageActivityHistory).

const (
	elevatedActivityEvent = "rights.elevated.session"
	elevatedActivityPages = 8
)

// activityElevated reads retained runtime events for the timeline.
func (p *servicePanel) activityElevated(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	reader, target, err := messageActivityHistory(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	out := activityPage{Outcome: "page", Events: []activityEvent{}, Complete: true}
	names := p.programTitler(ctx)
	cursor := ""
	edits := map[[2]string]bool{}
	for range elevatedActivityPages {
		page, err := reader.ReadContext(ctx, target, cursor, 64, 65536)
		if err != nil {
			panelError(w, err)
			return
		}
		if page.Outcome != logwire.HistoryPageOutcomePage {
			out.Outcome = page.Outcome.String()
			out.Events = []activityEvent{}
			break
		}
		for _, record := range page.Records {
			if event, ok := elevatedActivityLine(record, names); ok {
				out.Events = append(out.Events, event)
			} else if event, ok := openLaterActivityLine(record, names); ok {
				out.Events = append(out.Events, event)
			} else if event, ok := rightsChangeActivityLine(record, names); ok {
				out.Events = append(out.Events, event)
			} else if event, key, ok := pictureGraphEditActivityLine(record); ok && !edits[key] {
				edits[key] = true
				out.Events = append(out.Events, event)
			}
		}
		if page.AtEnd || page.Next == "" || page.Next == cursor {
			break
		}
		cursor = page.Next
	}
	panelJSON(w, out)
}

// elevatedActivityLine is one administrator line, or false for any other
// record. A record without a readable time or program is left out.
func elevatedActivityLine(record logwire.Record, names *programTitler) (activityEvent, bool) {
	program := record.Attrs["program"]
	if record.Attrs["oa_event"] != elevatedActivityEvent || program == "" || len(program) > 4096 {
		return activityEvent{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, record.Time)
	if err != nil {
		return activityEvent{}, false
	}
	return activityEvent{At: at.UnixMilli(), App: names.name(program), Program: program, What: "Ran as administrator", Result: "OpenAbstractions didn't ask"}, true
}

// openLaterActivityEvent is the runtime's record of an agent's open that the
// person allowed after the agent's call ended (serve
// application_v3_open_later.go, architect ruling 2026-11-22).
const openLaterActivityEvent = "applications.open.later"

// openLaterActivityLine is one deferred open, attributed to the agent that
// asked: "Open <App> after your Allow" and what came of it.
func openLaterActivityLine(record logwire.Record, names *programTitler) (activityEvent, bool) {
	program, title := record.Attrs["program"], record.Attrs["title"]
	if record.Attrs["oa_event"] != openLaterActivityEvent || program == "" || len(program) > 4096 {
		return activityEvent{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, record.Time)
	if err != nil {
		return activityEvent{}, false
	}
	if title == "" {
		title = record.Attrs["application"]
	}
	result := "Not opened: something changed since the question"
	switch {
	case record.Attrs["outcome"] == "ready":
		result = "Opened"
	case record.Attrs["reason"] == "blocked":
		result = "Not opened: you blocked it"
	case record.Attrs["outcome"] != "unavailable" && record.Attrs["outcome"] != "forbidden":
		result = "Not opened: " + title + " did not start"
	}
	return activityEvent{At: at.UnixMilli(), App: names.name(program), Program: program, What: "Open " + title + " after your Allow", Result: result}, true
}

// The runtime's attributed command audit records the accepted projection
// revision. A replay can have a different request receipt but cannot advance
// that revision. Keep one line for the instance and committed revision.
func pictureGraphEditActivityLine(record logwire.Record) (activityEvent, [2]string, bool) {
	if record.Msg != "abstraction.facade/appmodel@1" || record.Attrs["operation"] != "application.command" {
		return activityEvent{}, [2]string{}, false
	}
	call, err := appmodel.DecodeCallRecord([]byte(record.Attrs["call_record"]))
	registration, title := picturePackageIdentity()
	if err != nil || call.Application != registration || call.Command != "graph.edit" ||
		call.Class == nil || *call.Class != appmodel.CommandClassEdit || call.Outcome != appmodel.CommandOutcomeApplied.String() ||
		call.Reason != "" || call.Instance == "" || call.RevisionBefore == "" || call.RevisionAfter == "" || call.RevisionBefore == call.RevisionAfter {
		return activityEvent{}, [2]string{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, record.Time)
	if err != nil || record.Attrs["regarding"] == "" {
		return activityEvent{}, [2]string{}, false
	}
	return activityEvent{At: at.UnixMilli(), App: title, Program: record.Attrs["regarding"],
		What: "Edited the " + title + " canvas", Result: "Use Ctrl+Z in " + title + " to undo"}, [2]string{call.Instance, call.RevisionAfter}, true
}
