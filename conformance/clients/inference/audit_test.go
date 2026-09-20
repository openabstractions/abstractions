package inferencefixture_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstractions/conformance/clients/fixture"
)

// auditLines is the canonical text every audit reader prints: one tab-separated
// line per entry, in sequence order, with LF endings.
func auditLines(entries []iwire.AuditEntry) string {
	var b strings.Builder
	for _, e := range entries {
		fmt.Fprintf(&b, "AUDIT\t%d\t%s\t%s\t%s\t%d\t%d\t%s\t%s\n", e.Sequence, e.Route, e.Outcome, e.Reason, e.TokensIn, e.TokensOut, e.Program, e.Rung)
	}
	return b.String()
}

func onlyAudit(out []byte) string {
	var b strings.Builder
	for _, line := range strings.Split(strings.ReplaceAll(string(out), "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "AUDIT\t") {
			b.WriteString(line + "\n")
		}
	}
	return b.String()
}

// The shipped runtime with its gateway window: a Python llm-style client
// streams through the window under a local key, this process is refused on the
// native route, and the installed C++ consumer, the Python consumer and the Go
// operator client read the same audit through operator@1, with the window's
// socket rung and the native pipe or socket rung.
func TestInstalledClientsReadTheSameAudit(t *testing.T) {
	cppProbe, python := os.Getenv("OA_CPP_INFERENCE_PROBE"), os.Getenv("OA_PY_INFERENCE_PYTHON")
	if runtime.GOOS == "darwin" || cppProbe == "" && python == "" {
		t.Skip("needs Program proof and the C++ or Python consumer")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin", "openabstractions")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, ".")
	build.Dir = filepath.Join(root, "serve")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build openabstractions: %v\n%s", err, out)
	}
	up := newUpstream(t, 0)
	state := filepath.Join(dir, "state")
	for path, body := range map[string]string{
		filepath.Join(state, "credentials", "backend"):  "file-0600\n",
		filepath.Join(state, "inference", "hosts.json"): fmt.Sprintf(`{"local":[{"kind":"ollama","base":%q}]}`, up.ollama.URL),
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	gateway := l.Addr().String()
	l.Close()
	name := "oa-audit-" + strconv.FormatInt(time.Now().UnixNano()%1e9, 36)
	serve := exec.CommandContext(ctx, bin, "serve", "runtime", "--isolated", name, "--state-dir", state, "--gateway", gateway)
	stderr, err := serve.StderrPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := serve.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { serve.Process.Kill(); serve.Wait() })
	endpoints := make(chan string, 1)
	go func() {
		scanner := bufio.NewScanner(stderr)
		sent := false
		for scanner.Scan() {
			line := scanner.Text()
			t.Log("runtime: " + line)
			if value, ok := strings.CutPrefix(strings.TrimSpace(line), "ABSTRACTION_RUNTIME_ENDPOINT="); ok && !sent {
				endpoints <- value
				sent = true
			}
		}
		io.Copy(io.Discard, stderr)
	}()
	var endpoint string
	select {
	case endpoint = <-endpoints:
	case <-time.After(60 * time.Second):
		t.Fatal("the isolated runtime printed no endpoint")
	}
	cli := func(args ...string) []byte {
		t.Helper()
		out, err := fixture.Output(ctx, exec.CommandContext(ctx, bin, append(args, "--endpoint", endpoint, "--timeout", "30s")...))
		if err != nil {
			t.Fatalf("openabstractions %v: %v\n%s", args, err, out)
		}
		return out
	}
	account, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	grant := func(program, action, resource string) {
		cli("rights", "grant", "--program", program, "--account", account.Uid, "--action", action, "--resource", resource, "--runtime-program", bin)
	}
	readers := []string{filepath.Clean(self)}
	var pythonProgram string
	if python != "" {
		out, err := exec.CommandContext(ctx, python, "-c", "import os,sys;print(os.path.realpath(sys.executable))").Output()
		if err != nil {
			t.Fatal(err)
		}
		pythonProgram = strings.TrimSpace(string(out))
		readers = append(readers, pythonProgram)
	}
	if cppProbe != "" {
		readers = append(readers, filepath.Clean(cppProbe))
	}
	for _, program := range readers {
		grant(program, "abstraction.inference/audit.read", "account")
	}

	// The native route: this process holds no complete rule.
	machine := client.NewUnverified(endpoint)
	chat, err := machine.ResolveInference(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	if reply, err := chat.Complete(ctx, request(servedModel, "hi")); err != nil || reply.Outcome != iwire.ReplyOutcomeNotPermitted {
		t.Fatalf("native complete without a rule: %+v %v", reply, err)
	}
	// The window route: Python under its own key and complete rule. A root
	// runtime is machine scope and holds no key (no_secure_store); the audit
	// then carries the native route only.
	windowed := python != ""
	var issued struct{ Key string }
	if windowed {
		grant(pythonProgram, "abstraction.inference/complete", "host:ollama")
		out, err := exec.CommandContext(ctx, bin, "inference", "key", "issue", "--for", pythonProgram, "--json", "--endpoint", endpoint, "--timeout", "30s").CombinedOutput()
		switch {
		case err != nil && runtime.GOOS != "windows" && os.Getuid() == 0 && strings.Contains(string(out), "no_secure_store"):
			t.Logf("running as root: the machine-scope runtime holds no local key, so the window route is not exercised here: %s", out)
			windowed = false
		case err != nil:
			t.Fatalf("key issue: %v\n%s", err, out)
		default:
			if err := json.Unmarshal(out, &issued); err != nil || issued.Key == "" {
				t.Fatalf("key issue: %v\n%s", err, out)
			}
		}
	}
	if windowed {
		t.Cleanup(func() {
			exec.Command(bin, "inference", "key", "revoke", "--for", pythonProgram, "--endpoint", endpoint, "--timeout", "30s").Run()
		})
		script := filepath.Join(root, "openabstractions-flat", "abstraction-inference", "go", "gateway", "testdata", "llm_style.py")
		out, err := fixture.Output(ctx, exec.CommandContext(ctx, python, script, "http://"+gateway+"/v1", issued.Key, servedModel))
		if err != nil || !strings.Contains(string(out), replyText) {
			t.Fatalf("llm-style client through the window: %v\n%s", err, out)
		}
	}

	operator, err := machine.ResolveInferenceOperator(ctx, client.Requirements{Scope: client.ScopeLocal})
	if err != nil {
		t.Fatal(err)
	}
	var entries []iwire.AuditEntry
	for cursor := int64(0); ; {
		page, err := operator.Audit(ctx, cursor, 256)
		if err != nil || page.Outcome != iwire.AuditOutcomePage {
			t.Fatalf("go audit: %+v %v", page, err)
		}
		entries = append(entries, page.Entries...)
		cursor = page.Next
		if page.AtEnd || len(page.Entries) == 0 {
			break
		}
	}
	want := auditLines(entries)
	native := false
	window := !windowed
	for _, e := range entries {
		native = native || e.Route == iwire.AuditRouteNative && e.Outcome == "not_permitted" && strings.EqualFold(e.Program, filepath.Clean(self)) && strings.Contains(e.Rung, "user=kernel")
		window = window || e.Route == iwire.AuditRouteWindow && e.Outcome == "completed" && strings.EqualFold(e.Program, pythonProgram) && strings.HasPrefix(e.Rung, "tcp-loopback/"+runtime.GOOS)
	}
	if !native || !window {
		t.Fatalf("the audit lacks the native refusal or the window completion:\n%s", want)
	}
	if cppProbe != "" {
		out, err := fixture.Output(ctx, exec.CommandContext(ctx, cppProbe, endpoint, "audit"))
		if err != nil || onlyAudit(out) != want {
			t.Fatalf("the C++ consumer read another audit: %v\n%s\nwant:\n%s", err, out, want)
		}
		t.Log("cpp audit matches")
	}
	if python != "" {
		script, _ := filepath.Abs("py_consumer.py")
		out, err := fixture.Output(ctx, exec.CommandContext(ctx, python, script, endpoint, "audit"))
		if err != nil || onlyAudit(out) != want {
			t.Fatalf("the Python consumer read another audit: %v\n%s\nwant:\n%s", err, out, want)
		}
		t.Log("python audit matches")
	}
	fmt.Printf("PASS shared audit: %d entries read identically by go%s%s\n", len(entries), map[bool]string{true: ", cpp"}[cppProbe != ""], map[bool]string{true: ", python"}[python != ""])
}
