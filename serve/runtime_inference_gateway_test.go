package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	rights "github.com/openabstractions/abstraction-rights/go/client"
)

// fakeOllama lists one model and streams "Hello through the runtime".
func fakeOllama(t *testing.T) (*httptest.Server, *atomic.Int64) {
	t.Helper()
	var chats atomic.Int64
	mux := http.NewServeMux()
	mux.HandleFunc("/api/tags", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"models":[{"name":"fixture-chat:1b"}]}`))
	})
	mux.HandleFunc("/api/ps", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte(`{"models":[]}`)) })
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		chats.Add(1)
		io.Copy(io.Discard, r.Body)
		for _, word := range strings.SplitAfter("Hello through the runtime", " ") {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", word)
			w.(http.Flusher).Flush()
		}
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":6,\"completion_tokens\":4}}\n\ndata: [DONE]\n\n")
	})
	s := httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s, &chats
}

// scriptPython finds a Python interpreter and the real image path it runs as.
func scriptPython(t *testing.T) (string, []string, string) {
	t.Helper()
	candidates := [][]string{{"python3"}, {"python"}}
	if runtime.GOOS == "windows" {
		candidates = [][]string{{"py", "-3"}, {"python"}}
	}
	for _, c := range candidates {
		exe, err := exec.LookPath(c[0])
		if err != nil {
			continue
		}
		out, err := exec.Command(exe, append(c[1:], "-c", "import os,sys;print(os.path.realpath(sys.executable))")...).Output()
		if err == nil {
			return exe, c[1:], strings.TrimSpace(string(out))
		}
	}
	t.Skip("no Python interpreter on PATH for the fake llm-style client")
	return "", nil, ""
}

func freeLoopbackPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().String()
}

func runInference(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out, diagnostics bytes.Buffer
	err := inferenceCommand(args, &out, &diagnostics)
	return out.String() + diagnostics.String(), err
}

// The shipped runtime with --gateway: the command line adds a local host and
// issues a key for Python, an llm-style Python client streams through the
// window once Python holds complete, the audit read through operator@1 shows
// the window route and the socket rung, and a revoked key is refused.
func TestRuntimeGatewayWindowServesAKeyedProgram(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("this platform cannot bind a loopback peer")
	}
	pythonExe, pythonArgs, program := scriptPython(t)
	upstream, chats := fakeOllama(t)
	options, _ := isolatedRuntime(t)
	options.gateway = freeLoopbackPort(t)
	for _, file := range []struct{ path, body string }{
		{filepath.Join(options.stateDir, "credentials", credentialsBackendFile), "file-0600\n"},
		{filepath.Join(options.stateDir, "inference", inferenceHostsFile), `{"local":[]}`},
	} {
		if err := os.MkdirAll(filepath.Dir(file.path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file.path, []byte(file.body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		cancel()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	t.Cleanup(func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	})
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	out, err := runInference(t, append([]string{"host", "add", "ollama", "--base", upstream.URL}, endpoint...)...)
	if err != nil {
		t.Fatalf("host add: %v\n%s", err, out)
	}
	out, err = runInference(t, append([]string{"host", "add", "openrouter", "--base", "https://openrouter.invalid/api/v1", "--wire", "openai-compatible",
		"--credential", "openrouter", "--tokens-per-day", "1000", "--profiles", "chat"}, endpoint...)...)
	if err != nil {
		t.Fatalf("hosted host add: %v\n%s", err, out)
	}
	out, err = runInference(t, append([]string{"host", "list", "--json"}, endpoint...)...)
	if err != nil || !strings.Contains(out, `"Name":"ollama"`) || !strings.Contains(out, `"TokensPerDay":1000`) || !strings.Contains(out, `"Spend":{"Day"`) ||
		!strings.Contains(out, `"Profiles":["chat","embed","transcription","speech","image","live"],"DeclaredBy":"operator"`) || !strings.Contains(out, `"Profiles":["chat"],"DeclaredBy":"operator"`) {
		t.Fatalf("host list: %v\n%s", err, out)
	}
	// host add wrote complete on each host for this operator program, and the
	// runtime's own apply rule on the hosted credential for its listings.
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	call, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	operator, err := client.New(options.endpoint).ResolveRightsOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range [][2]string{{inference.ActionComplete, "host:ollama"}, {inference.ActionComplete, "host:openrouter"}, {"abstraction.credentials/apply", "credential:openrouter"}} {
		read, err := operator.ReadRuleContext(call, rights.Subject{Account: account.Uid, Program: filepath.Clean(self)}, rule[0], rule[1])
		if err != nil || read.Outcome.String() != "found" || !read.Record.Rule.Permit || read.Record.Why != hostAddWhy {
			t.Fatalf("rule %v after host add: %+v %v", rule, read, err)
		}
	}
	out, err = runInference(t, append([]string{"host", "remove", "openrouter"}, endpoint...)...)
	if err != nil {
		t.Fatalf("host remove: %v\n%s", err, out)
	}
	out, err = runInference(t, append([]string{"key", "issue", "--for", program, "--json"}, endpoint...)...)
	if err != nil {
		requireUserScopeAdd(t, err, out)
	}
	var issued struct{ Key string }
	if err := json.Unmarshal([]byte(out), &issued); err != nil || !strings.HasPrefix(issued.Key, localKeyPrefix) {
		t.Fatalf("key issue: %v\n%s", err, out)
	}
	out, err = runInference(t, append([]string{"key", "issue", "--for", program}, endpoint...)...)
	var exit *exitError
	if !errors.As(err, &exit) || exit.code != exitRefusedCall || !strings.Contains(err.Error(), "conflict") {
		t.Fatalf("a second key for the same program: %v\n%s", err, out)
	}
	script := filepath.Join("testdata", "llm_style.py")
	if _, err := os.Stat(script); os.IsNotExist(err) {
		// Private development keeps the canonical fixture with the inference
		// gateway. Publication copies that same file into serve/testdata so a
		// standalone charter checkout exercises the identical client.
		script = filepath.Join("..", "openabstractions-flat", "abstraction-inference", "go", "gateway", "testdata", "llm_style.py")
	}
	llm := func() (map[string]any, int) {
		cmd := exec.Command(pythonExe, append(pythonArgs, script, "http://"+options.gateway+"/v1", issued.Key, "fixture-chat:1b")...)
		cmd.Stderr = os.Stderr
		raw, err := cmd.Output()
		code := 0
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		if err := json.Unmarshal(raw, &result); err != nil {
			t.Fatalf("llm-style client printed %q", raw)
		}
		return result, code
	}
	if result, code := llm(); code != 3 || result["status"] != 403.0 || !strings.Contains(fmt.Sprint(result["body"]), "rights:not_granted") {
		t.Fatalf("before a complete rule: exit %d %v", code, result)
	}
	setCompleteRule(t, options.endpoint, program, "ollama", true)
	if result, code := llm(); code != 0 || result["text"] != "Hello through the runtime" {
		t.Fatalf("with a complete rule: exit %d %v", code, result)
	}
	if chats.Load() != 1 {
		t.Fatalf("upstream chat requests %d", chats.Load())
	}
	out, err = runInference(t, append([]string{"audit", "--json"}, endpoint...)...)
	if err != nil {
		t.Fatalf("audit: %v\n%s", err, out)
	}
	var audit struct {
		Entries []struct {
			Route, Rung, Program, Outcome, Reason string
			TokensOut                             int64 `json:"TokensOut"`
		}
	}
	if err := json.Unmarshal([]byte(out), &audit); err != nil {
		t.Fatalf("audit json: %v\n%s", err, out)
	}
	var completed, refused bool
	for _, e := range audit.Entries {
		if e.Route != inference.RouteWindow || !samePrograms(e.Program, program) || !strings.HasPrefix(e.Rung, identity.TransportLoopback+"/"+runtime.GOOS) {
			continue
		}
		completed = completed || e.Outcome == "completed" && e.TokensOut == 4
		refused = refused || e.Outcome == "not_permitted" && e.Reason == "rights:not_granted"
	}
	if !completed || !refused {
		t.Fatalf("audit lacks the window's refusal and completion for %s:\n%s", program, out)
	}
	if strings.Contains(out, issued.Key) {
		t.Fatal("the audit carries the key")
	}
	out, err = runInference(t, append([]string{"key", "revoke", "--for", program}, endpoint...)...)
	if err != nil {
		t.Fatalf("key revoke: %v\n%s", err, out)
	}
	if result, code := llm(); code != 3 || result["status"] != 401.0 {
		t.Fatalf("a revoked key: exit %d %v", code, result)
	}
	out, err = runInference(t, append([]string{"key", "list"}, endpoint...)...)
	if err != nil || !strings.Contains(out, "revoked") {
		t.Fatalf("key list: %v\n%s", err, out)
	}
	err = filepath.WalkDir(options.stateDir, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.Contains(path, string(filepath.Separator)+"items"+string(filepath.Separator)) {
			return err
		}
		if data, err := os.ReadFile(path); err == nil && bytes.Contains(data, []byte(issued.Key)) {
			t.Errorf("%s carries the key", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestGatewayAddressIsLoopbackOnly(t *testing.T) {
	for _, address := range []string{"127.0.0.1:8080", ""} {
		if err := gatewayAddress(address); err != nil {
			t.Fatalf("%q: %v", address, err)
		}
	}
	for _, address := range []string{"0.0.0.0:8080", "localhost:8080", "[::1]:8080", "127.0.0.1", "127.0.0.1:0", "127.0.0.1:x", "192.168.0.2:80"} {
		if err := gatewayAddress(address); err == nil {
			t.Fatalf("%q accepted", address)
		}
	}
}
