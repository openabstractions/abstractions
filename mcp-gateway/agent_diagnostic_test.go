package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestAgentDiagnosticC1DefaultOutputPreservesPanelContract(t *testing.T) {
	_, ctx, dir := diagnosticFixture(t)
	for _, client := range []string{"claude", "codex"} {
		t.Run(client, func(t *testing.T) {
			emit := func(include bool) []byte {
				t.Helper()
				var out, diagnostic bytes.Buffer
				if client == "claude" {
					args := []string{"connections", "claude", "--state-dir", dir}
					if include {
						args = append(args, "--helper-lifecycle")
					}
					if code := claudeConnectionsCommand(args, &out, &diagnostic); code != 0 {
						t.Fatal(code, diagnostic.String())
					}
				} else {
					options := codexSetupOptions{action: "connections", state: dir, helperLifecycle: include}
					page := codexSetupPage{Outcome: "page", Scope: "user", Registration: "absent", Name: "OpenAbstractions"}
					if err := writeCodexSetupPage(ctx, options, page, &out); err != nil {
						t.Fatal(err)
					}
				}
				return out.Bytes()
			}
			golden, err := os.ReadFile(filepath.Join("testdata", "helper-cause-"+client+"-default.json"))
			if err != nil {
				t.Fatal(err)
			}
			if raw := emit(false); !bytes.Equal(bytes.TrimSpace(raw), bytes.TrimSpace(golden)) {
				t.Fatalf("default output changed: %s", raw)
			}
			if raw := emit(true); !bytes.Contains(raw, []byte(`"helper_lifecycle":[{`)) {
				t.Fatalf("requested diagnostic absent: %s", raw)
			}
		})
	}
	for _, raw := range []string{"[]", `[{"pid":1,"cause":"private injected text"}]`} {
		if err := os.WriteFile(filepath.Join(dir, agentDiagnosticName), []byte(raw), 0600); err != nil {
			t.Fatal(err)
		}
		var out bytes.Buffer
		if err := writeCodexSetupPage(ctx, codexSetupOptions{action: "connections", state: dir, helperLifecycle: true}, codexSetupPage{Outcome: "page"}, &out); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(out.String(), "helper_lifecycle") {
			t.Fatal(out.String())
		}
		out.Reset()
		if code := claudeConnectionsCommand([]string{"connections", "claude", "--state-dir", dir, "--helper-lifecycle"}, &out, &bytes.Buffer{}); code != 0 || strings.Contains(out.String(), "helper_lifecycle") {
			t.Fatal(code, out.String())
		}
	}
}

func TestAgentDiagnosticBackendStartupNeverClaimsStdio(t *testing.T) {
	_, ctx, dir := diagnosticFixture(t)
	served, presence := 0, 0
	err := serveAgentConnection(ctx, func(context.Context) { presence++ }, nil, func(context.Context, gateway.Backend) error { served++; return nil })
	rows := readAgentDiagnostics(ctx, dir)
	if err == nil || served != 0 || presence != 0 || len(rows) != 1 || rows[0].Phase != "connection_end" || rows[0].Cause != "error" {
		t.Fatal(err, served, presence, rows)
	}
}

func diagnosticFixture(t *testing.T) (*agentDiagnostic, context.Context, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "state")
	if err := privatedir.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d := newAgentDiagnostic(testCaller(t), dir, program)
	if d == nil {
		t.Fatal("diagnostic unavailable")
	}
	t.Cleanup(d.close)
	return d, context.WithValue(context.Background(), agentDiagnosticKey{}, d), dir
}

func TestAgentDiagnosticFirstCauseAndAbruptUnknown(t *testing.T) {
	d, ctx, dir := diagnosticFixture(t)
	rows := readAgentDiagnostics(ctx, dir)
	if len(rows) != 1 || rows[0].Phase != "started" || rows[0].Cause != "unproved" {
		t.Fatal(rows)
	}
	d.observe("admission_end", "inbox_forbidden")
	d.observe("stdio_end", "completed")
	rows = readAgentDiagnostics(ctx, dir)
	if len(rows) != 1 || rows[0].Phase != "admission_end" || rows[0].Cause != "inbox_forbidden" {
		t.Fatal(rows)
	}
	if rows[0].PID != os.Getpid() || len(rows[0].ImageSHA256) != 64 {
		t.Fatal(rows)
	}
}

