package inferencefixture_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// emitter is a fake OpenAI-compatible local host that flushes one token per
// interval and records when each token left it.
type emitter struct {
	server   *httptest.Server
	tokens   int
	interval time.Duration
	mu       sync.Mutex
	emitted  []time.Duration
}

func newEmitter(t *testing.T, tokens int, interval time.Duration) *emitter {
	e := &emitter{tokens: tokens, interval: interval}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"models":[{"name":"latency-chat:1b"}]}`))
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"models":[]}`)) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		flusher := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		times := make([]time.Duration, 0, e.tokens)
		for i := 0; i < e.tokens; i++ {
			if i > 0 && e.interval > 0 {
				time.Sleep(e.interval)
			}
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"t%d \"}}]}\n\n", i)
			flusher.Flush()
			times = append(times, stamp())
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1}}\n\ndata: [DONE]\n\n")
		flusher.Flush()
		e.mu.Lock()
		e.emitted = times
		e.mu.Unlock()
	})
	e.server = httptest.NewServer(mux)
	t.Cleanup(e.server.Close)
	return e
}

func (e *emitter) last() []time.Duration {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]time.Duration(nil), e.emitted...)
}

// ollamaModel returns the first model a reachable local Ollama lists, or "".
func ollamaModel(base string) string {
	c := http.Client{Timeout: time.Second}
	resp, err := c.Get(base + "/api/tags")
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	var d struct {
		Models []struct {
			Name string `json:"name"`
		} `json:"models"`
	}
	if json.NewDecoder(resp.Body).Decode(&d) != nil || len(d.Models) == 0 {
		return ""
	}
	return d.Models[0].Name
}

type samples []time.Duration

func (s samples) percentile(p float64) float64 {
	if len(s) == 0 {
		return 0
	}
	sorted := append(samples(nil), s...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i] < sorted[j] })
	i := int(p * float64(len(sorted)-1))
	return float64(sorted[i].Nanoseconds()) / 1e6
}

func (s samples) line(name string) string {
	return fmt.Sprintf("LATENCY %-34s n=%-5d p50=%8.3f p90=%8.3f p99=%8.3f max=%8.3f ms", name, len(s), s.percentile(0.5), s.percentile(0.9), s.percentile(0.99), s.percentile(1))
}

func envInt(name string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil && v >= 0 {
		return v
	}
	return fallback
}

// TestStreamingLatency measures long-poll Observe through the production
// provider and service on this platform's native IPC (named pipes on Windows,
// Unix sockets on Linux). It runs only with OA_INFERENCE_MEASURE set.
//
// Upstream: a reachable local Ollama with a model (OA_INFERENCE_OLLAMA, default
// http://127.0.0.1:11434), otherwise the fake emitter, which flushes
// OA_INFERENCE_TOKENS tokens (50) every OA_INFERENCE_INTERVAL_MS (20 ms) and
// records each flush. Each of OA_INFERENCE_RUNS runs (20) starts a request and
// observes it to its end with the stream loop's bounds (256 deltas, 65536
// bytes, 25 s wait), recording Start, every Observe and every part delta's
// arrival. A second pass with interval 0 shows batching under a burst.
func TestStreamingLatency(t *testing.T) {
	if os.Getenv("OA_INFERENCE_MEASURE") == "" {
		t.Skip("set OA_INFERENCE_MEASURE to measure")
	}
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	runs, tokens := envInt("OA_INFERENCE_RUNS", 20), envInt("OA_INFERENCE_TOKENS", 50)
	intervals := []time.Duration{20 * time.Millisecond, 5 * time.Millisecond, 2 * time.Millisecond, 0}
	if list := os.Getenv("OA_INFERENCE_INTERVALS_MS"); list != "" {
		intervals = nil
		for _, word := range strings.Split(list, ",") {
			ms, err := strconv.Atoi(word)
			if err != nil || ms < 0 {
				t.Fatalf("OA_INFERENCE_INTERVALS_MS: %q", word)
			}
			intervals = append(intervals, time.Duration(ms)*time.Millisecond)
		}
	}
	base := os.Getenv("OA_INFERENCE_OLLAMA")
	if base == "" {
		base = "http://127.0.0.1:11434"
	}
	model := ollamaModel(base)
	fmt.Printf("LATENCY platform %s/%s transport %s clock %s\n", runtime.GOOS, runtime.GOARCH, map[bool]string{true: "named-pipe", false: "unix-socket"}[runtime.GOOS == "windows"], clockName)
	if model != "" {
		fmt.Printf("LATENCY upstream ollama %s model %s\n", base, model)
	} else {
		fmt.Printf("LATENCY upstream fake-emitter tokens=%d intervals %v (no local Ollama with a model at %s)\n", tokens, intervals, base)
	}
	for i, interval := range intervals {
		if model != "" && i > 0 {
			break
		}
		measure(t, fmt.Sprintf("every %v", interval), runs, tokens, interval, base, model, i == 0)
	}
}

