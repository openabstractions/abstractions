package main

import (
	"context"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// newLendingTestProvider builds a runtimeProviders holding one on-demand
// lending declaration, without launching any real process: the supervisor's
// readiness is driven directly by the test through s.set, exactly as a real
// probe would drive it after a launch.
func newLendingTestProvider(t *testing.T, name string) (*runtimeProviders, *providerSupervisor) {
	t.Helper()
	p := &runtimeProviders{supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}), report: func(error) {}}
	f := providerFile{Declaration: providerDeclaration{Name: name, Program: "unused", Endpoint: "oa-test-lend-" + name,
		Transport: transportNative, Contracts: []string{storageLending}, Activation: wire.ActivationOnDemand.String()}}
	s := newProviderSupervisor(p, f)
	p.supervisors[name] = s
	return p, s
}

// activated reports whether the supervisor's on-demand launch was asked for,
// without s.run ever having been started (these tests drive readiness
// directly, so activate's usual effect of starting supervision is unused).
func activated(s *providerSupervisor) bool {
	select {
	case <-s.wanted:
		return true
	default:
		return false
	}
}

// A provider already ready costs the first call nothing beyond the transport
// build: FAC-R8's "a provider that is ready costs a resolution nothing beyond
// the description read."
func TestLendingProviderFastReadySucceedsFirstCall(t *testing.T) {
	p, s := newLendingTestProvider(t, "fast")
	s.set(wire.DeclarationReadinessReady, "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, reason := p.lendingProvider(ctx)
	if client == nil || reason != "" {
		t.Fatalf("fast provider: client=%v reason=%q", client, reason)
	}
}

// FAC-R8: the first resolution that launches an on-demand provider waits for
// its readiness inside the caller's own deadline. A caller whose deadline
// passes first reads unavailable with reason activating:<name>, the launch
// continues, and the next call finds the provider ready.
func TestLendingProviderActivatingThenReadyOnLaterCall(t *testing.T) {
	p, s := newLendingTestProvider(t, "slow")

	// The provider becomes ready well after the first caller's short deadline,
	// simulating a slow-starting on-demand provider.
	go func() {
		time.Sleep(150 * time.Millisecond)
		s.set(wire.DeclarationReadinessReady, "")
	}()

	shortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	client, reason := p.lendingProvider(shortCtx)
	if client != nil || reason != "activating:slow" {
		t.Fatalf("first call under a short deadline: client=%v reason=%q, want activating:slow", client, reason)
	}
	if !activated(s) {
		t.Fatalf("the launch was not activated")
	}

	// The launch keeps running in the background; a later call with room to
	// wait finds it ready.
	laterCtx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	client, reason = p.lendingProvider(laterCtx)
	if client == nil || reason != "" {
		t.Fatalf("later call: client=%v reason=%q", client, reason)
	}
}

// FAC-R8: the wait never exceeds the runtime's launch budget, even with a
// caller context carrying no deadline of its own, and a provider that never
// becomes ready.
func TestLendingProviderLaunchBudgetBoundsANeverReadyProvider(t *testing.T) {
	previous := lendingLaunchBudget
	lendingLaunchBudget = 60 * time.Millisecond
	t.Cleanup(func() { lendingLaunchBudget = previous })

	p, s := newLendingTestProvider(t, "stuck")

	start := time.Now()
	client, reason := p.lendingProvider(context.Background())
	elapsed := time.Since(start)
	if client != nil || reason != "activating:stuck" {
		t.Fatalf("never-ready provider: client=%v reason=%q, want activating:stuck", client, reason)
	}
	if elapsed > lendingLaunchBudget+500*time.Millisecond {
		t.Fatalf("wait exceeded the launch budget: %v", elapsed)
	}
	if !activated(s) {
		t.Fatalf("the launch was not activated")
	}
}
