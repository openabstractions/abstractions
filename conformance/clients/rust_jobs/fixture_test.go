package rust_jobs_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	request "github.com/openabstractions/abstraction-download/go/abstraction/download/request"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync/atomic"
	"testing"
	"time"

	execution "github.com/openabstractions/abstraction-download/go/serve"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	resolution "github.com/openabstractions/abstraction-facade/go/resolution"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
)

func TestInstalledRustJobs(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("current Program proof limitation")
	}
	python := os.Getenv("OA_RUST_JOBS")
	if python == "" {
		t.Fatal("run the installed Rust fixture runner")
	}
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "XDG_CONFIG_HOME"} {
		t.Setenv(key, home)
	}
	t.Setenv("ProgramData", filepath.Join(home, "machine"))
	for _, key := range []string{"ABSTRACTION_STORE", "ABSTRACTION_NAS_STORE", "ABSTRACTION_LOG", "ABSTRACTION_LOG_SERVICE"} {
		t.Setenv(key, "")
	}
	body := make([]byte, 256*1025)
	for i := range body {
		body[i] = byte(i)
	}
	var gets atomic.Int32
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			gets.Add(1)
		}
		w.Header().Set("Content-Length", fmt.Sprint(len(body)))
		w.Write(body)
	}))
	defer source.Close()
	prefix := fmt.Sprintf("rust-jobs-%d-%d", os.Getpid(), time.Now().UnixNano())
	options := host.Options{Endpoint: listen.Endpoint(prefix), LogEndpoint: listen.Endpoint(prefix + "l"), ConfigEndpoint: listen.Endpoint(prefix + "c"), ModelEndpoint: listen.Endpoint(prefix + "m"), JobEndpoint: listen.Endpoint(prefix + "j"), JobRoot: filepath.Join(home, "private"), JobOwner: "rust-jobs-owner", JobExecutor: execution.HTTPExecution{}}
	h, err := host.Listen(options)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() {
		cancel()
		h.Close()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("runtime cleanup timeout")
		}
	}()
	// Provider attribution is independent of the durable owner's identity.
	var candidates []resolution.Candidate
	for _, contract := range []string{"abstraction.job/acceptance@1", "abstraction.job/operations@1", "abstraction.job/inventory@1"} {
		candidates = append(candidates, resolution.Candidate{Ready: true, Reference: wire.ServiceReference{Provider: "implementation-A", Capability: "abstraction.job", Contract: contract, Scope: "local", Transport: resolution.LocalTransport, Endpoint: options.JobEndpoint}})
	}
	catalog, err := resolution.New(candidates)
	if err != nil {
		t.Fatal(err)
	}
	aliasEndpoint := listen.Endpoint(prefix + "-alias")
	alias, err := resolution.Listen(aliasEndpoint, catalog, func(p *identity.Peer, _ wire.ServiceReference) bool {
		return p != nil && p.Check(listen.Program) == nil
	})
	if err != nil {
		t.Fatal(err)
	}
	aliasDone := make(chan error, 1)
	go func() { aliasDone <- alias.Serve(ctx) }()
	defer func() {
		alias.Close()
		select {
		case <-aliasDone:
		case <-time.After(5 * time.Second):
			t.Error("alias resolver cleanup timeout")
		}
	}()
	clientHome := filepath.Join(home, "client")
	if err := os.Mkdir(clientHome, 0700); err != nil {
		t.Fatal(err)
	}
	spec := request.Encode(&request.Request{Artifact: request.Artifact{Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(body)), Size: int64(len(body))}, Sources: []request.Source{{Scheme: "http", Locator: source.URL}}})
	cmd := exec.CommandContext(ctx, python, aliasEndpoint, hex.EncodeToString(spec))
	cmd.Dir = clientHome
	cmd.Env = append(os.Environ(), "HOME="+clientHome, "USERPROFILE="+clientHome, "APPDATA="+clientHome, "XDG_CONFIG_HOME="+clientHome, "ProgramData="+clientHome, "PYTHONDONTWRITEBYTECODE=1")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("Rust: %v %s", err, out)
	}
	t.Log(string(out))
	if gets.Load() != 1 {
		t.Fatalf("external effect repeated: %d", gets.Load())
	}
	entries, err := os.ReadDir(clientHome)
	if err != nil || len(entries) != 0 {
		t.Fatal("client touched provider storage", entries, err)
	}
}
