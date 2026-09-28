package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	routerwire "github.com/openabstractions/abstraction-router/go/abstraction/router"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

const opencodeConfigureAnnouncerArg = "-oa-opencode-configure-announcer"

func init() {
	if len(os.Args) >= 3 && os.Args[1] == opencodeConfigureAnnouncerArg {
		os.Exit(runOpenCodeConfigureAnnouncer(os.Args[2], os.Args[3:]))
	}
}

// runOpenCodeConfigureAnnouncer runs opencode configure as a program distinct
// from the command line that started the isolated runtime, so the runtime's
// rights gate on abstraction.router/inventory.read applies to it exactly as
// it would to a real caller such as OpenCode or an MCP gateway
// (copyTestBinary: "the command line (this process) never declares its own
// program"). It prints one line the parent test greps.
func runOpenCodeConfigureAnnouncer(endpoint string, rest []string) int {
	args := append([]string{"--endpoint", endpoint}, rest...)
	var out, diagnostics bytes.Buffer
	err := opencodeConfigure(args, &out, &diagnostics)
	if err != nil {
		var exit *exitError
		if errors.As(err, &exit) {
			fmt.Printf("ERR:%d:%s\n", exit.code, exit.err.Error())
			return exit.code
		}
		fmt.Printf("ERR:1:%s\n", err.Error())
		return 1
	}
	fmt.Printf("OUT:%s", out.String())
	return 0
}

func TestOpenCodeConfigureChoosesServableChatModelsDeterministically(t *testing.T) {
	families := []routerwire.Family{
		{Family: "alpha", Names: []routerwire.Alias{
			{Name: "b-alias", Servable: true, Profiles: nil},
			{Name: "a-alias", Servable: true, Profiles: []string{"chat"}},
		}},
		{Family: "beta", Names: []routerwire.Alias{
			{Name: "not-servable", Servable: false, Profiles: []string{"chat"}},
		}},
		{Family: "gamma", Names: []routerwire.Alias{
			{Name: "embed-only", Servable: true, Profiles: []string{"embed"}},
		}},
		{Family: "delta", Names: []routerwire.Alias{
			{Name: "delta-alias", Servable: true, Profiles: []string{"chat", "embed"}},
		}},
	}
	models := openCodeServableChatModels(families)
	if len(models) != 2 {
		t.Fatalf("want 2 servable chat families, got %d: %+v", len(models), models)
	}
	alpha, ok := models["alpha"]
	if !ok || alpha.Name != "a-alias" {
		t.Fatalf("alpha: an empty Profiles alias is chat-eligible and sorts before b-alias, got %+v ok=%v", alpha, ok)
	}
	if alpha.Limit.Context != openCodeDefaultContext || alpha.Limit.Output != openCodeDefaultOutput || !alpha.ToolCall {
		t.Fatalf("alpha model defaults: %+v", alpha)
	}
	delta, ok := models["delta"]
	if !ok || delta.Name != "delta-alias" {
		t.Fatalf("delta: %+v ok=%v", delta, ok)
	}
	if _, ok := models["beta"]; ok {
		t.Fatalf("beta: an unservable alias must not appear")
	}
	if _, ok := models["gamma"]; ok {
		t.Fatalf("gamma: an alias serving only embed must not appear")
	}
}

// A servable chat alias with a positive router.thrift context_length uses it
// as the written limit; one with none, or a zero-length host report, falls
// back to the documented default.
func TestOpenCodeConfigureUsesTheHostsReportedContextLength(t *testing.T) {
	families := []routerwire.Family{
		{Family: "reported", Names: []routerwire.Alias{
			{Name: "reported-alias", Servable: true, Profiles: []string{"chat"}, ContextLength: 131072},
		}},
		{Family: "unreported", Names: []routerwire.Alias{
			{Name: "unreported-alias", Servable: true, Profiles: []string{"chat"}},
		}},
	}
	models := openCodeServableChatModels(families)
	reported, ok := models["reported"]
	if !ok || reported.Limit.Context != 131072 {
		t.Fatalf("reported: %+v ok=%v, want context 131072", reported, ok)
	}
	unreported, ok := models["unreported"]
	if !ok || unreported.Limit.Context != openCodeDefaultContext {
		t.Fatalf("unreported: %+v ok=%v, want the default context %d", unreported, ok, openCodeDefaultContext)
	}
}

func testModels() map[string]openCodeModel {
	return map[string]openCodeModel{
		"llama3.1:8b": {Name: "llama3.1", Limit: openCodeLimit{Context: openCodeDefaultContext, Output: openCodeDefaultOutput}, ToolCall: true},
	}
}

