package opencode_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	askclient "github.com/openabstractions/abstraction-asks/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/grants"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	rights "github.com/openabstractions/abstraction-rights/go/client"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

func TestOpenCodeAsksImageEligibilityGuard(t *testing.T) {
	program := os.Getenv("OA_OPENCODE_BINARY")
	if program == "" {
		t.Skip("set the verified OA_OPENCODE_BINARY for the native image guard probe")
	}
	account := currentAccount(t)
	facts := identity.ClassifyProgram(program, identity.Code{})
	_, err := (asks.AnswererRule{Account: account}).Answerer(account, program, asks.Publisher{})
	t.Logf("pinned native image account=%s program=%s shared_interpreter=%t runs_foreign_code=%t evidence=%s owning_answerer_guard=%v", account, program, facts.SharedInterpreter, facts.RunsForeignCode, facts.ForeignCode, err)
	if !facts.RunsForeignCode || !errors.Is(err, asks.ErrAnswererForbidden) {
		t.Fatalf("ASK-R7 foreign-image refusal is not established: %+v %v", facts, err)
	}
}

func TestOpenCodeProviderFixtureLifetime(t *testing.T) {
	base := []string{"fixture.exe", openCodeProviderFixtureArg, "endpoint", "state", "image", "pids", "runtime.exe"}
	for _, test := range []struct {
		name string
		args []string
		want time.Duration
	}{
		{"existing argv", base, 120 * time.Second},
		{"minimum", append(append([]string{}, base...), "1"), time.Second},
		{"real render", append(append([]string{}, base...), "1500"), 1500 * time.Second},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := openCodeProviderFixtureLifetime(test.args)
			if err != nil || got != test.want {
				t.Fatalf("got %v, %v; want %v", got, err, test.want)
			}
		})
	}
}

func TestOpenCodeProviderFixtureRefusesLifetimeBeforeEffects(t *testing.T) {
	state, pids := t.TempDir(), t.TempDir()
	base := []string{"fixture.exe", openCodeProviderFixtureArg, "invalid endpoint", state, "image", pids, "runtime.exe"}
	for _, value := range []string{"0", "-1", "1501", "", "1.5", "bad", " 120", "999999999999999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			args := append(append([]string{}, base...), value)
			if code := runOpenCodeProviderFixtureArgs(args); code != 2 {
				t.Fatalf("invalid lifetime returned %d", code)
			}
		})
	}
	for _, args := range [][]string{nil, base[:6], append(append([]string{}, base...), "120", "extra")} {
		if code := runOpenCodeProviderFixtureArgs(args); code != 2 {
			t.Fatalf("invalid argument count returned %d", code)
		}
	}
	for _, dir := range []string{state, pids} {
		entries, err := os.ReadDir(dir)
		if err != nil || len(entries) != 0 {
			t.Fatalf("invalid lifetime changed fixture directory: %v, %v", entries, err)
		}
	}
}

func TestOpenCodeProviderNeverFallsBackToNodeAddonUnderBun(t *testing.T) {
	if _, err := findOpenCodeProviderSources(adopterRoot(t)); err != nil {
		if !privateWorkspace(t) && strings.Contains(err.Error(), "generated JavaScript packages absent") {
			// Public charter jobs need the three layer checkouts beside it.
			t.Skip("OpenCode provider integration requires facade, inference and identity JavaScript checkouts")
		}
		t.Fatal(err)
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is unavailable for the staged JavaScript provider check")
	}
	provider := stageOpenCodeProvider(t)
	fixtureDir := t.TempDir()
	addon := filepath.Join(fixtureDir, "node-addon-sentinel.cjs")
	if err := os.WriteFile(addon, []byte(`
require('node:fs').writeFileSync(process.env.OA_NODE_ADDON_MARKER, 'loaded');
module.exports = {};
`), 0o600); err != nil {
		t.Fatal(err)
	}

	run := func(t *testing.T, bun bool, library *string) (string, bool) {
		t.Helper()
		marker := filepath.Join(t.TempDir(), "node-addon-loaded")
		script := fmt.Sprintf(`
if (%t) globalThis.Bun = {};
const {createOpenAbstractions} = await import(%q);
const model = createOpenAbstractions({runtimeEndpoint: "controlled-test-endpoint"}).languageModel("fixture-model");
try {
  await model.doGenerate({prompt: []});
  console.log(JSON.stringify({error: "request unexpectedly succeeded"}));
} catch (error) {
  // The provider shows a safe sentence and keeps the underlying failure as cause.
  console.log(JSON.stringify({error: String(error) + " | cause: " + String(error?.cause ?? "")}));
}
`, bun, fileURL(provider))
		cmd := exec.Command(node, "--input-type=module", "--eval", script)
		cmd.Env = append(environmentWithout(os.Environ(), "ABSTRACTION_IPC_LIBRARY", "ABSTRACTION_IPC_NODE"),
			"ABSTRACTION_IPC_NODE="+addon, "OA_NODE_ADDON_MARKER="+marker)
		if library != nil {
			cmd.Env = append(cmd.Env, "ABSTRACTION_IPC_LIBRARY="+*library)
		}
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("staged provider: %v\n%s", err, output)
		}
		var result struct {
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(string(output))), &result); err != nil {
			t.Fatalf("staged provider output %q: %v", output, err)
		}
		_, markerErr := os.Stat(marker)
		return result.Error, markerErr == nil
	}

	for _, fixture := range []struct {
		name    string
		library *string
	}{
		{name: "missing-library"},
		{name: "relative-library", library: func() *string { value := "relative-library.dll"; return &value }()},
	} {
		t.Run("bun/"+fixture.name, func(t *testing.T) {
			message, loaded := run(t, true, fixture.library)
			if loaded {
				t.Fatalf("Bun loaded the Node addon; error = %q", message)
			}
			if !strings.Contains(message, "runtime_unavailable") {
				t.Fatalf("Bun configuration failure = %q", message)
			}
		})
	}
	t.Run("node", func(t *testing.T) {
		message, loaded := run(t, false, nil)
		if !loaded {
			t.Fatalf("Node did not retain the addon connector; error = %q", message)
		}
	})
}

