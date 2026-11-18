package main

import (
	"context"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/listen"
)

// A runtime whose own endpoint is taken fails in host.Listen after the
// credentials and applications compositions bound their listeners. Every
// companion name is free afterwards, and a second start on the same names
// reaches readiness.
func TestRuntimeStartupFailureReleasesCompanionListeners(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("the Unix fixture cannot prove Program; startup refuses before host.Listen")
	}
	options, _ := isolatedRuntime(t)
	blocker, err := listen.Listen(options.endpoint)
	if err != nil {
		t.Fatal(err)
	}
	err = runRuntimeReady(context.Background(), options, func() error {
		t.Error("a runtime without its endpoint acknowledged readiness")
		return nil
	})
	blocker.Close()
	if err == nil {
		t.Fatal("a runtime whose endpoint is taken started")
	}
	// A composition that keeps its listener after the failed start leaves its
	// name unbindable; the runtime only complains about a companion it could
	// not compose, so the second start alone would not show the leak.
	credentials, rights, _, err := credentialsEndpoints(options)
	if err != nil {
		t.Fatal(err)
	}
	companions := []string{credentials, rights}
	for _, derive := range []func(runtimeFlags) (string, error){inferenceEndpoint, inferenceRemoteEndpoint, asksEndpoint, runtimeRouterEndpoint,
		resourceTableEndpoint, registryEndpoint, lendingEndpoint, runtimeApplicationsEndpoint, runtimeContentEndpoint} {
		endpoint, err := derive(options)
		if err != nil {
			t.Fatal(err)
		}
		companions = append(companions, endpoint)
	}
	for _, endpoint := range companions {
		l, err := listen.Listen(endpoint)
		if err != nil {
			t.Fatalf("companion listener %s not released after the failed start: %v", endpoint, err)
		}
		l.Close()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("second start on the released names: %v", err)
	case <-time.After(runtimeWait):
		dumpGoroutines()
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	cancel()
	if err := awaitStopped(t, done, "runtime"); err != nil {
		t.Error(err)
	}
}