func TestClaudeBindingDiagnosticUsesClosedVocabularyWithoutAuthority(t *testing.T) {
	d, ctx, dir := diagnosticFixture(t)
	for _, next := range []claudeBindingDiagnostic{
		{Stage: "awaiting_start", Result: "none", Reason: "invalid_child_session"},
		{Stage: "start", Result: "pending", Reason: "none"},
		{Stage: "start", Result: "unavailable", Reason: "receiver_inbox_forbidden"},
		{Stage: "end", Result: "retired", Reason: "none"},
	} {
		observeClaudeBinding(ctx, next.Stage, next.Result, next.Reason)
		rows := readAgentDiagnostics(ctx, dir)
		if len(rows) != 1 || rows[0].ClaudeBinding == nil || *rows[0].ClaudeBinding != next || rows[0].Established || rows[0].WatchHealth != nil {
			t.Fatalf("diagnostic changed native/host proof: %+v", rows)
		}
	}
	before := *d.record.ClaudeBinding
	for _, bad := range []claudeBindingDiagnostic{
		{Stage: "private session", Result: "observed", Reason: "none"},
		{Stage: "start", Result: "observed", Reason: "private error"},
		{Stage: "start", Result: "unavailable", Reason: "receiver_inbox_private token"},
		{Stage: "end", Result: "observed", Reason: "none"},
	} {
		observeClaudeBinding(ctx, bad.Stage, bad.Result, bad.Reason)
		if *d.record.ClaudeBinding != before || validAgentDiagnosticRows([]agentDiagnosticRecord{{PID: d.record.PID, ImageSHA256: d.record.ImageSHA256, Phase: "started", Cause: "unproved", ClaudeBinding: &bad}}) {
			t.Fatal("open vocabulary retained", bad)
		}
	}
	d.observe("stdio_end", "eof")
	observeClaudeBinding(ctx, "start", "observed", "none")
	if *d.record.ClaudeBinding != before {
		t.Fatal("late lifecycle changed terminal snapshot")
	}
}

func TestAgentDiagnosticReceivingUsesExistingBoundedSnapshot(t *testing.T) {
	d, ctx, dir := diagnosticFixture(t)
	thread := "11111111-1111-4111-8111-111111111111"
	nativeContext := "native-11111111111111111111111111111111"
	observeCodexReceiving(ctx, "unavailable_missing_thread_metadata", "", nativeContext)
	rows := readAgentDiagnostics(ctx, dir)
	if len(rows) != 1 || rows[0].Receiving == nil || rows[0].Receiving.Reason != "missing_thread_metadata" || rows[0].Receiving.QueueAttempts != 0 {
		t.Fatal(rows)
	}
	for _, event := range []string{"selecting_cli", "watching", "queue_attempt", "queue_accepted", "watching"} {
		observeCodexReceiving(ctx, event, thread, nativeContext)
	}
	for _, event := range []string{"recovering_unknown", "recovering_unavailable"} {
		observeCodexReceiving(ctx, event, thread, nativeContext)
		if got := readAgentDiagnostics(ctx, dir)[0].Receiving; got.Stage != "recovering" || got.Reason != event {
			t.Fatal(got)
		}
	}
	observeCodexReceiving(ctx, "watching", thread, nativeContext)
	rows = readAgentDiagnostics(ctx, dir)
	r := rows[0].Receiving
	if r.Stage != "watching" || r.Thread != thread || r.Context != nativeContext || r.QueueAttempts != 1 || r.QueueResult != "accepted" {
		t.Fatal(r)
	}
	before := *r
	observeCodexReceiving(ctx, "secret arbitrary error output", thread, nativeContext)
	observeCodexReceiving(ctx, "watching", "private injected text", nativeContext)
	observeCodexReceiving(ctx, "watching", "22222222-2222-4222-8222-222222222222", nativeContext)
	if got := readAgentDiagnostics(ctx, dir)[0].Receiving; *got != before {
		t.Fatal("untrusted or foreign diagnostic changed snapshot", got)
	}
	observeCodexReceiving(ctx, "queue_attempt", thread, nativeContext)
	observeCodexReceiving(ctx, "cli_unavailable", "", nativeContext)
	observeCodexReceiving(ctx, "queue_unknown", thread, nativeContext)
	d.observe("presence_end", "inbox_forbidden")
	observeCodexReceiving(ctx, "unavailable_connection_closed", "", nativeContext)
	rows = readAgentDiagnostics(ctx, dir)
	r = rows[0].Receiving
	if rows[0].Phase != "presence_end" || rows[0].Cause != "inbox_forbidden" || r.Reason != "cli_unavailable" || r.QueueAttempts != 2 || r.QueueResult != "unknown" {
		t.Fatal(rows)
	}
	raw, err := os.ReadFile(filepath.Join(dir, agentDiagnosticName))
	if err != nil || len(raw) > 16<<10 || bytes.Contains(raw, []byte("secret")) {
		t.Fatal(err, string(raw))
	}
	for _, name := range []string{"receiving", "queue_attempts", "queue_result"} {
		if !bytes.Contains(raw, []byte(name)) {
			t.Fatal("missing bounded receiving field", name)
		}
	}
}

