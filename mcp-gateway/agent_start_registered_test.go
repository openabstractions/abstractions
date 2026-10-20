package main

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openabstractions/abstraction-identity/fileid"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile/startprofiletest"
)

type fixtureAgentStartCLI struct {
	program string
	version string
	closed  bool
}

func (c *fixtureAgentStartCLI) Program() string { return c.program }
func (c *fixtureAgentStartCLI) Version() string {
	if c.version != "" {
		return c.version
	}
	return "2.1.309"
}
func (c *fixtureAgentStartCLI) Check(context.Context) error {
	if c.closed {
		return errors.New("closed")
	}
	return nil
}
func (c *fixtureAgentStartCLI) Close() error { c.closed = true; return nil }

func agentStartFixtureMain() bool {
	if os.Getenv("OA_AGENT_START_REGISTERED_FIXTURE") != "1" {
		return false
	}
	if os.Getenv("OA_AGENT_START_FROZEN_ENV") != "before" || os.Getenv("ANTHROPIC_API_KEY") != "" {
		os.Exit(4)
	}
	if len(os.Args) == 2 && os.Args[1] == "--version" {
		_, _ = io.WriteString(os.Stdout, "2.1.309 (Claude Code)")
		return true
	}
	if len(os.Args) == 2 && os.Args[1] == "--help" {
		_, _ = io.WriteString(os.Stdout, "Usage: claude [options]. Some flags are not shown in --help.")
		return true
	}
	if len(os.Args) == 3 && os.Args[1] == "auth" && os.Args[2] == "status" {
		raw, _ := json.Marshal(map[string]any{"authMethod": "claude.ai", "configDirectory": os.Getenv("OA_AGENT_START_CONFIG_DIR")})
		_, _ = os.Stdout.Write(raw)
		return true
	}
	if len(os.Args) > 2 && os.Args[1] == "-p" {
		if os.Getenv("OA_AGENT_START_REJECT_ARGV") == "1" {
			os.Exit(9)
		}
		inbox := os.Getenv("OA_AGENT_START_EXPECT_INBOX")
		if inbox == "" {
			inbox = "recipe-inbox"
		}
		if os.Args[2] != agentStartPrompt(inbox) || fixtureAgentStartInbox(os.Args[3:]) != inbox {
			os.Exit(4)
		}
		if strings.HasPrefix(inbox, agentAttachPrefix) {
			// A copy of the person's session: its own folder and mode.
			if !fixtureAgentAttachAccess(os.Args[3:], os.Getenv("OA_AGENT_START_EXPECT_MODE")) {
				os.Exit(5)
			}
			if want := os.Getenv("OA_AGENT_START_EXPECT_CWD"); want != "" {
				if cwd, err := os.Getwd(); err != nil || !strings.EqualFold(filepath.Clean(cwd), filepath.Clean(want)) {
					os.Exit(6)
				}
			}
		} else if !fixtureAgentStartRestrictions(os.Args[3:]) {
			os.Exit(4)
		}
		id, _ := fixtureAgentSessionFlag(os.Args[3:])
		mode, source := "--session-id", ""
		for i, arg := range os.Args[3:] {
			if arg == "--resume" {
				mode, source = arg, os.Args[4+i]
			}
		}
		for _, arg := range os.Args[3:] {
			if arg == "--fork-session" {
				mode = "fork"
			}
		}
		if mode != "fork" {
			source = ""
		}
		if marker := os.Getenv("OA_AGENT_START_SESSION_MARKER"); marker != "" {
			raw, _ := json.Marshal(map[string]string{"mode": mode, "session": id, "source": source, "inbox": inbox})
			if os.WriteFile(marker, raw, 0600) != nil {
				os.Exit(4)
			}
		}
		if os.Getenv("OA_AGENT_START_NO_HISTORY") == "1" {
			os.Exit(8)
		}
		if os.Getenv("OA_AGENT_START_WRONG_SESSION") == "1" {
			id = "11111111-1111-4111-8111-111111111111"
		}
		raw, _ := json.Marshal(map[string]any{"type": "result", "subtype": "success", "is_error": false, "session_id": id, "result": "fixture-exit"})
		_, _ = os.Stdout.Write(raw)
		return true
	}
	return false
}

