package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	facade "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

const (
	titleSession = "3eb8fabf-7140-425f-babe-9ccbd2b32ff5"
	titleParent  = "f74e8d1a-dd1c-4f7e-8472-a6c3c91c2d1b"
)

func claudeTranscript(t *testing.T, config string) func(lines ...string) {
	t.Helper()
	dir := filepath.Join(config, "projects", "C--work-AI-abstractions")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, titleSession+".jsonl")
	return func(lines ...string) {
		t.Helper()
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		for _, line := range lines {
			if _, err := f.WriteString(line + "\n"); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func customTitle(session, title string) string {
	return `{"type":"custom-title","customTitle":"` + title + `","sessionId":"` + session + `"}`
}

func madeTitle(session, title string) string {
	return `{"type":"ai-title","aiTitle":"` + title + `","sessionId":"` + session + `"}`
}

// The person's custom title wins over Claude's own; a fork's copied lines
// name the parent session and are skipped.
func TestClaudeTranscriptTitlePrefersTheSessionsCustomTitle(t *testing.T) {
	config := t.TempDir()
	write := claudeTranscript(t, config)
	path := claudeTranscriptPath(config, titleSession)
	if path != "" {
		t.Fatal("transcript found before it exists", path)
	}
	write(customTitle(titleParent, "Parent title"), `{"type":"user","message":"hello"}`)
	path = claudeTranscriptPath(config, titleSession)
	if path == "" || claudeTranscriptTitle(path, titleSession) != "" {
		t.Fatal("parent title named the fork", path)
	}
	write(madeTitle(titleSession, "Find handoff file"))
	if got := claudeTranscriptTitle(path, titleSession); got != "Find handoff file" {
		t.Fatalf("title %q", got)
	}
	write(customTitle(titleSession, "Open\\u202e Abstractions"), madeTitle(titleSession, "Find handoff file"))
	if got := claudeTranscriptTitle(path, titleSession); got != "Open Abstractions" {
		t.Fatalf("title %q", got)
	}
	if claudeTranscriptPath(config, "../"+titleSession) != "" || claudeTranscriptPath("", titleSession) != "" {
		t.Fatal("a non-session name selected a transcript")
	}
}

// A long transcript is read from its tail, and a tail without a title line
// reads the longer tail once.
func TestClaudeTranscriptTitleReadsTheTail(t *testing.T) {
	saved := claudeTranscriptTails
	claudeTranscriptTails = []int64{256, 4096}
	t.Cleanup(func() { claudeTranscriptTails = saved })
	config := t.TempDir()
	write := claudeTranscript(t, config)
	write(customTitle(titleSession, "Old name"), `{"type":"assistant","text":"`+strings.Repeat("x", 1000)+`"}`)
	path := claudeTranscriptPath(config, titleSession)
	if got := claudeTranscriptTitle(path, titleSession); got != "Old name" {
		t.Fatalf("title %q", got)
	}
	write(customTitle(titleSession, "New name"))
	if got := claudeTranscriptTitle(path, titleSession); got != "New name" {
		t.Fatalf("title %q", got)
	}
}

// A rename in Claude reaches the native claim without a person message, and
// the Mod's unchanged older title does not revert it.
func TestClaudeRenameReachesNativeClaimFromTheTranscript(t *testing.T) {
	shortenAgentTiming(t)
	config := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", config)
	t.Setenv("CLAUDE_CODE_SESSION_ID", titleSession)
	write := claudeTranscript(t, config)
	runtime := newChaosRuntime()
	_, diagnosticContext, _ := diagnosticFixture(t)
	ctx, cancel := context.WithCancel(diagnosticContext)
	defer cancel()
	presence, err := runtime.Announce(ctx, facade.ApplicationPresence{Application: "oa-claude-integration", LeaseMs: 60000, Contexts: []facade.ApplicationContext{{Name: "source"}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.InboxOpenChat(presence.Instance, "source", "", msg.ChatBinding{}); err != nil {
		t.Fatal(err)
	}
	b := &messagesBackend{claudeTitleRefresh: 5 * time.Millisecond, application: "oa-claude-integration", instance: presence.Instance, contextName: "source", native: func(context.Context) (nativeMessages, error) { return runtime, nil }}
	configureInertChatPresence(b, runtime)
	st, ct := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() { done <- runClaudeChatServer(ctx, gateway.NewServer(b), b, st) }()
	client, err := mcp.NewClient(&mcp.Implementation{Name: "fixture", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		client.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Claude title helper did not join")
		}
	})
	start := func() {
		t.Helper()
		out, err := client.CallTool(ctx, &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: map[string]any{"chat_title": "AI abstractions"}})
		if err != nil || out.IsError {
			t.Fatal(out, err)
		}
	}
	claimed := func(want string) {
		t.Helper()
		for until := time.Now().Add(3 * time.Second); ; {
			runtime.mu.Lock()
			claim := runtime.chatBindings[b.chatContext]
			runtime.mu.Unlock()
			if claim.ChatTitle == want {
				return
			}
			if time.Now().After(until) {
				t.Fatalf("native claim title=%q want=%q", claim.ChatTitle, want)
			}
			time.Sleep(time.Millisecond)
		}
	}
	start()
	claimed("AI abstractions")
	write(madeTitle(titleSession, "Find handoff file"))
	claimed("Find handoff file")
	write(customTitle(titleSession, "Open Abstractions"), madeTitle(titleSession, "Find handoff file"))
	claimed("Open Abstractions")
	start()
	time.Sleep(50 * time.Millisecond)
	claimed("Open Abstractions")
}

// A read starts where the last one ended. A transcript without title lines
// is read once; later reads see only what was appended.
func TestClaudeTranscriptTitlesReadOnlyNewLines(t *testing.T) {
	saved := claudeTranscriptTails
	claudeTranscriptTails = []int64{256, 4096}
	t.Cleanup(func() { claudeTranscriptTails = saved })
	config := t.TempDir()
	write := claudeTranscript(t, config)
	write(`{"type":"assistant","text":"`+strings.Repeat("x", 6000)+`"}`, `{"type":"user","message":"hello"}`)
	path := claudeTranscriptPath(config, titleSession)
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	custom, made, end, ok := claudeTranscriptTitles(path, titleSession, 0)
	if !ok || custom != "" || made != "" || end != info.Size() {
		t.Fatal(custom, made, end, info.Size(), ok)
	}
	write(madeTitle(titleSession, "Find handoff file"))
	custom, made, next, ok := claudeTranscriptTitles(path, titleSession, end)
	if !ok || custom != "" || made != "Find handoff file" || next <= end {
		t.Fatal(custom, made, next, ok)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	_, err = f.WriteString(`{"type":"user","message":"partial`)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if custom, made, again, ok := claudeTranscriptTitles(path, titleSession, next); !ok || custom != "" || made != "" || again != next {
		t.Fatal("a partial line moved the read offset", custom, made, again)
	}
}

// Without an override, Claude's configuration is .claude under the home
// folder, here the personfolders stand-in.
func TestClaudeConfigDirectoryDefaultsToHomeDotClaude(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := claudeConfigDirectory(), filepath.Join(home, ".claude"); got != want {
		t.Fatalf("claudeConfigDirectory() = %q, want %q", got, want)
	}
}

// A relative override names no folder, so no transcript is read.
func TestClaudeConfigDirectoryRefusesRelativeOverride(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", filepath.Join("relative", "config"))
	if got := claudeConfigDirectory(); got != "" {
		t.Fatalf("claudeConfigDirectory() = %q, want empty", got)
	}
}
