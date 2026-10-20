package main

import (
	identity "github.com/openabstractions/abstraction-identity"
	logging "github.com/openabstractions/abstraction-logging/go"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRuntimeSelfLogWriterKeepsRawRetentionProvider(t *testing.T) {
	raw, err := logging.OpenRetainedFileSink(filepath.Join(t.TempDir(), "rows"), logging.LogsRetention{MaxUse: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	writer, err := runtimeAttributedLogWriter(raw)
	if err != nil {
		t.Fatal(err)
	}
	if writer == raw {
		t.Fatal("trusted writer did not stamp self events")
	}
	if err = writer.Write(logging.Record{Schema: 1, Time: logging.Timestamp{Time: time.Now().UTC()}, Msg: "trusted runtime event"}); err != nil {
		t.Fatal(err)
	}
	scan, err := raw.ScanApplicationHistory("")
	if err != nil || len(scan.Rows) != 1 || scan.Rows[0].Emitter == nil {
		t.Fatalf("self attribution: %+v %v", scan, err)
	}
	image, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if !identity.SameSubjectProgram(scan.Rows[0].Emitter.Program, image) {
		t.Fatalf("wrong self program: %+v", scan.Rows[0].Emitter)
	}
	// The receiving host continues to own raw, with history and collection APIs.
	if err = raw.Collect(); err != nil {
		t.Fatal(err)
	}
}
