package gateway_test

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

	dir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	gatewayBinary := filepath.Join(dir, "opencode-gateway"+suffix)
	buildFixture(t, gatewayBinary)

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
			arguments = `{"request_key":"oc-adopter-1","profile":"image_batch","model":"fixture/image","prompt":"draw","hosting":"hosted","credential":"fixture","count":1}`
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
	cfg := fixtureConfig{
		runtimeEndpoint: fixtureEndpoint(t, "opencode-runtime"), logEndpoint: fixtureEndpoint(t, "opencode-log"), configEndpoint: fixtureEndpoint(t, "opencode-config"),
		routerEndpoint: fixtureEndpoint(t, "opencode-router"), inferenceEndpoint: fixtureEndpoint(t, "opencode-inference"), jobEndpoint: fixtureEndpoint(t, "opencode-jobs"),
		jobRoot: filepath.Join(dir, "jobs"), allowed: map[string]bool{filepath.Clean(gatewayBinary): true}, prepares: &prepares, executorEnabled: &executorEnabled, upstream: oaProvider.URL,
	}
	fixture := startFixture(t, cfg)
	defer fixture.stop(t)

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
			"type": "local", "command": []string{gatewayBinary, "--runtime", cfg.runtimeEndpoint, "--state-dir", state}, "enabled": true,
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
	if strings.Contains(string(output), "fixture-secret") || strings.Contains(joined, "fixture-secret") {
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
