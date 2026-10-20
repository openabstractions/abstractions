//go:build windows

package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"time"

	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

const journeyChatModel = "journey-chat-small"

// J-MODELS: a local model host holds a model; the Panel's Models page lists
// it under Loaded now.
func init() { register(30, "J-MODELS", runModels) }

func runModels(ctx context.Context, e *env, o *options, r *result) {
	out, err := e.cli(ctx, "provider", "add", "journey-chat", "--program", filepath.Join(e.bin, "oa-journey-chat.exe"),
		"--provider-endpoint", "journey-chat-"+newID()[:6],
		"--contract", "abstraction.inference/chat@1", "--profiles", "chat", "--model", journeyChatModel,
		"--arg", "-endpoint", "--arg", "{endpoint}", "--arg", "-runtime", "--arg", e.runtime, "--arg", "-loaded", "--arg", journeyChatModel)
	if !r.check("model host declared", err == nil, "%v %s", err, strings.TrimSpace(out)) {
		return
	}
	// The person is chatting with the local model: the host is running and
	// holds it.
	a, err := e.enrollAgent(ctx, "oa-opencode-integration", "OpenCode")
	if !r.check("agent app kept", err == nil, "%v", err) {
		return
	}
	s, err := e.connect(ctx, a, []string{"OA_OPENCODE_HOST_CHAT_ID=" + newUUID()})
	if !r.check("agent connected", err == nil, "%v", err) {
		return
	}
	defer s.Close()
	for i := 0; i < 2; i++ {
		_, text, structured, _ := s.call(ctx, "oa_inference_complete", map[string]any{"model": journeyChatModel, "prompt": "hello", "hosting": "local", "max_output": 16})
		if !permissionPending(text, structured) && !strings.Contains(text, rights.DecisionOutcomeNotGranted.String()) {
			break
		}
		e.answers.waitIDs(ctx, questionIDs(structured), 30*time.Second)
	}
	type entry struct {
		Name    string   `json:"name"`
		Key     string   `json:"key"`
		Engine  string   `json:"engine"`
		Running bool     `json:"running"`
		Names   []string `json:"names"`
		Model   string   `json:"model"`
	}
	var page struct {
		Models  []entry `json:"models"`
		Running []entry `json:"running"`
	}
	var last string
	listed, flagged := false, false
	deadline := time.Now().Add(90 * time.Second)
	for time.Now().Before(deadline) && !(listed && flagged) {
		page.Models, page.Running = nil, nil
		raw := map[string]any{}
		_, err := e.panel(ctx, "GET", "/inference/library", nil, &raw)
		b, _ := json.Marshal(raw)
		last = string(b)
		if err != nil {
			last = err.Error()
		}
		_ = json.Unmarshal(b, &page)
		for _, m := range page.Running {
			if strings.Contains(m.Name+m.Key+m.Model+strings.Join(m.Names, ","), journeyChatModel) || strings.Contains(m.Engine, "journey-chat") {
				listed = true
			}
		}
		for _, m := range page.Models {
			if strings.Contains(m.Name+m.Key+strings.Join(m.Names, ","), journeyChatModel) && m.Running {
				flagged = true
			}
		}
		if !(listed && flagged) {
			time.Sleep(time.Second)
		}
	}
	r.check("Loaded now lists the host's model", listed, "%s", clip(last, 600))
	r.check("the model reads as loaded in the library", flagged, "")
}
