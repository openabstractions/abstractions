package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// A fieldless struct that refuses unknown members once produced a switch whose
// only arm returned, leaving the loop tail unreachable. go vet 1.26.0 reports
// that; later vet releases do not, so the emitted shape is asserted directly.
func TestFieldlessRefusingDecoderHasNoUnreachableTail(t *testing.T) {
	root := t.TempDir()
	var out bytes.Buffer
	if err := run([]string{productionFile(t, "abstraction-config/config.thrift"), root, "go"}, &out); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(root, "go", "abstraction", "config", "rec.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(data)
	unreachable := "\t\t\tswitch key {\n\t\t\tdefault:\n\t\t\t\treturn nil, r.refuse(\"unknown_field\")\n\t\t\t}\n\t\t\tr.ws()"
	if strings.Contains(source, unreachable) {
		t.Fatal("generated decoder still emits an unreachable loop tail after a returning switch")
	}
	start := strings.Index(source, "func (r *reader) decodeOAConfigEditorReadUserArguments()")
	if start < 0 {
		t.Fatal("fieldless fixture struct no longer generated; choose another fieldless refusing struct")
	}
	decoder := source[start:]
	decoder = decoder[:strings.Index(decoder, "\n}\n")]
	if !strings.Contains(decoder, "return nil, r.refuse(\"unknown_field\")") || strings.Contains(decoder, "switch key") {
		t.Fatalf("fieldless decoder must refuse every member without a switch:\n%s", decoder)
	}
}

// Generated Go is vetted as a module of its own, so any analyzer finding in
// emitted code fails the generator rather than the capability that imports it.
func TestGeneratedGoPassesVet(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain unavailable")
	}
	root := t.TempDir()
	for _, relative := range []string{"abstraction-config/config.thrift", "abstraction-job/acceptance.thrift"} {
		var out bytes.Buffer
		name := strings.TrimSuffix(filepath.Base(relative), ".thrift")
		if err := run([]string{productionFile(t, relative), filepath.Join(root, name), "go"}, &out); err != nil {
			t.Fatal(relative, err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.test/vet\ngo 1.26.0\n"), 0600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "vet", "./...")
	command.Dir = root
	command.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOPROXY=off")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("go vet on generated Go: %v\n%s", err, output)
	}
}
