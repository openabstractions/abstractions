package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
	"github.com/openabstractions/abstractions/mcp-gateway/nativecli"
)

// coldClaudeResumeArguments is inert preparation, never a start recipe. Its
// caller must hold the actual registered helper/CLI and retained host facts.
// Restriction flags reuse the registered-start owner. The official reference
// documents restricted since 2.1.248 and permission-prompts since 2.1.259:
// https://code.claude.com/docs/en/cli-reference
// claudeColdResumeVersion is the floor the cold resume's reply-only profile
// was checked at: Claude Code 2.1.317, and every later release within major 2.
func claudeColdResumeVersion(version string) bool { return claudeProfileVersionAtLeast(version, 317) }

// copy, when set, is the OA session ID of a copy of the original: its helper
// accepts that session and reads the original chat's inbox.
func coldClaudeResumeArguments(launch *msg.ChatLaunch, cwdKey, version, helper, state, inbox string, budgetCents uint64, copy ...string) ([]string, string, error) {
	refuse := func() ([]string, string, error) { return nil, "", errors.New("cold_resume_capability_unavailable") }
	if launch == nil || !claudeColdResumeVersion(version) || !claudeStartProfileVersion(version) || !claudeSessionProfileVersion(version) || !codexQueueThread.MatchString(launch.Session) || !agentRoutingContext.MatchString(inbox) || budgetCents == 0 {
		return refuse()
	}
	switch launch.Origin {
	case msg.ChatOriginDesktop, msg.ChatOriginCli:
	default:
		return refuse()
	}
	switch launch.Mode {
	case msg.ChatModePlan, msg.ChatModeReadOnly, msg.ChatModeDefault, msg.ChatModeAcceptEdits:
	default:
		return refuse()
	}
	if !filepath.IsAbs(launch.Cwd) || identity.LocalPath(launch.Cwd) != nil || cwdKey == "" || !filepath.IsAbs(helper) || !filepath.IsAbs(state) || nativecli.AgentApplicationBase(dedicatedAgentApplication(helper)) != "oa-claude-integration" {
		return refuse()
	}
	current, err := identity.FileKey(launch.Cwd)
	if err != nil || current != cwdKey {
		return refuse()
	}
	info, err := os.Stat(launch.Cwd)
	if err != nil || !info.IsDir() {
		return refuse()
	}
	// Historical launch facts remain correlation. The vendor must independently
	// populate CLAUDE_CODE_SESSION_ID; never set that environment from this ID.
	correlation := string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: launch.Session, Launch: launch}))
	server := []string{"--state-dir", state, "--inbox-context", inbox, "--messages-reply-only", "--claude-resume-binding", correlation, "--claude-resume-cwd-key", cwdKey}
	if len(copy) > 1 || len(copy) == 1 && !agentSessionUUID.MatchString(copy[0]) {
		return refuse()
	}
	if len(copy) == 1 {
		server = append(server, "--claude-resume-copy", copy[0])
	}
	config, err := json.Marshal(map[string]any{"mcpServers": map[string]any{"OpenAbstractions": map[string]any{"command": helper, "args": server}}})
	if err != nil {
		return refuse()
	}
	names := gateway.MessagesReplyToolNames()
	for i := range names {
		names[i] = "mcp__OpenAbstractions__" + names[i]
	}
	// allowedTools grants prompt-free use, not exclusion. Empty built-in
	// tools plus the strict one-server MCP config and fixed reply view own
	// exclusion. EndConversation otherwise remains when MCP tools exist.
	arguments := []string{"-p", agentStartPrompt(inbox), "--resume", launch.Session, "--permission-mode", "plan", "--permission-prompts", "none", "--restricted", "--strict-mcp-config", "--mcp-config", string(config), "--tools", "", "--allowedTools", strings.Join(names, ","), "--disallowedTools", "EndConversation", "--disable-slash-commands", "--settings", `{"disableAllHooks":true,"syncClaudeAiPlugins":false,"syncClaudeAiSkills":false}`, "--max-budget-usd", fmtBudget(budgetCents), "--output-format", "json"}
	return arguments, launch.Cwd, nil
}
