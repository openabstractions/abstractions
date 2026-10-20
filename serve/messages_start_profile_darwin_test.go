package main

import (
	"context"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-identity/fileid"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type darwinStartIdentity struct {
	key    string
	closes *int
}

func (f darwinStartIdentity) FileKey() string             { return f.key }
func (f darwinStartIdentity) Check(context.Context) error { return nil }
func (f darwinStartIdentity) Close() error                { *f.closes++; return nil }

func TestDarwinVersionedStartProfilePreservesExactNativeDescriptor(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cli := filepath.Join(root, "inert-cli")
	if err := os.WriteFile(cli, []byte("never executed"), 0700); err != nil {
		t.Fatal(err)
	}
	id, err := fileid.Resolve(t.Context(), fileid.Self, cli, fileid.Options{Links: fileid.RefuseLinks})
	if err != nil {
		t.Fatal(err)
	}
	stored, err := id.Stored()
	if err != nil {
		t.Fatal(err)
	}
	p := startprofile.Profile{Name: "claude-v1", Context: "exact-context", Folder: root, StateDir: root, CLIFileID: stored.String(), Model: "explicit-model", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 1 << 30, BudgetCents: 1}
	name := "oa-claude-integration-" + strings.Repeat("a", 32)
	d := wire.ApplicationDescriptor{Name: name, Program: filepath.Join(root, "oa-claude-integration-"+strings.Repeat("a", 64)), Title: "Kept integration", Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments(), Readiness: wire.ApplicationInterface{Name: "messages", Protocol: "mcp", Contract: "abstraction.facade/messages@1"}, ReadinessTimeoutMs: 1000}}
	holds, closes := 0, 0
	hold := func(context.Context, []string) (messageStartCLIIdentity, error) {
		holds++
		return darwinStartIdentity{key: id.Key(), closes: &closes}, nil
	}
	if err := validateClaudeMessageStartProfile(t.Context(), d, p, hold); err != nil || holds != 1 || closes != 1 {
		t.Fatal(err, holds, closes)
	}
	for _, program := range []string{d.Program + "-unknown", filepath.Join(root, "oa-claude-integration-"+strings.Repeat("b", 64))} {
		changed := d
		changed.Program = program
		if err := validateClaudeMessageStartProfile(t.Context(), changed, p, hold); err == nil || holds != 1 {
			t.Fatal("changed program accepted", err, holds)
		}
	}
	if d.Name != name || p.Context != "exact-context" {
		t.Fatal("native target canonicalized")
	}
}
