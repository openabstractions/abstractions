package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	attachPersonSession = "2b7c1d3e-4f50-4a61-8b72-9c83d4e5f601"
	attachOlderSession  = "3c8d2e4f-5061-4b72-9c83-ad94e5f60712"
)

// writeAttachSession writes an inert stand-in for a vendor session file.
func writeAttachSession(t *testing.T, configuration, project, id string, updated time.Time) string {
	t.Helper()
	folder := filepath.Join(configuration, "projects", project)
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(folder, id+".jsonl")
	// Only the cwd field is read; the rest stands in for private text.
	cwd, _ := json.Marshal(attachFixtureFolder("Users", "person", strings.TrimPrefix(project, "C--work-")))
	line := `{"type":"user","cwd":` + string(cwd) + `,"message":"private text"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, updated, updated); err != nil {
		t.Fatal(err)
	}
	return path
}

// writeAttachSessionAt writes an inert session filed the way Claude Code files
// it: under its working folder's project name, with that folder as its cwd,
// and, when mode is set, a later record carrying that permission mode.
func writeAttachSessionAt(t *testing.T, configuration, cwd, id, mode string, updated time.Time) string {
	t.Helper()
	folder := filepath.Join(configuration, "projects", claudeProjectName(cwd))
	if err := os.MkdirAll(folder, 0o700); err != nil {
		t.Fatal(err)
	}
	quoted, _ := json.Marshal(cwd)
	lines := `{"type":"user","cwd":` + string(quoted) + `,"permissionMode":"default","message":"private text"}` + "\n"
	if mode != "" {
		lines += `{"type":"user","cwd":` + string(quoted) + `,"permissionMode":"` + mode + `","message":"later private text"}` + "\n"
	}
	path := filepath.Join(folder, id+".jsonl")
	if err := os.WriteFile(path, []byte(lines), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, updated, updated); err != nil {
		t.Fatal(err)
	}
	return path
}

// The owner asked OA to reach every existing session. Discovery scans every
// project before sorting by time, labels each by its folder's last segment,
// and never offers a session OA holds in custody.
func TestAgentAttachDiscoversExistingSessionsByMetadata(t *testing.T) {
	configuration := t.TempDir()
	now := time.Now().Truncate(time.Second)
	writeAttachSession(t, configuration, "C--work-older", attachOlderSession, now.Add(-time.Hour))
	// Named last in folder order but newest: a name-order cut would lose it.
	writeAttachSession(t, configuration, "C--work-zz-current", attachPersonSession, now)
	held := "4d9e3f50-6172-4c83-9d94-be05f6071823"
	writeAttachSession(t, configuration, "C--work-zz-current", held, now.Add(time.Minute))
	for _, name := range []string{"notes.jsonl", "not-a-session.txt", attachOlderSession + ".json"} {
		if err := os.WriteFile(filepath.Join(configuration, "projects", "C--work-older", name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(configuration, "projects", "C--work-older", attachPersonSession), 0o700); err != nil {
		t.Fatal(err)
	}
	sessions, err := claudeAttachSessions(configuration, map[string]bool{held: true})
	if err != nil || len(sessions) != 2 {
		t.Fatal(sessions, err)
	}
	if sessions[0].Session != attachPersonSession || sessions[0].Folder != "zz-current" || sessions[1].Session != attachOlderSession || sessions[1].Folder != "older" {
		t.Fatal(sessions)
	}
	for _, s := range sessions {
		if s.Agent != "claude" || s.Session == "" {
			t.Fatal(s)
		}
		raw, _ := json.Marshal(s)
		if bytes.Contains(raw, []byte(s.Session)) {
			t.Fatal("vendor session ID disclosed in listing", string(raw))
		}
	}
	if empty, err := claudeAttachSessions(t.TempDir(), nil); err != nil || len(empty) != 0 {
		t.Fatal(empty, err)
	}
	if _, err := claudeAttachSessions("relative", nil); agentSessionReason(err) != "native_configuration_unavailable" {
		t.Fatal(err)
	}
}

// The attach form is exactly --resume SOURCE --fork-session --session-id NEW.
func fixtureAgentForkFlags(args []string) (string, bool, bool) {
	values, forks := map[string][]string{}, 0
	for i, arg := range args {
		switch arg {
		case "--continue", "-c":
			return "", true, false
		case "--fork-session":
			forks++
		case "--resume", "--session-id":
			value := ""
			if i+1 < len(args) {
				value = args[i+1]
			}
			values[arg] = append(values[arg], value)
		}
	}
	if forks == 0 {
		return "", false, false
	}
	source, id := values["--resume"], values["--session-id"]
	if forks != 1 || len(source) != 1 || len(id) != 1 || !agentSessionUUID.MatchString(source[0]) || !agentSessionUUID.MatchString(id[0]) || source[0] == id[0] {
		return "", true, false
	}
	return id[0], true, true
}

// fixtureAgentStartInbox reads the inbox context the recipe gave OA's server.
func fixtureAgentStartInbox(args []string) string {
	for i, arg := range args {
		if arg != "--mcp-config" || i+1 >= len(args) {
			continue
		}
		var config struct {
			Servers map[string]struct {
				Args []string `json:"args"`
			} `json:"mcpServers"`
		}
		if json.Unmarshal([]byte(args[i+1]), &config) != nil {
			return ""
		}
		server := config.Servers["OpenAbstractions"].Args
		for j, value := range server {
			if value == "--inbox-context" && j+1 < len(server) {
				return server[j+1]
			}
		}
	}
	return ""
}
