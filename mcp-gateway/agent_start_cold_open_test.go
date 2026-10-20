package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// coldClaudeFixtureMain is an inert stand-in for Claude Code's print mode: it
// records its arguments and answers with the session it was asked to run.
func coldClaudeFixtureMain() bool {
	if os.Getenv("OA_COLD_CLAUDE_FIXTURE") != "1" || len(os.Args) < 2 || os.Args[1] != "-p" {
		return false
	}
	session := ""
	for i := 2; i+1 < len(os.Args); i++ {
		if os.Args[i] == "--session-id" || os.Args[i] == "--resume" && session == "" {
			session = os.Args[i+1]
		}
	}
	raw, _ := json.Marshal(os.Args[1:])
	if os.WriteFile(os.Getenv("OA_COLD_CLAUDE_MARKER"), raw, 0o600) != nil {
		os.Exit(4)
	}
	out, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": session, "result": "fixture"})
	_, _ = os.Stdout.Write(out)
	return true
}

// A retained Claude conversation resumes in place only while no Claude runs.
// While one runs the turn runs in a copy OA holds: the first question forks
// the original, a later one resumes the same copy, and a changed original is
// copied again. No spawn omits --resume.
func TestColdClaudeStartResumesInPlaceOrCopies(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("owned process fixture is Windows-only")
	}
	cwd := t.TempDir()
	key, err := identity.FileKey(cwd)
	if err != nil {
		t.Fatal(err)
	}
	launch := &msg.ChatLaunch{Session: "11111111-1111-4111-8111-111111111111", Origin: msg.ChatOriginDesktop, Cwd: cwd, Mode: msg.ChatModeAcceptEdits}
	chat := "chat-" + strings.Repeat("a", 48)
	cold := &startprofile.ColdResume{Context: chat, CwdKey: key, Revision: strings.Repeat("b", 64), Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: launch.Session, Launch: launch}))}
	configuration := t.TempDir()
	original := filepath.Join(configuration, "projects", claudeProjectName(cwd), launch.Session+".jsonl")
	if err := os.MkdirAll(filepath.Dir(original), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	p := startprofile.Profile{Name: "claude-v1", Context: "recipe-inbox", Folder: state, StateDir: state, CLIFileID: "fixture", Model: "fixture", TimeoutMS: 10000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	helper := filepath.Join(t.TempDir(), "oa-claude-integration.exe")
	marker := filepath.Join(t.TempDir(), "argv.json")
	environment := append(os.Environ(), "OA_COLD_CLAUDE_FIXTURE=1", "OA_COLD_CLAUDE_MARKER="+marker)
	running := false
	prior := claudeRunning
	claudeRunning = func(context.Context) (bool, error) { return running, nil }
	t.Cleanup(func() { claudeRunning = prior })
	custody := fixtureAgentCustody(t, p, program)
	custody.target, custody.cold = chat, cold
	deliver := func() ([]string, agentStartResult) {
		t.Helper()
		_ = os.Remove(marker)
		var held []setupIdentityRef
		r, err := resolveSelectedColdClaudeStart(t.Context(), p, helper, configuration, &fixtureAgentStartCLI{program: program}, environment, custody, "2.1.317", func(context.Context) error { return nil }, func() error { return nil }, &held)
		if err != nil {
			t.Fatal(err)
		}
		r.capture = func(context.Context, agentStartExitMetadata) (string, error) { return "retained", nil }
		result := runAgentStart(t.Context(), p.Name, p.Context, func(context.Context, string, string) (*agentStartRecipe, error) { return r, nil })
		var args []string
		raw, _ := os.ReadFile(marker)
		_ = json.Unmarshal(raw, &args)
		return args, result
	}
	flag := func(args []string, name string) string {
		if i := slices.Index(args, name); i >= 0 && i+1 < len(args) {
			return args[i+1]
		}
		return ""
	}
	args, result := deliver()
	if result.Outcome != "exited" || flag(args, "--resume") != launch.Session || slices.Contains(args, "--fork-session") || result.Delivery != agentDeliveryResumed {
		t.Fatal("closed Claude did not resume in place", args, result)
	}
	running = true
	args, result = deliver()
	copyID := flag(args, "--session-id")
	if result.Outcome != "exited" || flag(args, "--resume") != launch.Session || !slices.Contains(args, "--fork-session") || !agentSessionUUID.MatchString(copyID) || copyID == launch.Session || result.Delivery != agentDeliveryCopy {
		t.Fatal("running Claude did not answer in a copy", args, result)
	}
	if !strings.Contains(flag(args, "--mcp-config"), `"--claude-resume-copy","`+copyID+`"`) {
		t.Fatal("the copy's helper does not name its copy", flag(args, "--mcp-config"))
	}
	args, result = deliver()
	if result.Outcome != "exited" || flag(args, "--resume") != copyID || slices.Contains(args, "--fork-session") {
		t.Fatal("an unchanged original did not reuse its copy", args, result)
	}
	later := time.Now().Add(time.Minute)
	if err := os.Chtimes(original, later, later); err != nil {
		t.Fatal(err)
	}
	args, result = deliver()
	if result.Outcome != "exited" || flag(args, "--resume") != launch.Session || !slices.Contains(args, "--fork-session") || flag(args, "--session-id") == copyID {
		t.Fatal("a changed original kept its old copy", args, result)
	}
	// A Claude started between the choice and the spawn holds the original
	// open: the in-place resume refuses and nothing runs.
	running = false
	var held []setupIdentityRef
	r, err := resolveSelectedColdClaudeStart(t.Context(), p, helper, configuration, &fixtureAgentStartCLI{program: program}, environment, custody, "2.1.317", func(context.Context) error { return nil }, func() error { return nil }, &held)
	if err != nil {
		t.Fatal(err)
	}
	running = true
	_ = os.Remove(marker)
	if err := r.run(t.Context(), "", &agentStartOutput{max: 4096}, p.MemoryBytes); agentSessionReason(err) != "claude_open" || r.execution.ExitKnown {
		t.Fatal("Claude started after the choice was resumed in place", err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("a refused resume reached Claude")
	}
}
