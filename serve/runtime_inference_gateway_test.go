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
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
	router "github.com/openabstractions/abstraction-router/go"
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
	mux.HandleFunc("/v1/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[{"id":"fixture-chat:1b"}]}`))
	})
	mux.HandleFunc("/api/v0/models", func(w http.ResponseWriter, _ *http.Request) {
		w.Write([]byte(`{"data":[{"id":"fixture-chat:1b","type":"llm","state":"not-loaded"}]}`))
	})
	mux.HandleFunc("/fixture/chats", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, chats.Load())
	})
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

// This separately launched image owns the listener and writes the product's
// actual server record. The runtime still establishes native process evidence.
func TestGatewayEngineProcess(t *testing.T) {
	home := os.Getenv("OA_GATEWAY_ENGINE_FIXTURE")
	if home == "" {
		return
	}
	server, _ := fakeOllama(t)
	_, port, err := net.SplitHostPort(strings.TrimPrefix(server.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(home, ".lmstudio", ".internal", "http-server-config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"port":`+port+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "ready"), []byte(server.URL), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
		t.Fatal(err)
	}
}

type gatewayFixtureEngine struct{ home, base, program string }

func startGatewayEngine(t *testing.T) gatewayFixtureEngine {
	t.Helper()
	home := t.TempDir()
	image := copyTestBinary(t, "gateway-fixture-engine")
	cmd := exec.Command(image, "-test.run=^TestGatewayEngineProcess$")
	cmd.Env = append(os.Environ(), "OA_GATEWAY_ENGINE_FIXTURE="+home)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	input, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		input.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			cmd.Process.Kill()
			<-done
			t.Error("fixture engine did not stop after its input closed")
		}
	})
	var base string
	eventually(t, 10*time.Second, "fixture engine product record", func() bool {
		raw, err := os.ReadFile(filepath.Join(home, "ready"))
		base = string(raw)
		return err == nil && base != ""
	})
	return gatewayFixtureEngine{home: home, base: base, program: image}
}

func (e gatewayFixtureEngine) chats(t *testing.T) int64 {
	t.Helper()
	response, err := (&http.Client{Timeout: 2 * time.Second}).Get(e.base + "/fixture/chats")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.ParseInt(string(raw), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	return n
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

func useGatewayProductRecord(t *testing.T, productHome string) {
	t.Helper()
	saved := declarationEnv
	declarationEnv = func(report func(error)) router.ProbeEnv {
		env := fixtureDeclarations(nil, nil)(report)
		env.GOOS, env.Home = runtime.GOOS, productHome
		env.ReadFile = os.ReadFile
		return env
	}
	t.Cleanup(func() { declarationEnv = saved })
}

func startGatewayFixture(t *testing.T, productHome string, operatorBase ...string) runtimeFlags {
	t.Helper()
	options, _ := isolatedRuntime(t, isolatedRuntimeOptions{Gateway: true})
	useGatewayProductRecord(t, productHome)
	hosts := `{"declared":true}`
	if len(operatorBase) > 0 {
		hosts = fmt.Sprintf(`{"local":[{"kind":"lmstudio","base":%q,"declared_by":"operator"}],"declared":true}`, operatorBase[0])
	}
	for _, file := range []struct{ path, body string }{
		{filepath.Join(options.stateDir, "credentials", credentialsBackendFile), "file-0600\n"},
		{filepath.Join(options.stateDir, "inference", inferenceHostsFile), hosts},
	} {
		writeStateFile(t, file.path, file.body)
	}
	_, _, namespace, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { removeStoreItems(t, namespace) })
	ctx, cancel := context.WithCancel(context.Background())
	ready := make(chan string, 1)
	done := make(chan error, 1)
	go func() {
		done <- runRuntimeReadyAddress(ctx, options, func(address string) error { ready <- address; return nil })
	}()
	select {
	case options.gateway = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		cancel()
		dumpGoroutines()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	t.Cleanup(func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	})
	return options
}

func gatewayLLM(t *testing.T, options runtimeFlags, key, python string, pythonArgs []string) (map[string]any, int) {
	t.Helper()
	script := filepath.Join("testdata", "llm_style.py")
	if _, err := os.Stat(script); os.IsNotExist(err) {
		script = privateInput(t, "..", "openabstractions-flat", "abstraction-inference", "adapters", "go", "gateway", "testdata", "llm_style.py")
	}
	cmd := exec.Command(python, append(pythonArgs, script, "http://"+options.gateway+"/v1", key, "fixture-chat:1b")...)
	cmd.Stderr = os.Stderr
	raw, err := cmd.Output()
	code := 0
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code = exit.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatalf("llm-style client printed %q", raw)
	}
	return result, code
}