func measure(t *testing.T, pass string, runs, tokens int, interval time.Duration, ollama, model string, probes bool) {
	dir := t.TempDir()
	endpoint := filepath.Join(dir, "latency.sock")
	if runtime.GOOS == "windows" {
		endpoint = fmt.Sprintf(`\\.\pipe\oa-inference-latency-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}
	var e *emitter
	var host *router.Host
	if model == "" {
		e = newEmitter(t, tokens, interval)
		host = router.Ollama(e.server.URL)
		model = "latency-chat:1b"
	} else {
		host = router.Ollama(ollama)
	}
	provider, err := inference.New(inference.Config{Router: router.New(host),
		Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "permitted", nil },
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "unavailable"
		}})
	if err != nil {
		t.Fatal(err)
	}
	defer provider.Close()
	svc, err := inferenceservice.Listen(endpoint, provider)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- svc.Serve(ctx) }()
	defer func() { cancel(); <-done }()
	chat := iclient.New(endpoint)
	request := iwire.Request{Model: model, Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeLocalOnly},
		Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: "Count to fifty."}}}}}
	if a, err := chat.Start(ctx, request); err != nil || a.Outcome != iwire.StartOutcomeAccepted {
		t.Fatalf("warm-up start: %+v %v", a, err)
	} else if _, err := chat.Cancel(ctx, a.Operation); err != nil {
		t.Fatal(err)
	}
	var start, firstToken, firstAfterEmit, perToken, observeRTT, emptyRTT samples
	observesPerToken, deltasPerPage := 0.0, 0.0
	pages := 0
	for run := 0; run < runs; run++ {
		began := stamp()
		a, err := chat.Start(ctx, request)
		if err != nil || a.Outcome != iwire.StartOutcomeAccepted {
			t.Fatalf("start: %+v %v", a, err)
		}
		start = append(start, stamp()-began)
		var arrivals []time.Duration
		cursor, observes := int64(0), 0
		for {
			page, err := chat.Observe(ctx, a.Operation, cursor, iclient.PageDeltas, iclient.PageBytes, iclient.PageWait.Milliseconds())
			returned := stamp()
			if err != nil || page.Outcome != iwire.PageOutcomePage {
				t.Fatalf("observe: %+v %v", page, err)
			}
			observes++
			if len(page.Deltas) > 0 {
				pages++
				deltasPerPage += float64(len(page.Deltas))
			}
			parts := 0
			for _, d := range page.Deltas {
				if d.Kind == iwire.DeltaKindPart {
					arrivals = append(arrivals, returned)
					parts++
				}
			}
			if parts > 0 && len(arrivals) == parts {
				firstToken = append(firstToken, returned-began)
			}
			cursor = page.Next
			if page.AtEnd {
				break
			}
		}
		observesPerToken += float64(observes) / float64(max(len(arrivals), 1))
		// A retained page read with no wait: the round trip of one Observe.
		for i := 0; i < 5; i++ {
			called := stamp()
			if _, err := chat.Observe(ctx, a.Operation, 0, 1, 65536, 0); err != nil {
				t.Fatal(err)
			}
			observeRTT = append(observeRTT, stamp()-called)
			called = stamp()
			if _, err := chat.Observe(ctx, a.Operation, cursor, 1, 65536, 0); err != nil {
				t.Fatal(err)
			}
			emptyRTT = append(emptyRTT, stamp()-called)
		}
		if e != nil {
			emitted := e.last()
			if len(emitted) != len(arrivals) {
				t.Fatalf("run %d: %d tokens emitted, %d arrived", run, len(emitted), len(arrivals))
			}
			firstAfterEmit = append(firstAfterEmit, arrivals[0]-emitted[0])
			for i := 1; i < len(arrivals); i++ {
				perToken = append(perToken, arrivals[i]-emitted[i])
			}
		}
	}
	fmt.Println(start.line(pass + " start"))
	fmt.Println(firstToken.line(pass + " first token from start call"))
	if e != nil {
		fmt.Println(firstAfterEmit.line(pass + " first token after upstream flush"))
		fmt.Println(perToken.line(pass + " later tokens after upstream flush"))
	}
	fmt.Println(observeRTT.line(pass + " observe one retained delta"))
	fmt.Println(emptyRTT.line(pass + " observe at end, no wait"))
	fmt.Printf("LATENCY %-34s %.2f observes per token, %.2f deltas per nonempty page\n", pass+" batching", observesPerToken/float64(runs), deltasPerPage/float64(max(pages, 1)))
	if !probes {
		return
	}
	// Other languages' clients at the same service endpoint: per-call round trips.
	for _, probe := range []struct{ env, script string }{{"OA_JS_INFERENCE_NODE", "js_rtt.mjs"}, {"OA_PY_INFERENCE_PYTHON", "py_rtt.py"}} {
		interpreter := os.Getenv(probe.env)
		if interpreter == "" {
			continue
		}
		script, _ := filepath.Abs(probe.script)
		out, err := exec.CommandContext(ctx, interpreter, script, endpoint, model, strconv.Itoa(runs)).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v\n%s", probe.script, err, out)
		}
		fmt.Print(strings.ReplaceAll(string(out), "\r\n", "\n"))
	}
}
