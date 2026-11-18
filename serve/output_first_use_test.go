package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
)

// syncOutputCalls are the synchronous calls that publish generated output:
// image@1 under output:image and speech@1 under output:speech, through the
// gateway window's routes.
var syncOutputCalls = []struct{ name, path, body, resource string }{
	{"image", "/v1/images/generations", `{"model":"gw-image-gen-model","prompt":"a lighthouse","n":1,"size":"512x512","response_format":"b64_json"}`, "output:image"},
	{"speech", "/v1/audio/speech", `{"model":"gw-speech-model","voice":"alloy","input":"hello","response_format":"wav"}`, "output:speech"},
}

// syncOutputApp starts an isolated runtime with the gateway window and one
// hosted image and speech host, and returns the runtime, the stand-in
// application (this test executable, calling through the window with its own
// key, the route OpenCode's image tool and Speak take) and its POST. The
// application holds no rule; complete permits the host when complete is true.
func syncOutputApp(t *testing.T, complete bool) (runtimeFlags, string, func(path, body string) (int, string)) {
	t.Helper()
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	if !identity.LoopbackCeiling().Bindable {
		t.Skip("this platform cannot bind a loopback peer")
	}
	fixture := newGwChatFixture(t)
	options, _ := isolatedRuntime(t, isolatedRuntimeOptions{Gateway: true})
	hosts := fmt.Sprintf(`{"local":[],"hosted":[{"name":"gwhost","base":%q,"wire":%q,"credential":"gw-cred","profiles":["speech","image"]}]}`,
		fixture.server.URL, router.WireOpenAICompatible)
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
	ready, done := make(chan string, 1), make(chan error, 1)
	go func() {
		done <- runRuntimeReadyAddress(ctx, options, func(address string) error { ready <- address; return nil })
	}()
	select {
	case options.gateway = <-ready:
	case err := <-done:
		cancel()
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		dumpGoroutines()
		cancel()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	t.Cleanup(func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	})
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	self = filepath.Clean(self)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	out, diagnostics, err := runCredentials(t, gwFixtureSecret+"\n", append([]string{"add", "gw-cred", "--target", "127.0.0.1",
		"--for", inference.SpeechContract, "--for", inference.ImageContract, "--for", "abstraction.router/router@1",
		"--from-stdin", "--use-by", self}, endpoint...)...)
	requireUserScopeAdd(t, err, out+diagnostics)
	if complete {
		gwGrant(t, options, self, inference.ActionComplete, inference.ResourceHost("gwhost"))
	}
	out, err = runInference(t, append([]string{"key", "issue", "--for", self, "--credential", "gw-cred", "--json"}, endpoint...)...)
	if err != nil {
		requireUserScopeAdd(t, err, out)
	}
	var issued struct{ Key string }
	if err := json.Unmarshal([]byte(out), &issued); err != nil || !strings.HasPrefix(issued.Key, localKeyPrefix) {
		t.Fatalf("key issue: %v\n%s", err, out)
	}
	client := &http.Client{Timeout: 20 * time.Second}
	post := func(path string, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, "http://"+options.gateway+path, bytes.NewReader([]byte(body)))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+issued.Key)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(raw)
	}
	return options, self, post
}

// pendingQuestionOn is the id of the pending first-use question of program
// on action and resource, or "".
func pendingQuestionOn(t *testing.T, options runtimeFlags, program, action, resource string) string {
	t.Helper()
	for _, q := range pendingFirstUse(t, options) {
		p, a, r, ok := asks.FirstUse(q.Key, q.About, q.Text)
		if ok && samePrograms(p, program) && a == action && r == resource {
			return q.ID
		}
	}
	return ""
}

// INF-S7, research/wake-and-inference-rights/REPORT.md recommendation 7: a
// synchronous image@1 or speech@1 call refused for want of its output
// profile admits the paired first-use question on output:image or
// output:speech, as a job refused for its output does. The call is refused at
// once (refuse and retry, as for jobs); one Allow writes content.write and
// content.read on that profile, and the retry completes.
func TestASynchronousOutputRefusalAdmitsThePairedQuestion(t *testing.T) {
	options, self, post := syncOutputApp(t, true)
	for _, c := range syncOutputCalls {
		t.Run(c.name, func(t *testing.T) {
			code, body := post(c.path, c.body)
			if code == http.StatusOK {
				t.Fatalf("a call without %s completed: %s", c.resource, body)
			}
			question := pendingQuestionOn(t, options, self, contentWriteAction, c.resource)
			if question == "" {
				t.Fatalf("no paired question on %s after a refused call (%d %s): %+v", c.resource, code, body, pendingFirstUse(t, options))
			}
			allowed := answerFirstUse(t, options, question, "allow")
			if allowed.Edit == nil || allowed.Edit.Outcome.String() != "applied" || allowed.PairedEdit == nil || allowed.PairedEdit.Outcome.String() != "applied" ||
				allowed.Paired == nil || allowed.Paired.Action != contentReadAction || allowed.Paired.Resource != c.resource {
				t.Fatalf("one Allow on %s %+v", c.resource, allowed)
			}
			if code, body := post(c.path, c.body); code != http.StatusOK {
				t.Fatalf("retry after Allow on %s: %d %s", c.resource, code, body)
			}
		})
	}
}

// INF-V4 for a hosted server (INF-A3): the call asks one question about
// the account in place of complete on the host. In this in-process fixture
// the test executable is both the app and the runtime, which holds apply on
// the account for its own host checks, so the account question cannot be
// reached here. runtime_hosted_use_test.go covers it
// (TestHostedServerFirstUseAsksOnceAboutItsAccount).

// refusalReason is the reason of the last refused window call of profile
// the audit records for program.
func refusalReason(t *testing.T, options runtimeFlags, program, profile string) string {
	t.Helper()
	out, err := runInference(t, "audit", "--json", "--endpoint", options.endpoint, "--timeout", "30s")
	if err != nil {
		t.Fatalf("audit: %v\n%s", err, out)
	}
	var audit struct {
		Entries []struct{ Route, Program, Profile, Outcome, Reason string }
	}
	if err := json.Unmarshal([]byte(out), &audit); err != nil {
		t.Fatalf("audit json: %v\n%s", err, out)
	}
	reason := ""
	for _, e := range audit.Entries {
		if e.Route == inference.RouteWindow && e.Profile == profile && e.Outcome != "completed" && samePrograms(e.Program, program) {
			reason = e.Reason
		}
	}
	if reason == "" {
		t.Fatalf("no refused %s window call in the audit:\n%s", profile, out)
	}
	return reason
}