func TestAgentStartRegisteredCustodyExactResumeAndFreshAdmission(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("owned process fixture is Windows-only")
	}
	t.Setenv("OA_AGENT_START_REGISTERED_FIXTURE", "1")
	t.Setenv("OA_AGENT_START_FROZEN_ENV", "before")
	startprofiletest.ClearProviderSelectors(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state, config := t.TempDir(), t.TempDir()
	t.Setenv("OA_AGENT_START_CONFIG_DIR", config)
	marker := filepath.Join(t.TempDir(), "native-session.json")
	t.Setenv("OA_AGENT_START_SESSION_MARKER", marker)
	p := startprofile.Profile{Name: "claude-v1", Context: "recipe-inbox", Folder: t.TempDir(), StateDir: state, CLIFileID: "fixture-locator", Model: "fixture-model", TimeoutMS: 10000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	d := wire.ApplicationDescriptor{Name: "fixture", Program: program, Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments()}}
	read := func(context.Context) (wire.ApplicationDescriptor, error) { return d, nil }
	custody := fixtureAgentCustody(t, p, program)
	admitted := true
	custody.admission = func(context.Context) error {
		if !admitted {
			return errors.New("canonical question ended")
		}
		if _, e := os.Stat(marker); e == nil {
			return errors.New("question answered by completed native run")
		}
		return nil
	}
	selectedCalls := 0
	selectCLI := func(ctx context.Context, _ startprofile.Profile, probe func(context.Context, string) ([]byte, error)) (agentStartCLI, error) {
		selectedCalls++
		if _, e := probe(ctx, program); e != nil {
			return nil, e
		}
		return &fixtureAgentStartCLI{program: program}, nil
	}
	launch := func() agentStartResult {
		t.Helper()
		r, e := resolveRegisteredAgentStart(context.Background(), p, program, read, selectCLI, custody)
		if e != nil {
			t.Fatal(e)
		}
		r.capture = func(context.Context, agentStartExitMetadata) (string, error) { return "submitted_unconfirmed", nil }
		return runAgentStart(context.Background(), p.Name, p.Context, func(context.Context, string, string) (*agentStartRecipe, error) { return r, nil })
	}
	first := launch()
	if first.Outcome != "exited" {
		t.Fatal(first)
	}
	var created map[string]string
	raw, _ := os.ReadFile(marker)
	json.Unmarshal(raw, &created)
	if created["mode"] != "--session-id" || !agentSessionUUID.MatchString(created["session"]) {
		t.Fatal("first native run did not use its own UUID")
	}
	admitted = false
	calls := selectedCalls
	if _, e := resolveRegisteredAgentStart(context.Background(), p, program, read, selectCLI, custody); e == nil || selectedCalls != calls {
		t.Fatal("historical custody supplied fresh launch permission")
	}
	admitted = true
	if e := os.Remove(marker); e != nil {
		t.Fatal(e)
	}
	second := launch()
	if second.Outcome != "exited" {
		t.Fatal(second)
	}
	var resumed map[string]string
	raw, _ = os.ReadFile(marker)
	json.Unmarshal(raw, &resumed)
	if resumed["mode"] != "--resume" || resumed["session"] != created["session"] {
		t.Fatal("later launch did not target the exact owned session")
	}
	if e := os.Remove(marker); e != nil {
		t.Fatal(e)
	}
	custody.binding.EnrollmentRevision = "fixture-enrollment-2"
	stale := launch()
	if stale.Outcome != "failed" || stale.Reason != "session_binding_changed" {
		t.Fatal("stale enrollment selected existing or new history", stale)
	}
	if _, e := os.Stat(marker); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("stale enrollment reached native model run")
	}
	custody.binding.EnrollmentRevision = "fixture-enrollment-1"
	t.Setenv("OA_AGENT_START_CONFIG_DIR", t.TempDir())
	changed := launch()
	if changed.Outcome != "failed" || changed.Reason != "session_configuration_changed" {
		t.Fatal(changed)
	}
	t.Setenv("OA_AGENT_START_CONFIG_DIR", config)
	t.Setenv("OA_AGENT_START_NO_HISTORY", "1")
	missing := launch()
	if missing.Outcome != "failed" {
		t.Fatal("missing history fell back to a new conversation", missing)
	}
	var last map[string]string
	raw, _ = os.ReadFile(marker)
	json.Unmarshal(raw, &last)
	if last["mode"] != "--resume" || last["session"] != created["session"] {
		t.Fatal("missing history changed the target UUID")
	}
}

