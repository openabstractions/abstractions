package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/resolution"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstraction-inference/go"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
)

func shortMediationEndpoint(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		return fmt.Sprintf(`\\.\pipe\oa-mediation-%d-%d`, os.Getpid(), time.Now().UnixNano())
	}
	dir, err := os.MkdirTemp("/tmp", "oa-mediation-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return filepath.Join(dir, "s")
}

func TestProviderBindingSurvivesRestartAndChangesOnRedeclaration(t *testing.T) {
	state := t.TempDir()
	report := func(err error) { t.Errorf("providers: %v", err) }
	p, err := openProviders(state, report)
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	f := providerFile{Version: providerFileVersion, DeclaredBy: program, DeclaredAt: 1,
		Declaration: providerDeclaration{Name: "binding", Program: program, Endpoint: "binding-fixture", Transport: transportNative,
			Contracts: []string{"abstraction.inference/chat@1"}, Activation: wire.ActivationAttach.String()}}
	_, revision, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	added := p.add(revision, f)
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("add: %+v", added)
	}
	original := p.supervisors["binding"].file
	if original.Generation == "" {
		t.Fatal("new declaration has no persisted generation")
	}
	reopened, err := openProviders(state, report)
	if err != nil {
		t.Fatal(err)
	}
	if reopened.supervisors["binding"].file.bindingID() != original.bindingID() {
		t.Fatal("ordinary restart changed binding")
	}
	removed := p.remove(added.Revision, "binding")
	if removed.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("remove: %+v", removed)
	}
	// Identical source configuration and timestamp still define a new generation.
	added = p.add(removed.Revision, f)
	if added.Outcome != wire.DeclarationEditOutcomeApplied {
		t.Fatalf("re-add: %+v", added)
	}
	if p.supervisors["binding"].file.bindingID() == original.bindingID() {
		t.Fatal("redeclaration reused binding")
	}
	changed := original
	changed.Declaration.Endpoint = "replacement-endpoint"
	if changed.bindingID() == original.bindingID() {
		t.Fatal("endpoint change retained binding")
	}
}

func TestResolverSelectionRetainsTheChosenProviderEndpointForSharedModel(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	provider := func(name, generation, guarantee string) providerFile {
		return providerFile{Version: providerFileVersion, Generation: generation, DeclaredBy: program,
			Declaration: providerDeclaration{Name: name, Program: program, Endpoint: "private-" + name, Transport: transportNative,
				Contracts: []string{inference.Contract}, Guarantees: []string{guarantee}, Models: []string{"owner/shared-model"}, Activation: wire.ActivationAttach.String()}}
	}
	first := provider("first", "first-generation", "fixture/first@1")
	second := provider("second", "second-generation", "fixture/second@1")
	p.supervisors["first"] = newProviderSupervisor(p, first)
	p.supervisors["second"] = newProviderSupervisor(p, second)
	p.supervisors["first"].readiness = wire.DeclarationReadinessReady
	p.supervisors["second"].readiness = wire.DeclarationReadinessReady
	p.setMediated(map[string]mediatedProvider{first.bindingID(): {Endpoint: "oa-first", Ready: true}, second.bindingID(): {Endpoint: "oa-second", Ready: true}})

	catalog, err := resolution.New(p.Candidates())
	if err != nil {
		t.Fatal(err)
	}
	result, err := catalog.ForCaller(func(wire.ServiceReference) bool { return true }).Resolve(wire.ResolveRequest{
		Capability: "abstraction.inference", Contracts: []string{inference.Contract}, Guarantees: []string{"fixture/second@1"}, Scope: wire.ScopeLocal,
	})
	if err != nil || result.Status != wire.ResolutionStatusResolved || result.Reference == nil {
		t.Fatalf("resolve: %+v, %v", result, err)
	}
	if result.Reference.Provider != "second" || result.Reference.Endpoint != "oa-second" {
		t.Fatalf("selected %+v", result.Reference)
	}
}

func TestProviderMediationRepeatsTheRegistryRouteBound(t *testing.T) {
	p := &runtimeProviders{report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i <= maxProviders; i++ {
		name := fmt.Sprintf("provider-%02d", i)
		file := providerFile{Version: providerFileVersion, Generation: fmt.Sprintf("generation-%02d", i), DeclaredBy: program,
			Declaration: providerDeclaration{Name: name, Program: program, Endpoint: "private-" + name, Transport: transportNative,
				Contracts: []string{inference.Contract}, Models: []string{"shared-model"}, Activation: wire.ActivationAttach.String()}}
		p.supervisors[name] = newProviderSupervisor(p, file)
	}
	m := &providerMediation{base: "unused", providers: p, routes: map[string]*providerMediationRoute{}}
	if err := m.sync(); err == nil {
		t.Fatalf("more than %d mediation listeners were accepted", maxProviders)
	}
	if len(m.routes) != 0 || len(p.mediated) != 0 {
		t.Fatalf("over-limit mediation published routes: %d, %d", len(m.routes), len(p.mediated))
	}
}

func TestProviderMediationEndpointIncludesTheOwningRuntime(t *testing.T) {
	binding := "provider-binding-v1:same"
	first := mediatedProviderEndpoint(listen.Endpoint("first-runtime"), binding)
	second := mediatedProviderEndpoint(listen.Endpoint("second-runtime"), binding)
	if first == second || first != mediatedProviderEndpoint(listen.Endpoint("first-runtime"), binding) {
		t.Fatalf("runtime endpoints %q and %q", first, second)
	}
}

func TestProviderMediationStartsAPendingRetirementSweep(t *testing.T) {
	providers := &runtimeProviders{report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}, changes: make(chan struct{})}
	provider := &inference.Provider{}
	binding := inference.ExecutionBinding{Host: "retired", BindingID: "retired-before-start"}
	endpoint := shortMediationEndpoint(t)
	host, err := inferenceservice.ListenMediated(endpoint, provider, inference.ExecutionLocal, binding)
	if err != nil {
		t.Fatal(err)
	}
	m := &providerMediation{base: endpoint, provider: provider, providers: providers, report: func(error) {}, routes: map[string]*providerMediationRoute{
		binding.BindingID: {endpoint: endpoint, host: host, retired: true},
	}}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := m.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { m.Close() })
	eventually(t, 3*time.Second, "the pre-Serve retired route being swept", func() bool {
		m.mu.Lock()
		defer m.mu.Unlock()
		_, exists := m.routes[binding.BindingID]
		return !exists
	})
}

