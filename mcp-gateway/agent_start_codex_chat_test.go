package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile/startprofiletest"
)

// A retained Codex chat is woken by queueing one pointer into its own thread
// while Codex Desktop runs. With Codex closed nothing reaches the vendor: the
// message waits for the person to open the chat.
func TestAgentStartRegisteredCodexRetainedChat(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("owned process fixture is Windows-only")
	}
	t.Setenv("OA_CODEX_ATTACH_FIXTURE", "1")
	t.Setenv("OA_CODEX_ATTACH_LOGIN", "Logged in using ChatGPT")
	startprofiletest.ClearProviderSelectors(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "codex-delivery.json")
	t.Setenv("OA_CODEX_ATTACH_MARKER", marker)
	p := startprofile.Profile{Name: "codex-v1", Context: "recipe-inbox", Folder: t.TempDir(), StateDir: t.TempDir(), CLIFileID: "fixture-locator", Model: "fixture-model", TimeoutMS: 10000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	d := wire.ApplicationDescriptor{Name: "fixture", Program: program, Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments()}}
	read := func(context.Context) (wire.ApplicationDescriptor, error) { return d, nil }
	selectCLI := func(ctx context.Context, _ startprofile.Profile, probe func(context.Context, string) ([]byte, error)) (agentStartCLI, error) {
		if _, e := probe(ctx, program); e != nil {
			return nil, e
		}
		return &fixtureAgentStartCLI{program: program}, nil
	}
	running := true
	prior := codexDesktopRunning
	codexDesktopRunning = func(context.Context) (bool, error) { return running, nil }
	t.Cleanup(func() { codexDesktopRunning = prior })
	chat := "chat-" + strings.Repeat("c", 48)
	cold := &startprofile.ColdResume{Context: chat, Revision: strings.Repeat("d", 64), Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: codexOtherThread}))}
	deliver := func(selection *startprofile.ColdResume, target string) (agentStartResult, []string, error) {
		t.Helper()
		_ = os.Remove(marker)
		custody := fixtureAgentCustody(t, p, program)
		custody.target, custody.cold = target, selection
		r, err := resolveRegisteredAgentStart(context.Background(), p, program, read, selectCLI, custody)
		if err != nil {
			return agentStartResult{}, nil, err
		}
		r.capture = func(context.Context, agentStartExitMetadata) (string, error) { return "submitted_unconfirmed", nil }
		result := runAgentStart(context.Background(), p.Name, p.Context, func(context.Context, string, string) (*agentStartRecipe, error) { return r, nil })
		var args []string
		raw, _ := os.ReadFile(marker)
		_ = json.Unmarshal(raw, &args)
		return result, args, nil
	}
	// The thread need not be among the listed ones: no discovery runs.
	result, args, err := deliver(cold, chat)
	if err != nil || result.Outcome != "exited" || !reflect.DeepEqual(args, []string{"queue", "--thread", codexOtherThread, "--message", agentStartPrompt(chat), "--resume"}) {
		t.Fatal("running Desktop did not receive the chat's pointer", result, args, err)
	}
	if result.Delivery != agentDeliveryDesktopQueue {
		t.Fatal("queued turn not reported as the Desktop's", result.Delivery)
	}
	running = false
	if _, _, err := deliver(cold, chat); agentSessionReason(err) != "codex_closed" {
		t.Fatal("closed Codex was started", err)
	}
	if _, e := os.Stat(marker); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("closed Codex reached the vendor")
	}
	running = true
	// The selection names the addressed chat only.
	if _, _, err := deliver(cold, "chat-"+strings.Repeat("e", 48)); agentSessionReason(err) != "cold_selection_invalid" {
		t.Fatal("selection for another chat accepted", err)
	}
	launch := &startprofile.ColdResume{Context: chat, CwdKey: "folder", Revision: cold.Revision, Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: codexOtherThread, Launch: &msg.ChatLaunch{Session: codexOtherThread, Origin: msg.ChatOriginDesktop, Cwd: t.TempDir(), Mode: msg.ChatModePlan}}))}
	if _, _, err := deliver(launch, chat); agentSessionReason(err) != "cold_selection_invalid" {
		t.Fatal("a Claude launch reached the Codex recipe", err)
	}
	t.Setenv("OA_CODEX_ATTACH_QUEUE_WARNING", "resume failed")
	if result, _, err := deliver(cold, chat); err != nil || result.Outcome != "failed" || result.Reason != "session_result_unconfirmed" {
		t.Fatal("zero-exit warning claimed a wake", result, err)
	}
	t.Setenv("OA_CODEX_ATTACH_QUEUE_WARNING", "")
	t.Setenv("OA_CODEX_ATTACH_LOGIN", "Logged in using an API key")
	if _, _, err := deliver(cold, chat); err == nil || !strings.Contains(err.Error(), "subscription_signin_unavailable") {
		t.Fatal("API-key sign-in accepted", err)
	}
}

// A thread Codex no longer lists is never queued into.
func TestAgentStartRegisteredCodexRetainedChatGoneThread(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("owned process fixture is Windows-only")
	}
	t.Setenv("OA_CODEX_ATTACH_FIXTURE", "1")
	t.Setenv("OA_CODEX_ATTACH_LOGIN", "Logged in using ChatGPT")
	startprofiletest.ClearProviderSelectors(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(t.TempDir(), "codex-delivery.json")
	t.Setenv("OA_CODEX_ATTACH_MARKER", marker)
	p := startprofile.Profile{Name: "codex-v1", Context: "recipe-inbox", Folder: t.TempDir(), StateDir: t.TempDir(), CLIFileID: "fixture-locator", Model: "fixture-model", TimeoutMS: 10000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	d := wire.ApplicationDescriptor{Name: "fixture", Program: program, Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments()}}
	read := func(context.Context) (wire.ApplicationDescriptor, error) { return d, nil }
	selectCLI := func(ctx context.Context, _ startprofile.Profile, probe func(context.Context, string) ([]byte, error)) (agentStartCLI, error) {
		if _, e := probe(ctx, program); e != nil {
			return nil, e
		}
		return &fixtureAgentStartCLI{program: program}, nil
	}
	prior := codexDesktopRunning
	codexDesktopRunning = func(context.Context) (bool, error) { return true, nil }
	t.Cleanup(func() { codexDesktopRunning = prior })
	chat := "chat-" + strings.Repeat("c", 48)
	custody := fixtureAgentCustody(t, p, program)
	custody.target, custody.cold = chat, &startprofile.ColdResume{Context: chat, Revision: strings.Repeat("d", 64), Binding: string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: "01a1ffff-0000-7000-8000-000000000000"}))}
	if _, err := resolveRegisteredAgentStart(context.Background(), p, program, read, selectCLI, custody); agentSessionReason(err) != "session_unknown" {
		t.Fatal("a gone thread resolved a queue", err)
	}
	if _, e := os.Stat(marker); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("a gone thread reached the vendor")
	}
}
