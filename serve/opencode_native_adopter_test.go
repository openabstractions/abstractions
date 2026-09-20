package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
)

const openCodeProviderFixtureArg = "-oa-opencode-provider-fixture"

func init() {
	if len(os.Args) == 7 && os.Args[1] == openCodeProviderFixtureArg {
		os.Exit(runOpenCodeProviderFixture(os.Args[2], os.Args[3], os.Args[4], os.Args[5], os.Args[6]))
	}
}

type openCodeOperation struct {
	Mode  string `json:"mode"`
	Label string `json:"label"`
}

type openCodeFixtureChat struct {
	dir   string
	label string
}

func (f openCodeFixtureChat) Start(request iwire.Request) (iwire.Admission, error) {
	raw, _ := json.Marshal(request)
	if file, err := os.OpenFile(filepath.Join(f.dir, "requests.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
		_, _ = file.Write(append(raw, '\n'))
		_ = file.Close()
	}
	mode := "tool"
	if len(request.Tools) == 0 {
		mode = "final"
	}
	if f.label == "incapable" && len(request.Tools) > 0 {
		return iwire.Admission{Outcome: iwire.StartOutcomeUnsupportedFeature,
			Reason: "feature:abstraction.inference/tools@1", Host: f.label, Model: request.Model}, nil
	}
	for _, message := range request.Messages {
		for _, part := range message.Parts {
			if part.Kind == iwire.PartKindToolResult {
				mode = "final"
			}
			if part.Kind == iwire.PartKindText && strings.Contains(part.Text, "OA_CANCEL_PROBE") {
				mode = "cancel"
			}
		}
	}
	operation := "oc-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	state, _ := json.Marshal(openCodeOperation{Mode: mode, Label: f.label})
	if err := os.WriteFile(filepath.Join(f.dir, operation+".json"), state, 0o600); err != nil {
		return iwire.Admission{}, err
	}
	return iwire.Admission{Outcome: iwire.StartOutcomeAccepted, Operation: operation, Host: f.label,
		Model: request.Model, RetentionMs: 60000, IdleMs: 10000, RetainedDeltas: 16}, nil
}

func (f openCodeFixtureChat) Observe(operation string, cursor, _, _, waitMS int64) (iwire.DeltaPage, error) {
	raw, err := os.ReadFile(filepath.Join(f.dir, operation+".json"))
	if err != nil {
		return iwire.DeltaPage{Outcome: iwire.PageOutcomeUnknown, Deltas: []iwire.Delta{}, Next: cursor}, nil
	}
	var state openCodeOperation
	if json.Unmarshal(raw, &state) != nil {
		return iwire.DeltaPage{}, errors.New("invalid fixture operation")
	}
	if cursor > 0 {
		if state.Mode == "cancel" {
			time.Sleep(time.Duration(min(waitMS, 50)) * time.Millisecond)
			return iwire.DeltaPage{Outcome: iwire.PageOutcomePage, Deltas: []iwire.Delta{}, Next: cursor}, nil
		}
		return iwire.DeltaPage{Outcome: iwire.PageOutcomePage, Deltas: []iwire.Delta{}, Next: cursor, AtEnd: true}, nil
	}
	if state.Mode == "cancel" {
		part := iwire.Part{Kind: iwire.PartKindText, Text: "waiting"}
		return iwire.DeltaPage{Outcome: iwire.PageOutcomePage,
			Deltas: []iwire.Delta{{Sequence: 0, Kind: iwire.DeltaKindPart, Index: 0, Part: &part}}, Next: 1}, nil
	}
	part := iwire.Part{Kind: iwire.PartKindText, Text: "native-oa-" + state.Label}
	stop := iwire.StopReasonEnd
	if state.Mode == "tool" {
		part = iwire.Part{Kind: iwire.PartKindToolCall, CallID: "oa-native-call", Name: "glob", Arguments: `{"pattern":"native-oa-never-match-*","path":"."}`}
		stop = iwire.StopReasonToolCalls
	}
	end := iwire.Reply{Outcome: iwire.ReplyOutcomeCompleted, Message: iwire.Message{Role: iwire.RoleAssistant, Parts: []iwire.Part{}},
		StopReason: stop, Usage: iwire.Usage{Input: 7, Output: 3}, Host: state.Label, Model: "fixture-model"}
	return iwire.DeltaPage{Outcome: iwire.PageOutcomePage, Deltas: []iwire.Delta{
		{Sequence: 0, Kind: iwire.DeltaKindPart, Index: 0, Part: &part},
		{Sequence: 1, Kind: iwire.DeltaKindEnd, End: &end},
	}, Next: 2, AtEnd: true}, nil
}

func (f openCodeFixtureChat) Cancel(operation string) (iwire.Cancellation, error) {
	_ = os.WriteFile(filepath.Join(f.dir, "cancelled-"+operation), nil, 0o600)
	outcome := iwire.ReplyOutcomeCancelled
	return iwire.Cancellation{Outcome: iwire.CancelOutcomeCancelled, ReplyOutcome: &outcome}, nil
}

func runOpenCodeProviderFixture(endpoint, dir, label, pidDir, brokerProgram string) int {
	if err := os.WriteFile(filepath.Join(pidDir, strconv.Itoa(os.Getpid())), nil, 0o600); err != nil {
		return 3
	}
	l, err := listen.Listen(listen.Endpoint(endpoint))
	if err != nil {
		return 4
	}
	time.AfterFunc(2*time.Minute, func() { os.Exit(5) })
	for {
		conn, err := l.Accept()
		if err != nil {
			return 6
		}
		go func() {
			defer conn.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 35*time.Second)
			defer cancel()
			call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if err != nil {
				return
			}
			peer, err := call.Peer()
			if err != nil {
				return
			}
			caller, err := identity.SubjectProgram(peer, listen.Program.Path)
			if err != nil || !samePrograms(caller, brokerProgram) {
				return
			}
			if response, err := (&iwire.ChatDispatcher{Handler: openCodeFixtureChat{dir: dir, label: label}}).ExchangeFrame(call.Frame); err == nil {
				_ = call.Reply(response)
			}
		}()
	}
}

