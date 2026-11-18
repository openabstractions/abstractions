package main

import (
	"context"
	"path/filepath"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	"github.com/openabstractions/abstractions/mcp-gateway/product"
)

// A model server reads by an engine's upstream name on the app page only
// when the runtime's standing for its declaration is product (A-SEC6,
// architect ruling 2026-11-22 (1)). A server no declaration names, an
// installation's declaration and a declaration without standing read
// "Model server", never "TheNoise".
func TestAppPageServerTitleNeedsProductStanding(t *testing.T) {
	machine := t.TempDir()
	shippedFolders(t, machine)
	panelInstalledPerMachine(t)
	evil := filepath.Join(t.TempDir(), "evil.exe")
	titles := func(declaredBy string, providers ...providerState) string {
		t.Helper()
		operator := factsInference{hosts: []iclient.HostState{{Entry: iclient.HostEntry{Name: product.TheNoise, DeclaredBy: declaredBy}}}}
		return appServerTitles(context.Background(), operator, fwire.ApplicationPage{}, providers)[product.TheNoise]
	}
	declared := func(program, by string, standing fwire.ProductStanding) providerState {
		var sent *fwire.ProductStanding
		if standing != 0 {
			sent = &standing
		}
		return providerState{DeclarationState: fwire.DeclarationState{DeclaredBy: by, Declaration: fwire.Declaration{Name: product.TheNoise, Program: program}}, Standing: sent}
	}
	shipped := filepath.Join(machine, "thenoise-provider.exe")
	if got := titles(evil); got != "Model server" {
		t.Errorf("a server no declaration names reads %q", got)
	}
	if got := titles("installation"); got != "Model server" {
		t.Errorf("a server the installation declared, with no declaration listed, reads %q", got)
	}
	if got := titles(evil, declared(shipped, "installation", 0)); got != "Model server" {
		t.Errorf("a declaration without standing reads %q", got)
	}
	if got := titles(evil, declared(shipped, evil, fwire.ProductStandingThirdParty)); got != "Model server" {
		t.Errorf("a third-party declaration reads %q", got)
	}
	if got := titles(evil, declared(shipped, evil, fwire.ProductStandingProduct)); got != "TheNoise" {
		t.Errorf("a declaration with product standing reads %q", got)
	}
}
