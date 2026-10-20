package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func coldBindingFixture(t *testing.T) *claudeColdBinding {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(t.Context(), fileid.Self, cwd, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	launch := wire.ChatLaunch{Session: codexAttachThread, Cwd: cwd, Origin: wire.ChatOriginDesktop, Mode: wire.ChatModeAcceptEdits}
	raw := string(wire.EncodeChatBinding(&wire.ChatBinding{ChatID: launch.Session, Launch: &launch}))
	c, err := parseClaudeColdBinding(raw, id.Key(), fixtureNativeChatContext(launch.Session), true)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestClaudeConfiguredColdStartupUsesOrdinaryCanonicalAdmission(t *testing.T) {
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", codexAttachThread)
	c := coldBindingFixture(t)
	runtime, queue := newChaosRuntime(), new(chaosQueue)
	h := startChaosHelperConfigured(t, runtime, queue, 96, func(ctx context.Context) context.Context { return context.WithValue(ctx, claudeColdBindingKey{}, c) }, true, "oa-claude-integration")
	h.recovered(3 * time.Second)
	runtime.mu.Lock()
	claim := runtime.chatBindings[c.context]
	runtime.mu.Unlock()
	if claim.ChatID != c.launch.Session || claim.Launch == nil || *claim.Launch != c.launch {
		t.Fatal("cold execution replaced historical host facts", claim)
	}
	tools, err := h.session.ListTools(t.Context(), nil)
	if err != nil || len(tools.Tools) != 2 {
		t.Fatal(tools, err)
	}
	for _, tool := range tools.Tools {
		if tool.Name != gateway.ToolMessagesWait && tool.Name != gateway.ToolMessagesReply {
			t.Fatal("expanded cold catalogue", tool.Name)
		}
	}
	for _, name := range []string{claudeSessionStartTool, claudeSessionEndTool} {
		out, err := h.session.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: map[string]any{}})
		if err == nil && !out.IsError {
			t.Fatal("cold model created lifecycle claim", name)
		}
	}
	if out, err := h.session.CallTool(t.Context(), &mcp.CallToolParams{Name: gateway.ToolMessagesWait, Arguments: map[string]any{"wait_ms": 0, "session_id": "foreign"}}); err == nil && !out.IsError {
		t.Fatal("model selected cold session")
	}
	// A later Desktop/helper claim owns the canonical route and fences this run.
	live := startChaosHelper(t, runtime, queue, 97, "oa-claude-integration")
	liveLaunch := wire.ChatLaunch{Session: c.launch.Session, Cwd: c.launch.Cwd, Origin: wire.ChatOriginCli, Mode: wire.ChatModePlan}
	if out := live.call(claudeSessionStartTool, map[string]any{"launch": map[string]any{"cwd": liveLaunch.Cwd, "origin": string(liveLaunch.Origin), "mode": string(liveLaunch.Mode)}}); out.IsError {
		t.Fatal(out.Content)
	}
	live.recovered(3 * time.Second)
	if out := h.wait(); !out.IsError {
		t.Fatal("retired cold helper kept body access", out.Content)
	}
	live.recovered(3 * time.Second)
	runtime.mu.Lock()
	claim = runtime.chatBindings[c.context]
	runtime.mu.Unlock()
	if claim.Launch == nil || *claim.Launch != liveLaunch {
		t.Fatal("retired cold helper replaced live host facts", claim)
	}
}

func TestClaudeConfiguredColdStartupMissingOrWrongFactsStayClosed(t *testing.T) {
	shortenAgentTiming(t)
	for _, variant := range []string{"unconfigured", "missing-session", "wrong-session", "wrong-cwd", "wrong-context", "source-refused"} {
		t.Run(variant, func(t *testing.T) {
			t.Setenv("CLAUDE_CODE_SESSION_ID", codexAttachThread)
			c := coldBindingFixture(t)
			if variant == "missing-session" {
				t.Setenv("CLAUDE_CODE_SESSION_ID", "")
			}
			if variant == "wrong-session" {
				t.Setenv("CLAUDE_CODE_SESSION_ID", codexOtherThread)
			}
			if variant == "wrong-cwd" {
				c.cwdKey = "foreign"
			}
			if variant == "wrong-context" {
				c.context = "chat-" + strings.Repeat("f", 48)
			}
			runtime := newChaosRuntime()
			runtime.forbid = variant == "source-refused"
			h := startChaosHelperConfigured(t, runtime, new(chaosQueue), 98, func(ctx context.Context) context.Context {
				if variant == "unconfigured" {
					return ctx
				}
				return context.WithValue(ctx, claudeColdBindingKey{}, c)
			}, true, "oa-claude-integration")
			out := h.wait()
			if !strings.Contains(out.Content[0].(*mcp.TextContent).Text, "unavailable") {
				t.Fatal("unproved cold binding became readable", out.Content)
			}
		})
	}
}

func TestClaudeConfiguredColdPayloadRefusesMalformedOrExpandedCorrelation(t *testing.T) {
	c := coldBindingFixture(t)
	valid := func() wire.ChatBinding {
		launch := c.launch
		return wire.ChatBinding{ChatID: launch.Session, Launch: &launch}
	}
	for _, variant := range []string{"relative-cwd", "nonlocal-cwd", "unknown-origin", "unknown-mode", "foreign-session", "source-instance", "title", "missing-launch"} {
		t.Run(variant, func(t *testing.T) {
			claim := valid()
			switch variant {
			case "relative-cwd":
				claim.Launch.Cwd = "relative"
			case "nonlocal-cwd":
				claim.Launch.Cwd = `\\server\share\folder`
			case "unknown-origin":
				claim.Launch.Origin = wire.ChatOrigin("future")
			case "unknown-mode":
				claim.Launch.Mode = wire.ChatMode("future")
			case "foreign-session":
				claim.ChatID = codexOtherThread
			case "source-instance":
				claim.SourceInstance = "chosen-source"
			case "title":
				claim.ChatTitle = "chosen-title"
			case "missing-launch":
				claim.Launch = nil
			}
			if out, err := parseClaudeColdBinding(string(wire.EncodeChatBinding(&claim)), c.cwdKey, c.context, true); err == nil || out != nil {
				t.Fatal("expanded cold startup accepted", out, err)
			}
		})
	}
	claim := valid()
	raw := string(wire.EncodeChatBinding(&claim))
	for _, pair := range [][2]string{{raw, ""}, {"", c.cwdKey}, {"{", c.cwdKey}} {
		if out, err := parseClaudeColdBinding(pair[0], pair[1], c.context, true); err == nil || out != nil {
			t.Fatal("partial/malformed correlation accepted", out, err)
		}
	}
	if out, err := parseClaudeColdBinding(raw, c.cwdKey, c.context, false); err == nil || out != nil {
		t.Fatal("ordinary view configured cold receiving", out, err)
	}
	var expanded map[string]any
	if err := json.Unmarshal([]byte(raw), &expanded); err != nil {
		t.Fatal(err)
	}
	expanded["program"] = "chosen-program"
	unknown, _ := json.Marshal(expanded)
	if out, err := parseClaudeColdBinding(string(unknown), c.cwdKey, c.context, true); err == nil || out != nil {
		t.Fatal("unknown startup field accepted", out, err)
	}
}
