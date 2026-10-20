package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/openabstractions/abstractions/mcp-gateway/gateway"
)

func TestOpenCodeModelSchemaStartResumeAndReasoningConstraints(t *testing.T) {
	path := os.Getenv("OA_OPENCODE_MODEL_SCHEMAS_OUT")
	if path == "" {
		t.Skip("focused OpenCode hook owner supplies its actual model-facing schema capture")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var captures []struct {
		Compact bool `json:"compactTools"`
		Tools   []struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"inputSchema"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &captures); err != nil {
		t.Fatal(err)
	}
	if len(captures) != 2 || captures[0].Compact || !captures[1].Compact {
		t.Fatal("both host hook modes required")
	}
	cases := []struct {
		name, input string
		valid       bool
	}{
		{"start", `{"model":"fixture","prompt":"hello","hosting":"local"}`, true},
		{"reasoning-start", `{"model":"fixture","prompt":"hello","hosting":"local","include_reasoning":true,"request_key":"persisted"}`, true},
		{"resume", `{"operation_id":"operation","cursor":0}`, true},
		{"reasoning-resume", `{"operation_id":"operation","cursor":0,"include_reasoning":true}`, true},
		{"empty", `{}`, false},
		{"mixed-start-resume", `{"model":"fixture","prompt":"hello","hosting":"local","operation_id":"operation","cursor":0}`, false},
		{"resume-without-cursor", `{"operation_id":"operation"}`, false},
		{"start-with-cursor", `{"model":"fixture","prompt":"hello","hosting":"local","cursor":0}`, false},
		{"reasoning-without-key", `{"model":"fixture","prompt":"hello","hosting":"local","include_reasoning":true}`, false},
		{"key-without-reasoning", `{"model":"fixture","prompt":"hello","hosting":"local","request_key":"persisted"}`, false},
		{"key-with-disabled-reasoning", `{"model":"fixture","prompt":"hello","hosting":"local","request_key":"persisted","include_reasoning":false}`, false},
		{"resume-with-start-key", `{"operation_id":"operation","cursor":0,"request_key":"persisted","include_reasoning":true}`, false},
	}
	for _, capture := range captures {
		found := false
		for _, tool := range capture.Tools {
			if tool.Name != "oa_inference_complete" {
				continue
			}
			found = true
			var schema jsonschema.Schema
			if err := json.Unmarshal(tool.Schema, &schema); err != nil {
				t.Fatal(err)
			}
			resolved, err := schema.Resolve(nil)
			if err != nil {
				t.Fatal(err)
			}
			for _, tc := range cases {
				var value any
				if err := json.Unmarshal([]byte(tc.input), &value); err != nil {
					t.Fatal(err)
				}
				if err := resolved.Validate(value); (err == nil) != tc.valid {
					t.Fatalf("compact=%t %s: valid=%t, error=%v", capture.Compact, tc.name, tc.valid, err)
				}
			}
		}
		if !found {
			t.Fatal("host did not expose native completion")
		}
	}
}

func generatedRosterNames(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var catalogue struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(raw, &catalogue); err != nil {
		t.Fatal(err)
	}
	if len(catalogue.Tools) == 0 {
		t.Fatal("empty generated tool catalogue", path)
	}
	names := make([]string, 0, len(catalogue.Tools))
	for _, tool := range catalogue.Tools {
		names = append(names, tool.Name)
	}
	return names
}

func assertCanonicalToolRoster(t *testing.T, actual []*mcp.Tool, catalogues ...[]string) {
	t.Helper()
	canonical := regexp.MustCompile(`^oa_[a-z0-9_]+$`)
	counts := map[string]int{}
	for _, tool := range actual {
		if !canonical.MatchString(tool.Name) {
			t.Errorf("noncanonical tool %q", tool.Name)
		}
		counts[tool.Name]++
		if counts[tool.Name] != 1 {
			t.Errorf("duplicate tool %q", tool.Name)
		}
	}
	if len(actual) == 0 {
		t.Fatal("empty actual tool roster")
	}
	declared := map[string]bool{}
	for _, names := range catalogues {
		for _, name := range names {
			if !canonical.MatchString(name) || declared[name] {
				t.Errorf("noncanonical or duplicate generated declaration %q", name)
			}
			declared[name] = true
			if counts[name] != 1 {
				t.Errorf("generated tool %q appears %d times", name, counts[name])
			}
		}
	}
}

func nativeHostToolRoster(t *testing.T, application string) []*mcp.Tool {
	tools, _ := nativeHostToolMetadata(t, application)
	return tools
}

func nativeHostToolMetadata(t *testing.T, application string) ([]*mcp.Tool, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	b := &messagesBackend{Backend: new(reconciliationBackend), application: application}
	serverTransport, clientTransport := mcp.NewInMemoryTransports()
	done := make(chan error, 1)
	go func() {
		done <- runNativeMCPServerTransport(ctx, gateway.NewServer(b), b, t.TempDir(),
			func(context.Context, string, string, []string) (codexCLIResult, error) {
				t.Error("tool inventory attempted a vendor CLI effect")
				return codexCLIResult{}, errors.New("inert roster")
			},
			func(context.Context, func(context.Context, string) ([]byte, error)) (codexReceiverCLI, func() error, error) {
				t.Error("tool inventory attempted vendor CLI selection")
				return nil, nil, errors.New("inert roster")
			}, serverTransport)
	}()
	session, err := mcp.NewClient(&mcp.Implementation{Name: "inert-roster", Version: "test"}, nil).Connect(ctx, clientTransport, nil)
	if err != nil {
		cancel()
		<-done
		t.Fatal(err)
	}
	defer func() {
		session.Close()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Error("native roster server did not join")
		}
	}()
	var tools []*mcp.Tool
	params := &mcp.ListToolsParams{}
	cursors := map[string]bool{}
	for range 32 {
		page, err := session.ListTools(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		tools = append(tools, page.Tools...)
		if page.NextCursor == "" {
			return tools, session.InitializeResult().Instructions
		}
		if cursors[page.NextCursor] {
			t.Fatal("tool-list cursor repeated")
		}
		cursors[page.NextCursor] = true
		params.Cursor = page.NextCursor
	}
	t.Fatal("tool-list pagination did not finish")
	return nil, ""
}

func TestAgentCatalogueMatchesActualNativeHelperPublicSurface(t *testing.T) {
	raw, err := os.ReadFile("../adopters/opencode/generated/agent-tools.json")
	if err != nil {
		t.Fatal(err)
	}
	var catalogue struct {
		Tools        []*mcp.Tool `json:"tools"`
		Instructions string      `json:"instructions"`
	}
	if err := json.Unmarshal(raw, &catalogue); err != nil {
		t.Fatal(err)
	}
	canonical := func(tools []*mcp.Tool) map[string]string {
		result := map[string]string{}
		for _, tool := range tools {
			switch tool.Name {
			case codexSessionStartTool, claudeSessionStartTool, claudeSessionEndTool, openCodeChatStartTool:
				continue // Owned host lifecycle calls are never model-projected.
			}
			body, err := json.Marshal(tool)
			if err != nil {
				t.Fatal(err)
			}
			if _, exists := result[tool.Name]; exists {
				t.Fatal("duplicate native tool", tool.Name)
			}
			result[tool.Name] = string(body)
		}
		return result
	}
	projected := canonical(catalogue.Tools)
	if len(projected) != len(catalogue.Tools) {
		t.Fatal("internal lifecycle tool projected")
	}
	for _, application := range []string{"oa-claude-integration", "oa-codex-integration", "oa-opencode-integration"} {
		t.Run(application, func(t *testing.T) {
			actual, instructions := nativeHostToolMetadata(t, application)
			if !reflect.DeepEqual(projected, canonical(actual)) {
				t.Fatal("public native helper tools, descriptions or schemas differ from the generated agent catalogue")
			}
			if catalogue.Instructions != instructions {
				t.Fatal("native helper instruction drift")
			}
		})
	}
}

func TestClaudePluginActualHelperToolRosterIsCanonicalAndUnique(t *testing.T) {
	self, state := filepath.Join(t.TempDir(), "oa-claude-integration.exe"), t.TempDir()
	files := claudePluginFiles("inert-roster", self, state)
	var manifest struct {
		MCP map[string]claudeEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(files["plugin/.claude-plugin/plugin.json"], &manifest); err != nil {
		t.Fatal(err)
	}
	entry := manifest.MCP["OpenAbstractions"]
	application := strings.TrimSuffix(filepath.Base(entry.Command), ".exe")
	if len(manifest.MCP) != 1 || entry.Command != self || application != "oa-claude-integration" {
		t.Fatal("payload does not select the dedicated Claude helper", manifest)
	}
	// The global companion reuses the existing MCP owner and declares no
	// second server. Its actual generated manifest is read independently.
	global := claudePluginFilesForScope("inert-roster", self, state, "user")
	var companion struct {
		MCP map[string]claudeEntry `json:"mcpServers"`
	}
	if err := json.Unmarshal(global["plugin/.claude-plugin/plugin.json"], &companion); err != nil || len(companion.MCP) != 0 {
		t.Fatal("companion duplicates the MCP owner", companion, err)
	}
	actual := nativeHostToolRoster(t, application)
	assertCanonicalToolRoster(t, actual, generatedRosterNames(t, "claude-plugin/hooks/messages-tools.json"), generatedRosterNames(t, "../adopters/opencode/generated/asks-tools.json"))
}

func TestCodexProductionToolRosterIsCanonicalAndUnique(t *testing.T) {
	actual := nativeHostToolRoster(t, "oa-codex-integration")
	assertCanonicalToolRoster(t, actual, generatedRosterNames(t, "../adopters/opencode/generated/messages-tools.json"), generatedRosterNames(t, "../adopters/opencode/generated/asks-tools.json"))
}
