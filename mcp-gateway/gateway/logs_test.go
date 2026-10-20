package gateway

import (
	"context"
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	"github.com/openabstractions/abstraction-logging/go/logview"
	"strings"
	"testing"
	"time"
)

type logsFixture struct {
	t               *testing.T
	reads, observes int
	deny            bool
	target          logclient.ApplicationLogTarget
	cursor          string
}

func (f *logsFixture) ReadContext(_ context.Context, target logclient.ApplicationLogTarget, cursor string, count, size int64) (logclient.ApplicationLogPage, error) {
	f.reads++
	f.target = target
	if cursor != "end" || count != 64 || size != 65536 {
		f.t.Fatal(cursor, count, size)
	}
	if f.deny {
		return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomeNotPermitted, Next: cursor}, nil
	}
	return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomePage, Next: "sealed-end", AtEnd: true}, nil
}
func (f *logsFixture) ObserveContext(_ context.Context, target logclient.ApplicationLogTarget, cursor string, count, size, wait int64) (logclient.ApplicationLogPage, error) {
	f.observes++
	f.cursor = cursor
	if target.Program != f.target.Program && f.reads > 0 || count != 64 || size != 65536 || wait != 25000 {
		f.t.Fatal(target, cursor, count, size, wait)
	}
	return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomeGap, Next: cursor}, nil
}
func TestLogsFollowEndExactTargetAndExplicitGap(t *testing.T) {
	f := &logsFixture{t: t}
	in := LogsFollowInput{App: "registered-app"}
	program := `C:\fixture\app.exe`
	out, err := followLogPage(t.Context(), f, f, program, in, logview.RecordFilter{})
	if err != nil || out.Outcome != "gap" || out.Cursor != "sealed-end" || !strings.Contains(out.Message, "History gap") || f.target.Selection != logclient.ApplicationLogSelectionOneApp || f.target.Program != program || f.reads != 1 || f.observes != 1 {
		t.Fatal(out, err, f)
	}
	in.Cursor = out.Cursor
	_, err = followLogPage(t.Context(), f, f, program, in, logview.RecordFilter{})
	if err != nil || f.reads != 1 || f.observes != 2 {
		t.Fatal(err, f)
	}
}
func TestLogsFollowDenialIsQuietAndKeepsCursor(t *testing.T) {
	f := &logsFixture{t: t, deny: true}
	out, err := followLogPage(t.Context(), f, f, `C:\fixture\app.exe`, LogsFollowInput{App: "registered-app"}, logview.RecordFilter{})
	if err != nil || out.Outcome != "not_permitted" || out.Cursor != "end" || len(out.Records) != 0 || f.observes != 0 || !strings.Contains(out.Message, "Permission refused") {
		t.Fatal(out, err, f)
	}
}

type logsBackendFixture struct {
	fixtureBackend
	input LogsFollowInput
}

