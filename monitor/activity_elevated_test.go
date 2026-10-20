package main

import (
	"context"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	logwire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
)

// The administrator lines (rights RIGHTS-R27): only the runtime's session
// record becomes a row, worded "<App> ran as administrator; OpenAbstractions
// didn't ask"; the per-action log lines and other records stay out.
func TestActivityElevatedOneRowPerSessionRecord(t *testing.T) {
	history := &messageHistoryFake{page: logclient.ApplicationLogPage{Outcome: logwire.HistoryPageOutcomePage, AtEnd: true, Records: []logwire.Record{
		{Time: "2026-11-11T09:00:00Z", Msg: "Ran as administrator; OpenAbstractions didn't ask", Attrs: map[string]string{"oa_event": "rights.elevated.session", "program": `C:\Program Files\Tool\tool.exe`}},
		{Time: "2026-11-11T09:00:00Z", Msg: "Admitted an administrator's request without asking", Attrs: map[string]string{"oa_event": "rights.elevated.admit", "program": `C:\Program Files\Tool\tool.exe`, "action": "a.b/c", "resource": "r"}},
		{Time: "2026-11-11T09:00:01Z", Msg: "Application message", Attrs: map[string]string{"oa_message": "1"}},
		{Time: "not a time", Attrs: map[string]string{"oa_event": "rights.elevated.session", "program": "x"}},
	}}}
	old := messageActivityHistory
	t.Cleanup(func() { messageActivityHistory = old })
	messageActivityHistory = func(context.Context) (messageHistoryReader, logclient.ApplicationLogTarget, error) {
		return history, logclient.ApplicationLogTarget{Selection: logwire.ApplicationLogSelectionOneApp, Program: "native-runtime"}, nil
	}
	panel := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, panel, "/activity/elevated", nil)
	page := decodePanel[activityPage](t, r.Code, r.Body.Bytes())
	if page.Outcome != "page" || len(page.Events) != 1 {
		t.Fatal(page)
	}
	e := page.Events[0]
	if e.What != "Ran as administrator" || e.Result != "OpenAbstractions didn't ask" || e.Program != `C:\Program Files\Tool\tool.exe` || e.App == "" || e.At != 1794387600000 {
		t.Fatal(e)
	}
}

func TestActivityComfyGraphEditsShowOnceWithoutUndoButton(t *testing.T) {
	row := func(application, at, key, outcome, before, after string) logwire.Record {
		call := appmodel.CallRecord{Application: application, Instance: "comfy-instance", Command: "graph.edit",
			Class: new(appmodel.CommandClassEdit), IdempotencyKey: key, Outcome: outcome, RevisionBefore: before, RevisionAfter: after}
		return logwire.Record{Time: at, Msg: "abstraction.facade/appmodel@1", Attrs: map[string]string{
			"operation": "application.command", "regarding": "comfy-connector", "call_record": string(appmodel.EncodeCallRecord(&call)),
		}}
	}
	const at = "2026-11-13T08:00:00Z"
	history := &messageHistoryFake{page: logclient.ApplicationLogPage{Outcome: logwire.HistoryPageOutcomePage, AtEnd: true, Records: []logwire.Record{
		row("comfyui", at, "first", "applied", "r1", "r2"),
		row("comfyui", "2026-11-13T08:00:01Z", "first", "applied", "r1", "r2"),
		row("comfyui", at, "no-op", "applied", "r2", "r2"),
		row("comfyui", at, "refused", "conflict", "r2", "r3"),
		row("other-editor", at, "other", "applied", "r2", "r4"),
		row("comfyui", at, "second", "applied", "r2", "r3"),
	}}}
	old := messageActivityHistory
	t.Cleanup(func() { messageActivityHistory = old })
	messageActivityHistory = func(context.Context) (messageHistoryReader, logclient.ApplicationLogTarget, error) {
		return history, logclient.ApplicationLogTarget{Selection: logwire.ApplicationLogSelectionOneApp, Program: "native-runtime"}, nil
	}
	panel := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, panel, "/activity/elevated", nil)
	page := decodePanel[activityPage](t, r.Code, r.Body.Bytes())
	if page.Outcome != "page" || len(page.Events) != 2 {
		t.Fatal(page)
	}
	for _, e := range page.Events {
		if e.App != "ComfyUI" || e.What != "Edited the ComfyUI canvas" || e.Result != "Use Ctrl+Z in ComfyUI to undo" || e.Undo != nil {
			t.Fatal(e)
		}
	}
}
