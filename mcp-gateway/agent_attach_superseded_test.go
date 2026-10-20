package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// A fork that a later fork replaced stays in the vendor's history. The custody
// record keeps its ID, so discovery never offers an old fork for attach.
func TestAgentAttachNeverOffersSupersededFork(t *testing.T) {
	ctx := context.Background()
	state, configuration := t.TempDir(), t.TempDir()
	binding := startprofile.CustodyBinding{Account: "fixture-account", ProgramKey: "fixture-key", EnrollmentRevision: "fixture-enrollment", RecipeDigest: strings.Repeat("a", 64)}
	inbox := "chat-" + strings.Repeat("a", 48)
	fork := func(updated int64) string {
		t.Helper()
		lease, err := beginAgentFork(ctx, state, binding, inbox, "configuration|fork:"+attachPersonSession+"@"+time.UnixMilli(updated).Format("150405"))
		if err != nil {
			t.Fatal(err)
		}
		if err := lease.observe([]byte(`{"type":"result","subtype":"success","is_error":false,"session_id":"` + lease.record.Session + `"}`)); err != nil {
			t.Fatal(err)
		}
		if err := lease.commit(ctx, func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
		return lease.record.Session
	}
	now := time.Now().Truncate(time.Second)
	first := fork(now.Add(-2 * time.Hour).UnixMilli())
	// The person's session changed afterwards, so the next question forks again.
	second := fork(now.Add(-time.Hour).UnixMilli())
	third := fork(now.UnixMilli())
	if first == second || second == third {
		t.Fatal("source change did not fork again", first, second, third)
	}
	writeAttachSession(t, configuration, "C--work-person", attachPersonSession, now.Add(-3*time.Hour))
	for i, id := range []string{first, second, third} {
		writeAttachSession(t, configuration, "C--work-person", id, now.Add(time.Duration(i)*time.Minute))
	}
	held := agentHeldSessions(state)
	for _, id := range []string{first, second, third} {
		if !held[id] {
			t.Fatal("fork not held", id, held)
		}
	}
	sessions, err := claudeAttachSessions(configuration, held)
	if err != nil || len(sessions) != 1 || sessions[0].Session != attachPersonSession {
		t.Fatal("discovery offered a fork OA made", sessions, err)
	}
	// The record carries the replaced forks in order and still decodes.
	records, err := filepath.Glob(filepath.Join(state, "claude-session-*.json"))
	if err != nil || len(records) != 1 {
		t.Fatal(records, err)
	}
	raw, err := os.ReadFile(records[0])
	if err != nil {
		t.Fatal(err)
	}
	record, err := decodeAgentSession(raw)
	if err != nil || record.Session != third || len(record.Superseded) != 2 || record.Superseded[0] != first || record.Superseded[1] != second {
		t.Fatal(record, err)
	}
	// A record without the field, as written before, still decodes.
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	delete(fields, "superseded")
	older, _ := json.Marshal(fields)
	if _, err := decodeAgentSession(older); err != nil {
		t.Fatal("record without superseded forks", err)
	}
	fields["superseded"] = []string{"not-a-session"}
	bad, _ := json.Marshal(fields)
	if _, err := decodeAgentSession(bad); agentSessionReason(err) != "session_custody_unavailable" {
		t.Fatal("malformed superseded fork accepted", err)
	}
}
