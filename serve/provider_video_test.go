package main

import (
	"os"
	"slices"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-inference/go"
	router "github.com/openabstractions/abstraction-router/go"
)

// A native provider that declares profile video with video@1 and a
// video:<model> entry becomes a router host the job executor selects for
// that model's video jobs (inference CONTRACT.md INF-G5); applications are
// offered no video@1 endpoint, and the model serves no other profile.
func TestVideoDeclarationRoutesVideoJobsToTheProvider(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	file := providerFile{Version: providerFileVersion, Generation: "video-generation", DeclaredBy: program,
		Declaration: providerDeclaration{Name: "modelhost", Program: program, Endpoint: "private-modelhost", Transport: transportNative,
			Contracts: []string{inference.Contract, inference.VideoContract}, Resources: []string{"profile:chat", "profile:video"},
			Models: []string{"chat:tiny:1b", "video:fastwan2.2-ti2v-5b"}, Activation: wire.ActivationAttach.String()}}
	if field := validProviderDeclaration(file.Declaration); field != "" {
		t.Fatalf("video declaration refused: %s", field)
	}
	if got := file.Declaration.mediatedInferenceProfiles(); !slices.Equal(got, []string{router.ProfileChat, router.ProfileVideo}) {
		t.Fatalf("mediated profiles %v", got)
	}
	without := file.Declaration
	without.Contracts = []string{inference.Contract}
	if field := validProviderDeclaration(without); field != "models" {
		t.Fatalf("a video-scoped model without video@1: %q", field)
	}

	p.supervisors[file.Declaration.Name] = newProviderSupervisor(p, file)
	p.supervisors[file.Declaration.Name].readiness = wire.DeclarationReadinessReady
	p.setMediated(map[string]mediatedProvider{file.bindingID(): {Endpoint: "oa-modelhost", Ready: true}})
	for _, c := range p.Candidates() {
		if c.Reference.Contract == inference.VideoContract {
			t.Fatalf("applications were offered video@1: %+v", c.Reference)
		}
	}

	hosts := p.nativeInferenceHosts()
	if len(hosts) != 1 || !slices.Contains(hosts[0].HostProfiles(), router.ProfileVideo) {
		t.Fatalf("native hosts %+v", hosts)
	}
	if _, ok := hosts[0].NativeTransport(); !ok {
		t.Fatal("the video host has no native transport for the job executor")
	}
	routes := router.New(hosts...)
	routes.Survey()
	if d, _ := routes.Route(router.Request{Model: "fastwan2.2-ti2v-5b", Profile: router.ProfileVideo}); d.Verdict != router.WouldLoad || d.Host != "modelhost" {
		t.Fatalf("video fastwan: %s %q (%s)", d.Verdict, d.Host, d.Why)
	}
	if d, _ := routes.Route(router.Request{Model: "fastwan2.2-ti2v-5b", Profile: router.ProfileChat}); d.Verdict != router.NoHost {
		t.Fatalf("chat fastwan: %s (%s)", d.Verdict, d.Why)
	}
}

// The PyTorch provider serving hummingbird-xt-i2v (pytorch PYTORCH-D1,
// PYTORCH-T1..T9) declares the same video route: the job executor reaches
// the model through the provider's native host, and the engine kind stays
// the provider's own.
func TestTorchVideoDeclarationRoutesHummingbirdToTheProvider(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	file := providerFile{Version: providerFileVersion, Generation: "torch-video-generation", DeclaredBy: program,
		Declaration: providerDeclaration{Name: pytorchName, Program: program, Endpoint: "private-pytorch", Transport: transportNative,
			Contracts: []string{inference.VideoContract}, Resources: []string{"card:0", "profile:video"},
			Models: []string{"video:hummingbird-xt-i2v"}, Activation: wire.ActivationAttach.String()}}
	if field := validProviderDeclaration(file.Declaration); field != "" {
		t.Fatalf("video declaration refused: %s", field)
	}
	p.supervisors[file.Declaration.Name] = newProviderSupervisor(p, file)
	p.supervisors[file.Declaration.Name].readiness = wire.DeclarationReadinessReady
	p.setMediated(map[string]mediatedProvider{file.bindingID(): {Endpoint: "oa-pytorch", Ready: true}})
	hosts := p.nativeInferenceHosts()
	if len(hosts) != 1 {
		t.Fatalf("native hosts %+v", hosts)
	}
	routes := router.New(hosts...)
	routes.Survey()
	if d, _ := routes.Route(router.Request{Model: "hummingbird-xt-i2v", Profile: router.ProfileVideo}); d.Verdict != router.WouldLoad || d.Host != pytorchName {
		t.Fatalf("video hummingbird: %s %q (%s)", d.Verdict, d.Host, d.Why)
	}
	if d, _ := routes.Route(router.Request{Model: "hummingbird-xt-i2v", Profile: router.ProfileImage}); d.Verdict != router.NoHost {
		t.Fatalf("image hummingbird: %s (%s)", d.Verdict, d.Why)
	}
}

// The PyTorch provider serving flux2-klein-4b beside hummingbird-xt-i2v on
// one engine (PYTORCH-T10, T11) declares image@1 for the image set and
// video@1 for the video set: each model routes to the provider for its own
// profile alone.
func TestTorchImageDeclarationRoutesFluxToTheProvider(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(error) {}, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
	file := providerFile{Version: providerFileVersion, Generation: "torch-image-generation", DeclaredBy: program,
		Declaration: providerDeclaration{Name: pytorchName, Program: program, Endpoint: "private-pytorch", Transport: transportNative,
			Contracts: []string{inference.VideoContract, inference.ImageContract}, Resources: []string{"card:0", "profile:video", "profile:image"},
			Models: []string{"video:hummingbird-xt-i2v", "image:flux2-klein-4b"}, Activation: wire.ActivationAttach.String()}}
	if field := validProviderDeclaration(file.Declaration); field != "" {
		t.Fatalf("image declaration refused: %s", field)
	}
	p.supervisors[file.Declaration.Name] = newProviderSupervisor(p, file)
	p.supervisors[file.Declaration.Name].readiness = wire.DeclarationReadinessReady
	p.setMediated(map[string]mediatedProvider{file.bindingID(): {Endpoint: "oa-pytorch", Ready: true}})
	hosts := p.nativeInferenceHosts()
	if len(hosts) != 1 || !slices.Contains(hosts[0].Profiles, router.ProfileImage) {
		t.Fatalf("native hosts %+v", hosts)
	}
	routes := router.New(hosts...)
	routes.Survey()
	if d, _ := routes.Route(router.Request{Model: "flux2-klein-4b", Profile: router.ProfileImage}); d.Verdict != router.WouldLoad || d.Host != pytorchName {
		t.Fatalf("image flux: %s %q (%s)", d.Verdict, d.Host, d.Why)
	}
	if d, _ := routes.Route(router.Request{Model: "hummingbird-xt-i2v", Profile: router.ProfileVideo}); d.Verdict != router.WouldLoad || d.Host != pytorchName {
		t.Fatalf("video hummingbird: %s %q (%s)", d.Verdict, d.Host, d.Why)
	}
	for _, wrong := range []router.Request{{Model: "flux2-klein-4b", Profile: router.ProfileVideo}, {Model: "hummingbird-xt-i2v", Profile: router.ProfileImage}} {
		if d, _ := routes.Route(wrong); d.Verdict != router.NoHost {
			t.Fatalf("%s %s: %s (%s)", wrong.Profile, wrong.Model, d.Verdict, d.Why)
		}
	}
}
