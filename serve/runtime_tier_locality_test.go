package main

import (
	"path/filepath"
	"testing"

	askswire "github.com/openabstractions/abstraction-asks/go/abstraction/asks/api"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// ASK-R6: complete is routine only on a model server OpenAbstractions runs,
// a native provider whose declaration has product standing. A loopback HTTP
// listener can forward to a paid hosted service.
func TestAnswererRoutineTierRequiresOnMachineEndpoint(t *testing.T) {
	machine := installedPerMachine(t)
	signedByRuntimeSigner(t)
	p := &runtimeProviders{installation: filepath.Join(machine, installationDeclarationsDir), supervisors: map[string]*providerSupervisor{}}
	unshipped := filepath.Join(t.TempDir(), "engine.exe")
	native := func(name, source, program string) *router.Host {
		t.Helper()
		f := providerFile{Version: providerFileVersion, Source: source, Generation: name, Declaration: providerDeclaration{Name: name, Program: program}}
		p.supervisors[name] = &providerSupervisor{file: f}
		h, err := router.NewNative(name, `\\.\pipe\`+name, listen.ServerExpectation{Program: program}, []string{"model"}, nil)
		if err != nil {
			t.Fatal(err)
		}
		h.BindingID = f.bindingID()
		return h
	}
	installedProgram := filepath.Join(machine, shippedFixtureProgram("modelhostd.exe"))
	installed := native(product.ModelHost, declarationSourceInstallation, installedProgram)
	// A shipped program under a name it does not ship (A-SEC6, ruling
	// 2026-11-22 (1)).
	localai := native("localai", declarationSourceOperator, installedProgram)
	misplaced := native("misplaced", declarationSourceInstallation, unshipped)
	registered := native("registered", declarationSourceOperator, unshipped)
	productNative := native("product-native", declarationSourceProduct, unshipped)
	ollama := router.Ollama("http://127.0.0.1:11434")
	p.supervisors["ollama"] = &providerSupervisor{file: providerFile{Version: providerFileVersion, Source: declarationSourceProduct, Declaration: providerDeclaration{Name: "ollama"}}}
	ollama.BindingID = p.supervisors["ollama"].file.bindingID()
	litellm := router.OpenAICompatible("litellm", "http://127.0.0.1:4000", "/v1", []string{"model"})
	remote := router.OpenAICompatible("operator-remote", "https://192.0.2.11:443", "/v1", []string{"model"})
	var a runtimeAsks
	a.useTierFacts(func() []*router.Host {
		return []*router.Host{ollama, litellm, remote, installed, registered, productNative, localai, misplaced}
	}, p.managedHost, nil)
	for _, name := range []string{"ollama", "litellm", "operator-remote", "registered", "product-native", "localai", "misplaced"} {
		if a.tierFacts.LocalServer(name) {
			t.Errorf("%s was classified as a server OpenAbstractions runs", name)
		}
	}
	if !a.tierFacts.LocalServer(product.ModelHost) {
		t.Error("a native installation provider with standing was not routine")
	}
}

// ASK-R6: an enrolled answerer's Allow on complete for a loopback operator
// host (the LiteLLM shape) is refused and writes no rule.
func TestAnswererCannotAllowCompleteOnLoopbackOperatorHost(t *testing.T) {
	f := newAnswererFixture(t)
	litellm := router.OpenAICompatible("litellm", "http://127.0.0.1:4000", "/v1", []string{"model"})
	f.a.useTierFacts(func() []*router.Host { return []*router.Host{litellm} }, func(*router.Host) bool { return true }, []string{"hf"})
	q := f.ask(t, f.comfy, inference.ActionComplete, "host:litellm")
	if edit := f.enroll(t, f.answerer); edit.Outcome != rwire.PolicyEditOutcomeApplied {
		t.Fatalf("enroll %v", edit.Outcome)
	}
	if outcome, effect := f.answer(f.me(), q.ID, "allow"); outcome != "forbidden" || effect == askswire.ResponderEffectOutcomeApplied {
		t.Fatalf("Allow on a loopback operator host: %s %s", outcome, effect)
	}
	if read := f.a.rights.policy.ReadRule(f.comfy, inference.ActionComplete, "host:litellm"); read.Record != nil {
		t.Fatalf("a rule was written: %+v", read.Record)
	}
}
