package main

import (
	"context"
	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	"os"
	"runtime/pprof"
	"testing"
	"time"
)

// dumpGoroutines writes every goroutine's stack to stderr: a runtime that
// misses its startup bound shows where it waits.
func dumpGoroutines() {
	//unchecked: a diagnostic written just before the test fails; nothing can act on a failed write
	_ = pprof.Lookup("goroutine").WriteTo(os.Stderr, 1)
}

// The runtime's startup and each call below wait for their event, the ready
// signal or the answer, bounded by runtimeWait. Startup alone took 6 to 9 s
// here; the test's former 5 s context failed healthy runs.
func TestRuntimeLogStorageFailurePreservesIndependentServices(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current platform")
	}
	options, _ := isolatedRuntime(t)
	options.out = t.TempDir() // A directory cannot be opened as the log file.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("failed log disabled runtime: %v", err)
	case <-time.After(runtimeWait):
		dumpGoroutines()
		cancel()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(runtimeWait):
			t.Error("runtime did not drain")
		}
	}()
	call, stop := context.WithTimeout(ctx, runtimeWait)
	defer stop()
	result, err := resolution.NewClient(options.endpoint, runtimeWait).Resolve(call, wire.ResolveRequest{Capability: "abstraction.logging", Contracts: []string{"abstraction.logging/sink@1"}, Scope: wire.ScopeLocal})
	if err != nil || result.Status.String() != "not_ready" {
		t.Fatalf("failed log advertised ready: %+v %v", result, err)
	}
	machine := client.New(options.endpoint)
	config, err := machine.ResolveConfig(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = config.ReadWithOverrides(configwire.RunOverrides{}); err != nil {
		t.Fatal(err)
	}
	jobs, err := machine.ResolveJobs(call, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobs.GetHistoryWindow(call); err != nil {
		t.Fatal(err)
	}
}
