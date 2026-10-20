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
// when OpenAbstractions shipped it. An unregistered program that declares
// the free name thenoise-image reads "Model server", never "TheNoise"
// (quality review of security finding F1, 2026-11-09).
func TestAppPageServerTitleNeedsProductStanding(t *testing.T) {
	machine := t.TempDir()
	shippedFolders(t, machine)
	signedByPanelSigner(t)
	evil := filepath.Join(t.TempDir(), "evil.exe")
	titles := func(declaredBy string, providers ...fwire.DeclarationState) string {
		t.Helper()
		operator := factsInference{hosts: []iclient.HostState{{Entry: iclient.HostEntry{Name: product.TheNoise, DeclaredBy: declaredBy}}}}
		return appServerTitles(context.Background(), operator, fwire.ApplicationPage{}, providers)[product.TheNoise]
	}
	declared := func(program, by string) fwire.DeclarationState {
		return fwire.DeclarationState{DeclaredBy: by, Declaration: fwire.Declaration{Name: product.TheNoise, Program: program}}
	}
	if got := titles(evil); got != "Model server" {
		t.Errorf("a server an unregistered program declared reads %q", got)
	}
	if got := titles(evil, declared(evil, evil)); got != "Model server" {
		t.Errorf("an unregistered program's own thenoise-image reads %q", got)
	}
	if got := titles(providerDeclaredByInstallation); got != "Model server" {
		t.Errorf("an installation declaration beside a per-user Panel reads %q", got)
	}
	if got := titles(evil, declared(filepath.Join(machine, "thenoise.exe"), evil)); got != "TheNoise" {
		t.Errorf("a provider in the per-machine folder reads %q", got)
	}
	panelInstalledPerMachine(t)
	if got := titles(providerDeclaredByInstallation); got != "TheNoise" {
		t.Errorf("a per-machine installation's declaration reads %q", got)
	}
}
