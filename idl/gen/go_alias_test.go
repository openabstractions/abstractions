package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestGoAliasProduction(t *testing.T) {
	root := t.TempDir()
	schema := filepath.Join("..", "..", "openabstractions-flat", "abstraction-facade", "facade.thrift")
	var out bytes.Buffer
	if err := run([]string{schema, filepath.Join(root, "canonical"), "go"}, &out); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{schema, filepath.Join(root, "compat"), "go", "--go-alias-package=example.test/canonical/go/abstraction/facade"}, &out); err != nil {
		t.Fatal(err)
	}
	alias, err := os.ReadFile(filepath.Join(root, "compat", "go", "abstraction", "facade", "rec.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(alias), "var Encode") || !strings.Contains(string(alias), "func Encode(") {
		t.Fatal("functions must forward")
	}
	os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test\ngo 1.26.0\n"), 0600)
	os.WriteFile(filepath.Join(root, "alias_test.go"), []byte(`package candidate_test
import("testing"; old "example.test/compat/go/abstraction/facade"; core "example.test/canonical/go/abstraction/facade")
func TestIdentityAndCodec(t *testing.T){
 var original core.ResolveResult = old.ResolveResult{Status:old.ResolutionStatusUnavailable}
 bytes:=old.Encode(&original); value,err:=core.Decode(bytes);if err!=nil||value.Status!="unavailable"{t.Fatal(value,err)}
 var _ *old.ResolverClient = core.NewResolverClient(nil)
 old.ScopeNames[0]="changed";if core.ScopeNames[0]!="changed"{t.Fatal("lost backing contents")}
}
`), 0600)
	command := exec.Command("go", "test", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off")
	if result, err := command.CombinedOutput(); err != nil {
		t.Fatalf("%s: %v", result, err)
	}
}
func TestGoAliasUnsupportedAndFlags(t *testing.T) {
	if _, err := goExportAliases("package p;var Counter=1", "example/core"); err == nil {
		t.Fatal("mutable scalar silently copied")
	}
	if _, err := goExportAliases("package p;type Box[T any] struct{V T}", "example/core"); err == nil {
		t.Fatal("unsupported generic")
	}
	var out bytes.Buffer
	schema := filepath.Join("..", "..", "openabstractions-flat", "abstraction-facade", "facade.thrift")
	for _, flag := range []string{"--go-alias-package=", "--go-alias-package=bad path"} {
		if run([]string{schema, t.TempDir(), "go", flag}, &out) == nil {
			t.Fatal(flag)
		}
	}
}
