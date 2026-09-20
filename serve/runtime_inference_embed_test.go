package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	fwire "github.com/openabstractions/abstraction-facade/go-core/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go-core/resolution"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceclient "github.com/openabstractions/abstraction-inference/go/client"
)

// An application resolves abstraction.inference/embed@1 from the runtime and
// embeds through a hosted host: refused without a complete rule and before
// any request, then served with the credential applied once for consumer
// embed@1, the vectors equal to the upstream's.
func TestRuntimeEmbedsThroughAHostedHost(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	var mu sync.Mutex
	var embedAuth []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorized := r.Header.Get("Authorization") == "Bearer "+testCredentialSecret
		if r.Method == http.MethodPost {
			mu.Lock()
			embedAuth = append(embedAuth, r.Header.Get("Authorization"))
			mu.Unlock()
		}
		switch {
		case !authorized:
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		case r.Method == http.MethodGet:
			w.Write([]byte(`{"data":[{"id":"text-embedding-3-small"}]}`))
		case r.URL.Path == "/api/v1/embeddings":
			w.Write([]byte(`{"data":[{"index":1,"embedding":[0.25,-2]},{"index":0,"embedding":[1.5,0]}],"usage":{"prompt_tokens":4}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(upstream.Close)
	options, _ := isolatedRuntime(t)
	config := fmt.Sprintf(`{"local":[],"hosted":[{"name":"openrouter","base":%q,"wire":"openai-compatible","credential":"openrouter"}]}`, upstream.URL+"/api/v1")
	for _, file := range []struct{ path, body string }{
		{filepath.Join(options.stateDir, "credentials", credentialsBackendFile), "file-0600\n"},
		{filepath.Join(options.stateDir, "inference", inferenceHostsFile), config},
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
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	exe = filepath.Clean(exe)
	out, diag, err := runCredentials(t, testCredentialSecret+"\n", "add", "openrouter", "--target", "127.0.0.1",
		"--for", inference.EmbedContract, "--for", "abstraction.router/router@1", "--from-stdin", "--use-by", exe, "--endpoint", options.endpoint, "--timeout", "30s")
	requireUserScopeAdd(t, err, out+diag)

	call, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	result, err := resolution.NewUnverifiedClient(options.endpoint, 2*time.Second).Resolve(call, fwire.ResolveRequest{Capability: "abstraction.inference", Contracts: []string{inference.EmbedContract}, Guarantees: []string{inference.GuaranteeHosted}, Scope: fwire.ScopeRemote})
	if err != nil || result.Status != fwire.ResolutionStatusResolved {
		t.Fatalf("resolve embed@1: %+v %v", result, err)
	}
	bound, err := resolution.BindLocal(call, *result.Reference, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	embeddings := inferenceclient.NewEmbeddingsWithTransport(bound)
	request := iwire.EmbedRequest{Model: "text-embedding-3-small", Inputs: []string{"first", "second"}, Guarantees: []iwire.RequestGuarantee{iwire.RequestGuaranteeHostedAllowed}, Credential: "openrouter"}

	refused, err := embeddings.Embed(call, request)
	if err != nil || refused.Outcome != iwire.EmbedOutcomeNotPermitted || refused.Reason != "rights:not_granted" {
		t.Fatalf("embed without a complete rule: %+v %v", refused, err)
	}
	mu.Lock()
	sent := len(embedAuth)
	mu.Unlock()
	if sent != 0 {
		t.Fatal("a refused embed reached the upstream")
	}

	setCompleteRule(t, options.endpoint, exe, "openrouter", true)
	reply, err := embeddings.Embed(call, request)
	if err != nil || reply.Outcome != iwire.EmbedOutcomeCompleted || reply.Host != "openrouter" || reply.Dimensions != 2 || reply.Usage.Input != 4 {
		t.Fatalf("embed with a complete rule: %+v %v", reply, err)
	}
	vectors, err := inferenceclient.Vectors(reply)
	if err != nil || len(vectors) != 2 || vectors[0][0] != 1.5 || vectors[0][1] != 0 || vectors[1][0] != 0.25 || vectors[1][1] != -2 {
		t.Fatalf("vectors %v %v", vectors, err)
	}
	mu.Lock()
	got := append([]string(nil), embedAuth...)
	mu.Unlock()
	if len(got) != 1 || got[0] != "Bearer "+testCredentialSecret {
		t.Fatalf("upstream embeddings Authorization %q", got)
	}
	raw, _ := json.Marshal([]any{refused, reply})
	if strings.Contains(string(raw), testCredentialSecret) {
		t.Fatal("a reply carries the secret")
	}
}