func TestClaudeStartProfileDocumentedVersionSupport(t *testing.T) {
	for _, version := range []string{"2.1.259", "2.1.309", "2.1.400", "2.2.309", "2.2.0"} {
		if !claudeStartProfileVersion(version) {
			t.Fatal("documented profile refused", version)
		}
	}
	for _, version := range []string{"", "2.1.258", "2.1.0259", "2.1.x", "2.0.400", "2.02.0", "3.1.309", "2.1.309.extra"} {
		if claudeStartProfileVersion(version) {
			t.Fatal("unsupported profile accepted", version)
		}
	}
	for _, version := range []string{"2.1.268", "2.1.309"} {
		if !claudeSessionProfileVersion(version) {
			t.Fatal("documented configuration metadata unavailable", version)
		}
	}
	for _, version := range []string{"2.1.259", "2.1.267", "2.1.0268", "3.1.309"} {
		if claudeSessionProfileVersion(version) {
			t.Fatal("older/unknown family admitted custodial startup", version)
		}
	}
}

func TestAgentStartRegisteredRejectsForgedCustodyBeforeVendor(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("native identity fixture")
	}
	startprofiletest.ClearProviderSelectors(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	p := startprofile.Profile{Name: "claude-v1", Context: "recipe-inbox", Folder: folder, StateDir: folder, CLIFileID: "fixture", Model: "fixture", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 1}
	d := wire.ApplicationDescriptor{Name: "fixture", Program: program, Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments()}}
	for _, field := range []string{"missing", "account", "program", "recipe", "cold_missing", "cold_mismatch", "cold_malformed"} {
		t.Run(field, func(t *testing.T) {
			custody := fixtureAgentCustody(t, p, program)
			switch field {
			case "missing":
				custody.binding = startprofile.CustodyBinding{}
			case "account":
				custody.binding.Account = "forged-account"
			case "program":
				custody.binding.ProgramKey = "forged-program"
			case "recipe":
				custody.binding.RecipeDigest = strings.Repeat("b", 64)
			case "cold_missing":
				custody.target = "chat-retained"
			case "cold_mismatch":
				custody.target = "chat-retained"
				custody.cold = &startprofile.ColdResume{Context: "chat-other"}
			case "cold_malformed":
				custody.target = "chat-retained"
				custody.cold = &startprofile.ColdResume{Context: custody.target, Binding: "{}", CwdKey: "forged", Revision: strings.Repeat("a", 64)}
			}
			called := false
			_, err := resolveRegisteredAgentStart(context.Background(), p, program, func(context.Context) (wire.ApplicationDescriptor, error) { return d, nil }, func(context.Context, startprofile.Profile, func(context.Context, string) ([]byte, error)) (agentStartCLI, error) {
				called = true
				return nil, errors.New("must not run")
			}, custody)
			if err == nil || called {
				t.Fatal("forged custody reached vendor boundary", err)
			}
		})
	}
}

func fixtureAgentStartRestrictions(args []string) bool {
	values, flags := map[string]string{}, map[string]bool{}
	for i, arg := range args {
		flags[arg] = true
		if i+1 < len(args) {
			values[arg] = args[i+1]
		}
	}
	var settings map[string]bool
	_, sessionOK := fixtureAgentSessionFlag(args)
	return sessionOK && flags["--tools"] && values["--tools"] == "" &&
		values["--permission-mode"] == "dontAsk" && values["--permission-prompts"] == "none" &&
		flags["--restricted"] && flags["--strict-mcp-config"] && flags["--disable-slash-commands"] &&
		!flags["--bare"] && !flags["--safe-mode"] &&
		values["--allowedTools"] == messagesReceiverAllowedTools() &&
		json.Unmarshal([]byte(values["--settings"]), &settings) == nil && reflect.DeepEqual(settings, map[string]bool{"disableAllHooks": true, "syncClaudeAiPlugins": false, "syncClaudeAiSkills": false})
}

