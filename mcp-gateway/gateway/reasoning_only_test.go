package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	facade "github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/fixture"
)

// thinkingServer is a local OpenAI-compatible model server whose one model
// spends any output limit on reasoning, and answers when asked for
// reasoning_effort none. It lists the model on Ollama's routes.
type thinkingServer struct {
	mu     sync.Mutex
	asks   []map[string]any
	server *httptest.Server
}

func newThinkingServer(t *testing.T) *thinkingServer {
	s := &thinkingServer{}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags", "/api/ps":
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"models":[{"name":"thinker"}]}`)
			return
		case "/v1/chat/completions":
		default:
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.mu.Lock()
		s.asks = append(s.asks, body)
		s.mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		limit, _ := body["max_tokens"].(float64)
		if body["reasoning_effort"] == "none" {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"Paris\"}}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
			fmt.Fprint(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
			return
		}
		for range int(limit) {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"reasoning_content\":\"hmm \"}}]}\n\n")
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"length\"}]}\n\n")
		fmt.Fprintf(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":12,\"completion_tokens\":%d}}\n\ndata: [DONE]\n\n", int(limit))
	}))
	t.Cleanup(s.server.Close)
	return s
}

func (s *thinkingServer) taken() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]map[string]any(nil), s.asks...)
}

// R34 owner machine, 2026-11-12: OpenCode asked qwen3.6-35b-a3b "What is
// the capital of France? Answer in one word." with max_output 10, and both
// chat@1 and chat@3 ended upstream:reasoning_only: the model spent the
// limit reasoning. A model server on this machine that answers only with
// reasoning is asked once more with reasoning_effort none, within the same
// limit, and the agent gets the answer. The chat@3 path is
// TestChat3ReasoningOnlyLocalReplyIsAskedAgainWithoutReasoning; this
// fixture runtime serves chat@1.
func TestReasoningOnlyReplyIsAskedAgainWithoutReasoning(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("native Program proof retains its existing Darwin ceiling")
	}
	upstream := newThinkingServer(t)
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cfg := fixture.NewConfig("reasoning-only")
	cfg.JobRoot = filepath.Join(t.TempDir(), "jobs")
	cfg.Allowed = []string{exe}
	cfg.Upstream, cfg.LocalUpstream = upstream.server.URL, true
	run := fixture.Start(t, cfg)
	t.Cleanup(func() { run.Stop(t) })
	oa, err := NewOA(facade.New(cfg.RuntimeEndpoint), t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	_, session := connected(t, oa, "naive-opencode", "2025-11-25")
	ask := map[string]any{"model": "thinker", "prompt": "What is the capital of France? Answer in one word.", "hosting": "local", "max_output": 10, "wait_ms": 10000}

	_, plain := call[CompleteOutput](t, session, ToolComplete, ask)
	if plain.Outcome != "completed" || plain.Text != "Paris" {
		t.Fatalf("chat@1 %+v", plain)
	}
	asks := upstream.taken()
	if len(asks) != 2 || asks[0]["reasoning_effort"] != nil || asks[1]["reasoning_effort"] != "none" || asks[0]["max_tokens"] != float64(10) || asks[1]["max_tokens"] != float64(10) {
		t.Fatalf("chat@1 upstream asks %v", asks)
	}
}
