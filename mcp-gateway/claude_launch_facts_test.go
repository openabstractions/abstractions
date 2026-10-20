package main

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/fileid"
)

func TestClaudeHostLaunchFactsUseFixedSessionAndNativeDirectory(t *testing.T) {
	cwd := t.TempDir()
	startup, err := fileid.Resolve(t.Context(), fileid.Self, cwd, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	valid := &claudeHostLaunchInput{Cwd: cwd, Origin: "desktop", Mode: "plan"}
	if out := claudeHostLaunch(t.Context(), codexAttachThread, startup, valid); out == nil || out.Session != codexAttachThread || out.Cwd != cwd || out.Mode != wire.ChatModePlan {
		t.Fatal(out)
	}
	for _, in := range []*claudeHostLaunchInput{nil, {Cwd: t.TempDir(), Origin: "desktop", Mode: "plan"}, {Cwd: cwd, Origin: "unknown", Mode: "plan"}, {Cwd: cwd, Origin: "desktop", Mode: "bypassPermissions"}} {
		if out := claudeHostLaunch(t.Context(), codexAttachThread, startup, in); out != nil {
			t.Fatal("unsupported host facts became launchable", out)
		}
	}
	if out := claudeHostLaunch(t.Context(), "opaque-non-resumable", startup, valid); out != nil {
		t.Fatal("opaque session became a CLI resume target", out)
	}
	if err := os.Rename(cwd, cwd+"-original"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(cwd, 0700); err != nil {
		t.Fatal(err)
	}
	if out := claudeHostLaunch(t.Context(), codexAttachThread, startup, valid); out != nil {
		t.Fatal("replacement startup directory accepted", out)
	}
}

func TestClaudeHostLaunchLifecycleReachesNativeClaimWithoutGrant(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", codexAttachThread)
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	runtime := newChaosRuntime()
	h := startChaosHelper(t, runtime, new(chaosQueue), 95, "oa-claude-integration")
	call := func(args map[string]any) *mcp.CallToolResult {
		t.Helper()
		return h.call(claudeSessionStartTool, args)
	}
	out := call(map[string]any{"launch": map[string]any{"cwd": cwd, "origin": "desktop", "mode": "plan"}})
	var binding map[string]string
	if err := json.Unmarshal([]byte(out.Content[0].(*mcp.TextContent).Text), &binding); err != nil || out.IsError || binding["launch_facts"] != "supported" {
		t.Fatal(binding, err)
	}
	h.recovered(3 * time.Second)
	read := func(want bool) {
		t.Helper()
		until := time.Now().Add(3 * time.Second)
		for {
			runtime.mu.Lock()
			launch := runtime.chatBindings[binding["context"]].Launch
			runtime.mu.Unlock()
			if (launch != nil) == want {
				if want && (launch.Session != codexAttachThread || launch.Cwd != cwd || launch.Mode != wire.ChatModePlan) {
					t.Fatal(launch)
				}
				return
			}
			if time.Now().After(until) {
				t.Fatal("native launch readback did not converge", launch)
			}
			time.Sleep(time.Millisecond)
		}
	}
	read(true)
	call(map[string]any{})
	read(false)
	call(map[string]any{"launch": map[string]any{"cwd": cwd, "origin": "desktop", "mode": "plan"}})
	read(true)
	// Model-facing arguments have no slot to nominate a different identity.
	for _, extra := range []string{"session", "program"} {
		result, err := h.session.CallTool(t.Context(), &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: map[string]any{"launch": map[string]any{"cwd": cwd, "origin": "desktop", "mode": "plan", extra: "foreign"}}})
		if err == nil && !result.IsError {
			t.Fatal("foreign launch identity accepted", extra)
		}
		read(true)
	}
	call(map[string]any{"launch": nil})
	read(false)
	h.recovered(3 * time.Second)
}