func gatewayRequest(t *testing.T, options runtimeFlags, program, outcome string) iwire.RequestEntry {
	t.Helper()
	call, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	operator, err := client.New(options.endpoint).ResolveInferenceOperator(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	page, err := operator.Requests(call, 0, 256)
	if err != nil || page.Outcome != iwire.AuditOutcomePage {
		t.Fatalf("request records: %+v %v", page, err)
	}
	for i := len(page.Entries) - 1; i >= 0; i-- {
		e := page.Entries[i]
		if e.Entry.Route == iwire.AuditRouteWindow && samePrograms(e.Entry.Program, program) && e.Entry.Outcome == outcome {
			return e
		}
	}
	t.Fatalf("request records lack %s for %s: %+v", outcome, program, page)
	return iwire.RequestEntry{}
}

func gatewayProductReady(t *testing.T, options runtimeFlags, engine gatewayFixtureEngine) {
	t.Helper()
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "10s"}
	// Removing a shadow invalidates product discovery. Its negative router
	// reading retains the existing INF-H1 snapshot bound before a fresh probe.
	var last string
	defer func() {
		if t.Failed() {
			t.Log("last product reading:", last)
		}
	}()
	eventually(t, productDeclarationAge, "verified fixture product", func() bool {
		state := providerStates(t, endpoint)["lmstudio"]
		last = fmt.Sprintf("%+v", state)
		return state.DeclaredBy == router.DeclaredByLMStudio && state.Why == "host:presence_verified" &&
			state.Declaration.Host != nil && state.Declaration.Host.Base == engine.base
	})
}

func gatewayCompletedProductRequest(t *testing.T, options runtimeFlags, engine gatewayFixtureEngine, program string) {
	t.Helper()
	record := gatewayRequest(t, options, program, "completed")
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "10s"}
	state := providerStates(t, endpoint)[record.Entry.Host]
	if record.Entry.Host != "lmstudio" || state.DeclaredBy != router.DeclaredByLMStudio || state.Why != "host:presence_verified" ||
		state.Declaration.Host == nil || state.Declaration.Host.Base != engine.base {
		t.Fatalf("completed request host lacks its verified product reading: %+v %+v", record, state)
	}
}

