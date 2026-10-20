package main

import (
	"os"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-inference/go"
	"github.com/openabstractions/abstraction-router/go"
)

func TestSelectedNativeProviderExtensionsStayBoundToTheirRegistration(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(err error) { t.Error(err) }, supervisors: map[string]*providerSupervisor{}}
	for _, spec := range []struct{ name, generation, key string }{
		{"first", "first-generation", "step/first@1"},
		{"second", "second-generation", "step/second@1"},
	} {
		file := providerFile{Version: providerFileVersion, Generation: spec.generation, DeclaredBy: program,
			Declaration: providerDeclaration{Name: spec.name, Program: program, Endpoint: "private-" + spec.name, Transport: transportNative,
				Contracts: []string{inference.ImageContract}, Resources: []string{"profile:image"}, Models: []string{"owner/shared-model"},
				Activation: wire.ActivationOnDemand.String(), SupportedExtensions: []providerExtension{{Profile: router.ProfileImage, Key: spec.key}}}}
		p.supervisors[spec.name] = newProviderSupervisor(p, file)
	}
	hosts := p.nativeInferenceHosts()
	if len(hosts) != 2 {
		t.Fatalf("native hosts: %+v", hosts)
	}
	first, second := hosts[0], hosts[1]
	if first.BindingID != p.supervisors["first"].file.bindingID() || second.BindingID != p.supervisors["second"].file.bindingID() {
		t.Fatal("extension inventories were not paired with their selected binding IDs")
	}
	if !first.SupportedExtensions[router.ProfileImage]["step/first@1"] || first.SupportedExtensions[router.ProfileImage]["step/second@1"] ||
		!second.SupportedExtensions[router.ProfileImage]["step/second@1"] || second.SupportedExtensions[router.ProfileImage]["step/first@1"] ||
		first.SupportedExtensions[router.ProfileChat]["step/first@1"] {
		t.Fatalf("cross-provider or cross-profile extension inventory: first=%v second=%v", first.SupportedExtensions, second.SupportedExtensions)
	}
	fresh := p.nativeInferenceHosts()[0]
	fresh.SupportedExtensions[router.ProfileImage]["step/first@1"] = false
	if !first.SupportedExtensions[router.ProfileImage]["step/first@1"] {
		t.Fatal("host inventories share mutable maps")
	}
	p.supervisors["first"].file.Declaration.SupportedExtensions[0].Key = "step/changed@1"
	if !first.SupportedExtensions[router.ProfileImage]["step/first@1"] || first.SupportedExtensions[router.ProfileImage]["step/changed@1"] {
		t.Fatal("host inventory changed with its source declaration")
	}
}

func TestSelectedRemoteProviderExtensionsDefaultUnsupported(t *testing.T) {
	_, _, trust := remoteTrustFiles(t, t.TempDir())
	p := &runtimeProviders{report: func(err error) { t.Error(err) }, supervisors: map[string]*providerSupervisor{}}
	file := providerFile{Version: providerFileVersion, Generation: "remote-generation",
		Declaration: providerDeclaration{Name: "remote", Endpoint: "tls://127.0.0.1:443", Transport: transportRemote,
			Activation: wire.ActivationRemote.String(), Remote: &trust,
			SupportedExtensions: []providerExtension{{Profile: router.ProfileImage, Key: "step/remote@1"}}}}
	p.supervisors["remote"] = newProviderSupervisor(p, file)
	hosts := p.remoteHosts()
	if len(hosts) != 1 || hosts[0].BindingID != file.bindingID() || !hosts[0].SupportedExtensions[router.ProfileImage]["step/remote@1"] ||
		hosts[0].SupportedExtensions[router.ProfileChat]["step/remote@1"] {
		t.Fatalf("remote extension inventory: %+v", hosts)
	}
	p.supervisors["remote"].file.Declaration.SupportedExtensions = nil
	if next := p.remoteHosts(); len(next) != 1 || next[0].SupportedExtensions[router.ProfileImage]["step/remote@1"] {
		t.Fatalf("undeclared remote extension became supported: %+v", next)
	}
}
