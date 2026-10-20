package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A woken copy runs with the session's own access (VISION 2026-10-31): its
// own folder and the permission mode it last recorded, never raised. An
// unrecorded or unknown mode answers in "default", and a session filed under
// a project that is not its folder's is refused.
func TestAgentAttachCopyKeepsSessionAccess(t *testing.T) {
	configuration := t.TempDir()
	work := filepath.Join(t.TempDir(), "my project")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for mode, want := range map[string]string{"": "default", "plan": "plan", "acceptEdits": "acceptEdits", "bypassPermissions": "bypassPermissions", "yolo-future-mode": "default"} {
		path := writeAttachSessionAt(t, configuration, work, attachPersonSession, mode, now)
		access, err := claudeAttachAccess(agentAttachSession{path: path})
		if err != nil || access.Mode != want || !strings.EqualFold(access.Folder, filepath.Clean(work)) {
			t.Fatal(mode, access, err)
		}
	}
	// Filed under another project: the copy would resume some other session.
	moved := filepath.Join(configuration, "projects", "C--elsewhere", attachOlderSession+".jsonl")
	if err := os.MkdirAll(filepath.Dir(moved), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(filepath.Join(configuration, "projects", claudeProjectName(work), attachPersonSession+".jsonl"))
	if err := os.WriteFile(moved, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := claudeAttachAccess(agentAttachSession{path: moved}); agentSessionReason(err) != "session_folder_unavailable" {
		t.Fatal("misfiled session", err)
	}
	relative := filepath.Join(configuration, "projects", "relative", attachOlderSession+".jsonl")
	if err := os.MkdirAll(filepath.Dir(relative), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(relative, []byte(`{"cwd":"relative"}`+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := claudeAttachAccess(agentAttachSession{path: relative}); agentSessionReason(err) != "session_folder_unavailable" {
		t.Fatal("relative folder", err)
	}
	// The copy's argv carries the session's mode, approvals failing closed,
	// the session's own MCP servers with OA's receiver in place of OA's own
	// connection, and no raising or restricting flag.
	args := claudeAttachRunArguments("pointer", "plan", `{"mcpServers":{"OpenAbstractions":{"command":"oa"}}}`, "1.00", "model")
	if fixtureAgentAttachAccess(append(args, "--resume", attachPersonSession, "--fork-session", "--session-id", attachOlderSession), "plan") != true {
		t.Fatal(args)
	}
	for _, raised := range []string{"--dangerously-skip-permissions", "acceptEdits", "bypassPermissions", "--tools", "--restricted", "--strict-mcp-config"} {
		if slices.Contains(args, raised) {
			t.Fatal("copy raised or restricted access with", raised)
		}
	}
}