// The runtime gateway discovers a separately running fixture engine from its
// product record on a private OS-assigned port. The command issues a key for
// Python, an llm-style Python client streams through the
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
	// The call refused before a complete rule is held for its question
	// (INF-W10); nobody answers it here.
	wait := gatewayQuestionWait
	gatewayQuestionWait = 200 * time.Millisecond
	t.Cleanup(func() { gatewayQuestionWait = wait })
	keyProgram := program
	if short, hasAlias := shortSubjectProgramAlias(t, program); hasAlias {
		keyProgram = short
	}
	engine := startGatewayEngine(t)
	options := startGatewayFixture(t, engine.home)
	gatewayProductReady(t, options, engine)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	out, err := runInference(t, append([]string{"key", "issue", "--for", keyProgram, "--json"}, endpoint...)...)
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
	llm := func() (map[string]any, int) {
		return gatewayLLM(t, options, issued.Key, pythonExe, pythonArgs)
	}
	if result, code := llm(); code != 3 || result["status"] != 403.0 || !strings.Contains(fmt.Sprint(result["body"]), "rights:not_granted") {
		t.Fatalf("before a complete rule: exit %d %v", code, result)
	}
	// The window verifies its loopback caller's code at bind and decides with
	// that peer: the first-use question the refusal admitted records the
	// publisher the native pipe binds for the same interpreter (ASK-A6). A
	// loopback peer outside Windows carries no code evidence (identity ID-T4).
	want := asks.Publisher{}
	if runtime.GOOS == "windows" {
		_, _, _, want = pythonPublisher(t)
	}
	asked := false
	for _, q := range pendingFirstUse(t, options) {
		if !samePrograms(q.About, program) {
			continue
		}
		asked = true
		if publisher, trusted := questionPublisher(t, options, q.ID); publisher != want.Subject || trusted != want.Trusted {
			t.Fatalf("a window question records publisher %q trusted=%v, want %+v", publisher, trusted, want)
		}
	}
	if !asked {
		t.Fatal("the window's not_granted refusal admitted no first-use question")
	}
	setCompleteRule(t, options.endpoint, program, "lmstudio", true)
	if result, code := llm(); code != 0 || result["text"] != "Hello through the runtime" {
		t.Fatalf("with a complete rule: exit %d %v", code, result)
	}
	if chats := engine.chats(t); chats != 1 {
		t.Fatalf("upstream chat requests %d", chats)
	}
	gatewayCompletedProductRequest(t, options, engine, program)
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
	out, err = runInference(t, append([]string{"key", "revoke", "--for", keyProgram}, endpoint...)...)
	if err != nil {
		t.Fatalf("key revoke: %v\n%s", err, out)
	}
	if result, code := llm(); code != 3 || result["status"] != 401.0 {
		t.Fatalf("a revoked key: exit %d %v", code, result)
	}
	out, err = runInference(t, append([]string{"key", "list", "--json"}, endpoint...)...)
	var keyList struct {
		Keys []struct {
			Program string
			State   iwire.KeyState
		}
	}
	if err != nil || json.Unmarshal([]byte(out), &keyList) != nil || len(keyList.Keys) != 1 ||
		keyList.Keys[0].Program != identity.NormalizeSubjectProgram(program) || keyList.Keys[0].State != iwire.KeyStateRevoked {
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

func TestRuntimeGatewayRefusesOperatorEngineWithoutProductEvidence(t *testing.T) {
	if !statusTransportProvesProgram(t) || !identity.LoopbackCeiling().Bindable {
		t.Skip("gateway requires native Program and loopback proof")
	}
	for _, mode := range []string{"operator-only", "shadows-product"} {
		t.Run(mode, func(t *testing.T) {
			engine := startGatewayEngine(t)
			home := t.TempDir()
			if mode == "shadows-product" {
				home = engine.home
			}
			options := startGatewayFixture(t, home, engine.base)
			endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
			out, err := runInference(t, append([]string{"host", "list", "--json"}, endpoint...)...)
			if err != nil || !strings.Contains(out, `"Name":"lmstudio"`) || !strings.Contains(out, `"DeclaredBy":"operator"`) {
				t.Fatalf("operator-only registration: %v\n%s", err, out)
			}
			python, pythonArgs, program := scriptPython(t)
			out, err = runInference(t, append([]string{"key", "issue", "--for", program, "--json"}, endpoint...)...)
			if err != nil {
				requireUserScopeAdd(t, err, out)
			}
			var issued struct{ Key string }
			if err := json.Unmarshal([]byte(out), &issued); err != nil || issued.Key == "" {
				t.Fatalf("key issue: %v\n%s", err, out)
			}
			setCompleteRule(t, options.endpoint, program, "lmstudio", true)
			if result, code := gatewayLLM(t, options, issued.Key, python, pythonArgs); code != 3 || !strings.Contains(fmt.Sprint(result["body"]), "no_host") {
				t.Fatalf("operator URL reached the engine: exit %d %v", code, result)
			}
			if chats := engine.chats(t); chats != 0 {
				t.Fatalf("operator URL sent %d upstream chats", chats)
			}
			if record := gatewayRequest(t, options, program, "no_host"); record.Entry.Reason != "engine:unverified:lmstudio" || len(record.UnavailableHosts) != 1 || record.UnavailableHosts[0] != "lmstudio" {
				t.Fatalf("request record lost the refusal reason: %+v", record)
			}
			if mode == "shadows-product" {
				call, cancel := context.WithTimeout(t.Context(), time.Minute)
				defer cancel()
				binding, err := client.New(options.endpoint).ResolveService(call, client.InferenceOperatorContract, client.Requirements{})
				if err != nil {
					t.Fatal(err)
				}
				// Withdrawal also refreshes product provenance and surveys the router.
				transport := binding.Transport()
				transport.Timeout = 15 * time.Second
				operator := inferenceclient.NewOperatorWithTransport(transport)
				hosts, err := operator.Hosts(call)
				if err != nil {
					t.Fatal(err)
				}
				removed, err := operator.RemoveHost(call, hosts.Revision, "lmstudio")
				if err != nil || removed.Outcome != iwire.EditOutcomeApplied || removed.Reason != "" {
					t.Fatalf("remove shadowing operator record: %+v %v", removed, err)
				}
				gatewayProductReady(t, options, engine)
				if result, code := gatewayLLM(t, options, issued.Key, python, pythonArgs); code != 0 || result["text"] != "Hello through the runtime" {
					t.Fatalf("product route after operator removal: exit %d %v", code, result)
				}
				if chats := engine.chats(t); chats != 1 {
					t.Fatalf("restored product sent %d chats", chats)
				}
				gatewayCompletedProductRequest(t, options, engine, program)
				hosts, err = operator.Hosts(call)
				if err != nil {
					t.Fatal(err)
				}
				disabled, err := operator.RemoveHost(call, hosts.Revision, "lmstudio")
				if err != nil || disabled.Outcome != iwire.EditOutcomeApplied || disabled.Reason != "disabled" {
					t.Fatalf("remove the product itself: %+v %v", disabled, err)
				}
				hosts, err = operator.Hosts(call)
				if err != nil {
					t.Fatal(err)
				}
				added, err := operator.AddHost(call, hosts.Revision, iwire.HostEntry{Name: "lmstudio", Kind: "lmstudio", Base: engine.base, Profiles: []string{"chat"}})
				if err != nil || added.Outcome != iwire.EditOutcomeApplied {
					t.Fatalf("re-enable the name: %+v %v", added, err)
				}
				if out, err := runInference(t, append([]string{"host", "list", "--json"}, endpoint...)...); err != nil || !strings.Contains(out, `"DeclaredBy":"operator"`) {
					t.Fatalf("re-enabled operator record: %v\n%s", err, out)
				}
				if result, code := gatewayLLM(t, options, issued.Key, python, pythonArgs); code != 3 || !strings.Contains(fmt.Sprint(result["body"]), "engine:unverified:lmstudio") || engine.chats(t) != 1 {
					t.Fatalf("re-enabled operator borrowed product proof: exit %d %v", code, result)
				}
			}
		})
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

func TestPrivateRuntimeGatewayAllowsOSAssignedPort(t *testing.T) {
	options, _ := isolatedRuntime(t, isolatedRuntimeOptions{Gateway: true})
	if err := validateRuntimeStateSelection(options); err != nil {
		t.Fatalf("private gateway cannot select an OS-assigned port: %v", err)
	}
}

func TestPrivateGatewayAllowanceDoesNotReachConfiguration(t *testing.T) {
	if _, err := parseRuntime([]string{"--gateway", "127.0.0.1:0"}, io.Discard); err == nil {
		t.Fatal("CLI accepted an ephemeral gateway port")
	}
	options, _ := isolatedRuntime(t, isolatedRuntimeOptions{Gateway: true})
	for _, change := range []func(*runtimeFlags){
		func(o *runtimeFlags) { o.gatewayEphemeral = false },
		func(o *runtimeFlags) { o.stateDir = "" },
		func(o *runtimeFlags) { o.gateway = "0.0.0.0:0" },
	} {
		copy := options
		change(&copy)
		if validateRuntimeStateSelection(copy) == nil {
			t.Fatal("ephemeral gateway escaped private test selection")
		}
	}
	path := filepath.Join(options.stateDir, "inference", inferenceGatewayFile)
	writeStateFile(t, path, `{"open":true,"address":"127.0.0.1:0"}`)
	g := &gatewayControl{path: path}
	if _, _, err := g.readSetting(); err == nil {
		t.Fatal("persisted gateway accepted an ephemeral port")
	}
}

func TestPrivateRuntimeGatewayReadyReportsBoundAddress(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current transport")
	}
	options, _ := isolatedRuntime(t, isolatedRuntimeOptions{Gateway: true})
	writeStateFile(t, filepath.Join(options.stateDir, "credentials", credentialsBackendFile), "file-0600\n")
	writeStateFile(t, filepath.Join(options.stateDir, "inference", inferenceHostsFile), `{"local":[]}`)
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	readyStop := errors.New("private ready receiving complete")
	address := ""
	err := runRuntimeReadyAddress(ctx, options, func(bound string) error {
		host, port, err := net.SplitHostPort(bound)
		if err != nil || host != "127.0.0.1" || port == "" || port == "0" {
			return fmt.Errorf("ready did not report a bound loopback address")
		}
		address = bound
		connection, err := net.DialTimeout("tcp4", bound, time.Second)
		if err != nil {
			return fmt.Errorf("listener was not bound before ready: %w", err)
		}
		if err := connection.Close(); err != nil {
			return err
		}
		return readyStop
	})
	if !errors.Is(err, readyStop) {
		t.Fatalf("startup/ready: %v", err)
	}
	// Callback failure must join and close the actual listener as it unwinds.
	_, port, err := net.SplitHostPort(address)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", port))
	if err != nil {
		t.Fatalf("ready failure retained its gateway listener: %v", err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
}