func TestOneMediationListenerFailureLeavesOtherCandidatesAvailable(t *testing.T) {
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	provider := func(name string) providerFile {
		return providerFile{Version: providerFileVersion, Generation: name + "-generation", DeclaredBy: program,
			Declaration: providerDeclaration{Name: name, Program: program, Endpoint: "private-" + name, Transport: transportNative,
				Contracts: []string{inference.Contract}, Models: []string{"shared-model"}, Activation: wire.ActivationAttach.String()}}
	}
	blocked, healthy := provider("blocked"), provider("healthy")
	p.supervisors["blocked"] = newProviderSupervisor(p, blocked)
	p.supervisors["healthy"] = newProviderSupervisor(p, healthy)
	p.supervisors["blocked"].readiness = wire.DeclarationReadinessReady
	p.supervisors["healthy"].readiness = wire.DeclarationReadinessReady
	base := listen.Endpoint(fmt.Sprintf("mediation-isolation-%d", time.Now().UnixNano()))
	occupied, err := listen.Listen(mediatedProviderEndpoint(base, blocked.bindingID()))
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	var reported []error
	m, err := newProviderMediation(base, &inference.Provider{}, p, func(err error) { reported = append(reported, err) })
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	if len(reported) != 1 {
		t.Fatalf("listener failures reported %d times: %v", len(reported), reported)
	}
	states := map[string]bool{}
	for _, candidate := range p.Candidates() {
		states[candidate.Reference.Provider] = candidate.Ready
	}
	if len(states) != 2 || states["blocked"] || !states["healthy"] {
		t.Fatalf("candidate readiness %v", states)
	}
	candidates := append(p.Candidates(), resolution.Candidate{Ready: true, Reference: wire.ServiceReference{
		Provider: "unrelated", Capability: "abstraction.logging", Contract: "abstraction.logging/sink@1",
		Scope: wire.ScopeLocal, Transport: resolution.LocalTransport, Endpoint: "logging-endpoint", Guarantees: []string{},
	}})
	catalog, err := resolution.New(candidates)
	if err != nil {
		t.Fatal(err)
	}
	unrelated, err := catalog.ForCaller(func(wire.ServiceReference) bool { return true }).Resolve(wire.ResolveRequest{
		Capability: "abstraction.logging", Contracts: []string{"abstraction.logging/sink@1"}, Guarantees: []string{}, Scope: wire.ScopeLocal,
	})
	if err != nil || unrelated.Status != wire.ResolutionStatusResolved || unrelated.Reference == nil || unrelated.Reference.Provider != "unrelated" {
		t.Fatalf("unrelated candidate after mediation failure: %+v, %v", unrelated, err)
	}
}

func TestNativeCandidatesUseMediationAndUnsupportedContractsStayPrivate(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	chat := providerFile{Version: providerFileVersion, Generation: "chat-generation", DeclaredBy: program,
		Declaration: providerDeclaration{Name: "chat", Program: program, Endpoint: "private-chat", Transport: transportNative,
			Contracts: []string{inference.Contract}, Guarantees: []string{"fixture"}, Resources: []string{"profile:chat"}, Models: []string{"owner/fixture-model"}, Activation: wire.ActivationAttach.String()}}
	unsupported := providerFile{Version: providerFileVersion, Generation: "other-generation", DeclaredBy: program,
		Declaration: providerDeclaration{Name: "other", Program: program, Endpoint: "private-other", Transport: transportNative,
			Contracts: []string{"example.unsupported/service@1"}, Models: []string{"owner/fixture-model"}, Activation: wire.ActivationAttach.String()}}
	p.supervisors["chat"] = newProviderSupervisor(p, chat)
	p.supervisors["other"] = newProviderSupervisor(p, unsupported)
	p.supervisors["chat"].readiness = wire.DeclarationReadinessReady
	p.supervisors["other"].readiness = wire.DeclarationReadinessReady
	p.setMediated(map[string]mediatedProvider{chat.bindingID(): {Endpoint: "oa-inference", Ready: true}})

	candidates := p.Candidates()
	if len(candidates) != 1 || candidates[0].Reference.Provider != "chat" || candidates[0].Reference.Endpoint != "oa-inference" || candidates[0].Reference.Endpoint == p.supervisors["chat"].endpoint {
		t.Fatalf("mediated candidates %+v", candidates)
	}
	hosts := p.nativeInferenceHosts()
	if len(hosts) != 1 || hosts[0].Name != "chat" || hosts[0].BindingID != chat.bindingID() {
		t.Fatalf("native hosts %+v", hosts)
	}
	transport, ok := hosts[0].NativeTransport()
	if !ok || transport.Endpoint != p.supervisors["chat"].endpoint || transport.Server == nil || transport.Server.Program != program {
		t.Fatalf("native transport %+v, %v", transport, ok)
	}
}
