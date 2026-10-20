//go:build !windows

package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestCodexExplicitCLIIdentityPreservesNoLinksOnOtherPlatforms(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(root, "selected-inert-cli")
	if err := os.WriteFile(program, []byte("never executed"), 0700); err != nil {
		t.Fatal(err)
	}
	held, stored, err := setupCodexCLIIdentity(context.Background(), program)
	if err != nil || held == nil || stored == "" {
		t.Fatal(held, stored, err)
	}
	if !setupIdentitiesCurrent(context.Background(), []setupIdentityRef{held}) {
		t.Fatal("selected CLI identity changed")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "linked-cli")
	if err := os.Symlink(program, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := setupCodexCLIIdentity(context.Background(), link); err == nil {
		t.Fatal("linked CLI admitted")
	}
}
