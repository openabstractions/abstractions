package main

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	logging "github.com/openabstractions/abstraction-logging/go"
)

func TestRuntimeLogSinkUsesRetainedProvider(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "records.jsonl")
	sink, closer, err := runtimeLogSink(path)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	fileSink, ok := sink.(*logging.FileSink)
	if !ok {
		t.Fatalf("runtime selected %T", sink)
	}
	// The runtime default must select the retained constructor. Native policy
	// replacement is unavailable on the shared append-only provider.
	if err := fileSink.SetRetention(logging.LogsRetention{MaxUse: 100}); err != nil {
		t.Fatal(err)
	}
	record := logging.Record{Schema: 1, Time: logging.Timestamp{Time: time.Now().UTC()}, Msg: "small"}
	if err := sink.Write(record); err != nil {
		t.Fatal(err)
	}
	record.Msg = "a record that does not fit within the configured byte limit after encoding"
	if err := sink.Write(record); !errors.Is(err, logging.ErrRetentionRecord) {
		t.Fatalf("runtime retained sink ignored small bound: %v", err)
	}
}