func (f *logsBackendFixture) LogsFollow(_ context.Context, in LogsFollowInput) (LogsFollowOutput, error) {
	f.input = in
	return logsFollowProjection(logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomePage, Next: "kept", AtEnd: true, Records: []wire.Record{{Schema: 1, Time: "2026-11-03T12:00:00Z", Level: 4, Msg: "fixture", Identity: []wire.Attestation{}, Attrs: map[string]string{"stage": "ready"}}}}, logview.RecordFilter{}), nil
}
func TestLogsFollowNativeCatalogueAndSchema(t *testing.T) {
	b := new(logsBackendFixture)
	_, session := connected(t, b, "logs-fixture", "2025-11-25")
	tools, err := session.ListTools(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, tool := range tools.Tools {
		if tool.Name == ToolLogsFollow {
			found = true
			raw, _ := json.Marshal(tool.InputSchema)
			for _, name := range []string{"app", "about", "contract", "cursor", "level", "tag", "since", "until"} {
				if !strings.Contains(string(raw), `"`+name+`"`) {
					t.Fatal(name, string(raw))
				}
			}
			if !strings.Contains(tool.Description, "untrusted") {
				t.Fatal(tool.Description)
			}
		}
	}
	if !found {
		t.Fatal("tool absent")
	}
	out, err := session.CallTool(t.Context(), &mcp.CallToolParams{Name: ToolLogsFollow, Arguments: map[string]any{"app": "registered-app", "cursor": "kept", "tag": "stage=ready"}})
	if err != nil || out.IsError || b.input.Cursor != "kept" || b.input.Tag != "stage=ready" {
		t.Fatal(out, err, b.input)
	}
	raw, err := json.Marshal(out.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var body struct {
		Records []json.RawMessage `json:"records"`
	}
	if err := json.Unmarshal(raw, &body); err != nil || len(body.Records) != 1 {
		t.Fatal(err, string(raw))
	}
	decoded, err := wire.DecodeRecord(body.Records[0])
	if err != nil || decoded.Msg != "fixture" || decoded.Attrs["stage"] != "ready" {
		t.Fatal(err, string(raw))
	}

}
func TestLogsFollowFiltersAndInvalidTime(t *testing.T) {
	now := time.Now().UTC()
	in := LogsFollowInput{App: "registered-app", Level: "warn", Tag: "stage=ready", Since: now.Add(-time.Second).Format(time.RFC3339Nano)}
	f, err := parseLogsFollow(in)
	if err != nil {
		t.Fatal(err)
	}
	page := logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomePage, Next: "next", Records: []wire.Record{{Schema: 1, Time: now.Format(time.RFC3339Nano), Level: 4, Attrs: map[string]string{"stage": "ready"}}, {Schema: 1, Time: now.Format(time.RFC3339Nano), Level: 4, Attrs: map[string]string{"stage": "ready-later"}}}}
	out := logsFollowProjection(page, f)
	if len(out.Records) != 1 || out.Hidden != 1 || out.Cursor != "next" {
		t.Fatal(out)
	}
	in.Since = "bad"
	if _, err := parseLogsFollow(in); err == nil {
		t.Fatal("invalid timestamp accepted")
	}
}

func TestLogsCallerScopeIsExclusive(t *testing.T) {
	for _, in := range []LogsFollowInput{{About: "my_requests", Contract: "inference"}, {App: "registered-app", Contract: "inference"}} {
		if _, err := parseLogsFollow(in); err != nil {
			t.Fatal(in, err)
		}
	}
	for _, in := range []LogsFollowInput{{}, {App: "other", About: "my_requests"}, {About: "other_requests"}} {
		if _, err := parseLogsFollow(in); err == nil {
			t.Fatal(in)
		}
	}
}

type callerFollowFixture struct {
	t            *testing.T
	reads, waits int
	since        time.Time
}

func (f *callerFollowFixture) ReadAboutCallerContext(_ context.Context, since time.Time, cursor string, count, size int64) (logclient.ApplicationLogPage, error) {
	f.reads++
	f.since = since
	if cursor != "end" || count != 64 || size != 65536 {
		f.t.Fatal(cursor, count, size)
	}
	return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomePage, Next: "own-end", AtEnd: true}, nil
}
func (f *callerFollowFixture) ObserveAboutCallerContext(_ context.Context, since time.Time, cursor string, count, size, wait int64) (logclient.ApplicationLogPage, error) {
	f.waits++
	if !since.Equal(f.since) || cursor != "own-end" || wait != 25000 {
		f.t.Fatal(since, cursor, wait)
	}
	return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomeGap, Next: cursor}, nil
}
func TestLogsFollowMyRequestsUsesOnlyBoundCallerReaderAndShowsGap(t *testing.T) {
	f := &callerFollowFixture{t: t}
	pages := callerLogPages{reader: f, since: time.Time{}}
	out, err := followLogPage(t.Context(), pages, pages, "", LogsFollowInput{About: "my_requests"}, logview.RecordFilter{})
	if err != nil || out.Outcome != "gap" || out.Cursor != "own-end" || !strings.Contains(out.Message, "History gap") || f.reads != 1 || f.waits != 1 {
		t.Fatal(out, err, f)
	}
}

func TestLogsFollowContractNarrowsAuthorizedRecordsAndKeepsGap(t *testing.T) {
	filter, err := parseLogsFollow(LogsFollowInput{About: "my_requests", Contract: "inference"})
	if err != nil {
		t.Fatal(err)
	}
	out := logsFollowProjection(logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomePage, Next: "continued", AtEnd: true, Records: []wire.Record{{Schema: 1, Time: "2026-11-03T12:00:00Z", Msg: "abstraction.inference/chat@3", Attrs: map[string]string{"oa.load_ms": "12"}}, {Schema: 1, Time: "2026-11-03T12:00:00Z", Msg: "abstraction.facade/appmodel@1"}}}, filter)
	if len(out.Records) != 1 || out.Hidden != 1 || out.Cursor != "continued" {
		t.Fatal(out)
	}
	gap := logsFollowProjection(logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomeGap, Next: "continued"}, filter)
	if gap.Outcome != "gap" || !strings.Contains(gap.Message, "History gap") {
		t.Fatal(gap)
	}
}
