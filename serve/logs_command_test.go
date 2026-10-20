package main

import (
	"bytes"
	"context"
	"errors"
	wire "github.com/openabstractions/abstraction-logging/go/abstraction/logging"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	"github.com/openabstractions/abstraction-logging/go/logview"
	"strings"
	"testing"
	"time"
)

type logsFixture struct {
	t      *testing.T
	calls  int
	record *wire.Record
	target logclient.ApplicationLogTarget
}

func (f *logsFixture) ReadContext(_ context.Context, target logclient.ApplicationLogTarget, cursor string, count, size int64) (logclient.ApplicationLogPage, error) {
	f.calls++
	f.target = target
	if f.record != nil {
		return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomePage, Next: "finite-end", AtEnd: true, Records: []wire.Record{*f.record, *f.record}}, nil
	}
	if cursor != "end" || count != 64 || size != 65536 {
		f.t.Fatal(cursor, count, size)
	}
	return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomePage, Next: "bound-end", AtEnd: true}, nil
}
func (f *logsFixture) ObserveContext(_ context.Context, target logclient.ApplicationLogTarget, cursor string, count, size, wait int64) (logclient.ApplicationLogPage, error) {
	f.calls++
	if target != f.target || cursor != "bound-end" || count != 64 || size != 65536 || wait != 25000 {
		f.t.Fatal(target, cursor, count, size, wait)
	}
	return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomeGap, Next: cursor}, nil
}
func TestLogsFollowExactTargetEndAndGap(t *testing.T) {
	f := &logsFixture{t: t}
	var output bytes.Buffer
	target := logclient.ApplicationLogTarget{Selection: logclient.ApplicationLogSelectionOneApp, Program: `C:\fixture\app.exe`}
	err := readLogs(t.Context(), f, f, target, logview.RecordFilter{}, true, &output, false)
	if err == nil || f.calls != 2 || !strings.Contains(output.String(), "History gap:") {
		t.Fatal(err, f.calls, output.String())
	}
}
func TestLogsHelpAndCancellation(t *testing.T) {
	var output bytes.Buffer
	if err := logsCommand([]string{"--help"}, &output, &output); err != nil || !strings.Contains(output.String(), "--follow") || !strings.Contains(output.String(), "registered application name") || strings.Contains(output.String(), "exact native program path") {
		t.Fatal(err, output.String())
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := readLogs(ctx, nil, nil, logclient.ApplicationLogTarget{}, logview.RecordFilter{}, false, &output, false); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLogsCLIJSONUsesCanonicalRecordCodec(t *testing.T) {
	record := wire.Record{Schema: 1, Time: "2026-11-03T12:00:00Z", Level: 4, Msg: "fixture", Identity: []wire.Attestation{}, Attrs: map[string]string{"stage": "ready"}}
	fixture := &logsFixture{t: t, record: &record}
	var output bytes.Buffer
	filter, err := logview.Parse("", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := readLogs(t.Context(), fixture, nil, logclient.ApplicationLogTarget{}, filter, false, &output, true); err != nil {
		t.Fatal(err)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	if len(lines) != 2 {
		t.Fatal("expected two JSON lines", output.String())
	}
	for _, line := range lines {
		decoded, err := wire.DecodeRecord(line)
		if err != nil || decoded.Msg != record.Msg || decoded.Attrs["stage"] != "ready" {
			t.Fatal(err, string(line))
		}
	}
}

func TestLogsFollowCallerAndApplicationForms(t *testing.T) {
	for _, v := range []struct {
		pos      []string
		app      string
		requests bool
		want     string
	}{{[]string{"follow"}, "", true, ""}, {[]string{"follow", "Notes"}, "", false, "Notes"}} {
		app, follow, err := logsSelection(v.pos, v.app, false, v.requests)
		if err != nil || !follow || app != v.want {
			t.Fatal(app, follow, err)
		}
	}
	for _, v := range []struct {
		pos      []string
		app      string
		requests bool
	}{{[]string{"follow", "Notes"}, "", true}, {[]string{"follow"}, "", false}, {[]string{"follow", "one", "two"}, "", false}} {
		if _, _, err := logsSelection(v.pos, v.app, false, v.requests); err == nil {
			t.Fatal(v)
		}
	}
}

type callerLogsFixture struct {
	t            *testing.T
	reads, waits int
}

func (f *callerLogsFixture) ReadAboutCallerContext(_ context.Context, _ time.Time, cursor string, count, size int64) (logclient.ApplicationLogPage, error) {
	f.reads++
	if cursor != "end" || count != 64 || size != 65536 {
		f.t.Fatal(cursor, count, size)
	}
	return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomePage, Next: "own-end", AtEnd: true}, nil
}
func (f *callerLogsFixture) ObserveAboutCallerContext(_ context.Context, _ time.Time, cursor string, count, size, wait int64) (logclient.ApplicationLogPage, error) {
	f.waits++
	if cursor != "own-end" || wait != 25000 {
		f.t.Fatal(cursor, wait)
	}
	return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomeGap, Next: cursor}, nil
}
func TestLogsCLIRequestsReuseBoundCallerReaderAndExplicitGap(t *testing.T) {
	f := &callerLogsFixture{t: t}
	pages := callerLogsHistory{reader: f}
	var out bytes.Buffer
	err := readLogs(t.Context(), pages, pages, logclient.ApplicationLogTarget{}, logview.RecordFilter{}, true, &out, false)
	if err == nil || f.reads != 1 || f.waits != 1 || !strings.Contains(out.String(), "History gap") {
		t.Fatal(err, f, out.String())
	}
}

type logsContractFixture struct{}

func (logsContractFixture) ReadContext(context.Context, logclient.ApplicationLogTarget, string, int64, int64) (logclient.ApplicationLogPage, error) {
	return logclient.ApplicationLogPage{Outcome: wire.HistoryPageOutcomePage, Next: "end", AtEnd: true, Records: []wire.Record{{Schema: 1, Msg: "abstraction.inference/chat@3", Attrs: map[string]string{"oa.load_ms": "12"}}, {Schema: 1, Msg: "abstraction.facade/appmodel@1"}}}, nil
}
func TestLogsCLIContractNarrowsAuthorizedPage(t *testing.T) {
	filter, err := logview.ParseContract("inference", "", "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err = readLogs(t.Context(), logsContractFixture{}, nil, logclient.ApplicationLogTarget{}, filter, false, &out, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "abstraction.inference/chat@3") || strings.Contains(out.String(), "abstraction.facade") {
		t.Fatal(out.String())
	}
}
