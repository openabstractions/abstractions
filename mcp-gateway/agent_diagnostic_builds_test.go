package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// During an upgrade an older helper keeps running beside a newer one. A row
// the running build cannot validate stays exactly as its build wrote it, and
// this build still records its own end.
func TestAgentDiagnosticKeepsRowsFromOtherBuilds(t *testing.T) {
	d, _, dir := diagnosticFixture(t)
	path := filepath.Join(dir, agentDiagnosticName)
	foreign := []byte(`{"pid":7,"birth":"2026-10-31T00:46:11.9388364Z","image_sha256":"` + string(bytes.Repeat([]byte("b"), 64)) + `","phase":"presence_end","cause":"inbox_future_word","later_field":1}`)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var rows []json.RawMessage
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 1 {
		t.Fatal(rows, err)
	}
	rows = append([]json.RawMessage{foreign}, rows...)
	raw, _ = json.Marshal(rows)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	d.observe("stdio_end", "eof")
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	rows = nil
	if err := json.Unmarshal(raw, &rows); err != nil || len(rows) != 2 || !bytes.Equal(rows[0], foreign) {
		t.Fatal("foreign row changed", string(raw), err)
	}
	var own agentDiagnosticRecord
	if err := json.Unmarshal(rows[1], &own); err != nil || own.PID != d.record.PID || own.Phase != "stdio_end" || own.Cause != "eof" {
		t.Fatal("own end not recorded", string(rows[1]), err)
	}
}

// The establishment mark never runs inside the presence loop: the call
// returns while another write holds the record, and the mark lands later.
func TestAgentDiagnosticEstablishDoesNotStallRenewal(t *testing.T) {
	d, ctx, dir := diagnosticFixture(t)
	d.mu.Lock()
	returned := make(chan struct{})
	go func() {
		observeAgentEstablished(ctx)
		observeAgentEstablished(ctx)
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		d.mu.Unlock()
		t.Fatal("establishment blocked the caller")
	}
	d.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if rows := readAgentDiagnostics(context.Background(), dir); len(rows) == 1 && rows[0].Established {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("establishment mark not written")
}
