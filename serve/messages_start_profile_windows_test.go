package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

func TestMessagesStartProfileUsesApprovedLiteralsAndNativeSession(t *testing.T) {
	if messageStartProfileResolver(nil) != nil {
		t.Fatal("missing C59 approval configured production resolver")
	}
	folder, state := t.TempDir(), t.TempDir()
	p := startprofile.Profile{Name: "claude-v1", Context: "fixture", Folder: folder, StateDir: state, CLIFileID: "fixture-cli-file", Model: "fixture", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 1 << 30, BudgetCents: 1}
	d := wire.ApplicationDescriptor{Name: "receiver", Program: "fixture-helper", Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments()}}
	approved := true
	resolve := messageStartProfileResolver(func(_ context.Context, current wire.ApplicationDescriptor, profile startprofile.Profile) error {
		if !approved || current.Name != d.Name || profile != p {
			return errors.New("unapproved profile")
		}
		return nil
	})
	r, err := resolve(context.Background(), d)
	if err != nil {
		t.Fatal(err)
	}
	session, err := messageStartNativeSession()
	if err != nil {
		t.Fatal(err)
	}
	if r.Session != session || r.Context != p.Context || r.ProfileFingerprint != p.Fingerprint(d.Program) || r.Validate(context.Background()) != nil {
		t.Fatal("native profile projection", r, err)
	}
	approved = false
	if r.Validate(context.Background()) == nil {
		t.Fatal("approval revocation ignored")
	}
	approved = true
	if err := os.Rename(folder, filepath.Join(filepath.Dir(folder), "moved-folder")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(folder, 0700); err != nil {
		t.Fatal(err)
	}
	if r.Validate(context.Background()) == nil {
		t.Fatal("folder replacement ignored")
	}
}

func TestMessagesStartProfileRejectsMissingBoundsAndFolder(t *testing.T) {
	resolve := messageStartProfileResolver(func(context.Context, wire.ApplicationDescriptor, startprofile.Profile) error { return nil })
	for _, args := range [][]string{nil, {"start", "--profile", "claude-v1"}} {
		if _, err := resolve(context.Background(), wire.ApplicationDescriptor{Activation: &wire.ApplicationActivationRecipe{Arguments: args}}); err == nil {
			t.Fatal("incomplete recipe")
		}
	}
	p := startprofile.Profile{Name: "codex-v1", Context: "fixture", Folder: filepath.Join(t.TempDir(), "absent"), StateDir: t.TempDir(), CLIFileID: "fixture", Model: "fixture", TimeoutMS: 1000, OutputBytes: 4096, MemoryBytes: 1 << 30, BudgetCents: 1}
	if _, err := resolve(context.Background(), wire.ApplicationDescriptor{Activation: &wire.ApplicationActivationRecipe{Arguments: p.Arguments()}}); err == nil {
		t.Fatal("missing approved folder")
	}
}