func TestAgentDiagnosticReceivingRejectsOpenVocabularyAndInvalidLabels(t *testing.T) {
	base := agentDiagnosticRecord{PID: 1, ImageSHA256: strings.Repeat("a", 64), Phase: "started", Cause: "unproved"}
	for _, bad := range []agentReceivingDiagnostic{
		{Stage: "secret", Reason: "none", QueueResult: "none"},
		{Stage: "unavailable", Reason: "raw vendor output", QueueResult: "none"},
		{Stage: "watching", Reason: "none", QueueResult: "none"},
		{Stage: "unavailable", Reason: "none", Thread: "arbitrary model text", QueueResult: "none"},
		{Stage: "unavailable", Reason: "none", QueueResult: "accepted"},
		{Stage: "unavailable", Reason: "none", QueueAttempts: 1000001, QueueResult: "unknown"},
	} {
		row := base
		row.Receiving = &bad
		if validAgentDiagnosticRows([]agentDiagnosticRecord{row}) {
			t.Fatal("invalid receiving metadata admitted", bad)
		}
	}
}

func TestAgentDiagnosticReceivingRetainsMissingMetadataStop(t *testing.T) {
	_, ctx, dir := diagnosticFixture(t)
	thread := "11111111-1111-4111-8111-111111111111"
	nativeContext := "native-11111111111111111111111111111111"
	for _, event := range []string{"selecting_cli", "watching", "unavailable_missing_thread_metadata", "observation_unavailable", "unavailable_connection_closed"} {
		observeCodexReceiving(ctx, event, thread, nativeContext)
	}
	rows := readAgentDiagnostics(ctx, dir)
	if len(rows) != 1 || rows[0].Receiving == nil || rows[0].Receiving.Stage != "stopped" || rows[0].Receiving.Reason != "missing_thread_metadata" {
		t.Fatal(rows)
	}
}

// A presence loss is counted and named on the row, and the row keeps its
// phase: the connection lives on, so the loss is not an end.
func TestAgentDiagnosticPresenceLossIsNotAnEnd(t *testing.T) {
	_, ctx, dir := diagnosticFixture(t)
	observeAgentLoss(ctx, errors.New("synthetic detail must not be persisted"), false)
	first := readAgentDiagnostics(ctx, dir)
	if len(first) != 1 || first[0].LastLossGate != "source" {
		t.Fatal("source loss gate unavailable", first)
	}
	observeAgentLoss(ctx, &agentInboxOutcomeError{outcome: msg.OutcomeHeld}, true)
	observeAgentReadmitted(ctx)
	rows := readAgentDiagnostics(ctx, dir)
	if len(rows) != 1 || rows[0].Phase != "started" || rows[0].Losses != 2 || rows[0].Readmitted != 1 || rows[0].LastLoss != "inbox_held" || rows[0].LastLossGate != "canonical" {
		t.Fatal(rows)
	}
	raw, err := os.ReadFile(filepath.Join(dir, agentDiagnosticName))
	if err != nil || strings.Contains(string(raw), "synthetic detail") {
		t.Fatal("loss detail persisted", err)
	}
	rows[0].LastLossGate = "untrusted arbitrary detail"
	if validAgentDiagnosticRows(rows) {
		t.Fatal("diagnostic loss gate accepted an open vocabulary")
	}
}

