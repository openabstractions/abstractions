package main

import (
	"encoding/json"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestClaudeModLifecycleUsesActualListedGatewayTools(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node unavailable")
	}
	shortenAgentTiming(t)
	t.Setenv("CLAUDE_CODE_SESSION_ID", codexAttachThread)
	runtime := newChaosRuntime()
	h := startChaosHelper(t, runtime, new(chaosQueue), 171, "oa-claude-integration")
	names := []string{}
	cursor := ""
	for {
		page, err := h.session.ListTools(t.Context(), &mcp.ListToolsParams{Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		for _, tool := range page.Tools {
			names = append(names, tool.Name)
		}
		cursor = page.NextCursor
		if cursor == "" {
			break
		}
	}
	raw, err := json.Marshal(names)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(node, "--test", "claude-plugin/hooks/register.test.mjs")
	command.Env = append(os.Environ(), "OA_CLAUDE_REAL_GATEWAY_TOOLS="+string(raw))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("Mod session tool lookup: %v\n%s", err, output)
	}
	until := time.Now().Add(3 * time.Second)
	for {
		out, err := h.session.CallTool(t.Context(), &mcp.CallToolParams{Name: claudeSessionStartTool, Arguments: map[string]any{}})
		if err == nil && !out.IsError {
			break
		}
		if time.Now().After(until) {
			t.Fatal("listed native lifecycle did not bind", out, err)
		}
		time.Sleep(time.Millisecond)
	}
	h.recovered(3 * time.Second)
}
