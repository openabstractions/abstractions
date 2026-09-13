package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-identity/listen"
	modelclient "github.com/openabstractions/abstraction-model/go/client"
)

func TestCentralRuntimeModelLookup(t *testing.T) {
	if runtime.GOOS == "darwin" {
		t.Skip("current Program proof limitation")
	}
	dir := t.TempDir()
	for _, key := range []string{"HOME", "APPDATA", "ProgramData", "XDG_CONFIG_HOME"} {
		t.Setenv(key, dir)
	}
	endpoint := listen.Endpoint(fmt.Sprintf("central-model-%d-%d", os.Getpid(), time.Now().UnixNano()))
	opts := runtimeFlags{endpoint: endpoint, logEndpoint: endpoint + "-log", configEndpoint: endpoint + "-config", out: filepath.Join(dir, "private-log"), withoutJobs: true}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, opts, func() error { close(ready); return nil }) }()
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("central runtime did not stop")
		}
	}()
	select {
	case <-ready:
	case <-ctx.Done():
		t.Fatal("central runtime not ready")
	}
	lookup, err := client.New(endpoint).ResolveModel(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	// Unknown registry proves default service wiring without external lookup.
	r, err := lookup.ResolveContext(ctx, modelclient.Ref{Registry: "unconfigured-test-registry", Repo: "weights"})
	if err != nil || r.Outcome != "unavailable" || r.Request != nil {
		t.Fatalf("lookup %+v %v", r, err)
	}
}