// A row whose process ended without recording an end is marked gone by the
// next helper's write, and gone probes are evicted before live connections.
func TestAgentDiagnosticMarksGoneHelpers(t *testing.T) {
	d, ctx, dir := diagnosticFixture(t)
	own := d.record
	gone := own
	gone.PID, gone.Established = 1, false
	gone.Birth = "2001-01-01T00:00:00Z"
	raw, _ := json.Marshal([]agentDiagnosticRecord{gone})
	if err := os.WriteFile(filepath.Join(dir, agentDiagnosticName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	d.persist()
	rows := readAgentDiagnostics(ctx, dir)
	wantGone := agentDiagnosticBirth() != ""
	if len(rows) != 2 || rows[0].Gone != wantGone || rows[1].Gone {
		t.Fatal(rows)
	}
	if victim := agentDiagnosticVictim([]agentDiagnosticRecord{{Phase: "started", Established: true}, {Phase: "started"}, {Phase: "started", Gone: true}}); victim != 2 {
		t.Fatal("gone probe not evicted first", victim)
	}
}

func TestAgentDiagnosticNormalStdioAndParent(t *testing.T) {
	for _, parent := range []bool{false, true} {
		t.Run(map[bool]string{false: "stdio", true: "parent"}[parent], func(t *testing.T) {
			d, base, dir := diagnosticFixture(t)
			ctx, cancel := context.WithCancel(base)
			d.parent = ctx
			defer cancel()
			err := serveAgentConnection(ctx, func(ctx context.Context) { <-ctx.Done() }, &agentMCPFixtureBackend{}, func(context.Context, gateway.Backend) error {
				if parent {
					cancel()
				}
				return nil
			})
			if !parent && err != nil {
				t.Fatal(err)
			}
			rows := readAgentDiagnostics(base, dir)
			want := "stdio_end"
			if parent {
				want = "parent_end"
			}
			if len(rows) != 1 || rows[0].Phase != want {
				t.Fatal(rows)
			}
		})
	}
}

func TestAgentDiagnosticBoundedAndUntrustedRead(t *testing.T) {
	d, ctx, dir := diagnosticFixture(t)
	for i := 1; i <= 25; i++ {
		d.record.PID = i
		d.persist()
	}
	rows := readAgentDiagnostics(ctx, dir)
	if len(rows) != 16 {
		t.Fatal(len(rows))
	}
	rows[0].Cause = "sensitive injected text"
	raw, _ := json.Marshal(rows)
	if err := os.WriteFile(filepath.Join(dir, agentDiagnosticName), raw, 0600); err != nil {
		t.Fatal(err)
	}
	// The injected row is left out; the rows this build understands remain.
	got := readAgentDiagnostics(ctx, dir)
	if len(got) != 15 {
		t.Fatal(len(got))
	}
	for _, row := range got {
		if row.Cause == "sensitive injected text" {
			t.Fatal("injected cause read back")
		}
	}
	if got := newAgentDiagnostic(context.Background(), filepath.Join(dir, "missing"), "missing"); got != nil {
		got.close()
		t.Fatal("missing state accepted")
	}
	var absent *agentDiagnostic
	absent.observe("stdio_end", "completed")
	absent.close()
}

// A helper its host starts has no caller deadline and keeps the ordinary write
// budget. A caller with a deadline, such as a test under host load, lets each
// write use what remains of it, and never less than the ordinary budget.
func TestAgentDiagnosticWriteBudgetDerivesFromCreatingCaller(t *testing.T) {
	if got := agentDiagnosticBudget(time.Time{}, agentDiagnosticAttempt); got != agentDiagnosticAttempt {
		t.Fatal("no caller deadline", got)
	}
	if got := agentDiagnosticBudget(time.Now().Add(-time.Second), agentDiagnosticFinalBudget); got != agentDiagnosticFinalBudget {
		t.Fatal("expired caller deadline", got)
	}
	if got := agentDiagnosticBudget(time.Now().Add(time.Minute), agentDiagnosticAttempt); got < 50*time.Second || got > time.Minute {
		t.Fatal("caller deadline", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(t.TempDir(), "state")
	if err := privatedir.Ensure(dir); err != nil {
		t.Fatal(err)
	}
	d := newAgentDiagnostic(ctx, dir, program)
	if d == nil {
		t.Fatal("diagnostic unavailable")
	}
	defer d.close()
	if want, _ := ctx.Deadline(); !d.deadline.Equal(want) {
		t.Fatal("record lost its creating caller's deadline", d.deadline, want)
	}
	if host := newAgentDiagnostic(context.Background(), filepath.Join(t.TempDir(), "missing"), program); host != nil {
		host.close()
		t.Fatal("missing state accepted")
	}
}
