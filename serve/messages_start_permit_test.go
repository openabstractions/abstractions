package main

import (
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/abstraction/facade/applications"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// A vendor self-update re-pins the recipe's saved command. The permit names
// the recipe's kind and limits, so the re-pin keeps the person's permit; a
// changed budget or model is a different recipe and asks again.
func TestMessageStartPermitIgnoresRepinnedCommand(t *testing.T) {
	folder := t.TempDir()
	base := startprofile.Profile{Name: "codex-v1", Context: "receiver-fixture", Folder: folder, StateDir: folder, CLIFileID: "saved-command-before-update", Model: "default", TimeoutMS: 120000, OutputBytes: 65536, MemoryBytes: 2 << 30, BudgetCents: 100}
	source := applications.CapturedAgentApplication{}
	resource := func(p startprofile.Profile) (string, string) {
		t.Helper()
		d := wire.ApplicationDescriptor{Name: "oa-codex-integration", Program: `C:\oa\oa-codex-integration.exe`, Title: "Codex", Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments(), Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 30000}}
		profile, err := startprofile.Parse(d.Activation.Arguments)
		if err != nil {
			t.Fatal(err)
		}
		r := applicationMessageStartRequest{
			Application: d.Name, PermitRegistration: messageStartPermitRegistration(d),
			ProfileFingerprint: profile.Fingerprint(d.Program), PermitRecipe: profile.PermitFingerprint(d.Program),
		}
		return messageStartResource(source, r), r.ProfileFingerprint
	}
	before, beforeRecipe := resource(base)
	repinned := base
	repinned.CLIFileID = "saved-command-after-update"
	after, afterRecipe := resource(repinned)
	if before != after {
		t.Fatal("a re-pinned command asks the person again")
	}
	if beforeRecipe == afterRecipe {
		t.Fatal("the owned invocation no longer binds the exact recipe")
	}
	for name, change := range map[string]func(*startprofile.Profile){
		"budget":  func(p *startprofile.Profile) { p.BudgetCents = 200 },
		"model":   func(p *startprofile.Profile) { p.Model = "other-model" },
		"timeout": func(p *startprofile.Profile) { p.TimeoutMS = 60000 },
		"kind":    func(p *startprofile.Profile) { p.Name = "claude-v1" },
	} {
		changed := base
		change(&changed)
		if got, _ := resource(changed); got == before {
			t.Fatal("a changed recipe kept the permit", name)
		}
	}
}
