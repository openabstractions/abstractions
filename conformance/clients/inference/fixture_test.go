package inferencefixture_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstractions/conformance/clients/fixture"
)

// Words every consumer and the fixture agree on.
const (
	servedModel = "fixture-chat:1b"
	deniedModel = "denied-chat"
	replyText   = "Hello from the fixture runtime"
	holdMarker  = "HOLD"
)

// upstream is a fake local model host pair. "ollama" lists servedModel and
// streams replyText one word per event; a request whose last message holds
// holdMarker streams one word and holds the stream open until the client goes
// away. "lmstudio" lists deniedModel, which no program is permitted to use.
type upstream struct {
	ollama, lmstudio *httptest.Server
	interval         time.Duration
	chats            atomic.Int64
	holds            atomic.Int64
	closed           atomic.Int64
}

func newUpstream(t *testing.T, interval time.Duration) *upstream {
	u := &upstream{interval: interval}
	chat := func(w http.ResponseWriter, r *http.Request) {
		u.chats.Add(1)
		var body strings.Builder
		buf := make([]byte, 64<<10)
		for {
			n, err := r.Body.Read(buf)
			body.Write(buf[:n])
			if err != nil {
				break
			}
		}
		w.Header().Set("Content-Type", "text/event-stream")
		flusher := w.(http.Flusher)
		send := func(data string) { fmt.Fprintf(w, "data: %s\n\n", data); flusher.Flush() }
		words := strings.SplitAfter(replyText, " ")
		if strings.Contains(body.String(), holdMarker) {
			u.holds.Add(1)
			send(fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, words[0]))
			<-r.Context().Done()
			u.closed.Add(1)
			return
		}
		for i, word := range words {
			if i > 0 && u.interval > 0 {
				time.Sleep(u.interval)
			}
			send(fmt.Sprintf(`{"choices":[{"delta":{"content":%q}}]}`, word))
		}
		send(`{"choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":5}}`)
		send("[DONE]")
	}
	ollama := http.NewServeMux()
	ollama.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"models":[{"name":"` + servedModel + `"}]}`))
	})
	ollama.HandleFunc("/api/ps", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"models":[]}`)) })
	ollama.HandleFunc("/v1/chat/completions", chat)
	lmstudio := http.NewServeMux()
	lmstudio.HandleFunc("/api/v0/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[{"id":"` + deniedModel + `","type":"llm","state":"loaded"}]}`))
	})
	lmstudio.HandleFunc("/v1/chat/completions", chat)
	u.ollama, u.lmstudio = httptest.NewServer(ollama), httptest.NewServer(lmstudio)
	t.Cleanup(u.ollama.Close)
	t.Cleanup(u.lmstudio.Close)
	return u
}

// served is the inference composition the runtime publishes: the production
// provider and service, deciding through a rights decision policy.
type served struct {
	*inferenceservice.Host
	provider *inference.Provider
}

func (s served) Close() error { return errors.Join(s.Host.Close(), s.provider.Close()) }

type recorder struct {
	mu       sync.Mutex
	programs []string
}

func (r *recorder) since(n int) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	seen := map[string]bool{}
	var out []string
	for _, p := range r.programs[n:] {
		if !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	return out
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.programs)
}

func request(model, text string) iwire.Request {
	return iwire.Request{Model: model, Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeLocalOnly},
		Messages: []iwire.Message{{Role: iwire.RoleUser, Parts: []iwire.Part{{Kind: iwire.PartKindText, Text: text}}}}}
}

var latencyLine = regexp.MustCompile(`(?m)^FIRST_TOKEN_MS (\S+) ([0-9.]+)\r?$`)

