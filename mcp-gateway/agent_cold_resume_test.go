package main

import (
	"bytes"
	"encoding/json"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	msg "github.com/openabstractions/abstraction-facade/go/abstraction/facade/messages"
	identity "github.com/openabstractions/abstraction-identity"
)

func TestColdClaudeResumeEnvironmentPreservesVendorContextWithoutEnclosingSession(t *testing.T) {
	environment := []string{"PATH=fixture", "CLAUDE_CODE_SESSION_ID=parent", "claude_code_session_id=other", "CLAUDE_CODE_SESSION_ID=", "CLAUDE_CODE_SESSION_ID_SUFFIX=kept", "UNCHANGED=value=with=equals"}
	for _, name := range []string{"CLAUDECODE", "CLAUDE_CODE_HOST_SESSION_ID", "CLAUDE_CODE_CHILD_SESSION", "CLAUDE_PID", "CLAUDE_CODE_MESSAGING_SOCKET", "CLAUDE_CODE_MESSAGING_TOKEN", "CLAUDE_CODE_SESSION_ATTENDED", "CLAUDE_CODE_ENTRYPOINT", "CLAUDE_CODE_EXECPATH", "CLAUDE_AGENT_SDK_FIXTURE", "claude_agent_sdk_unknown", "CLAUDE_CODE_ACCOUNT_UUID", "CLAUDE_CODE_ORGANIZATION_UUID", "CLAUDE_CODE_USER_EMAIL", "claude_code_subscription_type", "CLAUDE_CODE_RATE_LIMIT_TIER", "CLAUDE_CODE_OAUTH_SCOPES"} {
		environment = append(environment, name+"=inert-host-session-fixture")
	}
	before := append([]string(nil), environment...)
	clean := coldClaudeResumeEnvironment(environment)
	want := []string{"PATH=fixture", "CLAUDE_CODE_SESSION_ID_SUFFIX=kept", "UNCHANGED=value=with=equals"}
	if !reflect.DeepEqual(clean, want) || !reflect.DeepEqual(environment, before) {
		t.Fatal(clean, environment)
	}
	clean[0] = "changed"
	if !reflect.DeepEqual(environment, before) {
		t.Fatal("spawn environment shares the selected snapshot", environment)
	}
}

