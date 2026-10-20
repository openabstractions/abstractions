package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/cacheproducer"
)

func TestCompanionDeclareBeforeKeepMakesNoVendorOrOwnershipClaim(t *testing.T) {
	o, self, _ := claudeSetupFixture(t)
	image, _, err := setupIdentity(t.Context(), self, false)
	if err != nil {
		t.Fatal(err)
	}
	key := image.FileID().Key()
	if err := image.Close(); err != nil {
		t.Fatal(err)
	}
	args := []string{"--helper-program", self, "--helper-key", key, "--host", "claude", "--member", "required", "--state-dir", o.state, "--cli", o.cli, "--project", o.project, "--name", o.name}
	var out bytes.Buffer
	if code := declareCompanionsCommand(args, &out, &out); code != 0 {
		t.Fatal(code, out.String())
	}
	paths := claudeMarkers(t, o.state)
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	raw, err := os.ReadFile(paths[0])
	if err != nil {
		t.Fatal(err)
	}
	marker, err := decodeClaudeMarker(raw)
	if err != nil || marker.Active {
		t.Fatal("declaration invented saved ownership", marker, err)
	}
	if err := cacheproducer.ValidateCompanionDeclaration(marker.Companion); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(o.project, ".mcp.json")); !os.IsNotExist(err) {
		t.Fatal("declaration wrote vendor configuration", err)
	}
	var declaration struct {
		Program, FileKey string
		Members          []struct{ Requirement string }
	}
	if json.Unmarshal(marker.Companion, &declaration) != nil || declaration.Program != self || len(declaration.Members) != 1 || declaration.Members[0].Requirement != "required" {
		t.Fatal(string(marker.Companion))
	}
	args[3] = "wrong-held-key"
	if code := declareCompanionsCommand(args, &out, &out); code == 0 {
		t.Fatal("wrong held key admitted")
	}
	after, err := os.ReadFile(paths[0])
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("failed declaration changed receipt", err)
	}
}