func copyTree(t *testing.T, source, target string) {
	t.Helper()
	if err := filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destination := filepath.Join(target, rel)
		if entry.IsDir() {
			return os.MkdirAll(destination, 0o755)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		defer in.Close()
		out, err := os.OpenFile(destination, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, copyErr := io.Copy(out, in)
		closeErr := out.Close()
		return errors.Join(copyErr, closeErr)
	}); err != nil {
		t.Fatal(err)
	}
}

func fileURL(path string) string {
	path = filepath.ToSlash(path)
	if runtime.GOOS == "windows" {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

type openCodeProviderSources struct {
	provider  string
	facade    string
	inference string
	ipc       string
}

func findOpenCodeProviderSources(charterRoot string) (openCodeProviderSources, error) {
	var err error
	charterRoot, err = filepath.Abs(charterRoot)
	if err != nil {
		return openCodeProviderSources{}, fmt.Errorf("absolute charter root: %w", err)
	}
	sources := openCodeProviderSources{provider: filepath.Join(charterRoot, "adopters", "opencode")}
	if _, err := os.Stat(filepath.Join(sources.provider, "index.js")); err != nil {
		return openCodeProviderSources{}, fmt.Errorf("OpenCode provider source: %w", err)
	}

	// The private workspace keeps generated packages under openabstractions-flat.
	// A public workflow checks the layer repositories out beside the charter
	// repository, as the facade binding workflow does.
	layouts := [][3]string{
		{
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-facade", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-inference", "javascript"),
			filepath.Join(charterRoot, "openabstractions-flat", "abstraction-identity", "javascript"),
		},
		{
			filepath.Join(filepath.Dir(charterRoot), "abstraction-facade", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-inference", "javascript"),
			filepath.Join(filepath.Dir(charterRoot), "abstraction-identity", "javascript"),
		},
	}
	for _, layout := range layouts {
		complete := true
		for _, source := range layout {
			if _, err := os.Stat(filepath.Join(source, "package.json")); err != nil {
				complete = false
				break
			}
		}
		if complete {
			sources.facade, sources.inference, sources.ipc = layout[0], layout[1], layout[2]
			return sources, nil
		}
	}
	return openCodeProviderSources{}, fmt.Errorf("generated JavaScript packages absent from private workspace or public sibling checkouts")
}

func stageOpenCodeProviderFrom(t *testing.T, charterRoot string) string {
	t.Helper()
	sources, err := findOpenCodeProviderSources(charterRoot)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	provider := filepath.Join(root, "provider")
	copyTree(t, sources.provider, provider)
	modules := filepath.Join(provider, "node_modules", "@openabstractions")
	copyTree(t, sources.facade, filepath.Join(modules, "facade"))
	copyTree(t, sources.inference, filepath.Join(modules, "inference"))
	copyTree(t, sources.ipc, filepath.Join(modules, "ipc"))
	return filepath.Join(provider, "index.js")
}

func stageOpenCodeProvider(t *testing.T) string {
	t.Helper()
	return stageOpenCodeProviderFrom(t, filepath.Clean(".."))
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
	for _, fixture := range []struct{ repository, installed string }{
		{"abstraction-facade", "facade"},
		{"abstraction-inference", "inference"},
		{"abstraction-identity", "ipc"},
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
	} {
		marker := filepath.Join(filepath.Dir(provider), "node_modules", "@openabstractions", fixture.installed, "source-marker")
		data, err := os.ReadFile(marker)
		if err != nil || string(data) != fixture.repository+"\n" {
			t.Fatalf("staged %s from public sibling: data=%q err=%v", fixture.installed, data, err)
		}
	}
}

func isolatedOpenCodeEnvironment(t *testing.T, root, addon, library string) []string {
	t.Helper()
	overrides := map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg-config"), "XDG_DATA_HOME": filepath.Join(root, "xdg-data"),
		"XDG_CACHE_HOME": filepath.Join(root, "xdg-cache"), "XDG_STATE_HOME": filepath.Join(root, "xdg-state"),
		"ABSTRACTION_IPC_NODE": addon, "ABSTRACTION_IPC_LIBRARY": library, "NO_COLOR": "1",
	}
	for key, path := range overrides {
		if key != "NO_COLOR" {
			_ = os.MkdirAll(filepath.Dir(path), 0o755)
		}
	}
	env := make([]string, 0, len(os.Environ())+len(overrides))
	for _, item := range os.Environ() {
		key, _, _ := strings.Cut(item, "=")
		if _, replaced := overrides[strings.ToUpper(key)]; !replaced {
			env = append(env, item)
		}
	}
	for key, value := range overrides {
		env = append(env, key+"="+value)
	}
	return env
}

func TestOpenCodeUsesNativeOAProviderAndSurvivesProviderSubstitution(t *testing.T) {
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
	providerProgram := copyTestBinary(t, "oa-opencode-native-provider")
	providerState, pidDir := t.TempDir(), t.TempDir()
	name := "opencode-native"
	self, _ := os.Executable()

	declare := func(label string) {
		t.Helper()
		args := []string{"add", name, "--program", providerProgram, "--provider-endpoint", name,
			"--contract", inference.Contract, "--guarantee", fixtureGuarantee, "--guarantee", "abstraction.inference/local-only@1", "--model", "fixture-model",
			"--arg", openCodeProviderFixtureArg, "--arg", "{endpoint}", "--arg", providerState,
			"--arg", label, "--arg", pidDir, "--arg", self}
		if out, err := runProvider(t, append(args, endpointArgs...)...); err != nil {
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
	declare("one")
	t.Cleanup(func() {
		_, _ = runProvider(t, append([]string{"remove", name}, endpointArgs...)...)
	})

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
	run := func(prompt string) ([]byte, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, opencode, "--pure", "run", "--auto", "--print-logs", "--log-level", "DEBUG", "--model", "openabstractions/fixture-model", "--format", "json", "--dir", project, prompt)
		cmd.Dir, cmd.Env = project, env
		return cmd.CombinedOutput()
	}

	denied, _ := run("Use the available tools once, then report the native OA result.")
	if !strings.Contains(string(denied), "not_permitted") || !strings.Contains(string(denied), "rights:not_granted") {
		t.Fatalf("typed OA refusal missing:\n%s", denied)
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

	if out, err := runProvider(t, append([]string{"remove", name}, endpointArgs...)...); err != nil {
		t.Fatalf("remove first provider: %v\n%s", err, out)
	}
	eventually(t, 10*time.Second, "first provider stopping", func() bool { return !answers(name) })
	declare("incapable")
	incapable, _ := run("Use the available tools once, then report the native OA result.")
	if !strings.Contains(string(incapable), "unsupported_feature") ||
		!strings.Contains(string(incapable), "native:feature:abstraction.inference/tools@1") {
		t.Fatalf("native leaf typed refusal missing:\n%s", incapable)
	}
	if out, err := runProvider(t, append([]string{"remove", name}, endpointArgs...)...); err != nil {
		t.Fatalf("remove incapable provider: %v\n%s", err, out)
	}
	eventually(t, 10*time.Second, "incapable provider stopping", func() bool { return !answers(name) })
	declare("two")
	second, err := run("Use the available tools once, then report the native OA result.")
	if err != nil || !strings.Contains(string(second), "native-oa-two") {
		t.Fatalf("OpenCode substituted provider: %v\n%s", err, second)
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

	audit, err := runInference(t, append([]string{"audit", "--json"}, endpointArgs...)...)
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
