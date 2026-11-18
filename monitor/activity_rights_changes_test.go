package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

type fakeChangeOperator struct {
	current *rwire.RuleRecord
	applied []rwire.PolicyChange
}

func (f *fakeChangeOperator) ReadRuleContext(_ context.Context, subject rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error) {
	if f.current == nil {
		return rightsclient.RuleRead{Outcome: rightsclient.RuleReadOutcomeUnknown, Revision: "r"}, nil
	}
	return rightsclient.RuleRead{Outcome: rightsclient.RuleReadOutcomeFound, Revision: "r", Record: f.current}, nil
}

func (f *fakeChangeOperator) RequestChangeContext(_ context.Context, change rwire.PolicyChange, _ int64) (rwire.ChangeRequestState, error) {
	f.applied = append(f.applied, change)
	return rwire.ChangeRequestState{Outcome: rwire.ChangeRequestOutcomeApplied, ID: "x", Change: &change}, nil
}

// A rights change line reads who changed what. Its Undo names the line only:
// the Panel reads the runtime's own log again and applies the Undo recorded
// there, and only while the rule the change left stands (rights RIGHTS-O17,
// security review 2026-11-22).
func TestRightsChangeActivityLineAndUndo(t *testing.T) {
	subject := rwire.Subject{Account: "a", Program: `C:\apps\opencode.exe`}
	rule := rwire.PolicyRule{Subject: subject, Action: "abstraction.job/acceptance.submit", Resource: "job:download"}
	undo, _ := json.Marshal(map[string]any{
		"changes": []rwire.PolicyChange{{Kind: rwire.ChangeKindRevokeRule, Rule: &rule}},
		"after":   map[string]any{"subject": subject, "action": rule.Action, "resource": rule.Resource, "present": true, "permit": true},
	})
	record := logwire.Record{Time: "2026-11-22T10:00:00Z", Attrs: map[string]string{"oa_event": rightsChangeActivityEvent, "line": "line1",
		"program": `C:\oa\openabstractions.exe`, "exempt": "true", "change": "give opencode permission to download files", "undo": string(undo)}}
	line, ok := rightsChangeActivityLine(record, newProgramTitler(nil))
	if !ok || line.What != "Rights changed by the command line running as administrator" || line.Result != "Give opencode permission to download files" || line.Undo == nil || line.Undo.Line != "line1" {
		t.Fatalf("line %+v %v", line, ok)
	}
	history := &messageHistoryFake{page: logclient.ApplicationLogPage{Outcome: logwire.HistoryPageOutcomePage, AtEnd: true, Records: []logwire.Record{record}}}
	oldHistory := messageActivityHistory
	t.Cleanup(func() { messageActivityHistory = oldHistory })
	messageActivityHistory = func(context.Context) (messageHistoryReader, logclient.ApplicationLogTarget, error) {
		return history, logclient.ApplicationLogTarget{Selection: logwire.ApplicationLogSelectionOneApp, Program: "native-runtime"}, nil
	}

	post := func(op *fakeChangeOperator, body string) int {
		saved := rightsChangeUndoOperator
		rightsChangeUndoOperator = func(context.Context) (rightsChangeOperator, error) { return op, nil }
		defer func() { rightsChangeUndoOperator = saved }()
		w := httptest.NewRecorder()
		(&servicePanel{}).activityRightsChanges(w, httptest.NewRequest(http.MethodPost, "/activity/rights-changes", strings.NewReader(body)))
		return w.Code
	}
	named, _ := json.Marshal(line.Undo)
	standing := func() *fakeChangeOperator {
		return &fakeChangeOperator{current: &rwire.RuleRecord{Rule: rwire.PolicyRule{Subject: subject, Action: rule.Action, Resource: rule.Resource, Permit: true}}}
	}
	// A posted change list is no Undo: only the recorded one applies.
	forged := standing()
	if code := post(forged, `{"at":1,"line":"other","changes":[{"Kind":"set_rule"}]}`); code != http.StatusConflict || len(forged.applied) != 0 {
		t.Fatalf("an unrecorded Undo: %d %+v", code, forged.applied)
	}
	changed := &fakeChangeOperator{current: &rwire.RuleRecord{Rule: rwire.PolicyRule{Subject: subject, Action: rule.Action, Resource: rule.Resource, Permit: false}}}
	if code := post(changed, string(named)); code != http.StatusConflict || len(changed.applied) != 0 {
		t.Fatalf("Undo of a rule changed since: %d %+v", code, changed.applied)
	}
	ok2 := standing()
	if code := post(ok2, string(named)); code != http.StatusOK || len(ok2.applied) != 1 || ok2.applied[0].Kind != rwire.ChangeKindRevokeRule {
		t.Fatalf("Undo: %d %+v", code, ok2.applied)
	}
}
