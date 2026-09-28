package main

import (
	"context"
	"os"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
)

// newNativeAdmitTestProvider builds a runtimeProviders holding one on-demand
// native chat declaration, and the router.Host nativeInferenceHosts would
// build for it, without launching any real process: the supervisor's
// readiness is driven directly by the test through s.set, exactly as a real
// probe would drive it after a launch (mirrors newLendingTestProvider in
// runtime_lending_test.go).
func newNativeAdmitTestProvider(t *testing.T, name string) (*runtimeProviders, *providerSupervisor, *router.Host) {
	t.Helper()
	p := &runtimeProviders{supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}), report: func(error) {}}
	f := providerFile{Declaration: providerDeclaration{Name: name, Program: "unused", Endpoint: "oa-test-native-" + name,
		Transport: transportNative, Contracts: []string{inference.Contract}, Models: []string{"owner/fixture-model"}, Activation: wire.ActivationOnDemand.String()}}
	s := newProviderSupervisor(p, f)
	p.supervisors[name] = s
	host, err := router.NewNative(name, f.Declaration.Endpoint, listen.ServerExpectation{}, f.Declaration.Models, nil)
	if err != nil {
		t.Fatal(err)
	}
	host.BindingID = f.bindingID()
	return p, s, host
}

// A host already ready costs a Start nothing beyond the read: FAC-R8's "a
// provider that is ready costs a call nothing beyond the description read."
func TestNativeAdmitFastReadySucceedsFirstCall(t *testing.T) {
	p, s, host := newNativeAdmitTestProvider(t, "fast")
	s.set(wire.DeclarationReadinessReady, "")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	reason, ok := p.nativeAdmit(ctx, host, "owner/fixture-model")
	if !ok || reason != "" {
		t.Fatalf("fast provider: ok=%v reason=%q", ok, reason)
	}
	if activated(s) {
		t.Fatalf("a ready provider was activated")
	}
}

// FAC-R8: the first chat@1 Start that reaches an on-demand declared native
// provider's OA endpoint activates its launch and waits for readiness inside
// the caller's own deadline. A caller whose deadline passes first reads
// unavailable with reason activating:<name>, the launch continues, and the
// next call finds the provider ready, exactly as the lending forward's
// lendingProvider behaves.
func TestNativeAdmitActivatingThenReadyOnLaterCall(t *testing.T) {
	p, s, host := newNativeAdmitTestProvider(t, "slow")

	// The provider becomes ready well after the first caller's short deadline,
	// simulating a slow-starting on-demand provider.
	go func() {
		time.Sleep(150 * time.Millisecond)
		s.set(wire.DeclarationReadinessReady, "")
	}()

	shortCtx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	reason, ok := p.nativeAdmit(shortCtx, host, "owner/fixture-model")
	if ok || reason != "activating:slow" {
		t.Fatalf("first call under a short deadline: ok=%v reason=%q, want activating:slow", ok, reason)
	}
	if !activated(s) {
		t.Fatalf("the launch was not activated")
	}

	// The launch keeps running in the background; a later call with room to
	// wait finds it ready.
	laterCtx, cancel2 := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel2()
	reason, ok = p.nativeAdmit(laterCtx, host, "owner/fixture-model")
	if !ok || reason != "" {
		t.Fatalf("later call: ok=%v reason=%q", ok, reason)
	}
}

// FAC-R8: the wait never exceeds the runtime's launch budget, even with a
// caller context carrying no deadline of its own, and a provider that never
// becomes ready.
func TestNativeAdmitLaunchBudgetBoundsANeverReadyProvider(t *testing.T) {
	previous := nativeInferenceLaunchBudget
	nativeInferenceLaunchBudget = 60 * time.Millisecond
	t.Cleanup(func() { nativeInferenceLaunchBudget = previous })

	p, s, host := newNativeAdmitTestProvider(t, "stuck")

	start := time.Now()
	reason, ok := p.nativeAdmit(context.Background(), host, "owner/fixture-model")
	elapsed := time.Since(start)
	if ok || reason != "activating:stuck" {
		t.Fatalf("never-ready provider: ok=%v reason=%q, want activating:stuck", ok, reason)
	}
	if elapsed > nativeInferenceLaunchBudget+500*time.Millisecond {
		t.Fatalf("wait exceeded the launch budget: %v", elapsed)
	}
	if !activated(s) {
		t.Fatalf("the launch was not activated")
	}
}

// A host the router picked that is not a declared native provider (hosted,
// or a local product) is never gated: FAC-R8 is about a declared on-demand
// provider's own launch, not every host's readiness.
func TestNativeAdmitIgnoresNonNativeHosts(t *testing.T) {
	p, _, _ := newNativeAdmitTestProvider(t, "irrelevant")
	hosted := &router.Host{Name: "hosted-host"}
	reason, ok := p.nativeAdmit(context.Background(), hosted, "some-model")
	if !ok || reason != "" {
		t.Fatalf("hosted host: ok=%v reason=%q", ok, reason)
	}
}

// nativeInferenceHosts stays the router's supported local chat mediation
// profile, plus an on-demand declaration still starting its first launch
// (FAC-R8): the router then has a host for a pinned call to wait on. A
// declaration reached any other way must already answer to be routed at all,
// as before.
func TestNativeInferenceHostsIncludesOnDemandBeforeFirstReady(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}}
	onDemand := providerFile{Version: providerFileVersion, DeclaredBy: program,
		Declaration: providerDeclaration{Name: "ondemand", Program: program, Endpoint: "private-ondemand", Transport: transportNative,
			Contracts: []string{inference.Contract}, Models: []string{"owner/fixture-model"}, Activation: wire.ActivationOnDemand.String()}}
	attach := providerFile{Version: providerFileVersion, DeclaredBy: program,
		Declaration: providerDeclaration{Name: "attach", Program: program, Endpoint: "private-attach", Transport: transportNative,
			Contracts: []string{inference.Contract}, Models: []string{"owner/fixture-model"}, Activation: wire.ActivationAttach.String()}}
	p.supervisors["ondemand"] = newProviderSupervisor(p, onDemand)
	p.supervisors["attach"] = newProviderSupervisor(p, attach)
	// Neither has probed yet: the on-demand declaration reads idle and the
	// attach declaration reads unreachable (newProviderSupervisor's defaults).

	names := map[string]bool{}
	for _, h := range p.nativeInferenceHosts() {
		names[h.Name] = true
	}
	if !names["ondemand"] {
		t.Fatalf("an on-demand declaration must be a candidate before its first launch (FAC-R8)")
	}
	if names["attach"] {
		t.Fatalf("an attach declaration that has not answered is not yet a router candidate")
	}
}
