package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// installedShippedDeclarations places the installer's own declarations in a
// test installation whose tools folder is the per-machine install folder,
// with each program present, and returns the tools folder.
func installedShippedDeclarations(t *testing.T, names ...string) string {
	t.Helper()
	tools := filepath.Dir(placeInstallationDeclarations(t, names...))
	// These tests install optional components from the Windows catalogue as
	// stand-ins. POSIX packages need not ship those features; the fixture's
	// declarations and files must still use the native component spelling.
	if runtime.GOOS != "windows" {
		for _, name := range names {
			path := filepath.Join(tools, installationDeclarationsDir, name+".json")
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			var program string
			if err := json.Unmarshal(fields["program"], &program); err != nil {
				t.Fatal(err)
			}
			fields["program"], err = json.Marshal(shippedFixtureProgram(program))
			if err != nil {
				t.Fatal(err)
			}
			raw, err = json.Marshal(fields)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	savedMachine, savedUser := standingInstallFolders, standingUserInstallFolder
	t.Cleanup(func() { standingInstallFolders, standingUserInstallFolder = savedMachine, savedUser })
	standingInstallFolders = func() []string { return []string{tools} }
	standingUserInstallFolder = func() string { return "" }
	for _, program := range []string{"lemonaded.exe", "inventoryd.exe"} {
		if err := os.WriteFile(filepath.Join(tools, shippedFixtureProgram(program)), []byte("MZ"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return tools
}

// A-SEC6, architect ruling 2026-11-22 (1), FAC-R9: registry@2 Standings
// carries each declaration's standing, computed once by the runtime. The
// installation's lemonade-image reads product. The localai case: the same
// shipped program declared as localai reads third_party, as do a program
// outside the installation under a shipped name and a host.
func TestRegistryStandingsAreProductOnlyForAShippedProgramUnderItsOwnName(t *testing.T) {
	tools := installedShippedDeclarations(t, "lemonade-image")
	signedByRuntimeSigner(t)
	p, err := openFixtureProviders(t, t.TempDir(), true, func(err error) { t.Error(err) })
	if err != nil {
		t.Fatal(err)
	}
	lemonade, outside := filepath.Join(tools, shippedFixtureProgram("lemonaded.exe")), filepath.Join(t.TempDir(), shippedFixtureProgram("lemonaded.exe"))
	if err := os.WriteFile(outside, []byte("MZ"), 0o755); err != nil {
		t.Fatal(err)
	}
	files := []providerFile{
		pictureProviderFile(lemonade, "localai", "flux2-klein-4b"),
		pictureProviderFile(outside, product.TheNoise, "flux2-klein-4b"),
		newHostFile(iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:45679"}, declaredByOperator),
	}
	for _, f := range files {
		_, revision, err := p.read()
		if err != nil {
			t.Fatal(err)
		}
		f.Source = declarationSourceOperator
		if change := p.add(revision, f); change.Outcome != wire.DeclarationEditOutcomeApplied {
			t.Fatalf("declare %s: %+v", f.Declaration.Name, change)
		}
	}
	standings, _, err := p.standings()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]wire.ProductStanding{
		product.Lemonade: wire.ProductStandingProduct,
		"localai":        wire.ProductStandingThirdParty,
		product.TheNoise: wire.ProductStandingThirdParty,
		"ollama":         wire.ProductStandingThirdParty,
	}
	if len(standings) != len(want) {
		t.Fatalf("listed %d standings, want %d: %+v", len(standings), len(want), standings)
	}
	for _, s := range standings {
		if s.Standing != want[s.Name] {
			t.Errorf("%s reads standing %v, want %v", s.Name, s.Standing, want[s.Name])
		}
	}
}

// Security review 2026-11-22: a declaration of a shipped name, other than
// the installation's own, keeps product standing only while it repeats the
// installation's declaration exactly. The installed lemonaded.exe declared
// with arguments, an endpoint or resources of the declarer's choosing reads
// third_party. Setup's inventoryd stores keep standing, since inventoryd's
// arguments start no program.
func TestShippedProgramWithChangedArgumentsReadsThirdParty(t *testing.T) {
	tools := installedShippedDeclarations(t, "lemonade-image", "local-stores")
	signedByRuntimeSigner(t)
	p := &runtimeProviders{installation: filepath.Join(tools, installationDeclarationsDir)}
	installed := p.installedDeclarations(nil)
	at := slices.IndexFunc(installed, func(f providerFile) bool { return f.Declaration.Name == product.Lemonade })
	if at < 0 {
		t.Fatalf("no installed lemonade-image among %d declarations", len(installed))
	}
	shipped := installed[at]
	if !p.shippedProvider(shipped) {
		t.Fatal("the installation's lemonade-image has no standing")
	}
	declared := func(edit func(*providerDeclaration)) providerFile {
		f := providerFile{Version: providerFileVersion, Source: declarationSourceOperator, Declaration: shipped.Declaration}
		f.Declaration.Arguments = slices.Clone(shipped.Declaration.Arguments)
		f.Declaration.Resources = slices.Clone(shipped.Declaration.Resources)
		edit(&f.Declaration)
		return f
	}
	if !p.shippedProvider(declared(func(*providerDeclaration) {})) {
		t.Error("an exact copy of the installation's declaration has no standing")
	}
	for name, edit := range map[string]func(*providerDeclaration){
		"arguments": func(d *providerDeclaration) {
			d.Arguments = append(d.Arguments, "--dir", filepath.Join(t.TempDir(), "engine"))
		},
		"endpoint":  func(d *providerDeclaration) { d.Endpoint = "declarer-endpoint" },
		"resources": func(d *providerDeclaration) { d.Resources = append(d.Resources, "store:declarer") },
		"transport": func(d *providerDeclaration) { d.Transport = transportHTTP },
	} {
		f := declared(edit)
		if p.shippedProvider(f) || p.standing(f) != wire.ProductStandingThirdParty {
			t.Errorf("lemonaded.exe declared with changed %s reads product", name)
		}
	}
	inventory := filepath.Join(tools, shippedFixtureProgram("inventoryd.exe"))
	for name, want := range map[string]bool{"oa-setup-picture": true, "oa-setup-chat-467d9c6e43391a07c5853578": true, "local-stores": false} {
		f := providerFile{Version: providerFileVersion, Source: declarationSourceOperator,
			Declaration: providerDeclaration{Name: name, Program: inventory, Arguments: []string{"serve", "--endpoint", "{endpoint}", "--config", filepath.Join(t.TempDir(), "inventoryd.json")}}}
		if got := p.shippedProvider(f); got != want {
			t.Errorf("inventoryd declared as %s reads standing %v, want %v", name, got, want)
		}
	}
}
