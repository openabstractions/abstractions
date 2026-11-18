package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The Activity lines of rights changes an operator program requested (rights
// RIGHTS-O17, architect ruling A of 2026-11-22): the runtime writes one record
// to its own log for every change it applied, after the person's Allow or for
// an elevated caller without a question (serve runtime_rights_changes.go).
// Each is one row with Undo; the Panel window applies the Undo directly.

const rightsChangeActivityEvent = "rights.change.applied"

// rightsChangeUndo is the runtime's recorded Undo: the changes that reverse
// the line, and the exact rule state the change left.
type rightsChangeUndo struct {
	Changes []rwire.PolicyChange `json:"changes"`
	After   *struct {
		Subject  rwire.Subject `json:"subject"`
		Action   string        `json:"action"`
		Resource string        `json:"resource"`
		Present  bool          `json:"present"`
		Permit   bool          `json:"permit"`
	} `json:"after,omitempty"`
}

// maxRightsChangeUndo bounds a recorded Undo.
const maxRightsChangeUndo = 64 << 10

// rightsChangeActivityLine is one rights change line, or false for any other
// record. A record without a readable time or program is left out; an Undo
// the Panel cannot read leaves the line without Undo.
func rightsChangeActivityLine(record logwire.Record, names *programTitler) (activityEvent, bool) {
	program := record.Attrs["program"]
	if record.Attrs["oa_event"] != rightsChangeActivityEvent || program == "" || len(program) > 4096 {
		return activityEvent{}, false
	}
	at, err := time.Parse(time.RFC3339Nano, record.Time)
	if err != nil {
		return activityEvent{}, false
	}
	what := "Rights changed by the command line"
	if record.Attrs["exempt"] == "true" {
		what += " running as administrator"
	}
	change := record.Attrs["change"]
	if change == "" {
		change = "changed permissions"
	}
	event := activityEvent{At: at.UnixMilli(), App: names.name(program), Program: program, What: what, Result: upperFirst(change)}
	if _, ok := recordedUndo(record); ok {
		event.Undo = &activityUndo{At: at.UnixMilli(), Line: record.Attrs["line"]}
	}
	return event, true
}

// recordedUndo is a rights change record's stored Undo, when it carries a
// line id and an Undo this Panel reads.
func recordedUndo(record logwire.Record) (rightsChangeUndo, bool) {
	undo, line := record.Attrs["undo"], record.Attrs["line"]
	if record.Attrs["oa_event"] != rightsChangeActivityEvent || undo == "" || line == "" || len(undo) > maxRightsChangeUndo {
		return rightsChangeUndo{}, false
	}
	var decoded rightsChangeUndo
	if json.Unmarshal([]byte(undo), &decoded) != nil || len(decoded.Changes) == 0 || len(decoded.Changes) > 32 {
		return rightsChangeUndo{}, false
	}
	return decoded, true
}

// findRecordedUndo reads the runtime's own log again for the rights change
// line at at with id line, and returns its stored Undo.
func findRecordedUndo(ctx context.Context, at int64, line string) (rightsChangeUndo, bool, error) {
	reader, target, err := messageActivityHistory(ctx)
	if err != nil {
		return rightsChangeUndo{}, false, err
	}
	cursor := ""
	for range elevatedActivityPages {
		page, err := reader.ReadContext(ctx, target, cursor, 64, 65536)
		if err != nil {
			return rightsChangeUndo{}, false, err
		}
		if page.Outcome != logwire.HistoryPageOutcomePage {
			return rightsChangeUndo{}, false, errors.New("the activity history is " + page.Outcome.String())
		}
		for _, record := range page.Records {
			when, err := time.Parse(time.RFC3339Nano, record.Time)
			if err != nil || when.UnixMilli() != at || record.Attrs["line"] != line {
				continue
			}
			undo, ok := recordedUndo(record)
			return undo, ok, nil
		}
		if page.AtEnd || page.Next == "" || page.Next == cursor {
			break
		}
		cursor = page.Next
	}
	return rightsChangeUndo{}, false, nil
}

func upperFirst(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// rightsChangeOperator is the rights operator surface Undo uses.
type rightsChangeOperator interface {
	ReadRuleContext(ctx context.Context, subject rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error)
	RequestChangeContext(ctx context.Context, change rwire.PolicyChange, waitMS int64) (rwire.ChangeRequestState, error)
}

// rightsChangeUndoOperator resolves the rights operator per request. Tests
// replace it.
var rightsChangeUndoOperator = func(ctx context.Context) (rightsChangeOperator, error) {
	return panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// activityRightsChanges answers POST {at, line} with the Undo of one rights
// change line. Undo requires the rule the change left to still stand, then
// applies each reversing change as the Panel window: directly, with no
// question.
func (p *servicePanel) activityRightsChanges(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	var posted activityUndo
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&posted); err != nil || posted.Line == "" || len(posted.Line) > 64 || posted.At <= 0 {
		http.Error(w, "choose one rights change to undo", http.StatusBadRequest)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	// The Undo applied is the one the runtime recorded, read again from its
	// own log; the request names the line only.
	undo, found, err := findRecordedUndo(ctx, posted.At, posted.Line)
	if err != nil {
		panelError(w, err)
		return
	}
	if !found {
		http.Error(w, "This change has no Undo any more.", http.StatusConflict)
		return
	}
	operator, err := rightsChangeUndoOperator(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	if after := undo.After; after != nil {
		read, err := operator.ReadRuleContext(ctx, after.Subject, after.Action, after.Resource)
		if err != nil {
			panelError(w, err)
			return
		}
		present := read.Outcome == rightsclient.RuleReadOutcomeFound && read.Record != nil
		if present != after.Present || present && read.Record.Rule.Permit != after.Permit {
			http.Error(w, "This permission changed since. Nothing was undone.", http.StatusConflict)
			return
		}
	}
	for _, change := range undo.Changes {
		change.ExpectedRevision = ""
		state, err := operator.RequestChangeContext(ctx, change, 0)
		if err != nil {
			panelError(w, err)
			return
		}
		if state.Outcome != rwire.ChangeRequestOutcomeApplied {
			panelError(w, errors.New("the rights change could not be undone: "+state.Outcome.String()))
			return
		}
	}
	p.logAction(ctx, "rights.change_undo", map[string]string{"rights.changes": strconv.Itoa(len(undo.Changes))})
	panelJSON(w, struct {
		Message string `json:"message"`
	}{"Undone."})
}
