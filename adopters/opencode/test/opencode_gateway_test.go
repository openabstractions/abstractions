// Black-box OpenCode harness for the MCP gateway: it builds the gateway
// program from mcp-gateway/, composes the gateway's fixture OA runtime
// (mcp-gateway/fixture) in process and runs a real OpenCode CLI against it.
// Run the six-file package through the existing gateway module with GOWORK=off;
// test/README.md gives the complete command and real-process prerequisites.
package opencode_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
)

// TestOpenCodeInvokesGatewayInventory is opt-in because it exercises a real,
// separately distributed adopter. OA_OPENCODE_BINARY must name an OpenCode CLI
// binary. The test confines OpenCode's config, data, cache and state to TempDir
// and gives it only local fixture providers.
func TestOpenCodeInvokesGatewayInventory(t *testing.T) {
	opencode := os.Getenv("OA_OPENCODE_BINARY")
	if opencode == "" {
		t.Skip("set OA_OPENCODE_BINARY to run the real OpenCode adopter proof")
	}
	if _, err := os.Stat(opencode); err != nil {
		t.Fatalf("OpenCode binary: %v", err)
	}
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof is deliberately unavailable on Darwin")
	}
	if err := identity.CanEver(listen.Program); err != nil {
		t.Skipf("native Program proof unavailable: %v", err)
	}

	dir := t.TempDir()
	gatewayBinary := fixture.BuildGateway(t, dir, "opencode-gateway")

	var modelRequests atomic.Int64
	var mu sync.Mutex
	var requestBodies [][]byte
	agentModel := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"tool-model","object":"model","created":1,"owned_by":"fixture"}]}`)
			return
		}
		body := new(bytes.Buffer)
		_, _ = body.ReadFrom(r.Body)
		raw := append([]byte(nil), body.Bytes()...)

		var request struct {
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.Unmarshal(raw, &request); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if len(request.Tools) == 0 {
			w.Header().Set("Content-Type", "text/event-stream")
			writeChatChunk(w, map[string]any{"role": "assistant", "content": "OA gateway exercise"}, nil)
			writeChatChunk(w, map[string]any{}, "stop")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		mu.Lock()
		requestBodies = append(requestBodies, raw)
		mu.Unlock()
		modelRequests.Add(1)
		toolResults := 0
		var toolContent []byte
		for _, message := range request.Messages {
			if message.Role == "tool" {
				toolResults++
				toolContent = append(toolContent, message.Content...)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if toolResults >= 6 {
			writeChatChunk(w, map[string]any{"role": "assistant", "content": "OA inventory received"}, nil)
			writeChatChunk(w, map[string]any{}, "stop")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		wanted, arguments := "oa_models_list", `{"fresh":true}`
		switch toolResults {
		case 1:
			wanted = "oa_inference_complete"
			arguments = `{"model":"fixture/chat-small","prompt":"hello","max_output":8,"hosting":"hosted","credential":"fixture"}`
		case 2:
			wanted = "oa_inference_job_submit"
			arguments = `{"request_key":"oc-adopter-1","profile":"video","model":"fixture/video","prompt":"draw","hosting":"hosted","credential":"fixture","duration_ms":1000}`
		case 3, 4:
			wanted = "oa_inference_job_status"
			if toolResults == 4 {
				wanted = "oa_inference_job_cancel"
			}
			handle := regexp.MustCompile(`[A-Za-z0-9_-]{32}`).Find(toolContent)
			if len(handle) != 32 {
				http.Error(w, "OpenCode request omitted accepted job handle", http.StatusBadRequest)
				return
			}
			arguments = fmt.Sprintf(`{"handle":%q}`, handle)
		case 5:
			wanted = "oa_inference_job_cancel"
			arguments = `{"handle":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"}`
		}
		toolName := ""
		for _, tool := range request.Tools {
			if strings.HasSuffix(tool.Function.Name, wanted) {
				toolName = tool.Function.Name
				break
			}
		}
		if toolName == "" {
			http.Error(w, "OpenCode request omitted "+wanted, http.StatusBadRequest)
			return
		}
		writeChatChunk(w, map[string]any{
			"role": "assistant",
			"tool_calls": []any{map[string]any{
				"index": 0, "id": "call_inventory", "type": "function",
				"function": map[string]any{"name": toolName, "arguments": arguments},
			}},
		}, nil)
		writeChatChunk(w, map[string]any{}, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer agentModel.Close()

	var providerGets, providerPosts atomic.Int64
	oaProvider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			providerPosts.Add(1)
			w.Header().Set("Content-Type", "text/event-stream")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"opencode-ok\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
			return
		}
		providerGets.Add(1)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"id":"fixture/chat-small"}]}`)
	}))
	defer oaProvider.Close()

	var prepares atomic.Int64
	var executorEnabled atomic.Bool
	cfg := fixture.NewConfig("opencode")
	cfg.JobRoot, cfg.Allowed, cfg.Prepares, cfg.ExecutorEnabled, cfg.Upstream = filepath.Join(dir, "jobs"), []string{gatewayBinary}, &prepares, &executorEnabled, oaProvider.URL
	oa := fixture.Start(t, cfg)
	defer oa.Stop(t)

	project := filepath.Join(dir, "project")
	state := filepath.Join(dir, "gateway-state")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	config := map[string]any{
		"$schema": "https://opencode.ai/config.json",
		"provider": map[string]any{"fixture": map[string]any{
			"npm": "@ai-sdk/openai-compatible", "name": "Fixture",
			"options": map[string]any{"baseURL": agentModel.URL + "/v1", "apiKey": "fixture"},
			"models":  map[string]any{"tool-model": map[string]any{"name": "Tool Model", "limit": map[string]any{"context": 32768, "output": 4096}}},
		}},
		"mcp": map[string]any{"openabstractions": map[string]any{
			"type": "local", "command": []string{gatewayBinary, "--runtime", cfg.RuntimeEndpoint, "--state-dir", state}, "enabled": true,
		}},
		"model": "fixture/tool-model",
	}
	configBytes, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), configBytes, 0o600); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, opencode, "--pure", "run", "--model", "fixture/tool-model", "--format", "json", "--dir", project,
		"Exercise the curated OpenAbstractions tools as instructed by the deterministic local fixture, then say OA inventory received.")
	cmd.Dir = project
	cmd.Env = isolatedOpenCodeEnv(t, dir)
	output, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("OpenCode timed out: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("OpenCode run: %v\n%s\nmodel requests:\n%s", err, output, joinedBodies(requestBodies))
	}
	mu.Lock()
	requests := append([][]byte(nil), requestBodies...)
	mu.Unlock()
	joined := joinedBodies(requests)
	if modelRequests.Load() < 7 || providerGets.Load() != 2 || providerPosts.Load() != 1 {
		t.Fatalf("expected the complete model/tool exchange and bounded OA effects: model=%d gets=%d posts=%d\n%s\n%s", modelRequests.Load(), providerGets.Load(), providerPosts.Load(), output, joined)
	}
	if !strings.Contains(joined, "oa_models_list") || !strings.Contains(joined, "fixture/chat-small") ||
		!strings.Contains(joined, "oa_inference_complete") || !strings.Contains(joined, "opencode-ok") ||
		!strings.Contains(joined, "oa_inference_job_submit") || !strings.Contains(joined, `\"outcome\":\"accepted\"`) ||
		!strings.Contains(joined, "oa_inference_job_status") || !strings.Contains(joined, `\"state\":\"pending\"`) ||
		!strings.Contains(joined, "oa_inference_job_cancel") || !strings.Contains(joined, `\"outcome\":\"requested\"`) ||
		!strings.Contains(joined, "unknown handle") {
		t.Fatalf("OpenCode did not expose and return all curated structured results:\n%s\n%s", output, joined)
	}
	if strings.Contains(string(output), fixture.Secret) || strings.Contains(joined, fixture.Secret) {
		t.Fatalf("service credential leaked into adopter-visible data:\n%s\n%s", output, joined)
	}
	if !strings.Contains(string(output), "OA inventory received") {
		t.Fatalf("OpenCode did not finish after the tool result:\n%s", output)
	}
}