// fixtureAgentAttachAccess accepts a copy run with exactly the session's
// recorded mode, approvals failing closed, the session's own MCP servers with
// OA's receiver in place of OA's own connection and pre-allowed, and none of
// the recipe's tool or setting restrictions.
func fixtureAgentAttachAccess(args []string, mode string) bool {
	if mode == "" {
		mode = "default"
	}
	values, flags := map[string]string{}, map[string]bool{}
	for i, arg := range args {
		flags[arg] = true
		if i+1 < len(args) {
			values[arg] = args[i+1]
		}
	}
	_, sessionOK := fixtureAgentSessionFlag(args)
	for _, raised := range []string{"--dangerously-skip-permissions", "--allow-dangerously-skip-permissions", "--tools", "--restricted", "--settings", "--disable-slash-commands", "--bare", "--add-dir", "--strict-mcp-config"} {
		if flags[raised] {
			return false
		}
	}
	var config struct {
		Servers map[string]json.RawMessage `json:"mcpServers"`
	}
	if json.Unmarshal([]byte(values["--mcp-config"]), &config) != nil || len(config.Servers) != 1 {
		return false
	}
	_, receiver := config.Servers["OpenAbstractions"]
	return sessionOK && receiver && values["--permission-mode"] == mode && values["--permission-prompts"] == "none" &&
		values["--allowedTools"] == messagesReceiverAllowedTools()
}

func fixtureAgentSessionFlag(args []string) (string, bool) {
	if id, fork, ok := fixtureAgentForkFlags(args); fork {
		return id, ok
	}
	id, count := "", 0
	for i, arg := range args {
		if arg == "--continue" || arg == "-c" || arg == "--fork-session" {
			return "", false
		}
		if arg == "--session-id" || arg == "--resume" {
			count++
			if i+1 < len(args) {
				id = args[i+1]
			}
		}
	}
	return id, count == 1 && agentSessionUUID.MatchString(id)
}

