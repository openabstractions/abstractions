package main

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// The router receives the picture providers Diffusers, Lemonade, TheNoise
// (architect ruling 2026-11-09); any other registered local image provider
// follows them, stable by name, and ComfyUI's provider has no place in the
// preference.
func TestPictureProvidersReachTheRouterInRulingOrder(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	signedByRuntimeSigner(t, program)
	// Each engine runs from its own shipped program (A-SEC6, ruling
	// 2026-11-22 (1)); any other provider runs from the runtime's own.
	machine := installedPerMachine(t)
	shipped := map[string]string{}
	for name, file := range map[string]string{"pytorch-image": "pytorchd.exe", "lemonade-image": "lemonaded.exe", "thenoise-image": "thenoise-provider.exe"} {
		shipped[name] = filepath.Join(machine, shippedFixtureProgram(file))
		if err := os.WriteFile(shipped[name], []byte("MZ"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	order := func(names ...string) []string {
		t.Helper()
		p := &runtimeProviders{principal: principal, report: func(err error) { t.Error(err) }, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{}}
		for _, name := range names {
			program := program
			if own, ok := shipped[name]; ok {
				program = own
			}
			file := pictureProviderFile(program, name, "flux2-klein-4b")
			if _, ok := shipped[name]; ok {
				file.Source = declarationSourceInstallation
			}
			p.supervisors[name] = newFixtureProviderSupervisor(t, p, file)
			p.supervisors[name].readiness = wire.DeclarationReadinessReady
		}
		var got []string
		for _, h := range p.nativeInferenceHosts() {
			got = append(got, h.Name)
		}
		return got
	}
	for _, c := range []struct{ registered, want []string }{
		{[]string{"zz-image", "thenoise-image", "lemonade-image", "pytorch-image"}, []string{"pytorch-image", "lemonade-image", "thenoise-image", "zz-image"}},
		{[]string{"zz-image", "thenoise-image", "lemonade-image"}, []string{"lemonade-image", "thenoise-image", "zz-image"}},
		{[]string{"zz-image", "comfy-image", "thenoise-image"}, []string{"thenoise-image", "comfy-image", "zz-image"}},
	} {
		if got := order(c.registered...); !slices.Equal(got, c.want) {
			t.Fatalf("registered %v reach the router as %v, want %v", c.registered, got, c.want)
		}
	}
	if slices.Contains(pictureProviderPreference, "comfy-image") {
		t.Fatal("ComfyUI's provider ranks among the picture engines")
	}
}

// A preferred name ranks first only for a shipped declaration: one a
// per-machine installation placed or whose program sits beside the runtime
// with the runtime's signer. An app that manages providers and names its own
// program pytorch-image ranks after them, by name.
func TestPictureRankNeedsAShippedDeclaration(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	beside, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	elsewhere := filepath.Join(t.TempDir(), "evil.exe")
	if err := os.WriteFile(elsewhere, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	signedByRuntimeSigner(t, beside)
	machine := installedPerMachine(t)
	lemonade := filepath.Join(machine, shippedFixtureProgram("lemonaded.exe"))
	if err := os.WriteFile(lemonade, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &runtimeProviders{principal: principal, report: func(err error) { t.Error(err) }, supervisors: map[string]*providerSupervisor{}, mediated: map[string]mediatedProvider{},
		installation: filepath.Join(machine, installationDeclarationsDir)}
	for _, c := range []struct{ name, program, source string }{
		{"pytorch-image", lemonade, declarationSourceOperator},
		{"lemonade-image", lemonade, declarationSourceInstallation},
		{"thenoise-image", elsewhere, declarationSourceInstallation},
		{"aa-image", beside, declarationSourceOperator},
	} {
		file := pictureProviderFile(c.program, c.name, "flux2-klein-4b")
		file.Source = c.source
		p.supervisors[c.name] = newFixtureProviderSupervisor(t, p, file)
		p.supervisors[c.name].readiness = wire.DeclarationReadinessReady
	}
	var got []string
	for _, h := range p.nativeInferenceHosts() {
		got = append(got, h.Name)
	}
	// Only lemonaded.exe under its own name ranks (A-SEC6, ruling
	// 2026-11-22 (1)): the same shipped program under pytorch-image, the
	// installation's declaration of a program elsewhere and a same-signer
	// program under a name no component ships rank after it, in name order.
	if want := []string{"lemonade-image", "aa-image", "pytorch-image", "thenoise-image"}; !slices.Equal(got, want) {
		t.Fatalf("picture providers reach the router as %v, want %v", got, want)
	}
}
