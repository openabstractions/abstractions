package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

// Threads named by UUIDv7, as Codex names them: the first 48 bits are the
// creation time in Unix milliseconds.
const (
	codexMainThreadV7   = "01a1fdec-0000-7000-8000-000000000001"
	codexWorkerThreadV7 = "01a1fdec-0000-7000-8000-000000000002"
)

// writeCodexSessionRecord writes a thread's session record the way Codex
// 0.171 does: one session_meta line in the day folder of its creation time.
func writeCodexSessionRecord(t *testing.T, home, thread string, payload map[string]any) {
	t.Helper()
	day := time.UnixMilli(0x01a1fdec0000).Local()
	dir := filepath.Join(home, "sessions", day.Format("2006"), day.Format("01"), day.Format("02"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	payload["id"] = thread
	line, err := json.Marshal(map[string]any{"type": "session_meta", "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	name := "rollout-" + day.Format("2006-01-02T15-04-05") + "-" + thread + ".jsonl"
	if err := os.WriteFile(filepath.Join(dir, name), append(line, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codexWorkerPayload(parent string) map[string]any {
	return map[string]any{"source": map[string]any{"subagent": map[string]any{"thread_spawn": map[string]any{"parent_thread_id": parent, "depth": 1}}}, "thread_source": "subagent", "parent_thread_id": parent, "session_id": parent}
}

// The session record decides: a plain source with no parent is top-level;
// a sub-agent source, a parent, a missing or foreign record is not.
func TestCodexSessionRecordNamesTopLevelThreads(t *testing.T) {
	home := t.TempDir()
	writeCodexSessionRecord(t, home, codexMainThreadV7, map[string]any{"source": "vscode", "session_id": codexMainThreadV7})
	writeCodexSessionRecord(t, home, codexWorkerThreadV7, codexWorkerPayload(codexMainThreadV7))
	if !codexSessionRecordTopLevel(home, codexMainThreadV7) {
		t.Fatal("a top-level thread's record did not prove it")
	}
	if codexSessionRecordTopLevel(home, codexWorkerThreadV7) {
		t.Fatal("a worker thread's record proved it top-level")
	}
	if codexSessionRecordTopLevel(home, "01a1fdec-0000-7000-8000-000000000003") || codexSessionRecordTopLevel(home, "00000001-0000-4000-8000-000000000001") {
		t.Fatal("a thread with no record was proven")
	}
	// A second record for the same thread, in the next day's folder, proves
	// nothing (security review 2026-11-22).
	next := time.UnixMilli(0x01a1fdec0000).Add(24 * time.Hour).Local()
	nextDir := filepath.Join(home, "sessions", next.Format("2006"), next.Format("01"), next.Format("02"))
	if err := os.MkdirAll(nextDir, 0o700); err != nil {
		t.Fatal(err)
	}
	top, err := os.ReadFile(codexSessionRecordPath(home, codexMainThreadV7))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nextDir, "rollout-later-"+codexMainThreadV7+".jsonl"), top, 0o600); err != nil {
		t.Fatal(err)
	}
	if codexSessionRecordPath(home, codexMainThreadV7) != "" || codexSessionRecordTopLevel(home, codexMainThreadV7) {
		t.Fatal("a thread with two session records was proven")
	}
	for name, line := range map[string]string{
		"parent":       `{"type":"session_meta","payload":{"id":"` + codexMainThreadV7 + `","source":"vscode","parent_thread_id":"x"}}`,
		"subagent tag": `{"type":"session_meta","payload":{"id":"` + codexMainThreadV7 + `","source":"vscode","thread_source":"subagent"}}`,
		"other id":     `{"type":"session_meta","payload":{"id":"` + codexWorkerThreadV7 + `","source":"vscode"}}`,
		"no source":    `{"type":"session_meta","payload":{"id":"` + codexMainThreadV7 + `"}}`,
		"other type":   `{"type":"response_item","payload":{"id":"` + codexMainThreadV7 + `","source":"vscode"}}`,
	} {
		if codexSessionMetaTopLevel([]byte(line), codexMainThreadV7) {
			t.Fatal(name, "proved a thread top-level")
		}
	}
}

// Architect ruling 3, 2026-11-22: a restarted helper whose worker thread
// calls first stays unbound; the main chat then binds on its next call, and
// the worker's listing shows no mine row and no other_chats counts while
// needs_permission stays.
func TestCodexWorkerCallingFirstLeavesTheRestartedHelperUnbound(t *testing.T) {
	shortenAgentTiming(t)
	home := t.TempDir()
	writeCodexSessionRecord(t, home, codexMainThreadV7, map[string]any{"source": "vscode", "session_id": codexMainThreadV7})
	writeCodexSessionRecord(t, home, codexWorkerThreadV7, codexWorkerPayload(codexMainThreadV7))
	priorHome, priorTop := codexReceivingHome, codexThreadTopLevel
	codexReceivingHome = func() (string, error) { return home, nil }
	codexThreadTopLevel = codexProvenTopLevel
	t.Cleanup(func() { codexReceivingHome, codexThreadTopLevel = priorHome, priorTop })

	runtime, queue := newChaosRuntime(), new(chaosQueue)
	mine := true
	runtime.listing = &msg.InboxPage{Outcome: msg.OutcomePage,
		Inboxes:         []msg.Inbox{{Program: "app:oa-codex-integration", Context: "native-00000000000000000000000000000001", ChatTitle: "Main chat", Revision: "r1", Mine: &mine}, {Program: "app:oa-claude-integration", Context: "chat-peer", ChatTitle: "Peer", Revision: "r2"}},
		OtherChats:      []msg.ProgramChats{{Program: "app:oa-claude-integration", Chats: 3}},
		NeedsPermission: []msg.AgentApplication{{Program: "app:oa-opencode-integration", Title: "OpenCode"}}}
	h := startChaosHelper(t, runtime, queue, 0)
	h.thread = codexMainThreadV7
	worker := mcp.Meta{"threadId": codexWorkerThreadV7}

	// The worker calls first: its chat-bound tools are refused and nothing binds.
	out := callAs(t, h, worker, gateway.ToolMessagesInboxOpen, map[string]any{})
	if outcome, reason := toolAnswer(t, out); !out.IsError || outcome != "not_permitted" || reason != codexForeignThreadReason {
		t.Fatal("the worker's first inbox open was not refused", outcome, reason)
	}
	workerList := func() msg.InboxPage {
		t.Helper()
		out := callAs(t, h, worker, gateway.ToolMessagesInboxesList, map[string]any{})
		if out.IsError {
			t.Fatal("worker listing", out.Content[0])
		}
		page, err := msg.DecodeInboxPage([]byte(out.Content[0].(*mcp.TextContent).Text))
		if err != nil {
			t.Fatal(err)
		}
		return *page
	}
	assertWorkerList := func(page msg.InboxPage) {
		t.Helper()
		for _, inbox := range page.Inboxes {
			if inbox.Mine != nil && *inbox.Mine {
				t.Fatal("a worker thread's listing marks the chat mine", page.Inboxes)
			}
		}
		if len(page.OtherChats) != 0 || len(page.NeedsPermission) != 1 {
			t.Fatal("worker listing counts or permission rows", page.OtherChats, page.NeedsPermission)
		}
	}
	assertWorkerList(workerList())
	if _, ok := codexHeldThreads.Load(codexWorkerThreadV7); ok {
		t.Fatal("the worker thread holds the chat's thread lock")
	}
	if record := h.record(); record.Receiving != nil && record.Receiving.Thread != "" {
		t.Fatal("the worker's metadata bound the restarted helper", record.Receiving.Thread)
	}

	// The main chat binds on its next call.
	h.recovered(5 * time.Second)
	assertWorkerList(workerList())
	runtime.deliver()
	woken(t, queue, 1, 5*time.Second)
	queue.mu.Lock()
	woke := queue.threads[codexMainThreadV7]
	queue.mu.Unlock()
	if woke != 1 {
		t.Fatal("the wake did not reach the main thread", queue.threads)
	}
}
