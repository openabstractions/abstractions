package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile/startprofiletest"
)

func listFixture(t *testing.T, p startprofile.Profile) ([]receiverSession, error) {
	t.Helper()
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	d := wire.ApplicationDescriptor{Name: "fixture", Program: program, Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments()}}
	read := func(context.Context) (wire.ApplicationDescriptor, error) { return d, nil }
	selectCLI := func(ctx context.Context, _ startprofile.Profile, probe func(context.Context, string) ([]byte, error)) (agentStartCLI, error) {
		if _, e := probe(ctx, program); e != nil {
			return nil, e
		}
		return &fixtureAgentStartCLI{program: program}, nil
	}
	sessions := []receiverSession{}
	custody := fixtureAgentCustody(t, p, program)
	custody.list = &sessions
	r, err := resolveRegisteredAgentStart(context.Background(), p, program, read, selectCLI, custody)
	if r != nil {
		t.Fatal("a listing resolved a start")
	}
	if !errors.Is(err, errReceiverSessionsListed) {
		return nil, err
	}
	return sessions, nil
}

// The registered Codex recipe lists the existing threads with their folder
// and title. The private listing names threads by their vendor IDs and
// carries no attach-* context; nothing starts.
func TestAgentSessionsListCodexThreads(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("owned process fixture is Windows-only")
	}
	t.Setenv("OA_CODEX_ATTACH_FIXTURE", "1")
	t.Setenv("OA_CODEX_ATTACH_LOGIN", "Logged in using ChatGPT")
	startprofiletest.ClearProviderSelectors(t)
	marker := filepath.Join(t.TempDir(), "codex-delivery.json")
	t.Setenv("OA_CODEX_ATTACH_MARKER", marker)
	work := filepath.Join(t.TempDir(), "current")
	if err := os.Mkdir(work, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OA_CODEX_ATTACH_CWD", work)
	p := startprofile.Profile{Name: "codex-v1", Context: "recipe-inbox", Folder: t.TempDir(), StateDir: t.TempDir(), CLIFileID: "fixture-locator", Model: "fixture-model", TimeoutMS: 10000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	sessions, err := listFixture(t, p)
	if err != nil || len(sessions) == 0 {
		t.Fatal(sessions, err)
	}
	found := false
	for _, s := range sessions {
		found = found || s.Chat == codexAttachThread && s.Cwd == work
	}
	raw, _ := json.Marshal(receiverSessionsPage{Sessions: sessions})
	if !found || strings.Contains(string(raw), agentAttachPrefix) {
		t.Fatal("listing", string(raw))
	}
	if _, e := os.Stat(marker); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("a listing reached a thread")
	}
}

// The registered Claude recipe lists the sessions of its signed-in
// configuration with the launch facts their records state. Sessions OA holds
// are never listed; a session whose records name no origin lists without one.
func TestAgentSessionsListClaudeSessions(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("owned process fixture is Windows-only")
	}
	t.Setenv("OA_AGENT_START_REGISTERED_FIXTURE", "1")
	t.Setenv("OA_AGENT_START_FROZEN_ENV", "before")
	startprofiletest.ClearProviderSelectors(t)
	state, config := t.TempDir(), t.TempDir()
	t.Setenv("OA_AGENT_START_CONFIG_DIR", config)
	work := t.TempDir()
	project := filepath.Join(config, "projects", claudeProjectName(work))
	if err := os.MkdirAll(project, 0o700); err != nil {
		t.Fatal(err)
	}
	cwd, _ := json.Marshal(work)
	write := func(session, entrypoint, extra string) {
		t.Helper()
		text := `{"type":"user","cwd":` + string(cwd) + `,"entrypoint":"` + entrypoint + `","permissionMode":"plan"}` + "\n" + extra
		if err := os.WriteFile(filepath.Join(project, session+".jsonl"), []byte(text), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	desktop, unknown := "2b7c1d3e-4f50-4a61-8b72-9c83d4e5f601", "3c8d2e4f-5061-4b72-9c83-ad94e5f6a712"
	write(desktop, "claude-desktop", `{"type":"custom-title","customTitle":"Design team","sessionId":"x"}`+"\n")
	write(unknown, "sdk-ts", "")
	p := startprofile.Profile{Name: "claude-v1", Context: "recipe-inbox", Folder: t.TempDir(), StateDir: state, CLIFileID: "fixture-locator", Model: "fixture-model", TimeoutMS: 10000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	program, _ := os.Executable()
	lease, err := beginAgentFork(t.Context(), state, fixtureAgentCustody(t, p, program).binding, "chat-"+strings.Repeat("c", 48), "copy:"+desktop+"@1")
	if err != nil {
		t.Fatal(err)
	}
	write(lease.record.Session, "claude-desktop", "")
	sessions, err := listFixture(t, p)
	if err != nil {
		t.Fatal(err)
	}
	byChat := map[string]receiverSession{}
	for _, s := range sessions {
		byChat[s.Chat] = s
	}
	if _, held := byChat[lease.record.Session]; held || len(byChat) != 2 {
		t.Fatal("OA's own copy listed", sessions)
	}
	if s := byChat[desktop]; s.Origin != "desktop" || s.Mode != "plan" || s.Cwd != filepath.Clean(work) || s.Title != "Design team" {
		t.Fatal("desktop session facts", s)
	}
	if s := byChat[unknown]; s.Origin != "" {
		t.Fatal("an unknown origin was stated", s)
	}
}
