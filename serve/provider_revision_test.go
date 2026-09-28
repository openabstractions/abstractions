package main

import (
	"os"
	"path/filepath"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

func TestInstallationContentInvalidatesDeclarationRevision(t *testing.T) {
	for _, shadowed := range []bool{false, true} {
		name := "visible"
		if shadowed {
			name = "shadowed"
		}
		t.Run(name, func(t *testing.T) {
			p := &runtimeProviders{
				dir: t.TempDir(), installation: t.TempDir(),
				disabledPath: filepath.Join(t.TempDir(), "disabled.json"),
				report:       func(err error) { t.Errorf("unexpected declaration error: %v", err) },
			}
			program := filepath.Join(t.TempDir(), "provider")
			writeBundledDeclaration(t, p.installation, "example", program, "example", []string{"before"})
			if shadowed {
				writeBundledDeclaration(t, p.dir, "example", program, "example", []string{"override"})
			}
			_, before, err := p.read()
			if err != nil {
				t.Fatal(err)
			}
			_, unchanged, err := p.read()
			if err != nil || unchanged != before {
				t.Fatalf("unchanged declarations changed revision: %q -> %q (%v)", before, unchanged, err)
			}
			writeBundledDeclaration(t, p.installation, "example", program, "example", []string{"after"})
			_, after, err := p.read()
			if err != nil {
				t.Fatal(err)
			}
			if after == before {
				t.Fatal("same-name installation content changed without invalidating its revision")
			}
			if result := p.remove(before, "example"); result.Outcome != wire.DeclarationEditOutcomeConflict {
				t.Fatalf("stale withdrawal: %+v", result)
			}
			if _, err := os.Stat(filepath.Join(p.installation, "example.json")); err != nil {
				t.Fatalf("stale withdrawal changed installation: %v", err)
			}
		})
	}
}
