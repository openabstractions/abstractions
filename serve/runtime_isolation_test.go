package main

import (
	"context"
	configwire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	"testing"
	"time"
)

func TestRuntimeLogStorageFailurePreservesIndependentServices(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current platform")
	}
	options, _ := isolatedRuntime(t)
	options.out = t.TempDir() // A directory cannot be opened as the log file.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("failed log disabled runtime: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(5 * time.Second):
			t.Error("runtime did not drain")
		}
	}()
	result, err := resolution.NewClient(options.endpoint, time.Second).Resolve(ctx, wire.ResolveRequest{Capability: "abstraction.logging", Contracts: []string{"abstraction.logging/sink@1"}, Scope: wire.ScopeLocal})
	if err != nil || result.Status != "not_ready" {
		t.Fatalf("failed log advertised ready: %+v %v", result, err)
	}
	machine := client.New(options.endpoint)
	config, err := machine.ResolveConfig(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = config.ReadWithOverrides(configwire.RunOverrides{}); err != nil {
		t.Fatal(err)
	}
	jobs, err := machine.ResolveJobs(ctx, client.Requirements{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = jobs.GetHistoryWindow(ctx); err != nil {
		t.Fatal(err)
	}
}
