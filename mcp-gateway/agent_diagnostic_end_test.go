package main

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	cas "github.com/openabstractions/abstraction-cas/go"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
)

// On 2026-10-30 two Claude helpers ended themselves within 20 ms while a third
// started; neither end reached the snapshot. A writer holding the snapshot
// longer than one ordinary attempt must not erase this helper's end.
func TestAgentDiagnosticEndSurvivesContendedSnapshot(t *testing.T) {
	d, ctx, dir := diagnosticFixture(t)
	held := make(chan struct{})
	released := make(chan error, 1)
	go func() {
		released <- cas.ChangeLimitContext(context.Background(), filepath.Join(dir, agentDiagnosticName), 16<<10, func(raw []byte) ([]byte, error) {
			close(held)
			time.Sleep(3 * agentDiagnosticAttempt)
			return raw, nil
		})
	}()
	<-held
	observeAgentDiagnostic(ctx, "presence_end", &agentInboxOutcomeError{outcome: messages.OutcomeHeld})
	if err := <-released; err != nil {
		t.Fatal(err)
	}
	rows := readAgentDiagnostics(ctx, dir)
	if len(rows) != 1 || rows[0].Phase != "presence_end" || rows[0].Cause != "inbox_held" {
		t.Fatal(rows)
	}
	if !d.terminal {
		t.Fatal("end not retained in memory")
	}
}

// Every generated Messages outcome names an end. An unnamed outcome keeps the
// phase with the generic cause instead of losing the whole record.
func TestAgentDiagnosticRecordsEveryInboxOutcome(t *testing.T) {
	named := 0
	for value := messages.Outcome(0); value < 64; value++ {
		word := value.String()
		if _, ok := messages.ParseOutcome(word); !ok {
			continue
		}
		named++
		_, ctx, dir := diagnosticFixture(t)
		observeAgentDiagnostic(ctx, "presence_end", &agentInboxOutcomeError{outcome: value})
		if rows := readAgentDiagnostics(ctx, dir); len(rows) != 1 || rows[0].Phase != "presence_end" || rows[0].Cause != "inbox_"+word {
			t.Fatal(word, rows)
		}
	}
	if named < 11 {
		t.Fatal("generated outcomes not enumerated", named)
	}
	_, ctx, dir := diagnosticFixture(t)
	observeAgentDiagnostic(ctx, "presence_end", &agentInboxOutcomeError{outcome: messages.Outcome(200)})
	if rows := readAgentDiagnostics(ctx, dir); len(rows) != 1 || rows[0].Phase != "presence_end" || rows[0].Cause != "error" {
		t.Fatal(rows)
	}
	d, ctx, dir := diagnosticFixture(t)
	d.observe("connection_end", "inbox_injected words")
	if rows := readAgentDiagnostics(ctx, dir); len(rows) != 1 || rows[0].Phase != "connection_end" || rows[0].Cause != "error" {
		t.Fatal(rows)
	}
}

// On 2026-10-31 Codex catalogue probes filled all sixteen rows within three
// minutes. A working connection and recorded ends outlast a burst of probes.
func TestAgentDiagnosticProbesDoNotEvictWorkingConnections(t *testing.T) {
	if agentDiagnosticBirth() == "" {
		t.Skip("no native process birth here: probes are never marked gone")
	}
	d, ctx, dir := diagnosticFixture(t)
	ended := &agentDiagnostic{root: d.root, record: d.record}
	ended.record.PID, ended.record.Phase, ended.record.Cause = 7, "stdio_end", "eof"
	if !ended.persist() {
		t.Fatal("ended row")
	}
	d.establish()
	probe := &agentDiagnostic{root: d.root, record: d.record}
	probe.record.Established = false
	for pid := 100; pid < 140; pid++ {
		probe.record.PID = pid
		if !probe.persist() {
			t.Fatal("probe row", pid)
		}
	}
	rows := readAgentDiagnostics(ctx, dir)
	if len(rows) != 16 {
		t.Fatal(len(rows))
	}
	var working, recorded bool
	for _, row := range rows {
		working = working || row.PID == d.record.PID && row.Established && row.Phase == "started"
		recorded = recorded || row.PID == 7 && row.Phase == "stdio_end"
	}
	if !working || !recorded || rows[len(rows)-1].PID != 139 {
		t.Fatal(rows)
	}
	d.observe("stdio_end", "eof")
	d.establish()
	for _, row := range readAgentDiagnostics(ctx, dir) {
		if row.PID == d.record.PID && (!row.Established || row.Phase != "stdio_end") {
			t.Fatal(row)
		}
	}
}