func fixtureAgentCustody(t *testing.T, p startprofile.Profile, program string) agentStartCustody {
	t.Helper()
	account, err := agentStartAccount()
	if err != nil {
		t.Fatal(err)
	}
	image, err := fileid.Resolve(context.Background(), fileid.Self, program, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	return agentStartCustody{binding: startprofile.CustodyBinding{Account: account, ProgramKey: image.Key(), EnrollmentRevision: "fixture-enrollment-1", RecipeDigest: p.Fingerprint(program)}, admission: func(context.Context) error { return nil }}
}

func TestAgentStartSubscriptionEnvironmentRefusesOverrides(t *testing.T) {
	for _, name := range []string{"ANTHROPIC_API_KEY", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_CUSTOM_HEADERS", "CLAUDE_CODE_USE_BEDROCK", "CLAUDE_CODE_USE_VERTEX", "CLAUDE_CODE_USE_FOUNDRY", "CLAUDE_CODE_USE_ANTHROPIC_AWS", "CLAUDE_CODE_USE_MANTLE", "ANTHROPIC_BEDROCK_BASE_URL", "ANTHROPIC_VERTEX_BASE_URL", "ANTHROPIC_FOUNDRY_BASE_URL", "ANTHROPIC_PROFILE", "ANTHROPIC_IDENTITY_TOKEN_FILE", "ANTHROPIC_FEDERATION_RULE_ID", "CLAUDE_CODE_SIMPLE", "CLAUDE_CODE_PROVIDER_MANAGED_BY_HOST"} {
		t.Run(name, func(t *testing.T) {
			env := []string{"HOME=native-home", name + "=private-value"}
			before := append([]string(nil), env...)
			_, err := agentStartSubscriptionEnvironment(env)
			var refusal *agentStartEnvironmentRefusal
			if !errors.As(err, &refusal) || !reflect.DeepEqual(refusal.Names, []string{name}) || strings.Contains(err.Error(), "private-value") || !reflect.DeepEqual(env, before) {
				t.Fatal("override refusal did not preserve context and redact values")
			}
		})
	}
	env := []string{"HOME=native-home", "CLAUDE_CONFIG_DIR=native-config", "CLAUDE_CODE_OAUTH_TOKEN=native-subscription", "HTTP_PROXY=native-proxy", "HTTPS_PROXY=native-proxy", "NO_PROXY=native-direct", "AWS_ACCESS_KEY_ID=inactive-provider", "ANTHROPIC_API_KEY="}
	frozen, err := agentStartSubscriptionEnvironment(env)
	if err != nil || !reflect.DeepEqual(frozen, env) {
		t.Fatal("native subscription context changed")
	}
	env[0] = "HOME=changed"
	if frozen[0] != "HOME=native-home" {
		t.Fatal("environment snapshot aliases caller")
	}
}

func TestAgentStartSubscriptionEnvironmentRefusesHostAuthChannels(t *testing.T) {
	for _, name := range []string{
		"CLAUDE_CODE_SDK_HAS_HOST_AUTH_REFRESH", "CLAUDE_CODE_SDK_HAS_OAUTH_REFRESH", "CLAUDE_CODE_SDK_HAS_SESSIONS_OAUTH_REFRESH",
		"CLAUDE_CODE_HOST_AUTH_ENV_VAR", "CLAUDE_CODE_HOST_CREDS_FILE", "CLAUDE_CODE_OAUTH_TOKEN_FILE_DESCRIPTOR",
		"CLAUDE_CODE_WEBSOCKET_AUTH_FILE_DESCRIPTOR", "CLAUDE_CODE_SESSION_ACCESS_TOKEN", "CLAUDE_CODE_SESSIONS_OAUTH_TOKEN", "CLAUDE_SESSION_INGRESS_TOKEN_FILE",
		"CLAUDE_BG_AUTH_SNAPSHOT_PATH", "CLAUDE_CODE_GATEWAY_TOKEN_FILE_DESCRIPTOR", "CLAUDE_CODE_API_KEY_FILE_DESCRIPTOR", "CCR_AGENT_PROXY_TOKEN_FILE_DESCRIPTOR",
	} {
		t.Run(name, func(t *testing.T) {
			for _, spelling := range []string{name, strings.ToLower(name)} {
				_, err := agentStartSubscriptionEnvironment([]string{spelling + "=private-host-auth"})
				var refusal *agentStartEnvironmentRefusal
				if !errors.As(err, &refusal) || !reflect.DeepEqual(refusal.Names, []string{name}) || strings.Contains(err.Error(), "private-host-auth") {
					t.Fatal("host auth channel was not refused with names only", err)
				}
			}
			if _, err := agentStartSubscriptionEnvironment([]string{name + "="}); err != nil {
				t.Fatal("empty host auth channel", err)
			}
		})
	}
}

func TestAgentStartRegisteredEnvironmentRefusalPrecedesVendorWork(t *testing.T) {
	for _, name := range startprofile.ProviderSelectors() {
		t.Run(name, func(t *testing.T) {
			t.Setenv(name, "private-value")
			called := false
			read := func(context.Context) (wire.ApplicationDescriptor, error) {
				called = true
				return wire.ApplicationDescriptor{}, nil
			}
			for _, profile := range []string{"claude-v1", "codex-v1"} {
				_, err := resolveRegisteredAgentStart(context.Background(), startprofile.Profile{Name: profile}, "", read, nil, agentStartCustody{})
				var refusal *agentStartEnvironmentRefusal
				if !errors.As(err, &refusal) || called || os.Getenv(name) != "private-value" || strings.Contains(err.Error(), "private-value") {
					t.Fatal("override reached registration/vendor work or changed parent environment", profile)
				}
			}
		})
	}
}

func TestAgentStartRegisteredRecipeRevalidation(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("owned process supervision currently Windows-only")
	}
	t.Setenv("OA_AGENT_START_REGISTERED_FIXTURE", "1")
	t.Setenv("OA_AGENT_START_FROZEN_ENV", "before")
	startprofiletest.ClearProviderSelectors(t)
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	folder := t.TempDir()
	t.Setenv("OA_AGENT_START_CONFIG_DIR", folder)
	p := startprofile.Profile{Name: "claude-v1", Context: "recipe-inbox", Folder: folder, StateDir: folder, CLIFileID: "fixture-locator", Model: "fixture-model", TimeoutMS: 10000, OutputBytes: 4096, MemoryBytes: 256 << 20, BudgetCents: 100}
	d := wire.ApplicationDescriptor{Name: "fixture", Program: program, Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments()}}
	read := func(context.Context) (wire.ApplicationDescriptor, error) { return d, nil }
	selected := &fixtureAgentStartCLI{program: program}
	selectCLI := func(ctx context.Context, _ startprofile.Profile, probe func(context.Context, string) ([]byte, error)) (agentStartCLI, error) {
		if _, err := probe(ctx, program); err != nil {
			return nil, err
		}
		t.Setenv("OA_AGENT_START_FROZEN_ENV", "after")
		t.Setenv("ANTHROPIC_API_KEY", "later-parent-override")
		return selected, nil
	}
	custody := fixtureAgentCustody(t, p, program)
	r, err := resolveRegisteredAgentStart(context.Background(), p, program, read, selectCLI, custody)
	if err != nil {
		t.Fatal(err)
	}
	r.capture = func(context.Context, agentStartExitMetadata) (string, error) { return "submitted_unconfirmed", nil }
	result := runAgentStart(context.Background(), p.Name, p.Context, func(context.Context, string, string) (*agentStartRecipe, error) { return r, nil })
	if result.Outcome != "exited" || result.Metadata == nil || result.Metadata.StdoutBytes == 0 || !selected.closed {
		t.Fatal(result, selected.closed)
	}
	// A supported installed version may still reject the actual argv. Preserve
	// the owned execution failure rather than retrying with weaker restrictions.
	t.Setenv("OA_AGENT_START_REJECT_ARGV", "1")
	t.Setenv("OA_AGENT_START_FROZEN_ENV", "before")
	startprofiletest.ClearProviderSelectors(t)
	selected = &fixtureAgentStartCLI{program: program}
	rejected, err := resolveRegisteredAgentStart(context.Background(), p, program, read, selectCLI, custody)
	if err != nil {
		t.Fatal(err)
	}
	rejected.capture = func(context.Context, agentStartExitMetadata) (string, error) { return "submitted_unconfirmed", nil }
	result = runAgentStart(context.Background(), p.Name, p.Context, func(context.Context, string, string) (*agentStartRecipe, error) { return rejected, nil })
	if result.Outcome != "failed" || result.Reason != "execution" || !selected.closed {
		t.Fatalf("argv refusal changed: %+v", result)
	}
	startprofiletest.ClearProviderSelectors(t)
	selected = &fixtureAgentStartCLI{program: program, version: "2.1.258"}
	_, err = resolveRegisteredAgentStart(context.Background(), p, program, read, func(context.Context, startprofile.Profile, func(context.Context, string) ([]byte, error)) (agentStartCLI, error) {
		return selected, nil
	}, custody)
	if err == nil || err.Error() != "restriction_unsupported" || !selected.closed {
		t.Fatal("unsupported native version reached command preflight", err, selected.closed)
	}
	startprofiletest.ClearProviderSelectors(t)
	t.Setenv("OA_AGENT_START_REJECT_ARGV", "")
	t.Setenv("OA_AGENT_START_FROZEN_ENV", "before")
	t.Setenv("OA_AGENT_START_CONFIG_DIR", "")
	selected = &fixtureAgentStartCLI{program: program}
	if _, err := resolveRegisteredAgentStart(context.Background(), p, program, read, selectCLI, custody); agentSessionReason(err) != "native_configuration_unavailable" || !selected.closed {
		t.Fatal("missing native configuration identity reached a model run", err, selected.closed)
	}
	t.Setenv("OA_AGENT_START_CONFIG_DIR", folder)
	// A different registered executable never reaches capability or model work.
	startprofiletest.ClearProviderSelectors(t)
	d.Program = filepath.Join(folder, "missing-helper.exe")
	if _, err := resolveRegisteredAgentStart(context.Background(), p, program, read, selectCLI, custody); err == nil {
		t.Fatal("wrong registered image accepted")
	}
}