func writeChatChunk(w http.ResponseWriter, delta map[string]any, finish any) {
	payload := map[string]any{
		"id": "chatcmpl-fixture", "object": "chat.completion.chunk", "created": 1, "model": "tool-model",
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	raw, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", raw)
}

func isolatedOpenCodeEnv(t *testing.T, root string) []string {
	t.Helper()
	overrides := map[string]string{
		"XDG_CONFIG_HOME": filepath.Join(root, "xdg-config"),
		"XDG_DATA_HOME":   filepath.Join(root, "xdg-data"),
		"XDG_CACHE_HOME":  filepath.Join(root, "xdg-cache"),
		"XDG_STATE_HOME":  filepath.Join(root, "xdg-state"),
		"NO_COLOR":        "1",
	}
	for _, path := range overrides {
		if strings.Contains(path, string(filepath.Separator)) {
			if err := os.MkdirAll(path, 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	fixture.CopyPreparedOpenCodeBootstrap(t, overrides["XDG_CONFIG_HOME"], overrides["XDG_CACHE_HOME"])
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

func joinedBodies(bodies [][]byte) string {
	parts := make([]string, len(bodies))
	for i := range bodies {
		parts[i] = string(bodies[i])
	}
	return strings.Join(parts, "\n")
}

// TestOpenCodeVendorBusyWake observes a real OpenCode model request after a
// local fixture tool delivers a watch page. Only the watch peer is inert.
func TestOpenCodeVendorBusyWake(t *testing.T) {
	opencode := os.Getenv("OA_OPENCODE_BINARY")
	cache := os.Getenv("OA_OPENCODE_BOOTSTRAP_CACHE_SOURCE")
	bootstrap := os.Getenv("OA_OPENCODE_BOOTSTRAP_CONFIG_SOURCE")
	if opencode == "" || cache == "" || bootstrap == "" {
		t.Skip("set OA_OPENCODE_BINARY and the verified OA_OPENCODE_BOOTSTRAP_CONFIG_SOURCE and OA_OPENCODE_BOOTSTRAP_CACHE_SOURCE")
	}
	for _, input := range []string{opencode, bootstrap, cache} {
		if !filepath.IsAbs(input) {
			t.Fatalf("OpenCode fixture input must be absolute: %s", input)
		}
		if _, err := os.Stat(input); err != nil {
			t.Fatal(err)
		}
	}

	const hint = "OpenAbstractions inbox: 1 unread; 0 pending questions."
	var steps atomic.Int64
	var finalValidated atomic.Bool
	var advertisedMu sync.Mutex
	var advertised [][]string
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Messages json.RawMessage
			Tools    []struct {
				Function struct{ Name string }
			}
		}
		if r.Method != http.MethodPost || json.NewDecoder(r.Body).Decode(&request) != nil {
			http.Error(w, "expected a local completion request", http.StatusBadRequest)
			return
		}
		names := make([]string, 0, len(request.Tools))
		for _, candidate := range request.Tools {
			names = append(names, candidate.Function.Name)
		}
		advertisedMu.Lock()
		advertised = append(advertised, names)
		advertisedMu.Unlock()
		nextTool, arguments := "", "{}"
		final := false
		if len(request.Tools) > 0 {
			step := steps.Add(1)
			got := strings.Count(string(request.Messages), hint)
			want := 0
			switch step {
			case 1:
				nextTool = "fixture_deliver"
			case 2:
				want, nextTool, arguments = 1, "oa_messages_wait", `{"wait_ms":0}`
			case 3:
				if !strings.Contains(string(request.Messages), "fixture-cursor") {
					http.Error(w, "the model did not receive the inbox read", http.StatusBadRequest)
					return
				}
				final = true
			default:
				http.Error(w, "unexpected extra model step", http.StatusBadRequest)
				return
			}
			if got != want {
				http.Error(w, fmt.Sprintf("step %d: got %d busy hints, want %d", step, got, want), http.StatusBadRequest)
				return
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if nextTool == "" {
			response := "vendor-wake-auxiliary"
			if final {
				finalValidated.Store(true)
				response = "vendor-wake-complete"
			}
			writeChatChunk(w, map[string]any{"role": "assistant", "content": response}, nil)
			writeChatChunk(w, map[string]any{}, "stop")
		} else {
			for _, candidate := range request.Tools {
				if candidate.Function.Name != nextTool {
					continue
				}
				writeChatChunk(w, map[string]any{
					"role": "assistant", "tool_calls": []any{map[string]any{
						"index": 0, "id": fmt.Sprintf("call_%s", nextTool), "type": "function",
						"function": map[string]any{"name": nextTool, "arguments": arguments},
					}},
				}, nil)
				writeChatChunk(w, map[string]any{}, "tool_calls")
				fmt.Fprint(w, "data: [DONE]\n\n")
				return
			}
			http.Error(w, fmt.Sprintf("fixture tool missing: %s (available: %s)", nextTool, strings.Join(names, ", ")), http.StatusBadRequest)
			return
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer model.Close()

	provider := filepath.Dir(stageOpenCodeProvider(t))
	for _, dependency := range []string{"@opencode-ai/plugin", "zod", "@openabstractions/facade"} {
		if _, err := os.Stat(filepath.Join(provider, "node_modules", dependency, "package.json")); err != nil {
			t.Fatalf("staged busy wake plugin dependency %s: %v", dependency, err)
		}
	}
	// The vendor resolves the containing package's server export even for a
	// file URL, so the fixture must own its manifest inside the staged tree.
	plugin := filepath.Join(provider, "vendor-busy-wake")
	if err := os.Mkdir(plugin, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := []byte(`{"type":"module","exports":{"./server":"./server.mjs"}}`)
	if err := os.WriteFile(filepath.Join(plugin, "package.json"), manifest, 0o600); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(adopterRoot(t), "adopters", "opencode", "test", "fixtures", "vendor-busy-wake.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(plugin, "server.mjs"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	project, home := t.TempDir(), t.TempDir()
	config := map[string]any{
		"provider": map[string]any{"fixture": map[string]any{
			"npm": "@ai-sdk/openai-compatible", "name": "Fixture",
			"options": map[string]any{"baseURL": model.URL + "/v1", "apiKey": "fixture"},
			"models":  map[string]any{"tool-model": map[string]any{"name": "Fixture", "tool_call": true, "limit": map[string]int{"context": 32768, "output": 4096}}},
		}},
		"plugin":     []string{fileURL(plugin)},
		"permission": map[string]string{"*": "deny", "fixture_deliver": "allow", "oa_messages_wait": "allow"},
	}
	raw, err = json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	// The vendor waits for its config dependency check before importing a
	// local plugin. Supply the frozen package tree so that check skips npm.
	isolated := isolatedOpenCodeEnv(t, home)
	requireVendorWakeBootstrap(t, filepath.Join(home, "xdg-config", "opencode"))
	env := environmentWithout(isolated, "HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA",
		"OPENCODE_CONFIG", "OPENCODE_CONFIG_DIR", "OPENCODE_CONFIG_CONTENT", "OPENCODE_DISABLE_PROJECT_CONFIG", "OPENCODE_TEST_HOME",
		"ABSTRACTION_RUNTIME_ENDPOINT", "NODE_OPTIONS", "BUN_OPTIONS", "OPENCODE_PURE", "OPENCODE_DISABLE_AUTOUPDATE",
		"OPENCODE_DISABLE_MODELS_FETCH", "OPENCODE_DISABLE_DEFAULT_PLUGINS", "OPENCODE_DISABLE_EXTERNAL_SKILLS", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS")
	env = append(env, "HOME="+home, "USERPROFILE="+home, "APPDATA="+filepath.Join(home, "roaming"),
		"LOCALAPPDATA="+filepath.Join(home, "local"), "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_MODELS_FETCH=1",
		"OPENCODE_DISABLE_DEFAULT_PLUGINS=1", "OPENCODE_DISABLE_EXTERNAL_SKILLS=1", "OPENCODE_DISABLE_CLAUDE_CODE_SKILLS=1",
		"OPENCODE_TEST_HOME="+home, "OPENCODE_CONFIG="+filepath.Join(project, "opencode.json"), "OPENCODE_DISABLE_PROJECT_CONFIG=1")
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	// OpenCode 1.18.34's --pure omits every configured external plugin.
	cmd := exec.CommandContext(ctx, opencode, "run", "--print-logs", "--log-level", "DEBUG", "--model", "fixture/tool-model", "--format", "json", "--dir", project,
		"Run the local busy wake fixture.")
	cmd.Dir, cmd.Env = project, env
	output, err := cmd.CombinedOutput()
	registered, registrationErr := os.ReadFile(filepath.Join(plugin, "registered"))
	loaded := registrationErr == nil && string(registered) == "registered"
	advertisedMu.Lock()
	t.Logf("vendor busy wake: fixture_loaded=%t, advertised_tool_names=%v", loaded, advertised)
	advertisedMu.Unlock()
	if !loaded || err != nil || steps.Load() != 3 || !finalValidated.Load() {
		t.Fatalf("vendor busy wake: steps=%d err=%v\n%s", steps.Load(), err, output)
	}
}

func requireVendorWakeBootstrap(t *testing.T, directory string) {
	t.Helper()
	const plugin, version = "@opencode-ai/plugin", "1.18.34"
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatalf("staged vendor bootstrap config: %v", err)
	}
	if len(entries) != 3 || entries[0].Name() != "node_modules" || !entries[0].IsDir() ||
		entries[1].Name() != "package-lock.json" || entries[2].Name() != "package.json" {
		t.Fatal("vendor bootstrap config must hold only its locked dependency tree")
	}
	type packageInfo struct {
		Version      string
		Dependencies map[string]string
	}
	read := func(name string, target any) {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			t.Fatalf("vendor bootstrap %s: %v", name, err)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatalf("vendor bootstrap %s: %v", name, err)
		}
	}
	var requested, installed packageInfo
	var lock struct{ Packages map[string]packageInfo }
	read("package.json", &requested)
	read("package-lock.json", &lock)
	read("node_modules/"+plugin+"/package.json", &installed)
	if len(requested.Dependencies) != 1 || requested.Dependencies[plugin] != version ||
		lock.Packages[""].Dependencies[plugin] != version || lock.Packages["node_modules/"+plugin].Version != version || installed.Version != version {
		t.Fatalf("staged bootstrap must contain the declared, locked, installed %s %s", plugin, version)
	}
}