func TestColdClaudeResumeArgumentsAreOriginalSessionAndFixedReplyOnly(t *testing.T) {
	cwd := t.TempDir()
	key, err := identity.FileKey(cwd)
	if err != nil {
		t.Fatal(err)
	}
	launch := &msg.ChatLaunch{Session: "11111111-1111-4111-8111-111111111111", Origin: msg.ChatOriginDesktop, Cwd: cwd, Mode: msg.ChatModeAcceptEdits}
	helper, state := filepath.Join(t.TempDir(), "oa-claude-integration"+attachFixtureExe), t.TempDir()
	args, folder, err := coldClaudeResumeArguments(launch, key, "2.1.317", helper, state, "chat-fixture", 10)
	if runtime.GOOS == "linux" {
		// Linux has no dedicated Claude connector identity in this release.
		if err == nil || err.Error() != "cold_resume_capability_unavailable" || len(args) != 0 || folder != "" {
			t.Fatalf("Linux cold resume escaped the native connector boundary: %v %q %v", args, folder, err)
		}
		return
	}
	if err != nil || folder != cwd {
		t.Fatal(args, folder, err)
	}
	values := map[string]string{}
	flags := map[string]bool{}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--restricted" || arg == "--strict-mcp-config" || arg == "--disable-slash-commands" {
			flags[arg] = true
			continue
		}
		if i+1 >= len(args) {
			t.Fatal(args)
		}
		values[arg] = args[i+1]
		i++
	}
	if values["--resume"] != launch.Session || values["--permission-mode"] != "plan" || values["--tools"] != "" || values["--permission-prompts"] != "none" || values["--disallowedTools"] != "EndConversation" || !flags["--restricted"] || !flags["--strict-mcp-config"] {
		t.Fatal(args)
	}
	if values["--allowedTools"] != "mcp__OpenAbstractions__oa_messages_wait,mcp__OpenAbstractions__oa_messages_reply" {
		t.Fatal(args)
	}
	for _, forbidden := range []string{"--model", "--fork-session", "--session-id", "--add-dir", "--dangerously-skip-permissions"} {
		if _, ok := values[forbidden]; ok || flags[forbidden] {
			t.Fatal("expanded resume", args)
		}
	}
	var config struct {
		Servers map[string]struct {
			Command string
			Args    []string
		} `json:"mcpServers"`
	}
	if err := json.Unmarshal([]byte(values["--mcp-config"]), &config); err != nil {
		t.Fatal(err)
	}
	server := config.Servers["OpenAbstractions"]
	correlation := string(msg.EncodeChatBinding(&msg.ChatBinding{ChatID: launch.Session, Launch: launch}))
	if len(config.Servers) != 1 || server.Command != helper || !reflect.DeepEqual(server.Args, []string{"--state-dir", state, "--inbox-context", "chat-fixture", "--messages-reply-only", "--claude-resume-binding", correlation, "--claude-resume-cwd-key", key}) {
		t.Fatal(config)
	}
	if cold, err := parseClaudeColdBinding(correlation, key, "chat-fixture", true); err != nil || cold.launch != *launch {
		t.Fatal(cold, err)
	}
	if bytes.Contains([]byte(values["--mcp-config"]), []byte("CLAUDE_CODE_SESSION_ID")) {
		t.Fatal("expected session replaced actual vendor environment")
	}
	for _, version := range []string{"2.1.318", "2.2.0"} {
		if _, _, err := coldClaudeResumeArguments(launch, key, version, helper, state, "chat-fixture", 10); err != nil {
			t.Fatal("a later 2.x release refused", version, err)
		}
	}
	for _, version := range []string{"", "2.1.258", "2.1.316", "3.0.0"} {
		if _, _, err := coldClaudeResumeArguments(launch, key, version, helper, state, "chat-fixture", 10); err == nil {
			t.Fatal("unprobed version accepted", version)
		}
	}
	if _, _, err := coldClaudeResumeArguments(launch, "other-cwd", "2.1.317", helper, state, "chat-fixture", 10); err == nil {
		t.Fatal("changed cwd accepted")
	}
	if _, _, err := coldClaudeResumeArguments(nil, key, "2.1.317", helper, state, "chat-fixture", 10); err == nil {
		t.Fatal("missing facts accepted")
	}
	for _, words := range [][2]string{{"future-host", "plan"}, {"desktop", "future-mode"}} {
		raw, err := json.Marshal(map[string]string{"session": launch.Session, "origin": words[0], "mode": words[1], "cwd": cwd})
		if err != nil {
			t.Fatal(err)
		}
		var unknown msg.ChatLaunch
		if err := msg.FromJSON(raw, &unknown); err != nil {
			t.Fatal("open host words refused by codec", err)
		}
		args, folder, err := coldClaudeResumeArguments(&unknown, key, "2.1.317", helper, state, "chat-fixture", 10)
		if err == nil || len(args) != 0 || folder != "" {
			t.Fatal("unknown host fact prepared a launch", args, folder, err)
		}
	}
}

func TestMessagesReplyOnlyFlagKeepsNativeAdmissionRequirements(t *testing.T) {
	flags, _, check, ordinary := commandFlags(&bytes.Buffer{}, "state")
	narrow := messagesReplyOnlyFlag(flags)
	if err := flags.Parse([]string{"--messages-reply-only"}); err != nil || !*narrow {
		t.Fatal(err)
	}
	if validateMessagesReplyOnlyOptions(*narrow, *ordinary, *check, "") == nil || validateMessagesReplyOnlyOptions(true, true, false, "context") == nil || validateMessagesReplyOnlyOptions(true, false, true, "context") == nil {
		t.Fatal("invalid narrow selection accepted")
	}
	if err := validateMessagesReplyOnlyOptions(true, false, false, "context"); err != nil {
		t.Fatal(err)
	}
}
