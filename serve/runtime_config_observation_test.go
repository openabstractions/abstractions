package main

import (
	"context"
	"testing"

	wire "github.com/openabstractions/abstraction-config/go/abstraction/config"
	"github.com/openabstractions/abstraction-facade/go/client"
)

// isolatedRuntime's composition gives the runtime a state-dir-scoped
// configuration store (serve/runtime.go's isolatedConfigStore), the same
// store every --isolated runtime and any composition given an explicit
// --state-dir gets. Before fileConfigObservationSource (serve/runtime_config_
// observation.go) was wired into hostOptions.ConfigObservationSource, no
// observation source was ever enabled for that store: EnableObservation was
// never called, abstraction.config/observer@1 never registered as a
// candidate, and the Panel's "Watch settings" always failed as
// "incompatible". This proves the contract now resolves and delivers a
// snapshot.
func TestIsolatedRuntimeConfigObserverDeliversASnapshot(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current platform")
	}
	options, _ := isolatedRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	}()
	observer, err := client.New(options.endpoint).ResolveConfigObserver(ctx, client.Requirements{})
	if err != nil {
		t.Fatalf("resolve abstraction.config/observer@1: %v", err)
	}
	snapshot, err := observer.ObserveContext(ctx, wire.RunOverrides{}, "", 0)
	if err != nil || snapshot.Outcome.String() != "snapshot" || snapshot.Snapshot == nil {
		t.Fatalf("observe: %+v %v", snapshot, err)
	}
}
