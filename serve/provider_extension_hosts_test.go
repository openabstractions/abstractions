package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstraction-router/go"
)

func TestCurrentComfyBindingReadsDisabledProductAndRejectsStaleIdentity(t *testing.T) {
	file := newHostFile(iwire.HostEntry{Name: "comfyui", Kind: "comfyui", Base: "http://127.0.0.1:12345"}, "fixture-product")
	file.Generation = "first"
	file.Source = declarationSourceProduct
	available := true
	probes := 0
	p := &runtimeProviders{dir: t.TempDir(), disabledPath: filepath.Join(t.TempDir(), "disabled.json"), report: func(err error) { t.Error(err) }, products: func() []providerFile {
		probes++
		if available {
			return []providerFile{file}
		}
		return nil
	}}
	ctx := context.Background()
	if p.currentHostBinding(ctx, file.bindingID(), "ollama", file.Declaration.Host.Base) {
		t.Fatal("wrong engine kind admitted")
	}
	if !p.currentHostBinding(ctx, file.bindingID(), "comfyui", file.Declaration.Host.Base) {
		t.Fatal("enabled product binding unavailable")
	}
	if p.currentHostBinding(ctx, file.bindingID(), "comfyui", "http://127.0.0.1:54321") || p.currentHostBinding(ctx, "other-generation", "comfyui", file.Declaration.Host.Base) {
		t.Fatal("foreign base or binding accepted")
	}
	available = false
	if !p.currentHostBinding(ctx, file.bindingID(), "comfyui", file.Declaration.Host.Base) || probes != 1 {
		t.Fatal("product withdrawal bypassed shared discovery snapshot")
	}
	if err := p.disable("comfyui"); err != nil {
		t.Fatal(err)
	}
	if p.currentHostBinding(ctx, file.bindingID(), "comfyui", file.Declaration.Host.Base) {
		t.Fatal("disabled product stayed admitted")
	}
	if err := p.enable("comfyui"); err != nil {
		t.Fatal(err)
	}
	if !p.currentHostBinding(ctx, file.bindingID(), "comfyui", file.Declaration.Host.Base) {
		t.Fatal("enabled product did not recover")
	}
	p.forgetProducts()
	if p.currentHostBinding(ctx, file.bindingID(), "comfyui", file.Declaration.Host.Base) || probes != 2 {
		t.Fatal("forgotten product snapshot retained withdrawn declaration")
	}
	available = true
	p.forgetProducts()
	if !p.currentHostBinding(ctx, file.bindingID(), "comfyui", file.Declaration.Host.Base) {
		t.Fatal("refreshed product unavailable")
	}
	available = false
	p.productsMu.Lock()
	p.productAt = time.Now().Add(-productDeclarationAge - time.Second)
	p.productsMu.Unlock()
	if p.currentHostBinding(ctx, file.bindingID(), "comfyui", file.Declaration.Host.Base) || probes != 4 {
		t.Fatal("expired product snapshot retained withdrawn declaration")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if p.currentHostBinding(cancelled, file.bindingID(), "comfyui", file.Declaration.Host.Base) {
		t.Fatal("cancelled binding lookup admitted")
	}
	if (*runtimeProviders)(nil).currentHostBinding(ctx, file.bindingID(), "comfyui", file.Declaration.Host.Base) {
		t.Fatal("absent directory admitted")
	}
}

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
		p.supervisors[spec.name] = newFixtureProviderSupervisor(t, p, file)
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
	p.supervisors["remote"] = newFixtureProviderSupervisor(t, p, file)
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