func TestOpenCodeConfigureWritesIntoAnEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	app := opencodeApplication{command: "opencode configure", path: path, providerID: "openabstractions", scope: "local",
		guarantee: "abstraction.inference/local-only@1", models: testModels()}

	var out bytes.Buffer
	if err := openCodeApply(&out, io.Discard, app); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(out.String(), "wrote provider") {
		t.Fatalf("confirmation: %q", out.String())
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read written file: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("written file is not JSON: %v\n%s", err, raw)
	}
	if doc["model"] != "openabstractions/llama3.1:8b" {
		t.Fatalf("default model: %v", doc["model"])
	}
	providers, ok := doc["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider key: %v", doc["provider"])
	}
	mine, ok := providers["openabstractions"].(map[string]any)
	if !ok {
		t.Fatalf("provider.openabstractions: %v", providers)
	}
	if mine["npm"] != openCodeNPM || mine["name"] != openCodeDisplayName {
		t.Fatalf("provider identity: %+v", mine)
	}
	options, ok := mine["options"].(map[string]any)
	if !ok || options["scope"] != "local" {
		t.Fatalf("provider options: %v", mine["options"])
	}
	models, ok := mine["models"].(map[string]any)
	if !ok {
		t.Fatalf("provider models: %v", mine["models"])
	}
	entry, ok := models["llama3.1:8b"].(map[string]any)
	if !ok || entry["name"] != "llama3.1" || entry["tool_call"] != true {
		t.Fatalf("model entry: %v", models["llama3.1:8b"])
	}
	limit, ok := entry["limit"].(map[string]any)
	if !ok || limit["context"] != float64(openCodeDefaultContext) || limit["output"] != float64(openCodeDefaultOutput) {
		t.Fatalf("model limit: %v", entry["limit"])
	}

	// A second apply over the same file is idempotent: it changes nothing.
	var again bytes.Buffer
	if err := openCodeApply(&again, io.Discard, app); err != nil {
		t.Fatalf("second apply: %v", err)
	}
	if !strings.Contains(again.String(), "unchanged") {
		t.Fatalf("second apply should report unchanged: %q", again.String())
	}
	raw2, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, raw2) {
		t.Fatalf("second apply changed the file: %v\nfirst:\n%s\nsecond:\n%s", err, raw, raw2)
	}
}

