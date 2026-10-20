package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/appmodel"
	logging "github.com/openabstractions/abstraction-logging/go"
)

func TestApplicationV3GatewayAuditLongRecordReadback(t *testing.T) {
	sink, closeSink, err := runtimeLogSink(filepath.Join(t.TempDir(), "records.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSink.Close()
	raw := sink.(*logging.FileSink)
	if raw.MaxLine == 65536 {
		t.Fatal("audit changed the ordinary sink cap")
	}
	write, check, err := applicationV3AuditWriter(raw, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	program := `C:\` + strings.Repeat(`segment\`, 600) + "application.exe"
	record := appmodel.CallRecord{Subject: program, Application: "fixture", Instance: "instance", Command: "note.read", Outcome: "applied", ArgumentsDigest: "sha256:" + strings.Repeat("a", 64), ResultDigest: "sha256:" + strings.Repeat("b", 64)}
	if err = check(record, program); err != nil {
		t.Fatal(err)
	}
	if err = write(context.Background(), record, program); err != nil {
		t.Fatal(err)
	}
	history, err := raw.ScanApplicationHistory("")
	if err != nil || len(history.Rows) != 1 {
		t.Fatalf("history %+v %v", history, err)
	}
	row := history.Rows[0]
	got, err := appmodel.DecodeCallRecord([]byte(row.Record.Attrs["call_record"]))
	if err != nil || !reflect.DeepEqual(got, &record) || row.Record.Attrs["regarding"] != program || row.Record.Attrs["logging.truncated"] != "" || row.Emitter == nil {
		t.Fatalf("audit changed: %+v %v", row, err)
	}
	// Legacy native string members are not all bounded. Preserve that concrete
	// acceptance limit rather than silently shrinking a successful audit write.
	record.IdempotencyKey = strings.Repeat("x", applicationV3AuditPhysicalCap)
	if err = check(record, program); !errors.Is(err, logging.ErrLineCap) {
		t.Fatalf("oversize preflight: %v", err)
	}
	if err = write(context.Background(), record, program); !errors.Is(err, logging.ErrLineCap) {
		t.Fatalf("oversize write: %v", err)
	}
	history, err = raw.ScanApplicationHistory("")
	if err != nil || len(history.Rows) != 1 {
		t.Fatal("oversize audit appended a partial record")
	}
}

func applicationV3ReadPhysicalAudit(t *testing.T, path string) (appmodel.CallRecord, logging.Emitter) {
	t.Helper()
	rows := applicationV3ReadPhysicalAudits(t, path)
	if len(rows) != 1 {
		t.Fatalf("physical audit count: %d", len(rows))
	}
	return rows[0].record, rows[0].emitter
}

type applicationV3PhysicalAudit struct {
	record  appmodel.CallRecord
	emitter logging.Emitter
}

func applicationV3ReadPhysicalAudits(t *testing.T, path string) []applicationV3PhysicalAudit {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	var rows []applicationV3PhysicalAudit
	for {
		var fields struct {
			Attrs   map[string]string `json:"attrs"`
			Emitter logging.Emitter   `json:"_oa_emitter"`
		}
		if err = decoder.Decode(&fields); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		record, err := appmodel.DecodeCallRecord([]byte(fields.Attrs["call_record"]))
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, applicationV3PhysicalAudit{*record, fields.Emitter})
	}
	return rows
}

// The proven caller a command was decided for is the audit row's private
// subject, so ReadAboutCaller returns the row to that caller (LOG-P9).
func TestApplicationV3GatewayAuditStampsTheCaller(t *testing.T) {
	sink, closeSink, err := runtimeLogSink(filepath.Join(t.TempDir(), "records.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer closeSink.Close()
	raw := sink.(*logging.FileSink)
	write, _, err := applicationV3AuditWriter(raw, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	caller := filepath.Join(t.TempDir(), "agent.exe")
	application := filepath.Join(t.TempDir(), "application.exe")
	record := appmodel.CallRecord{Subject: caller, Application: "fixture", Instance: "instance", Command: "note.read", Outcome: "applied", ArgumentsDigest: "sha256:" + strings.Repeat("a", 64), ResultDigest: "sha256:" + strings.Repeat("b", 64)}
	if err = write(context.Background(), record, application); err != nil {
		t.Fatal(err)
	}
	history, err := raw.ScanApplicationHistory("")
	if err != nil || len(history.Rows) != 1 || history.Rows[0].Emitter == nil {
		t.Fatalf("history %+v %v", history, err)
	}
	row := history.Rows[0]
	if !logging.SameCaller(row.Emitter.Subject, caller) || logging.SameCaller(row.Emitter.Subject, application) || row.Record.Attrs["regarding"] != application {
		t.Fatalf("audit subject %q regarding %q", row.Emitter.Subject, row.Record.Attrs["regarding"])
	}
	for k := range row.Record.Attrs {
		if strings.HasPrefix(k, "_oa") {
			t.Fatalf("private stamp in public record: %v", row.Record.Attrs)
		}
	}
}
