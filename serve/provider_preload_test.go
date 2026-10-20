package main

import (
	"os"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
)

// A native provider's router host takes preload@1 only when its declaration
// lists the service beside chat@1 (inference INF-PL2); the model host's own
// declaration lists it (modelhost MODELHOST-W1).
func TestANativeHostPreloadsOnlyWhenItsDeclarationSaysSo(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name      string
		contracts []string
		want      bool
	}{
		{"chat alone", []string{inference.Contract}, false},
		{"chat and preload", []string{inference.Contract, inference.PreloadContract}, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{})}
			f := providerFile{Declaration: providerDeclaration{Name: "fixture", Program: program, Endpoint: "private-fixture", Transport: transportNative,
				Contracts: c.contracts, Models: []string{"owner/fixture-model"}, Resources: []string{"profile:chat"}, Activation: wire.ActivationOnDemand.String()}}
			p.supervisors[f.Declaration.Name] = newProviderSupervisor(p, f)
			hosts := p.nativeInferenceHosts()
			if len(hosts) != 1 || hosts[0].Preloads != c.want {
				t.Fatalf("hosts %d, preloads %v; want %v", len(hosts), len(hosts) == 1 && hosts[0].Preloads, c.want)
			}
		})
	}
	host := modelHostDeclaration(hostSettings{program: program, endpoint: "modelhost", engine: "llama-server", resource: "leases"},
		[]hostedModel{{Name: "qwen3-4b", Store: "hf", Object: "qwen3"}}, []string{"runtime"})
	if want := []string{inference.Contract, inference.PreloadContract}; len(host.Contracts) != 2 || host.Contracts[0] != want[0] || host.Contracts[1] != want[1] {
		t.Fatalf("model host contracts %v, want %v", host.Contracts, want)
	}
}