// TestResolvedInference runs the facade runtime with the production inference
// provider and service over two fake local hosts, and drives complete, stream,
// cancel, refusal and absence through the Go client and each language's
// consumer. A consumer first runs without a rule and reads not_permitted; the
// fixture then grants abstraction.inference/complete on host:ollama to the
// program the service bound, and the consumer runs its served cases.
func TestResolvedInference(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("Program proof unavailable")
	}
	dir := t.TempDir()
	token := strconv.FormatInt(time.Now().UnixNano(), 36)
	endpoint := func(name string) string {
		if runtime.GOOS == "windows" {
			return `\\.\pipe\oa-inference-` + token + "-" + name
		}
		return filepath.Join(dir, name+".sock")
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Minute)
	defer cancel()
	up := newUpstream(t, 0)

	policy, err := rights.LoadDecisionPolicy(filepath.Join(dir, "rights.json"), rwire.ResourceActions)
	if err != nil {
		t.Fatal(err)
	}
	for _, action := range iwire.ResourceActions {
		if err := policy.RegisterAction(action); err != nil {
			t.Fatal(err)
		}
	}
	asked := &recorder{}
	r := router.New(router.Ollama(up.ollama.URL), router.LMStudio(up.lmstudio.URL))
	var recordsMu sync.Mutex
	var records []inference.Record
	provider, err := inference.New(inference.Config{Router: r, Idle: 3 * time.Second,
		Decide: func(_ context.Context, s inference.Subject, action, resource string) (string, error) {
			asked.mu.Lock()
			asked.programs = append(asked.programs, s.Program)
			asked.mu.Unlock()
			return policy.Decide(rwire.Subject{Account: s.Account, Program: s.Program}, action, resource).Outcome.String(), nil
		},
		Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
			return nil, "unavailable"
		},
		Record: func(rec inference.Record) { recordsMu.Lock(); records = append(records, rec); recordsMu.Unlock() },
	})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := inferenceservice.Listen(endpoint("inference"), provider)
	if err != nil {
		t.Fatal(err)
	}
	sink, err := logging.OpenFileSink(filepath.Join(dir, "runtime-log.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	onError := func(err error) { t.Log("runtime:", err) }
	o := host.Options{Endpoint: endpoint("resolver"), LogEndpoint: endpoint("log"), ConfigEndpoint: endpoint("config"), Sink: sink, OnError: onError,
		Inference: served{Host: svc, provider: provider}, InferenceEndpoint: endpoint("inference")}
	h, err := host.Listen(o)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() { cancel(); h.Close(); <-done }()
	// A second runtime publishes no inference: resolution there is the absence case.
	absent := host.Options{Endpoint: endpoint("absent"), LogEndpoint: endpoint("absent-log"), ConfigEndpoint: endpoint("absent-config"), Sink: sink, OnError: onError}
	ah, err := host.Listen(absent)
	if err != nil {
		t.Fatal(err)
	}
	absentDone := make(chan error, 1)
	go func() { absentDone <- ah.Serve(ctx) }()
	defer func() { ah.Close(); <-absentDone }()

	grant := func(program string) {
		t.Helper()
		if err := policy.Set(rwire.Subject{Account: account.Uid, Program: program}, inference.ActionComplete, inference.ResourceHost("ollama"), true); err != nil {
			t.Fatal(err)
		}
	}
	latencies := map[string]string{}

	// Go: the fixture process is the application.
	machine := client.NewUnverified(o.Endpoint)
	if _, err := client.NewUnverified(absent.Endpoint).ResolveInference(ctx, client.Requirements{Scope: client.ScopeLocal}); err == nil {
		t.Fatal("go: a runtime without inference resolved chat@1")
	} else {
		var resolution *client.ResolutionError
		if !errors.As(err, &resolution) || resolution.Status != "unavailable" {
			t.Fatalf("go absence: %v", err)
		}
	}
	chat, err := machine.ResolveInference(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	before := asked.count()
	reply, err := chat.Complete(ctx, request(servedModel, "hi"))
	if err != nil || reply.Outcome != iwire.ReplyOutcomeNotPermitted || reply.Reason != "rights:not_granted" {
		t.Fatalf("go refusal before a rule: %+v %v", reply, err)
	}
	programs := asked.since(before)
	if len(programs) != 1 {
		t.Fatalf("go: bound programs %v", programs)
	}
	grant(programs[0])
	goConsumer(ctx, t, chat, up, latencies)

	type consumer struct {
		name    string
		command func(mode string) *exec.Cmd
	}
	var consumers []consumer
	if probe := os.Getenv("OA_CPP_INFERENCE_PROBE"); probe != "" {
		consumers = append(consumers, consumer{"cpp", func(mode string) *exec.Cmd {
			return exec.CommandContext(ctx, probe, o.Endpoint, absent.Endpoint, mode)
		}})
	}
	if node := os.Getenv("OA_JS_INFERENCE_NODE"); node != "" {
		script, _ := filepath.Abs("js_consumer.mjs")
		consumers = append(consumers, consumer{"javascript", func(mode string) *exec.Cmd {
			return exec.CommandContext(ctx, node, script, o.Endpoint, absent.Endpoint, mode)
		}})
	}
	if python := os.Getenv("OA_PY_INFERENCE_PYTHON"); python != "" {
		script, _ := filepath.Abs("py_consumer.py")
		consumers = append(consumers, consumer{"python", func(mode string) *exec.Cmd {
			return exec.CommandContext(ctx, python, script, o.Endpoint, absent.Endpoint, mode)
		}})
	}
	if probe := os.Getenv("OA_RUST_INFERENCE_PROBE"); probe != "" {
		consumers = append(consumers, consumer{"rust", func(mode string) *exec.Cmd {
			return exec.CommandContext(ctx, probe, o.Endpoint, absent.Endpoint, mode)
		}})
	}
	for _, c := range consumers {
		before := asked.count()
		out, err := fixture.Output(ctx, c.command("refused"))
		if err != nil {
			t.Fatalf("%s refused: %v\n%s", c.name, err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
		programs := asked.since(before)
		if len(programs) != 1 {
			t.Fatalf("%s: bound programs %v", c.name, programs)
		}
		grant(programs[0])
		holds, closed := up.holds.Load(), up.closed.Load()
		out, err = fixture.Output(ctx, c.command("served"))
		if err != nil {
			t.Fatalf("%s served: %v\n%s", c.name, err, out)
		}
		t.Log(strings.TrimSpace(string(out)))
		if m := latencyLine.FindStringSubmatch(string(out)); m == nil || m[1] != c.name {
			t.Fatalf("%s reported no first-token latency", c.name)
		} else {
			latencies[c.name] = m[2]
		}
		awaitClosed(t, c.name, up, holds, closed)
	}
	if want := os.Getenv("OA_INFERENCE_CONSUMERS"); want != "" {
		for _, name := range strings.Split(want, ",") {
			if _, ok := latencies[name]; !ok {
				t.Fatalf("consumer %s was requested and did not run", name)
			}
		}
	}
	names := make([]string, 0, len(latencies))
	for name := range latencies {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Printf("FIRST_TOKEN_MS %s %s\n", name, latencies[name])
	}
	recordsMu.Lock()
	defer recordsMu.Unlock()
	outcomes := map[string]int{}
	for _, rec := range records {
		outcomes[rec.Outcome]++
	}
	if outcomes["completed"] < 2*(len(consumers)+1) || outcomes["cancelled"] < len(consumers)+1 || outcomes["not_permitted"] < 2*(len(consumers)+1) {
		t.Fatalf("log records by outcome %v", outcomes)
	}
	t.Logf("PASS resolved inference: go and %d consumers; records %v", len(consumers), outcomes)
}

// awaitClosed requires the consumer's abandoned stream to have closed its
// upstream request once the consumer exited.
func awaitClosed(t *testing.T, name string, up *upstream, holds, closed int64) {
	t.Helper()
	if up.holds.Load() != holds+1 {
		t.Fatalf("%s: %d held streams, want one", name, up.holds.Load()-holds)
	}
	deadline := time.Now().Add(5 * time.Second)
	for up.closed.Load() != closed+1 {
		if time.Now().After(deadline) {
			t.Fatalf("%s: the cancelled stream's upstream request stayed open", name)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// goConsumer is the Go application's served cases.
func goConsumer(ctx context.Context, t *testing.T, chat *iclient.Chat, up *upstream, latencies map[string]string) {
	t.Helper()
	reply, err := chat.Complete(ctx, request(servedModel, "hi"))
	if err != nil || reply.Outcome != iwire.ReplyOutcomeCompleted || len(reply.Message.Parts) != 1 || reply.Message.Parts[0].Text != replyText || reply.Host != "ollama" || reply.Usage.Input != 5 {
		t.Fatalf("go complete: %+v %v", reply, err)
	}
	started := time.Now()
	var first time.Duration
	var text strings.Builder
	var last iwire.Delta
	for d, err := range chat.Stream(ctx, request(servedModel, "hi")) {
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind == iwire.DeltaKindPart {
			if first == 0 {
				first = time.Since(started)
			}
			text.WriteString(d.Part.Text)
		}
		last = d
	}
	if text.String() != replyText || last.Kind != iwire.DeltaKindEnd || last.End.Outcome != iwire.ReplyOutcomeCompleted {
		t.Fatalf("go stream: %q %+v", text.String(), last)
	}
	latencies["go"] = strconv.FormatFloat(float64(first.Microseconds())/1000, 'f', 2, 64)
	holds, closed := up.holds.Load(), up.closed.Load()
	for d, err := range chat.Stream(ctx, request(servedModel, holdMarker)) {
		if err != nil {
			t.Fatal(err)
		}
		if d.Kind == iwire.DeltaKindPart {
			break
		}
	}
	awaitClosed(t, "go", up, holds, closed)
	denied, err := chat.Complete(ctx, request(deniedModel, "hi"))
	if err != nil || denied.Outcome != iwire.ReplyOutcomeNotPermitted || denied.Reason != "rights:not_granted" {
		t.Fatalf("go denied host: %+v %v", denied, err)
	}
	t.Log("PASS go: complete, stream, cancel, refusal, absence")
}
