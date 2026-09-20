package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestOpenCodeUsesBoundBrowserContext is opt-in because it runs the official
// OpenCode CLI against an already-authorized disposable ComfyUI browser.
func TestOpenCodeUsesBoundBrowserContext(t *testing.T) {
	opencode := os.Getenv("OA_OPENCODE_BINARY")
	endpoint := os.Getenv("OA_COMFY_BRIDGE_URL")
	tokenFile := os.Getenv("OA_COMFY_BRIDGE_TOKEN_FILE")
	oaEndpoint := os.Getenv("OA_COMFY_OA_ENDPOINT")
	oaRuntimeProgram := os.Getenv("OA_COMFY_OA_RUNTIME_PROGRAM")
	serverOverride := os.Getenv("OA_COMFY_SERVER_BINARY")
	nodeID, err := strconv.ParseInt(os.Getenv("OA_COMFY_NODE_ID"), 10, 64)
	if opencode == "" || endpoint == "" || tokenFile == "" || err != nil {
		t.Skip("set OA_OPENCODE_BINARY, OA_COMFY_BRIDGE_URL, OA_COMFY_BRIDGE_TOKEN_FILE and OA_COMFY_NODE_ID")
	}
	token, err := os.ReadFile(tokenFile)
	if err != nil {
		t.Fatal(err)
	}
	token = bytes.TrimSpace(token)
	if len(token) < 32 {
		t.Fatal("bridge credential is invalid")
	}
	if (oaEndpoint == "") != (oaRuntimeProgram == "") {
		t.Fatal("OA_COMFY_OA_ENDPOINT and OA_COMFY_OA_RUNTIME_PROGRAM must be set together")
	}
	oaMode := oaEndpoint != ""

	dir := t.TempDir()
	serverBinary := serverOverride
	if oaMode && serverBinary == "" {
		t.Fatal("OA_COMFY_SERVER_BINARY is required in OA mode so policy can bind its exact program")
	}
	if serverBinary == "" {
		serverBinary = filepath.Join(dir, "comfy-mcp.exe")
		build := exec.Command("go", "build", "-o", serverBinary, ".")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build server: %v\n%s", err, output)
		}
	} else if !filepath.IsAbs(serverBinary) {
		t.Fatal("OA_COMFY_SERVER_BINARY must be an absolute path")
	} else if info, err := os.Stat(serverBinary); err != nil || info.IsDir() {
		t.Fatalf("OA_COMFY_SERVER_BINARY is unavailable: %v", err)
	}

	var mu sync.Mutex
	var requestBodies [][]byte
	model := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"object":"list","data":[{"id":"tool-model","object":"model","created":1,"owned_by":"fixture"}]}`)
			return
		}
		raw, _ := readBounded(r, 1<<20)
		mu.Lock()
		requestBodies = append(requestBodies, raw)
		mu.Unlock()
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
			http.Error(w, err.Error(), 400)
			return
		}
		var toolContents []json.RawMessage
		for _, message := range request.Messages {
			if message.Role == "tool" {
				toolContents = append(toolContents, message.Content)
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		if len(toolContents) >= 2 {
			if !containsField(toolContents[len(toolContents)-1], "outcome", "proposed") {
				http.Error(w, "preview result missing", 400)
				return
			}
			writeFixtureChunk(w, map[string]any{"role": "assistant", "content": "Bound Comfy preview received"}, "stop")
			fmt.Fprint(w, "data: [DONE]\n\n")
			return
		}
		wanted := "comfy_read_context"
		arguments := map[string]any{}
		if len(toolContents) == 1 {
			binding, ok := extractBinding(toolContents[0])
			if !ok {
				http.Error(w, "context binding missing", 400)
				return
			}
			wanted = "comfy_preview_parameter"
			arguments = map[string]any{"instance": binding["instance"], "context": binding["context"], "revision": binding["revision"], "node_id": nodeID, "widget": "steps", "value": 24}
			if oaMode {
				if binding["application_instance"] == "" {
					http.Error(w, "OA application instance binding missing", 400)
					return
				}
				arguments["application_instance"] = binding["application_instance"]
			}
		}
		toolName := ""
		for _, tool := range request.Tools {
			if strings.HasSuffix(tool.Function.Name, wanted) {
				toolName = tool.Function.Name
				break
			}
		}
		if toolName == "" {
			http.Error(w, "tool missing: "+wanted, 400)
			return
		}
		argumentBytes, _ := json.Marshal(arguments)
		writeFixtureChunk(w, map[string]any{"role": "assistant", "tool_calls": []any{map[string]any{"index": 0, "id": "call_comfy", "type": "function", "function": map[string]any{"name": toolName, "arguments": string(argumentBytes)}}}}, "tool_calls")
		fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer model.Close()

	project := filepath.Join(dir, "project")
	if err := os.MkdirAll(project, 0o755); err != nil {
		t.Fatal(err)
	}
	serverArguments := []string{serverBinary, "--mode", "fixture", "--comfy-url", endpoint}
	if oaMode {
		serverArguments = []string{serverBinary, "--mode", "oa", "--comfy-url", endpoint, "--oa-endpoint", oaEndpoint, "--oa-runtime-program", oaRuntimeProgram}
	}
	config := map[string]any{
		"$schema":  "https://opencode.ai/config.json",
		"provider": map[string]any{"fixture": map[string]any{"npm": "@ai-sdk/openai-compatible", "name": "Fixture", "options": map[string]any{"baseURL": model.URL + "/v1", "apiKey": "fixture"}, "models": map[string]any{"tool-model": map[string]any{"name": "Tool Model", "limit": map[string]any{"context": 32768, "output": 4096}}}}},
		"mcp":      map[string]any{"comfy": map[string]any{"type": "local", "command": serverArguments, "enabled": true}},
		"model":    "fixture/tool-model",
	}
	configBytes, _ := json.MarshalIndent(config, "", "  ")
	if err := os.WriteFile(filepath.Join(project, "opencode.json"), configBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, opencode, "--pure", "run", "--model", "fixture/tool-model", "--format", "json", "--dir", project, "Read the current Comfy context and preview node steps at 24 using the supplied tools.")
	command.Dir = project
	command.Env = isolatedEnv(t, dir, string(token))
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("OpenCode timed out: %v\n%s", ctx.Err(), output)
	}
	if err != nil {
		t.Fatalf("OpenCode: %v\n%s", err, output)
	}
	mu.Lock()
	joined := joinBodies(requestBodies)
	mu.Unlock()
	if !strings.Contains(string(output), "Bound Comfy preview received") || !strings.Contains(joined, "comfy_read_context") || !strings.Contains(joined, "comfy_preview_parameter") {
		t.Fatalf("OpenCode did not complete bound preview:\n%s\n%s", output, joined)
	}
	if strings.Contains(string(output), string(token)) || strings.Contains(joined, string(token)) {
		t.Fatal("bridge credential leaked")
	}
}

func TestExtractBindingIncludesOAApplicationInstance(t *testing.T) {
	raw := json.RawMessage(`{"structuredContent":{"outcome":"current","application_instance":"oa-1","instance":"page-1","context":"workflow-1","revision":"7"}}`)
	binding, ok := extractBinding(raw)
	if !ok || binding["application_instance"] != "oa-1" || binding["instance"] != "page-1" {
		t.Fatalf("OA binding = %#v, %v", binding, ok)
	}
}

func readBounded(r *http.Request, limit int64) ([]byte, error) {
	return io.ReadAll(io.LimitReader(r.Body, limit))
}

func writeFixtureChunk(w http.ResponseWriter, delta map[string]any, finish string) {
	payload := map[string]any{"id": "chatcmpl-comfy", "object": "chat.completion.chunk", "created": 1, "model": "tool-model", "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
	raw, _ := json.Marshal(payload)
	fmt.Fprintf(w, "data: %s\n\n", raw)
}

func extractBinding(raw json.RawMessage) (map[string]string, bool) {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil, false
	}
	return findBinding(value)
}

func findBinding(value any) (map[string]string, bool) {
	switch current := value.(type) {
	case map[string]any:
		instance, iok := current["instance"].(string)
		contextName, cok := current["context"].(string)
		revision, rok := current["revision"].(string)
		if iok && cok && rok {
			binding := map[string]string{"instance": instance, "context": contextName, "revision": revision}
			if applicationInstance, ok := current["application_instance"].(string); ok {
				binding["application_instance"] = applicationInstance
			}
			return binding, true
		}
		for _, nested := range current {
			if found, ok := findBinding(nested); ok {
				return found, true
			}
		}
	case []any:
		for _, nested := range current {
			if found, ok := findBinding(nested); ok {
				return found, true
			}
		}
	case string:
		var nested any
		if json.Unmarshal([]byte(current), &nested) == nil {
			return findBinding(nested)
		}
	}
	return nil, false
}

func containsField(raw json.RawMessage, key, expected string) bool {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	return findField(value, key, expected)
}

func findField(value any, key, expected string) bool {
	switch current := value.(type) {
	case map[string]any:
		if current[key] == expected {
			return true
		}
		for _, nested := range current {
			if findField(nested, key, expected) {
				return true
			}
		}
	case []any:
		for _, nested := range current {
			if findField(nested, key, expected) {
				return true
			}
		}
	case string:
		var nested any
		if json.Unmarshal([]byte(current), &nested) == nil {
			return findField(nested, key, expected)
		}
	}
	return false
}

func isolatedEnv(t *testing.T, root, token string) []string {
	overrides := map[string]string{"XDG_CONFIG_HOME": filepath.Join(root, "config"), "XDG_DATA_HOME": filepath.Join(root, "data"), "XDG_CACHE_HOME": filepath.Join(root, "cache"), "XDG_STATE_HOME": filepath.Join(root, "state"), "NO_COLOR": "1", "OA_COMFY_BRIDGE_TOKEN": token}
	for key, path := range overrides {
		if strings.HasPrefix(key, "XDG_") {
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

func joinBodies(values [][]byte) string {
	parts := make([]string, len(values))
	for i := range values {
		parts[i] = string(values[i])
	}
	return strings.Join(parts, "\n")
}