func TestOpenCodeStartupPluginRefreshesModelsFromRouterInventory(t *testing.T) {
	opencode, library := os.Getenv("OA_OPENCODE_BINARY"), os.Getenv("OA_IPC_LIBRARY")
	if opencode == "" || library == "" {
		t.Skip("set OA_OPENCODE_BINARY and OA_IPC_LIBRARY for the OpenCode startup inventory proof")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	upstream, _ := fakeOllama(t)
	options, _ := isolatedRuntime(t)
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(`{"local":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	startInferenceRuntime(t, options)
	endpointArgs := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	if out, err := runInference(t, options.program, append([]string{"host", "add", "ollama", "--base", upstream.URL}, endpointArgs...)...); err != nil {
		t.Fatalf("register fake local model host: %v\n%s", err, out)
	}

	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operator, err := client.New(options.endpoint).ResolveRightsOperator(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("list rights policy: %+v %v", page, err)
	}
	rule := rights.PolicyRule{Subject: rights.Subject{Account: account.Uid, Program: filepath.Clean(opencode)},
		Action: routerservice.ActionInventory, Resource: routerservice.ResourceInventory, Permit: true}
	if edit, err := operator.SetRuleContext(ctx, page.Revision, rule); err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("grant inventory visibility to OpenCode: %+v %v", edit, err)
	}

	providerIndex := stageOpenCodeProvider(t)
	providerDir := filepath.Dir(providerIndex)
	project := t.TempDir()
	configPath := filepath.Join(project, "opencode.json")
	// The block adopters/opencode/configure.js writes, holding one managed
	// model the router no longer offers.
	oldModel := map[string]any{"name": "old", "limit": map[string]any{"context": 32768, "output": 4096}, "tool_call": true}
	configured, err := json.Marshal(map[string]any{
		"model":  "openabstractions/removed",
		"plugin": []any{[]any{providerDir, map[string]string{"providerId": "openabstractions"}}},
		"provider": map[string]any{"openabstractions": map[string]any{
			"name": "OpenAbstractions · this computer", "npm": openCodeFileURL(filepath.Join(providerDir, "index.js")),
			"options": map[string]any{"runtimeEndpoint": options.endpoint, "scope": "local",
				"guarantees": []string{"abstraction.inference/local-only@1"}, "oaInventoryManagedModels": map[string]any{"removed": oldModel}},
			"models": map[string]any{"removed": oldModel},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configured, 0o600); err != nil {
		t.Fatal(err)
	}
	env := isolatedOpenCodeEnvironment(t, t.TempDir(), "", library)
	call, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	cmd := exec.CommandContext(call, opencode, "--print-logs", "--log-level", "DEBUG", "models", "openabstractions")
	cmd.Dir, cmd.Env = project, env
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("OpenCode startup model listing: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "openabstractions/fixture-1b") || strings.Contains(string(output), "openabstractions/removed") {
		t.Fatalf("startup plugin did not replace the old inventory with the live router model:\n%s", output)
	}
}

func environmentWithout(environment []string, names ...string) []string {
	return fixture.EnvironmentWithout(environment, names...)
}

func TestStageOpenCodeProviderSupportsPublishedSiblingLayout(t *testing.T) {
	workspace := t.TempDir()
	charter := filepath.Join(workspace, "abstractions")
	write := func(path, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(charter, "adopters", "opencode", "index.js"), "// provider fixture\n")
	// This layout control stages bytes only; it needs no installed npm tree.
	external := filepath.Join(workspace, "external")
	write(filepath.Join(external, "@ai-sdk", "provider", "package.json"), "{}\n")
	t.Setenv("OA_AI_SDK_PROVIDER_MODULES", external)
	if _, err := findOpenCodeProviderSources(charter); err == nil {
		t.Fatal("public checkout without generated siblings was accepted as complete")
	}
	for _, fixture := range []struct{ repository, installed string }{
		{"abstraction-facade", "facade"},
		{"abstraction-inference", "inference"},
		{"abstraction-identity", "ipc"},
		{"abstraction-router", "router"},
		{"abstraction-asks", "asks"},
		{"abstraction-rights", "rights"},
		{"abstraction-storage", "storage-content"},
		{"abstraction-logging", "logging"},
		{"abstraction-download", "download-request"},
		{"abstraction-job", "job-acceptance"},
	} {
		source := filepath.Join(workspace, fixture.repository, "javascript")
		write(filepath.Join(source, "package.json"), "{}\n")
		write(filepath.Join(source, "source-marker"), fixture.repository+"\n")
	}

	serve := filepath.Join(charter, "serve")
	if err := os.MkdirAll(serve, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(serve)
	provider := stageOpenCodeProviderFrom(t, filepath.Clean(".."))
	if _, err := os.Stat(provider); err != nil {
		t.Fatalf("staged provider: %v", err)
	}
	for _, fixture := range []struct{ repository, installed string }{
		{"abstraction-facade", "facade"},
		{"abstraction-inference", "inference"},
		{"abstraction-identity", "ipc"},
		{"abstraction-router", "router"},
		{"abstraction-asks", "asks"},
		{"abstraction-rights", "rights"},
		{"abstraction-storage", "storage-content"},
		{"abstraction-logging", "logging"},
		{"abstraction-download", "download-request"},
		{"abstraction-job", "job-acceptance"},
	} {
		marker := filepath.Join(filepath.Dir(provider), "node_modules", "@openabstractions", fixture.installed, "source-marker")
		data, err := os.ReadFile(marker)
		if err != nil || string(data) != fixture.repository+"\n" {
			t.Fatalf("staged %s from public sibling: data=%q err=%v", fixture.installed, data, err)
		}
	}
	if err := os.Mkdir(filepath.Join(charter, "openabstractions-flat"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := findOpenCodeProviderSources(charter); err != nil {
		t.Fatalf("partial public projection hid sibling sources: %v", err)
	}
	if err := os.Mkdir(filepath.Join(charter, "openabstractions-flat", "abstraction-identity"), 0755); err != nil {
		t.Fatal(err)
	}
	if _, err := findOpenCodeProviderSources(charter); err == nil {
		t.Fatal("private workspace with missing generated packages fell back to public siblings")
	}
}

func canonicalAsksResult(text string) (string, error) {
	notice, payload, ok := strings.Cut(text, "\n")
	if !ok || !strings.HasPrefix(notice, "OA Asks result.") {
		return "", errors.New("native Asks result envelope missing")
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal([]byte(payload), &value); err != nil {
		return "", err
	}
	if len(value["outcome"]) == 0 {
		return "", errors.New("native Asks outcome missing")
	}
	encoded, err := json.Marshal(value)
	return string(encoded), err
}

func TestOpenCodeAsksResultReceivesFormattedMCPJSON(t *testing.T) {
	value, err := canonicalAsksResult("OA Asks result. answer_recorded=false; permission_applied=false.\n{\n  \"outcome\": \"not_granted\",\n  \"policy_revision\": \"observed\"\n}")
	if err != nil || !strings.Contains(value, `"outcome":"not_granted"`) {
		t.Fatal(value, err)
	}
	if _, err := canonicalAsksResult("uncertain"); err == nil {
		t.Fatal("unknown result accepted")
	}
}

// This uses OpenCode's --auto tool approval in an isolated runtime. It proves
// the real tool and caller-bound public IPC path, not a human approval prompt.
func TestOpenCodeAnswersScopedFirstUseThroughPublicIPC(t *testing.T) {
	opencode, addon, library := os.Getenv("OA_OPENCODE_BINARY"), os.Getenv("OA_IPC_NODE"), os.Getenv("OA_IPC_LIBRARY")
	if opencode == "" || library == "" {
		t.Skip("set OA_OPENCODE_BINARY and OA_IPC_LIBRARY for the native OpenCode question proof")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	for _, path := range []string{opencode, library} {
		if !filepath.IsAbs(path) {
			t.Fatalf("fixture path must be absolute: %s", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}
	if addon != "" {
		if _, err := os.Stat(addon); err != nil {
			t.Fatal(err)
		}
	}

	options, _ := isolatedRuntime(t)
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(`{"retention_ms":10000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	startInferenceRuntime(t, options)
	endpointArgs := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	providerProgram := copyTestBinary(t, "oa-opencode-question-provider")
	providerState, pidDir := t.TempDir(), t.TempDir()
	self := options.program
	name := "opencode-question"
	args := []string{"add", name, "--program", providerProgram, "--provider-endpoint", name,
		"--contract", inference.Contract, "--guarantee", fixtureGuarantee,
		"--guarantee", "abstraction.inference/local-only@1", "--model", "fixture-model",
		"--arg", openCodeProviderFixtureArg, "--arg", "{endpoint}", "--arg", providerState,
		"--arg", "question", "--arg", pidDir, "--arg", self}
	if out, err := runProvider(t, options.program, append(args, endpointArgs...)...); err != nil {
		t.Fatalf("declare question provider: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = runProvider(t, options.program, append([]string{"remove", name}, endpointArgs...)...) })
	setCompleteRule(t, options.endpoint, filepath.Clean(opencode), name, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	machine := client.NewVerified(options.endpoint, runtimeExpectation(t, options.program))
	rightsOperator, err := machine.ResolveRightsOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := rightsOperator.ListPolicyContext(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	inventory := rights.PolicyRule{Subject: rights.Subject{Account: currentAccount(t), Program: filepath.Clean(opencode)},
		Action: routerservice.ActionInventory, Resource: routerservice.ResourceInventory, Permit: true}
	if set, err := rightsOperator.SetRuleContext(ctx, policy.Revision, inventory); err != nil || set.Outcome.String() != "applied" {
		t.Fatalf("OpenCode model inventory %+v %v", set, err)
	}

	providerIndex := stageOpenCodeProvider(t)
	project := t.TempDir()
	config := map[string]any{
		// The image and question tools this test calls register only when
		// enabled (adopters/opencode/README.md "Tools").
		"plugin": []any{[]any{filepath.Dir(providerIndex), map[string]any{
			"tools": map[string]any{"generate_image": true}}}},
		"provider": map[string]any{"openabstractions": map[string]any{
			"name": "OpenAbstractions", "npm": fileURL(providerIndex),
			"options": map[string]any{"runtimeEndpoint": options.endpoint, "scope": "local", "guarantees": []string{fixtureGuarantee}},
			"models": map[string]any{"fixture-model": map[string]any{"name": "OA fixture", "tool_call": true,
				"limit": map[string]any{"context": 32768, "output": 4096}}},
		}},
		"model": "openabstractions/fixture-model",
	}
	rawConfig, _ := json.Marshal(config)
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	env := isolatedOpenCodeEnvironment(t, t.TempDir(), addon, library)
	gatewayProgram := fixture.BuildGateway(t, t.TempDir(), "oa-opencode-integration")
	classification := identity.ClassifyProgram(gatewayProgram, identity.Code{})
	if classification.RunsForeignCode || classification.SharedInterpreter {
		t.Fatalf("source gateway must preserve ASK-R7 eligibility: %+v", classification)
	}
	config["mcp"] = map[string]any{"OpenAbstractions": map[string]any{"type": "local", "command": []string{gatewayProgram, "--runtime", options.endpoint, "--state-dir", t.TempDir()}, "timeout": 60000}}
	rawConfig, _ = json.Marshal(config)
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), rawConfig, 0600); err != nil {
		t.Fatal(err)
	}
	run := func(tool, id string) string {
		t.Helper()
		call, _ := json.Marshal(map[string]string{"id": id, "option": "allow"})
		if tool == "oa_asks_request_answering" {
			call = []byte(`{}`)
		}
		if err := os.WriteFile(filepath.Join(providerState, "question-tool.txt"), []byte(tool), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(providerState, "question-call.json"), call, 0o600); err != nil {
			t.Fatal(err)
		}
		before, _ := os.ReadFile(filepath.Join(providerState, "requests.jsonl"))
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, opencode, "run", "--auto", "--print-logs", "--log-level", "DEBUG", "--model", "openabstractions/fixture-model", "--format", "json", "--dir", project,
			"Answer the delegated OpenAbstractions authorization question once, then report the tool result.")
		cmd.Dir, cmd.Env = project, env
		output, err := cmd.CombinedOutput()
		if err != nil {
			diagnostic, _ := os.ReadFile(filepath.Join(providerState, "question-dispatch.json"))
			t.Fatalf("OpenCode question tool: %v\nfixture dispatch: %s\n%s", err, diagnostic, output)
		}
		if diagnostic, err := os.ReadFile(filepath.Join(providerState, "question-dispatch.json")); err == nil {
			var selected struct{ Wanted, Selected string }
			if json.Unmarshal(diagnostic, &selected) == nil {
				t.Logf("native OpenCode tool wanted=%s received=%s", selected.Wanted, selected.Selected)
			}
		}
		after, err := os.ReadFile(filepath.Join(providerState, "requests.jsonl"))
		if err != nil || len(after) <= len(before) {
			t.Fatalf("OpenCode did not reach OA fixture: %v\n%s", err, output)
		}
		var results []string
		for _, line := range bytes.Split(bytes.TrimSpace(after[len(before):]), []byte{'\n'}) {
			var request iwire.Request
			if err := json.Unmarshal(line, &request); err != nil {
				t.Fatal(err)
			}
			for _, message := range request.Messages {
				for _, part := range message.Parts {
					if part.Kind == iwire.PartKindToolResult {
						results = append(results, part.Text)
					}
				}
			}
		}
		if len(results) == 0 {
			t.Fatalf("real OpenCode did not return a tool result; output tail: %s", string(output[max(0, len(output)-2000):]))
		}
		if strings.Contains(strings.Join(results, "\n"), "unavailable tool 'oa_asks_answer'") {
			t.Fatalf("question plugin unavailable; OpenCode output head: %s", string(output[:min(len(output), 7000)]))
		}
		result, err := canonicalAsksResult(results[len(results)-1])
		if err != nil {
			t.Fatalf("real OpenCode Asks receiving: %v; %s", err, results[len(results)-1])
		}
		return result
	}

	if requested := run("oa_asks_request_answering", ""); !strings.Contains(requested, `"outcome":"not_granted"`) {
		t.Fatalf("launcher did not request person-only enrollment: %s", requested)
	}
	questions, err := machine.ResolveAsksOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	page, err := questions.ListQuestionsContext(ctx, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	enrollmentID := ""
	for _, question := range page.Records {
		_, action, _, ok := asks.FirstUse(question.Key, question.About, question.Text)
		if ok && action == asks.AnswerAction && identity.SameSubjectProgram(question.About, gatewayProgram) && question.Option == "" {
			enrollmentID = question.ID
		}
	}
	if enrollmentID == "" {
		t.Fatalf("native enrollment question missing: %+v", page)
	}
	// The person's existing first-use owner records the answer and applies its
	// exact rule. A recorded Asks answer alone deliberately grants nothing.
	if answered, err := grants.AnswerQuestion(ctx, questions, rightsOperator, currentAccount(t), enrollmentID, "allow"); err != nil || answered.Decision.Outcome.String() != "answered" || answered.Edit == nil || answered.Edit.Outcome.String() != "applied" {
		t.Fatal(answered, err)
	}
	if requested := run("oa_asks_request_answering", ""); !strings.Contains(requested, `"outcome":"permitted"`) {
		t.Fatalf("launcher enrollment not applied: %s", requested)
	}
	bin := probeBinary(t)
	if result, _ := probeAs(t, bin, options, "model", "resolve", "hf:org/model@not/a/revision"); result.Outcome != "forbidden" {
		t.Fatalf("first-use denial %+v", result)
	}
	pending := pendingFirstUse(t, options)
	if len(pending) != 1 || pending[0].About != bin {
		t.Fatalf("first-use question %+v", pending)
	}
	allowed := run("oa_asks_answer", pending[0].ID)
	if !strings.Contains(allowed, `"effect":"applied"`) || !strings.Contains(allowed, `"target_outcome":"permitted"`) {
		t.Fatalf("real OpenCode tool did not apply exact answer: %s", allowed)
	}
	target := rights.Subject{Account: currentAccount(t), Program: bin}
	read, err := rightsOperator.ReadRuleContext(ctx, target, host.ModelLookupAction, "hf")
	if err != nil || read.Outcome.String() != "found" || read.Record == nil || !read.Record.Rule.Permit {
		t.Fatalf("target rule after OpenCode answer %+v %v", read, err)
	}

	if refused := run("oa_asks_answer", enrollmentID); !strings.Contains(refused, `"outcome":"forbidden"`) {
		t.Fatalf("enrolled launcher answered a person-only enrollment question: %s", refused)
	}
	if replay := run("oa_asks_answer", pending[0].ID); !strings.Contains(replay, `"outcome":"answered"`) {
		t.Fatalf("lost reply did not preserve the recorded answer: %s", replay)
	}
	notifier, err := machine.ResolveAsksNotificationOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	notices, err := notifier.ListNotificationsContext(ctx, "", 64)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, notice := range notices.Records {
		if notice.Request.Title == "Answered for you" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("service must notify once independently of host announcements: %+v", notices)
	}
	if revoke, err := rightsOperator.RevokeRuleContext(ctx, read.Revision, target, host.ModelLookupAction, "hf"); err != nil || revoke.Outcome.String() != "applied" {
		t.Fatalf("revoke target rule %+v %v", revoke, err)
	}
	if retired, err := questions.RetireQuestionContext(ctx, pending[0].ID); err != nil || retired.Outcome.String() != "retired" {
		t.Fatal(retired, err)
	}
	policy, err = rightsOperator.ListPolicyContext(ctx, "", 1)
	if err != nil {
		t.Fatal(err)
	}
	holder := rights.Subject{Account: currentAccount(t), Program: filepath.Clean(gatewayProgram)}
	if revoked, err := rightsOperator.RevokeRuleContext(ctx, policy.Revision, holder, asks.AnswerAction, "apps"); err != nil || revoked.Outcome.String() != "applied" {
		t.Fatal(revoked, err)
	}
	if result, _ := probeAs(t, bin, options, "model", "resolve", "hf:org/model@not/a/revision"); result.Outcome != "forbidden" {
		t.Fatal(result)
	}
	retry := pendingFirstUse(t, options)
	if len(retry) != 1 {
		t.Fatal(retry)
	}
	if denied := run("oa_asks_answer", retry[0].ID); !strings.Contains(denied, `"outcome":"forbidden"`) {
		t.Fatal("revoked enrollment answered", denied)
	}
	read, err = rightsOperator.ReadRuleContext(ctx, target, host.ModelLookupAction, "hf")
	if err != nil || read.Outcome.String() != "unknown" {
		t.Fatal("revoked target was restored", read, err)
	}

}

func TestOpenCodeUsesNativeOAProviderAndSurvivesProviderSubstitution(t *testing.T) {
	artifact := r6FirstStartArtifact(t)
	opencode, addon, library := os.Getenv("OA_OPENCODE_BINARY"), os.Getenv("OA_IPC_NODE"), os.Getenv("OA_IPC_LIBRARY")
	if artifact != nil {
		opencode, addon, library = artifact.cli, "", artifact.library
	}
	if opencode == "" || library == "" {
		t.Skip("set OA_OPENCODE_BINARY and OA_IPC_LIBRARY for the native OpenCode adopter proof")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	paths := []string{opencode, library}
	if addon != "" {
		paths = append(paths, addon)
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			t.Fatalf("fixture path must be absolute: %s", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}

	var options *runtimeFixture
	if artifact == nil {
		options, _ = isolatedRuntime(t)
	} else {
		state := filepath.Join(t.TempDir(), "private")
		options = &runtimeFixture{stateDir: state, out: filepath.Join(state, "logging", "records.jsonl"), program: artifact.runtime}
		if err := privatedir.Ensure(state); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(`{"retention_ms":10000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	startInferenceRuntime(t, options)
	endpointArgs := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	providerProgram := copyTestBinary(t, "oa-opencode-native-provider")
	providerState, pidDir := t.TempDir(), t.TempDir()
	name := "opencode-native"
	self := options.program

	declare := func(label string) {
		t.Helper()
		args := []string{"add", name, "--program", providerProgram, "--provider-endpoint", name,
			"--contract", inference.Contract, "--guarantee", fixtureGuarantee, "--guarantee", "abstraction.inference/local-only@1", "--model", "fixture-model",
			"--arg", openCodeProviderFixtureArg, "--arg", "{endpoint}", "--arg", providerState,
			"--arg", label, "--arg", pidDir, "--arg", self}
		if artifact != nil {
			args = append(args, "--model", "qwen3.6-35b-a3b", "--model", "qwen3-0.6b")
		}
		if out, err := runProvider(t, options.program, append(args, endpointArgs...)...); err != nil {
			t.Fatalf("declare %s: %v\n%s", label, err, out)
		}
		resolve := func() fwire.ResolveResult {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result, err := resolution.NewUnverifiedClient(options.endpoint, 5*time.Second).Resolve(ctx, fwire.ResolveRequest{
				Capability: "abstraction.inference", Contracts: []string{inference.Contract},
				Guarantees: []string{fixtureGuarantee}, Scope: fwire.ScopeLocal})
			if err != nil {
				t.Fatal(err)
			}
			return result
		}
		_ = resolve()
		eventually(t, 20*time.Second, "OpenCode provider becoming ready", func() bool {
			return resolve().Status == fwire.ResolutionStatusResolved
		})
	}
	if artifact == nil {
		declare("one")
	} else {
		declare("firststart")
	}
	t.Cleanup(func() {
		_, _ = runProvider(t, options.program, append([]string{"remove", name}, endpointArgs...)...)
	})

	providerIndex := ""
	if artifact == nil {
		providerIndex = stageOpenCodeProvider(t)
	} else {
		providerIndex = artifact.provider
	}
	project := t.TempDir()
	config := map[string]any{
		"provider": map[string]any{"openabstractions": map[string]any{
			"name": "OpenAbstractions", "npm": fileURL(providerIndex),
			"options": map[string]any{"runtimeEndpoint": options.endpoint, "scope": "local", "guarantees": []string{fixtureGuarantee}},
			"models": map[string]any{"fixture-model": map[string]any{"name": "OA fixture", "tool_call": true,
				"limit": map[string]any{"context": 32768, "output": 4096}}},
		}},
		"model": "openabstractions/fixture-model",
	}
	if artifact != nil {
		config["model"] = "openabstractions/oa-chat"
		config["small_model"] = "openabstractions/oa-chat"
		config["plugin"] = []any{[]any{filepath.Dir(providerIndex), map[string]string{"providerId": "openabstractions"}}}
		provider := config["provider"].(map[string]any)["openabstractions"].(map[string]any)
		models := provider["models"].(map[string]any)
		models["qwen3.6-35b-a3b"], models["qwen3-0.6b"] = models["fixture-model"], models["fixture-model"]
		// A saved, configured policy model has an explicit display limit. The
		// inert native peer offers names, not an engine's observed context.
		models["oa-chat"] = models["fixture-model"]
		provider["options"].(map[string]any)["oaSelectionPolicies"] = map[string]string{"oa-chat": "oa-chat-default"}
	}
	rawConfig, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	envRoot := t.TempDir()
	env := isolatedOpenCodeEnvironment(t, envRoot, addon, library)
	if artifact != nil {
		// Every profile/config/cache location belongs to this one fixture.
		env = environmentWithout(env, "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "ABSTRACTION_RUNTIME_ENDPOINT", "NODE_OPTIONS", "BUN_OPTIONS")
		env = append(env, "HOME="+envRoot, "USERPROFILE="+envRoot, "APPDATA="+filepath.Join(envRoot, "roaming"), "LOCALAPPDATA="+filepath.Join(envRoot, "local"), "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_MODELS_FETCH=1")
	}
	resume := false
	run := func(prompt string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		args := []string{"--pure", "run", "--auto", "--print-logs", "--log-level", "DEBUG", "--model", "openabstractions/fixture-model", "--format", "json", "--dir", project, prompt}
		if artifact != nil {
			args = []string{"run", "--auto", "--print-logs", "--log-level", "DEBUG", "--format", "json", "--dir", project}
			if resume {
				args = append(args, "--continue")
			}
			args = append(args, prompt)
		}
		cmd := exec.CommandContext(ctx, opencode, args...)
		cmd.Dir, cmd.Env = project, env
		return cmd.CombinedOutput()
	}
	type auditRecord struct {
		Program string `json:"Program"`
		Outcome string `json:"Outcome"`
		Reason  string `json:"Reason"`
	}
	readAudit := func() []auditRecord {
		t.Helper()
		output, err := runInference(t, options.program, append([]string{"audit", "--json"}, endpointArgs...)...)
		if err != nil {
			t.Fatalf("audit: %v\n%s", err, output)
		}
		var page struct {
			Entries []auditRecord `json:"entries"`
		}
		if err := json.Unmarshal([]byte(output), &page); err != nil {
			t.Fatal(err)
		}
		return page.Entries
	}
	countOutcome := func(entries []auditRecord, outcome string) int {
		count := 0
		for _, entry := range entries {
			if filepath.Clean(entry.Program) == filepath.Clean(opencode) && entry.Outcome == outcome {
				count++
			}
		}
		return count
	}
	assertOneStreamPerAgent := func(output []byte) {
		t.Helper()
		for _, agent := range []string{"title", "build"} {
			count := 0
			for _, line := range strings.Split(string(output), "\n") {
				if strings.Contains(line, "message=stream providerID=openabstractions") &&
					strings.Contains(line, "agent="+agent+" mode=primary") {
					count++
				}
			}
			if count != 1 {
				t.Fatalf("OpenCode %s model stream count %d, want one:\n%s", agent, count, output)
			}
		}
	}
	if artifact != nil {
		setCompleteRule(t, options.endpoint, filepath.Clean(opencode), name, true)
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		operator, err := client.New(options.endpoint).ResolveRightsOperator(ctx, client.Requirements{})
		if err != nil {
			t.Fatal(err)
		}
		page, err := operator.ListPolicyContext(ctx, "", 1)
		if err != nil {
			t.Fatal(err)
		}
		rule := rights.PolicyRule{Subject: rights.Subject{Account: currentAccount(t), Program: filepath.Clean(opencode)}, Action: routerservice.ActionInventory, Resource: routerservice.ResourceInventory, Permit: true}
		if edit, err := operator.SetRuleContext(ctx, page.Revision, rule); err != nil || edit.Outcome.String() != "applied" {
			t.Fatalf("fixture inventory grant: %+v %v", edit, err)
		}
		selection, err := client.NewVerified(options.endpoint, runtimeExpectation(t, options.program)).ResolveInferenceSelectionOperatorV2(ctx, local)
		if err != nil {
			t.Fatal(err)
		}
		policy := iwire.SelectionPolicyV2{Name: "oa-chat-default", Candidates: []iwire.SelectionCandidateV2{{Model: "qwen3.6-35b-a3b", Placement: iwire.CandidatePlacementLocal}}}
		change, err := selection.Put(ctx, filepath.Clean(opencode), "", policy)
		if err != nil || change.Outcome != iwire.EditOutcomeApplied {
			t.Fatalf("fixture saved chat policy: %+v %v", change, err)
		}
		policyRevision := change.Revision
		output, err := run("hi")
		requests, readErr := os.ReadFile(filepath.Join(providerState, "requests.jsonl"))
		t.Logf("R6 first-start host output (including host diagnostics):\n%s\nprovider Start requests:\n%s\naudit:\n%+v", output, requests, readAudit())
		if err != nil || readErr != nil || !strings.Contains(string(output), "native-oa-firststart") {
			t.Fatalf("R6 first hi: process=%v requests=%v", err, readErr)
		}
		assertOneStreamPerAgent(output)
		if !strings.Contains(string(output), "modelID=oa-chat ") {
			t.Fatal("host did not use the saved policy model")
		}
		var main, title int
		for _, line := range bytes.Split(bytes.TrimSpace(requests), []byte("\n")) {
			var request iwire.Request
			if err := json.Unmarshal(line, &request); err != nil {
				t.Fatal(err)
			}
			if len(request.Tools) == 0 {
				title++
			} else {
				main++
			}
		}
		if main < 1 || title != 1 {
			t.Fatalf("first hi Start boundaries main=%d title=%d", main, title)
		}
		firstSession := r6AnswerSession(t, output, "qwen3.6-35b-a3b")
		resume = true
		second, err := run("hi")
		t.Logf("R6 same-session second hi:\n%s", second)
		if err != nil || !strings.Contains(string(second), "native-oa-firststart") {
			t.Fatalf("second hi: %v", err)
		}
		if r6AnswerSession(t, second, "qwen3.6-35b-a3b") != firstSession {
			t.Fatal("second hi did not resume the initial session")
		}
		// A fresh small-default profile uses the same actual host and saved-config
		// binding path. No --model argument supplies either profile's selection.
		resume = false
		project, envRoot = t.TempDir(), t.TempDir()
		policy.Candidates[0].Model = "qwen3-0.6b"
		if change, err := selection.Put(ctx, filepath.Clean(opencode), policyRevision, policy); err != nil || change.Outcome != iwire.EditOutcomeApplied {
			t.Fatalf("fixture small chat policy: %+v %v", change, err)
		}
		rawConfig, _ = json.MarshalIndent(config, "", "  ")
		if err := os.WriteFile(filepath.Join(project, "opencode.json"), rawConfig, 0600); err != nil {
			t.Fatal(err)
		}
		env = isolatedOpenCodeEnvironment(t, envRoot, addon, library)
		env = environmentWithout(env, "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "ABSTRACTION_RUNTIME_ENDPOINT", "NODE_OPTIONS", "BUN_OPTIONS")
		env = append(env, "HOME="+envRoot, "USERPROFILE="+envRoot, "APPDATA="+filepath.Join(envRoot, "roaming"), "LOCALAPPDATA="+filepath.Join(envRoot, "local"), "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_MODELS_FETCH=1")
		small, err := run("hi")
		t.Logf("R6 fresh small-default hi:\n%s", small)
		if err != nil || !strings.Contains(string(small), "native-oa-firststart") {
			t.Fatalf("small-default hi: %v", err)
		}
		assertOneStreamPerAgent(small)
		if r6AnswerSession(t, small, "qwen3-0.6b") == firstSession {
			t.Fatal("small-default profile reused the original session")
		}
		requests, err = os.ReadFile(filepath.Join(providerState, "requests.jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		counts := map[string]int{}
		for _, line := range bytes.Split(bytes.TrimSpace(requests), []byte("\n")) {
			var request iwire.Request
			if err := json.Unmarshal(line, &request); err != nil {
				t.Fatal(err)
			}
			kind := "main"
			if len(request.Tools) == 0 {
				kind = "title"
			}
			counts[request.Model+"/"+kind]++
		}
		if counts["qwen3.6-35b-a3b/main"] != 2 || counts["qwen3.6-35b-a3b/title"] != 1 || counts["qwen3-0.6b/main"] != 1 || counts["qwen3-0.6b/title"] != 1 || len(counts) != 4 {
			t.Fatalf("actual Start boundary counts: %+v", counts)
		}
		t.Logf("actual native Start boundaries: %+v", counts)
		return
	}

	// A fresh OpenCode run starts one title call and one build call. Count both
	// separately so an automatic retry cannot hide behind title generation.
	beforeDenied := countOutcome(readAudit(), "not_permitted")
	denied, _ := run("Use the available tools once, then report the native OA result.")
	if !strings.Contains(string(denied), "not yet permitted") || !strings.Contains(string(denied), "OpenAbstractions Panel") {
		t.Fatalf("plain-language OA refusal missing:\n%s", denied)
	}
	assertOneStreamPerAgent(denied)
	if got := countOutcome(readAudit(), "not_permitted"); got != beforeDenied+2 {
		t.Fatalf("OpenCode retried denied Start: audit count %d, before %d\n%s", got, beforeDenied, denied)
	}
	if requests, _ := os.ReadFile(filepath.Join(providerState, "requests.jsonl")); len(requests) != 0 {
		t.Fatalf("refused OpenCode reached provider: %s", requests)
	}

	setCompleteRule(t, options.endpoint, filepath.Clean(opencode), name, true)
	first, err := run("Use the available tools once, then report the native OA result.")
	if err != nil || !strings.Contains(string(first), "native-oa-one") {
		t.Fatalf("OpenCode native provider one: %v\n%s", err, first)
	}
	requests, _ := os.ReadFile(filepath.Join(providerState, "requests.jsonl"))
	if !strings.Contains(string(requests), `"Kind":"tool_result"`) {
		t.Fatalf("real OpenCode conversation did not return a tool result through OA; OpenCode output:\n%s", first)
	}

	if out, err := runProvider(t, options.program, append([]string{"remove", name}, endpointArgs...)...); err != nil {
		t.Fatalf("remove first provider: %v\n%s", err, out)
	}
	eventually(t, 10*time.Second, "first provider stopping", func() bool { return !answers(filepath.Join(providerState, "provider-endpoint")) })
	declare("incapable")
	beforeUnsupported := countOutcome(readAudit(), "unsupported_feature")
	incapable, _ := run("Use the available tools once, then report the native OA result.")
	if !strings.Contains(string(incapable), "OpenAbstractions could not complete this model call") {
		t.Fatalf("neutral native refusal missing:\n%s", incapable)
	}
	assertOneStreamPerAgent(incapable)
	unsupported := readAudit()
	if got := countOutcome(unsupported, "unsupported_feature"); got != beforeUnsupported+1 {
		t.Fatalf("OpenCode retried unsupported Start: audit count %d, before %d\n%s", got, beforeUnsupported, incapable)
	}
	foundReason := false
	for _, entry := range unsupported {
		if filepath.Clean(entry.Program) == filepath.Clean(opencode) && entry.Outcome == "unsupported_feature" &&
			strings.Contains(entry.Reason, "feature:abstraction.inference/tools@1") {
			foundReason = true
		}
	}
	if !foundReason {
		t.Fatalf("typed unsupported reason missing from audit: %+v", unsupported)
	}
	if out, err := runProvider(t, options.program, append([]string{"remove", name}, endpointArgs...)...); err != nil {
		t.Fatalf("remove incapable provider: %v\n%s", err, out)
	}
	eventually(t, 10*time.Second, "incapable provider stopping", func() bool { return !answers(filepath.Join(providerState, "provider-endpoint")) })
	declare("unavailable")
	beforeUnavailable := countOutcome(readAudit(), "unavailable")
	partial, _ := run("Use the available tools once, then report the native OA result.")
	if !strings.Contains(string(partial), "OpenAbstractions could not complete this model call") {
		t.Fatalf("partial native failure missing neutral refusal:\n%s", partial)
	}
	assertOneStreamPerAgent(partial)
	unavailable := readAudit()
	if got := countOutcome(unavailable, "unavailable"); got != beforeUnavailable+2 {
		t.Fatalf("OpenCode retried after accepted partial Start: audit count %d, before %d\n%s", got, beforeUnavailable, partial)
	}
	foundUnavailableReason := false
	for _, entry := range unavailable {
		if filepath.Clean(entry.Program) == filepath.Clean(opencode) && entry.Outcome == "unavailable" &&
			entry.Reason == "upstream:error" {
			foundUnavailableReason = true
		}
	}
	if !foundUnavailableReason {
		t.Fatalf("typed unavailable reason missing from audit: %+v", unavailable)
	}
	if out, err := runProvider(t, options.program, append([]string{"remove", name}, endpointArgs...)...); err != nil {
		t.Fatalf("remove unavailable provider: %v\n%s", err, out)
	}
	eventually(t, 10*time.Second, "unavailable provider stopping", func() bool { return !answers(filepath.Join(providerState, "provider-endpoint")) })
	declare("two")
	second, err := run("Use the available tools once, then report the native OA result.")
	if err != nil || !strings.Contains(string(second), "native-oa-two") {
		t.Fatalf("OpenCode substituted provider: %v\n%s", err, second)
	}
	if addon == "" {
		t.Log("Node addon absent; native OpenCode refusal and substitution proof complete, Node cancellation leg skipped")
		return
	}

	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatal("node is required for the cancellation leg")
	}
	node, _ = filepath.Abs(node)
	setCompleteRule(t, options.endpoint, filepath.Clean(node), name, true)
	cancelScript := filepath.Join(project, "cancel.mjs")
	script := fmt.Sprintf(`import {createOpenAbstractions} from %q;
const model=createOpenAbstractions({runtimeEndpoint:%q,scope:'local',guarantees:[%q]}).languageModel('fixture-model');
const result=await model.doStream({prompt:[{role:'user',content:[{type:'text',text:'OA_CANCEL_PROBE'}]}]});
const reader=result.stream.getReader();
for(;;){const next=await reader.read();if(next.done)throw new Error('ended before cancellation');if(next.value.type==='text-delta')break;}
await reader.cancel('test cancellation');
`, fileURL(providerIndex), options.endpoint, fixtureGuarantee)
	if err := os.WriteFile(cancelScript, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	cancelCmd := exec.Command(node, cancelScript)
	cancelCmd.Dir, cancelCmd.Env = project, env
	if output, err := cancelCmd.CombinedOutput(); err != nil {
		t.Fatalf("native cancellation consumer: %v\n%s", err, output)
	}
	eventually(t, 5*time.Second, "OA cancellation reaching the native provider", func() bool {
		matches, _ := filepath.Glob(filepath.Join(providerState, "cancelled-*"))
		return len(matches) > 0
	})

	audit, err := runInference(t, options.program, append([]string{"audit", "--json"}, endpointArgs...)...)
	if err != nil {
		t.Fatalf("audit: %v\n%s", err, audit)
	}
	var document struct {
		Entries []struct {
			Program string `json:"Program"`
			Outcome string `json:"Outcome"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(audit), &document); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range document.Entries {
		seen[filepath.Clean(entry.Program)+"\x00"+entry.Outcome] = true
	}
	if !seen[filepath.Clean(opencode)+"\x00not_permitted"] || !seen[filepath.Clean(opencode)+"\x00completed"] ||
		!seen[filepath.Clean(node)+"\x00cancelled"] {
		t.Fatalf("audit lacks original OpenCode and cancellation-helper attribution:\n%s", audit)
	}
}

// killServeTree stops an `opencode serve` child. On Windows the portable
// build has been observed running under a different PID than the one Start
// returns, so a plain Process.Kill can leave the real server running;
// taskkill's process-tree option reaches it.
func killServeTree(cmd *exec.Cmd) {
	if cmd.Process == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
		return
	}
	_ = cmd.Process.Kill()
}

// TestOpenCodeServerAPICancelsMidStreamAndAttributesToOpenCode drives the same
// isolated runtime and slow provider fixture as
// TestOpenCodeUsesNativeOAProviderAndSurvivesProviderSubstitution through
// OpenCode's own HTTP session API instead of the TUI, because the Windows
// console cannot script Escape into the TUI reliably. It starts `opencode
// serve`, completes one request through the synchronous message endpoint,
// then starts a second request whose prompt makes the fixture stream
// indefinitely (openCodeFixtureChat.Observe's "cancel" mode above), waits for
// that streamed text to arrive on the server's SSE event feed, and calls the
// session's abort endpoint. The fixture's own cancellation marker file proves
// Cancel ran; the runtime's inference audit shows the real OpenCode
// executable, never a helper process, for both the completed and the
// cancelled call.
func TestOpenCodeServerAPICancelsMidStreamAndAttributesToOpenCode(t *testing.T) {
	opencode, addon, library := os.Getenv("OA_OPENCODE_BINARY"), os.Getenv("OA_IPC_NODE"), os.Getenv("OA_IPC_LIBRARY")
	if opencode == "" || addon == "" || library == "" {
		t.Skip("set OA_OPENCODE_BINARY, OA_IPC_NODE, and OA_IPC_LIBRARY for the native OpenCode adopter proof")
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	for _, path := range []string{opencode, addon, library} {
		if !filepath.IsAbs(path) {
			t.Fatalf("fixture path must be absolute: %s", path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatal(err)
		}
	}

	options, _ := isolatedRuntime(t)
	if err := os.MkdirAll(filepath.Join(options.stateDir, "inference"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(options.stateDir, "inference", inferenceHostsFile), []byte(`{"retention_ms":10000}`), 0o600); err != nil {
		t.Fatal(err)
	}
	startInferenceRuntime(t, options)
	endpointArgs := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	providerProgram := copyTestBinary(t, "oa-opencode-native-provider-server-api")
	providerState, pidDir := t.TempDir(), t.TempDir()
	name := "opencode-native-server-api"
	self := options.program

	declareArgs := []string{"add", name, "--program", providerProgram, "--provider-endpoint", name,
		"--contract", inference.Contract, "--guarantee", fixtureGuarantee, "--guarantee", "abstraction.inference/local-only@1", "--model", "fixture-model",
		"--arg", openCodeProviderFixtureArg, "--arg", "{endpoint}", "--arg", providerState,
		"--arg", "one", "--arg", pidDir, "--arg", self}
	if out, err := runProvider(t, options.program, append(declareArgs, endpointArgs...)...); err != nil {
		t.Fatalf("declare: %v\n%s", err, out)
	}
	t.Cleanup(func() { _, _ = runProvider(t, options.program, append([]string{"remove", name}, endpointArgs...)...) })
	resolveOnce := func() fwire.ResolveResult {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		result, err := resolution.NewUnverifiedClient(options.endpoint, 5*time.Second).Resolve(ctx, fwire.ResolveRequest{
			Capability: "abstraction.inference", Contracts: []string{inference.Contract},
			Guarantees: []string{fixtureGuarantee}, Scope: fwire.ScopeLocal})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	eventually(t, 20*time.Second, "OpenCode server-API provider becoming ready", func() bool {
		return resolveOnce().Status == fwire.ResolutionStatusResolved
	})
	setCompleteRule(t, options.endpoint, filepath.Clean(opencode), name, true)

	providerIndex := stageOpenCodeProvider(t)
	project := t.TempDir()
	config := map[string]any{
		"provider": map[string]any{"openabstractions": map[string]any{
			"name": "OpenAbstractions", "npm": fileURL(providerIndex),
			"options": map[string]any{"runtimeEndpoint": options.endpoint, "scope": "local", "guarantees": []string{fixtureGuarantee}},
			"models": map[string]any{"fixture-model": map[string]any{"name": "OA fixture", "tool_call": true,
				"limit": map[string]any{"context": 32768, "output": 4096}}},
		}},
		"model": "openabstractions/fixture-model",
	}
	rawConfig, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), rawConfig, 0o600); err != nil {
		t.Fatal(err)
	}
	envRoot := t.TempDir()
	env := isolatedOpenCodeEnvironment(t, envRoot, addon, library)

	address := freeLoopbackPort(t)
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + port

	serverCmd := exec.Command(opencode, "--pure", "serve", "--port", port, "--hostname", "127.0.0.1", "--print-logs", "--log-level", "DEBUG")
	serverCmd.Dir, serverCmd.Env = project, env
	var serverOut, serverErrOut bytes.Buffer
	serverCmd.Stdout, serverCmd.Stderr = &serverOut, &serverErrOut
	if err := serverCmd.Start(); err != nil {
		t.Fatalf("start opencode serve: %v", err)
	}
	t.Cleanup(func() { killServeTree(serverCmd); _ = serverCmd.Wait() })

	client := &http.Client{Timeout: 10 * time.Second}
	ready := false
	for deadline := time.Now().Add(20 * time.Second); time.Now().Before(deadline); {
		if serverCmd.ProcessState != nil {
			break
		}
		if resp, err := client.Get(base + "/doc"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Skipf("OpenCode server API did not become ready on %s:\nstdout:\n%s\nstderr:\n%s", base, serverOut.String(), serverErrOut.String())
	}

	createSession := func() string {
		t.Helper()
		resp, err := client.Post(base+"/session", "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatalf("create session: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("create session status %d: %s", resp.StatusCode, raw)
		}
		var session struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(raw, &session); err != nil || session.ID == "" {
			t.Fatalf("create session response %s: %v", raw, err)
		}
		return session.ID
	}
	sendMessage := func(sessionID, text string) (int, []byte) {
		t.Helper()
		body, _ := json.Marshal(map[string]any{
			"model": map[string]string{"providerID": "openabstractions", "modelID": "fixture-model"},
			"parts": []map[string]string{{"type": "text", "text": text}},
		})
		resp, err := client.Post(base+"/session/"+sessionID+"/message", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatalf("message: %v", err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, raw
	}

	// The baseline: a normal request through the server's synchronous
	// /message endpoint completes, giving one "completed" audit line
	// attributed to OpenCode's own executable.
	completeSession := createSession()
	if status, raw := sendMessage(completeSession, "Report the native OA result."); status != http.StatusOK || !strings.Contains(string(raw), "native-oa-one") {
		t.Fatalf("OpenCode server API baseline completion: status=%d\n%s", status, raw)
	}

	// The cancellation leg. The fixture streams the text "waiting" forever
	// once the prompt contains OA_CANCEL_PROBE, so the request is sent
	// asynchronously and the session is aborted once that streamed text is
	// observed on the server's own SSE feed, proving the abort reaches OA
	// mid-stream rather than after the fixture would have finished anyway.
	cancelSession := createSession()
	events, err := client.Get(base + "/event")
	if err != nil {
		t.Fatalf("subscribe to events: %v", err)
	}
	t.Cleanup(func() { events.Body.Close() })
	streaming := make(chan struct{}, 1)
	go func() {
		scanner := bufio.NewScanner(events.Body)
		scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for scanner.Scan() {
			line := scanner.Text()
			payload, found := strings.CutPrefix(line, "data: ")
			if !found {
				continue
			}
			if strings.Contains(payload, cancelSession) && strings.Contains(payload, `"type":"message.part.delta"`) && strings.Contains(payload, "waiting") {
				select {
				case streaming <- struct{}{}:
				default:
				}
				return
			}
		}
	}()

	asyncBody, _ := json.Marshal(map[string]any{
		"model": map[string]string{"providerID": "openabstractions", "modelID": "fixture-model"},
		"parts": []map[string]string{{"type": "text", "text": "OA_CANCEL_PROBE"}},
	})
	asyncResp, err := client.Post(base+"/session/"+cancelSession+"/prompt_async", "application/json", bytes.NewReader(asyncBody))
	if err != nil {
		t.Fatalf("prompt_async: %v", err)
	}
	asyncRaw, _ := io.ReadAll(asyncResp.Body)
	asyncResp.Body.Close()
	if asyncResp.StatusCode != http.StatusNoContent {
		t.Fatalf("prompt_async status %d: %s", asyncResp.StatusCode, asyncRaw)
	}

	select {
	case <-streaming:
	case <-time.After(15 * time.Second):
		t.Fatal("OpenCode server API never streamed the fixture's part before the abort")
	}

	abortResp, err := client.Post(base+"/session/"+cancelSession+"/abort", "application/json", nil)
	if err != nil {
		t.Fatalf("abort: %v", err)
	}
	abortRaw, _ := io.ReadAll(abortResp.Body)
	abortResp.Body.Close()
	if abortResp.StatusCode != http.StatusOK || strings.TrimSpace(string(abortRaw)) != "true" {
		t.Fatalf("abort status %d: %s", abortResp.StatusCode, abortRaw)
	}

	eventually(t, 5*time.Second, "OA cancellation reaching the native provider through the OpenCode server API", func() bool {
		matches, _ := filepath.Glob(filepath.Join(providerState, "cancelled-*"))
		return len(matches) > 0
	})

	audit, err := runInference(t, options.program, append([]string{"audit", "--json"}, endpointArgs...)...)
	if err != nil {
		t.Fatalf("audit: %v\n%s", err, audit)
	}
	var document struct {
		Entries []struct {
			Program string `json:"Program"`
			Outcome string `json:"Outcome"`
		} `json:"entries"`
	}
	if err := json.Unmarshal([]byte(audit), &document); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, entry := range document.Entries {
		seen[filepath.Clean(entry.Program)+"\x00"+entry.Outcome] = true
	}
	if !seen[filepath.Clean(opencode)+"\x00completed"] || !seen[filepath.Clean(opencode)+"\x00cancelled"] {
		t.Fatalf("audit lacks OpenCode server-API attribution for both the completed and the cancelled call:\n%s", audit)
	}
}

const openCodeProviderFixtureArg = fixture.OpenCodeProviderFixtureArg

// This diagnostic consumes the frozen R6 product, never a caller-selected
// executable override. The receipt pins bind its source and complete payload.
type r6Artifact struct{ runtime, library, cli, provider string }

func r6AnswerSession(t *testing.T, output []byte, model string) string {
	t.Helper()
	for _, line := range bytes.Split(output, []byte("\n")) {
		var event struct {
			Type, SessionID string
			Part            struct {
				Text     string
				Metadata struct {
					OA struct{ Model string } `json:"openabstractions"`
				}
			}
		}
		if json.Unmarshal(bytes.TrimSpace(line), &event) == nil && event.Type == "text" && event.Part.Text == "native-oa-firststart" {
			if event.Part.Metadata.OA.Model != model {
				t.Fatalf("answer model %q, want %q", event.Part.Metadata.OA.Model, model)
			}
			return event.SessionID
		}
	}
	t.Fatal("no actual host text event")
	return ""
}

type r6FilePin struct {
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
}

func readR6Pinned(path string, pin r6FilePin) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != pin.Bytes {
		return nil, fmt.Errorf("R6 file size/type differs: %s", path)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if fmt.Sprintf("%x", sha256.Sum256(raw)) != pin.SHA256 {
		return nil, fmt.Errorf("R6 file hash differs: %s", path)
	}
	return raw, nil
}

func TestR6FirstStartPinRejectsChangedBytes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "input")
	if err := os.WriteFile(path, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	pin := r6FilePin{5, fmt.Sprintf("%x", sha256.Sum256([]byte("first")))}
	if _, err := readR6Pinned(path, pin); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readR6Pinned(path, pin); err == nil {
		t.Fatal("changed artifact accepted")
	}
}

func r6FirstStartArtifact(t *testing.T) *r6Artifact {
	t.Helper()
	root := os.Getenv("OA_OPENCODE_R6_FIRSTSTART_BUILD")
	if root == "" {
		return nil
	}
	if !filepath.IsAbs(root) {
		t.Fatal("R6 build root must be absolute")
	}
	read := func(relative string, pin r6FilePin) []byte {
		raw, err := readR6Pinned(filepath.Join(root, filepath.FromSlash(relative)), pin)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	preview := read("preview-receipt.json", r6FilePin{180543, "0a3da2a8aecdbe9fe37444eadab89fc7e71bbe037d24d3788e107615618d79cd"})
	var provenance struct {
		Binding struct {
			Source string `json:"source_sha"`
		} `json:"binding"`
	}
	if err := json.Unmarshal(preview, &provenance); err != nil || provenance.Binding.Source != "3804cffdf8be0736f71d6c2730e6900c4029a0d3" {
		t.Fatal("R6 source provenance differs", err)
	}
	raw := read("opencode/payload-receipt.json", r6FilePin{1477378, "4963079b7000affa951b13de5957eab16744ee498e03966cbe8835028488fcdc"})
	var receipt struct {
		Payload map[string]r6FilePin `json:"payload"`
	}
	if err := json.Unmarshal(raw, &receipt); err != nil || len(receipt.Payload) != 7641 {
		t.Fatal("R6 payload receipt differs", err)
	}
	destination := t.TempDir()
	// Verify and copy every retained member before any runtime/host child starts.
	for relative, pin := range receipt.Payload {
		if relative == "" || strings.Contains(relative, "\\") || filepath.IsAbs(relative) || strings.Contains(relative, "..") {
			t.Fatal("unsafe R6 payload member", relative)
		}
		data := read("opencode/payload/"+relative, pin)
		target := filepath.Join(destination, "payload", filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(target), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	core := map[string]r6FilePin{
		"openabstractions.exe": {35472384, "39a202adbfc14b787a71a55ce4518e2ab0a85250c049a65941fbd9bf36c3c77e"},
		"abstraction_ipc.dll":  {231936, "1de6b4c11a60ec12f790530cbc5c6765a309726694e8bc9bd4fb2915c777a405"},
	}
	for name, pin := range core {
		data := read("core/payload/tools/"+name, pin)
		if err := os.WriteFile(filepath.Join(destination, name), data, 0700); err != nil {
			t.Fatal(err)
		}
	}
	cli := filepath.Join(destination, "payload", "cli", "opencode.exe")
	if _, err := readR6Pinned(cli, r6FilePin{180599176, "5283c2cd5b4285855daa97e6f7d17bf48146227cdf2a940e79e0f2b7869fd06e"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("OA_OPENCODE_BOOTSTRAP_CONFIG_SOURCE", filepath.Join(destination, "payload", "bootstrap", "config"))
	t.Setenv("OA_OPENCODE_BOOTSTRAP_CACHE_SOURCE", filepath.Join(destination, "payload", "bootstrap", "cache"))
	t.Log("Frozen R6 source3804; official OpenCode1.18.34 sourceaec0b9a6; isolated CLI scope, no Desktop UI")
	return &r6Artifact{filepath.Join(destination, "openabstractions.exe"), filepath.Join(destination, "abstraction_ipc.dll"), cli, filepath.Join(destination, "payload", "plugin", "0.8.0", "index.js")}
}

func init() {
	if len(os.Args) > 1 && os.Args[1] == openCodeProviderFixtureArg {
		os.Exit(fixture.RunOpenCodeProviderFixtureArgs(os.Args))
	}
}
func openCodeProviderFixtureLifetime(args []string) (time.Duration, error) {
	return fixture.OpenCodeProviderFixtureLifetime(args)
}
func runOpenCodeProviderFixtureArgs(args []string) int {
	return fixture.RunOpenCodeProviderFixtureArgs(args)
}
func copyTree(t *testing.T, source, target string) { fixture.CopyTree(t, source, target) }
func fileURL(path string) string                   { return fixture.FileURL(path) }
func openCodeFileURL(path string) string           { return fixture.FileURL(path) }
func findOpenCodeProviderSources(root string) (fixture.OpenCodeProviderSources, error) {
	return fixture.FindOpenCodeProviderSources(root)
}
func stageOpenCodeProviderFrom(t *testing.T, root string) string {
	return fixture.StageOpenCodeProviderFrom(t, root)
}
func stageOpenCodeProvider(t *testing.T) string { return stageOpenCodeProviderFrom(t, adopterRoot(t)) }
func isolatedOpenCodeEnvironment(t *testing.T, root, addon, library string) []string {
	return fixture.IsolatedOpenCodeEnvironment(t, root, addon, library)
}

// The black-box harness launches the shipped CLI in an owned private state
// directory. Service endpoints come from its startup output and facade resolver.
type runtimeFixture struct {
	stateDir, endpoint, out, program string
}

const inferenceHostsFile = "hosts.json"
const fixtureGuarantee = "openabstractions-test/fixture-provider@1"
const testCredentialSecret = "hf_RUNTIME_TEST_SECRET_9c41d7a2_EXAMPLE"

var local = client.Requirements{Scope: client.ScopeLocal}

func adopterRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("locate adopter harness")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", ".."))
}

func privateWorkspace(t *testing.T) bool {
	info, err := os.Stat(filepath.Join(adopterRoot(t), "openabstractions-flat"))
	return err == nil && info.IsDir()
}

func buildRuntime(t *testing.T, name string) string {
	t.Helper()
	name += goExecutableSuffix(t)
	target := filepath.Join(t.TempDir(), name)
	cmd := exec.Command("go", "-C", filepath.Join(adopterRoot(t), "serve"), "build", "-o", target, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build runtime: %v\n%s", err, out)
	}
	return target
}

func isolatedRuntime(t *testing.T) (*runtimeFixture, []string) {
	t.Helper()
	state := filepath.Join(t.TempDir(), "private")
	// The ordinary runtime CLI prepares and guards its private --state-dir.
	return &runtimeFixture{stateDir: state, out: filepath.Join(state, "logging", "records.jsonl"), program: buildRuntime(t, "openabstractions")}, nil
}

func startInferenceRuntime(t *testing.T, o *runtimeFixture) func() {
	t.Helper()
	if runtime.GOOS == "linux" {
		command := exec.Command(o.program, "credentials", "backend", "file", "--state-dir", o.stateDir)
		if out, err := command.CombinedOutput(); err != nil {
			t.Fatalf("private file store selection: %v\n%s", err, out)
		}
	}
	operator, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("adopter-%d-%d", os.Getpid(), time.Now().UnixNano())
	// The named test operator belongs to foreground isolated mode. Installed
	// parent supervision deliberately refuses an added operator program.
	cmd := exec.Command(o.program, "serve", "runtime", "--isolated", name, "--state-dir", o.stateDir, "--operator-program", operator)
	reader, writer := io.Pipe()
	var diagnostics safeBuffer
	cmd.Stdout = io.MultiWriter(writer, &diagnostics)
	cmd.Stderr = io.MultiWriter(writer, &diagnostics)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { err := cmd.Wait(); _ = writer.Close(); done <- err }()
	lines := make(chan string, 4)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		defer reader.Close()
		scanner := bufio.NewScanner(reader)
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if strings.HasPrefix(line, "ABSTRACTION_RUNTIME_ENDPOINT=") {
				lines <- line
			}
		}
	}()
	stopped := false
	stop := func() {
		if stopped {
			return
		}
		stopped = true
		select {
		case err := <-done:
			t.Errorf("isolated runtime exited before fixture stop: %v\n%s", err, diagnostics.String())
		default:
			// This fixture owns this exact child and its private state. Foreground
			// mode has no installed-parent stdin EOF contract; terminate its tree
			// by the PID we started and receive process and output closure.
			killServeTree(cmd)
			select {
			case <-done:
			case <-time.After(10 * time.Second):
				_ = cmd.Process.Kill()
				<-done
				t.Error("isolated runtime exceeded fixture stop deadline")
			}
		}
		<-readDone
	}
	t.Cleanup(stop)
	timer := time.NewTimer(30 * time.Second)
	defer timer.Stop()
	for {
		select {
		case line := <-lines:
			line = strings.TrimSpace(line)
			if value, ok := strings.CutPrefix(line, "ABSTRACTION_RUNTIME_ENDPOINT="); ok {
				o.endpoint = value
			}
			if o.endpoint != "" {
				return stop
			}
		case err := <-done:
			stopped = true
			<-readDone
			t.Fatalf("runtime startup: %v\n%s", err, diagnostics.String())
		case <-timer.C:
			t.Fatalf("runtime readiness timeout\n%s", diagnostics.String())
		}
	}
}

func runtimeExpectation(t *testing.T, program string) listen.ServerExpectation {
	t.Helper()
	who, err := identity.Self()
	if err != nil {
		t.Fatal(err)
	}
	return listen.ServerExpectation{Principal: who, Program: filepath.Clean(program)}
}

func currentAccount(t *testing.T) string {
	t.Helper()
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	return account.Uid
}

func runCLI(t *testing.T, program, prefix string, stdin string, args ...string) (string, string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, program, append([]string{prefix}, args...)...)
	cmd.Stdin = strings.NewReader(stdin)
	var out, diagnostics bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &diagnostics
	err := cmd.Run()
	return out.String(), diagnostics.String(), err
}

func runProvider(t *testing.T, program string, args ...string) (string, error) {
	out, errout, err := runCLI(t, program, "provider", "", args...)
	return out + errout, err
}
func runInference(t *testing.T, program string, args ...string) (string, error) {
	out, errout, err := runCLI(t, program, "inference", "", args...)
	return out + errout, err
}
func runCredentials(t *testing.T, program, stdin string, args ...string) (string, string, error) {
	return runCLI(t, program, "credentials", stdin, args...)
}

func samePrograms(a, b string) bool { return identity.SameSubjectProgram(a, b) }

func requireUserScopeAdd(t *testing.T, err error, transcript string) {
	t.Helper()
	if runtime.GOOS != "windows" && os.Geteuid() == 0 {
		if err == nil || !strings.Contains(transcript, "no_secure_store") {
			t.Fatalf("machine-scope credential add: %v\n%s", err, transcript)
		}
		t.Skip("machine-scope credentials refused; remainder requires user account")
	}
	if err != nil {
		t.Fatalf("credentials add: %v\n%s", err, transcript)
	}
}

func copyTestBinary(t *testing.T, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	name += goExecutableSuffix(t)
	target := filepath.Join(t.TempDir(), name)
	raw, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, raw, 0700); err != nil {
		t.Fatal(err)
	}
	return fixture.Program(target)
}

func goExecutableSuffix(t *testing.T) string {
	t.Helper()
	suffix, err := exec.Command("go", "env", "GOEXE").Output()
	if err != nil {
		t.Fatalf("go env GOEXE: %v", err)
	}
	return strings.TrimSpace(string(suffix))
}

func probeBinary(t *testing.T) string { return buildRuntime(t, "openabstractions-probe") }
func probeAs(t *testing.T, bin string, o *runtimeFixture, args ...string) (struct{ Outcome string }, int) {
	t.Helper()
	cmd := exec.Command(bin, append(args, "--endpoint", o.endpoint, "--runtime-program", o.program, "--json")...)
	raw, err := cmd.Output()
	code := 0
	if exit, ok := err.(*exec.ExitError); ok {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var result struct{ Outcome string }
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("probe output %s: %v", raw, err)
	}
	return result, code
}

func pendingFirstUse(t *testing.T, o *runtimeFixture) []askclient.RecordMetadata {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operator, err := client.NewVerified(o.endpoint, runtimeExpectation(t, o.program)).ResolveAsksOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	page, err := operator.ListQuestionsContext(ctx, "", 64)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("questions %+v %v", page, err)
	}
	var pending []askclient.RecordMetadata
	for _, r := range page.Records {
		if _, action, _, ok := asks.FirstUse(r.Key, r.About, r.Text); r.Key == asks.FirstUseKey && r.Option == "" && !(ok && action == asks.AnswerAction) {
			pending = append(pending, r)
		}
	}
	return pending
}

func setCompleteRule(t *testing.T, endpoint, program, host string, permit bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	operator, err := client.New(endpoint).ResolveRightsOperator(ctx, local)
	if err != nil {
		t.Fatal(err)
	}
	page, err := operator.ListPolicyContext(ctx, "", 1)
	if err != nil || page.Outcome.String() != "page" {
		t.Fatalf("list policy %+v %v", page, err)
	}
	rule := rights.PolicyRule{Subject: rights.Subject{Account: currentAccount(t), Program: program}, Action: inference.ActionComplete, Resource: inference.ResourceHost(host), Permit: permit}
	edit, err := operator.SetRuleContext(ctx, page.Revision, rule)
	if err != nil || edit.Outcome.String() != "applied" {
		t.Fatalf("set rule %+v %v", edit, err)
	}
}

func eventually(t *testing.T, within time.Duration, what string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen within %v", what, within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func answers(record string) bool {
	raw, err := os.ReadFile(record)
	if err != nil {
		return false
	}
	conn, err := listen.Dial(string(raw))
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}
func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

// Probe the real shared transport before selecting platform assertions.
func statusTransportProvesProgram(t *testing.T) bool {
	t.Helper()
	endpoint := fixture.Endpoint("adopter-program-proof")
	listener, err := listen.Listen(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	stop := context.AfterFunc(ctx, func() { listener.Close() })
	defer stop()
	done := make(chan error, 1)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer conn.Close()
		call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1024)
		if call != nil {
			defer call.Close()
		}
		done <- err
	}()
	_ = (listen.FrameClient{Endpoint: endpoint, Timeout: 5 * time.Second}).WriteFrameContext(ctx, []byte("proof probe"))
	select {
	case err := <-done:
		if err == nil {
			return true
		}
		if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
			return false
		}
		t.Fatal(err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	return false
}

type hostedUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	chat []string
}

func newHostedUpstream(t *testing.T) *hostedUpstream {
	t.Helper()
	u := &hostedUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorized := r.Header.Get("Authorization") == "Bearer "+testCredentialSecret
		if r.Method == http.MethodPost {
			u.mu.Lock()
			u.chat = append(u.chat, r.Header.Get("Authorization"))
			u.mu.Unlock()
		}
		if !authorized {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Method == http.MethodGet {
			w.Write([]byte(`{"data":[{"id":"anthropic/claude-sonnet-5"}]}`))
			return
		}
		for _, c := range []string{"Hel", "lo", "!"} {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", c)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":3}}\n\ndata: [DONE]\n\n")
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *hostedUpstream) authorizations() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.chat...)
}

func fakeOllama(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var chats atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"models":[{"name":"fixture-chat:1b"}]}`))
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"models":[]}`)) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		chats.Add(1)
		io.Copy(io.Discard, r.Body)
		for _, word := range strings.SplitAfter("Hello through the runtime", " ") {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", word)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n")
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, &chats
}
