package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	inference "github.com/openabstractions/abstraction-inference/go"
)

// presentFixtureProgram is an absolute program path that exists and is not
// this test binary (a declaration naming the runtime itself is refused as
// program:self). A declaration naming a missing program reads
// program:missing and leaves the router.
func presentFixtureProgram() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("SystemRoot"), "System32", "cmd.exe")
	}
	return "/bin/sh"
}

// A local provider declaration whose program image is gone is skipped by the
// router and the resolver, logged once, kept registered, and offered again
// when the program is back (architect ruling 2026-11-09, item 4: the stale
// comfy-image-1828 declaration from a deleted dev worktree).
func TestMissingProviderProgramIsSkippedAndRestored(t *testing.T) {
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	other, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "worktree", "pytorchd.exe")
	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	var logMu sync.Mutex
	var logged []string
	report := func(err error) {
		logMu.Lock()
		defer logMu.Unlock()
		logged = append(logged, err.Error())
	}
	lines := func(name string) []string {
		logMu.Lock()
		defer logMu.Unlock()
		var out []string
		for _, line := range logged {
			if strings.HasPrefix(line, "provider "+name+":") {
				out = append(out, line)
			}
		}
		return out
	}
	p := &runtimeProviders{principal: principal, report: report, supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}),
		mediated: map[string]mediatedProvider{}}
	declare := func(name, prog string) providerFile {
		return providerFile{Version: providerFileVersion, DeclaredBy: other,
			Declaration: providerDeclaration{Name: name, Program: prog, Endpoint: "private-" + name, Transport: transportNative,
				Contracts: []string{inference.ImageContract}, Models: []string{"image:owner/fixture-model"}, Resources: []string{"profile:image"},
				Activation: wire.ActivationOnDemand.String()}}
	}
	stale, kept := declare("comfy-image-1828", program), declare("pytorch-image", other)
	for _, f := range []providerFile{stale, kept} {
		p.supervisors[f.Declaration.Name] = newFixtureProviderSupervisor(t, p, f)
		p.mediated[f.bindingID()] = mediatedProvider{Endpoint: "private-mediated-" + f.Declaration.Name, Ready: true}
	}
	routed := func() map[string]bool {
		names := map[string]bool{}
		for _, h := range p.nativeInferenceHosts() {
			names[h.Name] = true
		}
		return names
	}
	resolvable := func() map[string]bool {
		names := map[string]bool{}
		for _, c := range p.Candidates() {
			names[c.Reference.Provider] = true
		}
		return names
	}

	if names := routed(); !names["comfy-image-1828"] || !names["pytorch-image"] {
		t.Fatalf("with both programs present the router has %v", names)
	}
	if got := lines("comfy-image-1828"); len(got) != 0 {
		t.Fatalf("a present program logs nothing: %q", got)
	}

	if err := os.RemoveAll(filepath.Dir(program)); err != nil {
		t.Fatal(err)
	}
	p.recheckPrograms()
	p.recheckPrograms()
	if names := routed(); names["comfy-image-1828"] || !names["pytorch-image"] {
		t.Fatalf("a missing program must be skipped and the other provider kept: %v", names)
	}
	if names := resolvable(); names["comfy-image-1828"] || !names["pytorch-image"] {
		t.Fatalf("a missing program offers no resolver candidate: %v", names)
	}
	state := p.supervisors["comfy-image-1828"].state()
	if state.Readiness != wire.DeclarationReadinessNotReady || state.Why != programMissingReason {
		t.Fatalf("missing program reads %v %q", state.Readiness, state.Why)
	}
	if p.supervisors["comfy-image-1828"] == nil {
		t.Fatal("the declaration stays registered")
	}
	if got := lines("comfy-image-1828"); len(got) != 1 || !strings.Contains(got[0], "is missing") {
		t.Fatalf("a missing program logs one line, got %q", got)
	}
	// A replaced supervisor of the same declaration says nothing new.
	p.supervisors["comfy-image-1828"] = newFixtureProviderSupervisor(t, p, stale)
	if got := lines("comfy-image-1828"); len(got) != 1 {
		t.Fatalf("a replaced supervisor repeats the line: %q", got)
	}
	if got := lines("pytorch-image"); len(got) != 0 {
		t.Fatalf("the other provider logs nothing: %q", got)
	}
	if state := p.supervisors["pytorch-image"].state(); state.Why == programMissingReason {
		t.Fatalf("the other provider reads %q", state.Why)
	}

	if err := os.MkdirAll(filepath.Dir(program), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(program, []byte("image"), 0o600); err != nil {
		t.Fatal(err)
	}
	p.recheckPrograms()
	p.recheckPrograms()
	if names := routed(); !names["comfy-image-1828"] || !names["pytorch-image"] {
		t.Fatalf("a restored program is routed again: %v", names)
	}
	if names := resolvable(); !names["comfy-image-1828"] {
		t.Fatalf("a restored program is a resolver candidate again: %v", names)
	}
	if state := p.supervisors["comfy-image-1828"].state(); state.Readiness != wire.DeclarationReadinessIdle {
		t.Fatalf("a restored on-demand program reads %v %q", state.Readiness, state.Why)
	}
	if got := lines("comfy-image-1828"); len(got) != 2 || !strings.Contains(got[1], "is back") {
		t.Fatalf("a restored program logs one more line, got %q", got)
	}
}