func TestOpenCodeConfigureMergesIntoExistingProvidersAndMCPKeys(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	existing := `{
  "provider": {
    "anthropic": {"name": "Anthropic", "npm": "@ai-sdk/anthropic", "options": {"apiKey": "{env:ANTHROPIC_API_KEY}"}}
  },
  "mcp": {
    "playwright": {"type": "local", "command": ["npx", "@playwright/mcp"]}
  },
  "model": "anthropic/claude-sonnet-4-20250514"
}`
	if err := os.WriteFile(path, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	app := opencodeApplication{command: "opencode configure", path: path, providerID: "openabstractions", scope: "local",
		guarantee: "abstraction.inference/local-only@1", models: testModels()}
	var out bytes.Buffer
	if err := openCodeApply(&out, io.Discard, app); err != nil {
		t.Fatalf("apply: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("merged file is not JSON: %v\n%s", err, raw)
	}
	// The pre-existing default model is untouched: configure only supplies
	// one when the document names none.
	if doc["model"] != "anthropic/claude-sonnet-4-20250514" {
		t.Fatalf("existing default model was overwritten: %v", doc["model"])
	}
	mcp, ok := doc["mcp"].(map[string]any)
	if !ok || mcp["playwright"] == nil {
		t.Fatalf("mcp key was not preserved: %v", doc["mcp"])
	}
	providers, ok := doc["provider"].(map[string]any)
	if !ok {
		t.Fatalf("provider key: %v", doc["provider"])
	}
	if providers["anthropic"] == nil {
		t.Fatalf("the existing anthropic provider was dropped: %v", providers)
	}
	if providers["openabstractions"] == nil {
		t.Fatalf("the openabstractions provider was not added: %v", providers)
	}
}

func TestOpenCodeConfigureDryRunPrintsTheBlockWithoutWriting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	app := opencodeApplication{command: "opencode configure", path: path, providerID: "openabstractions", scope: "remote",
		guarantee: "abstraction.inference/hosted-allowed@1", dryRun: true, models: testModels()}
	var out bytes.Buffer
	if err := openCodeApply(&out, io.Discard, app); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(out.String(), `"provider"`) || !strings.Contains(out.String(), `"openabstractions"`) ||
		!strings.Contains(out.String(), `"llama3.1:8b"`) || !strings.Contains(out.String(), "hosted-allowed") {
		t.Fatalf("dry-run block: %s", out.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("--dry-run must not write %s: %v", path, err)
	}
}

func TestOpenCodeConfigureWritesNothingAndSaysWhyWithNoServableModel(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")
	app := opencodeApplication{command: "opencode configure", path: path, providerID: "openabstractions", scope: "local",
		guarantee: "abstraction.inference/local-only@1", models: map[string]openCodeModel{}}
	var out bytes.Buffer
	if err := openCodeApply(&out, io.Discard, app); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if !strings.Contains(out.String(), "no servable chat model") {
		t.Fatalf("explanation: %q", out.String())
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("no servable model must not write %s: %v", path, err)
	}
}

func TestOpenCodeConfigureRefusesJSONCByName(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.jsonc")
	var out, diagnostics bytes.Buffer
	err := opencodeConfigure([]string{"--config", path}, &out, &diagnostics)
	exit := assertExit(t, err, exitRefusedCall, "opencode configure --config *.jsonc")
	if !strings.Contains(exit.err.Error(), "JSONC") && !strings.Contains(exit.err.Error(), "jsonc") {
		t.Fatalf("refusal must name JSONC: %v", exit.err)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a refused .jsonc file must not be written: %v", statErr)
	}
}

func TestOpenCodeConfigureUsageAndHelp(t *testing.T) {
	var help bytes.Buffer
	if err := opencodeCommand([]string{"--help"}, &help, io.Discard); err != nil ||
		!strings.Contains(help.String(), "configure") || !strings.Contains(help.String(), "servable") {
		t.Fatalf("help: %v %q", err, help.String())
	}
	// No arguments at all is a missing-command mistake (item 7), not a help
	// request: it names what is missing and exits usage, not help's exit 0.
	var diagnostics bytes.Buffer
	if err := opencodeCommand(nil, io.Discard, &diagnostics); err == nil {
		t.Fatal("no-argument opencode accepted")
	} else {
		assertExit(t, err, exitUsage, "opencode with no arguments")
	}
	if !strings.Contains(diagnostics.String(), "a command is required") {
		t.Fatalf("no-argument opencode: diagnostics %q does not name what is missing", diagnostics.String())
	}

	err := opencodeCommand([]string{"unknown"}, io.Discard, io.Discard)
	assertExit(t, err, exitUsage, "opencode unknown")

	for _, args := range [][]string{
		{"configure", "extra"},
		{"configure", "--scope", "bogus"},
		{"configure", "--provider-id", "Not Valid!"},
		{"configure", "--timeout", "-1s"},
	} {
		err := opencodeCommand(args, io.Discard, io.Discard)
		assertExit(t, err, exitUsage, strings.Join(args, " "))
	}
}

// TestOpenCodeConfigureRightsRefusalNamesThePanel runs configure as a program
// distinct from the runtime's command line. Refused with no rule at all, its
// typed refusal names both the missing right and the Panel; granted
// abstraction.router/inventory.read, the same call succeeds and, since the
// isolated runtime declares no local inference host, reports why it wrote
// nothing rather than guessing a model.
func TestOpenCodeConfigureRightsRefusalNamesThePanel(t *testing.T) {
	options := gatedRuntime(t)
	bin := copyTestBinary(t, "oa-opencode-configure-fixture")
	dir := t.TempDir()
	path := filepath.Join(dir, "opencode.json")

	run := func() (string, int) {
		t.Helper()
		cmd := exec.Command(bin, opencodeConfigureAnnouncerArg, options.endpoint, "--config", path, "--timeout", "10s")
		output, err := cmd.CombinedOutput()
		code := 0
		if exit, ok := err.(*exec.ExitError); ok {
			code = exit.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(output), code
	}

	refused, code := run()
	if code != exitRefusedCall || !strings.Contains(refused, "abstraction.router/inventory.read") || !strings.Contains(refused, "Panel") {
		t.Fatalf("refusal without a grant: exit %d %q", code, refused)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("a rights refusal must not write %s: %v", path, statErr)
	}

	reply, err := runRightsCommand(t, "grant", "--endpoint", options.endpoint, "--program", bin,
		"--action", routerservice.ActionInventory, "--resource", routerservice.ResourceInventory)
	if err != nil || reply.Outcome != "applied" {
		t.Fatalf("grant %s: %v %+v", routerservice.ActionInventory, err, reply)
	}
	allowed, code := run()
	if code != 0 || !strings.Contains(allowed, "no servable chat model") {
		t.Fatalf("after a grant, no local hosts still explains itself: exit %d %q", code, allowed)
	}
	if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("no servable model must not write %s: %v", path, statErr)
	}
}
