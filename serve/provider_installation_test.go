package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	inference "github.com/openabstractions/abstraction-inference/go"
)

// copyTestBinaryTo places a copy of this test binary at dir/name (plus .exe
// on Windows): the same fake-program pattern copyTestBinary uses elsewhere in
// this package, at a caller-chosen directory instead of a fresh t.TempDir(),
// so the fixture lands at the exact tools directory a bundled program
// resolves against.
func copyTestBinaryTo(t *testing.T, dir, name string) string {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	target := filepath.Join(dir, name)
	in, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		t.Fatal(err)
	}
	if err := out.Close(); err != nil {
		t.Fatal(err)
	}
	return target
}

// newInstallationDir creates a fresh "declarations" directory and makes the
// runtime read it as the installation's. It returns the tools directory the
// declarations directory sits beside, the directory a bare name resolves
// against, and the declarations directory itself.
func newInstallationDir(t *testing.T) (tools, dir string) {
	t.Helper()
	tools = t.TempDir()
	dir = filepath.Join(tools, installationDeclarationsDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	saved := installationDeclarationPath
	installationDeclarationPath = func() (string, error) { return dir, nil }
	t.Cleanup(func() { installationDeclarationPath = saved })
	return tools, dir
}

// writeBundledDeclaration writes a version 2 declaration naming program (a
// bare name or an absolute path, as the caller chooses) into dir.
func writeBundledDeclaration(t *testing.T, dir, name, program, endpoint string, arguments []string) {
	t.Helper()
	f := providerFile{Version: providerFileVersion, Declaration: providerDeclaration{
		Name: name, Program: program, Arguments: arguments, Endpoint: endpoint, Transport: transportNative,
		Contracts: []string{inference.Contract}, Activation: "on_demand",
	}}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}

// Parse every authored installer declaration through the runtime's installed
// reader. This exercises bundled-program resolution and field validation, not
// a separate spelling list in the package tests.
func TestAuthoredInstallerDeclarationsLoad(t *testing.T) {
	for _, platform := range []string{"windows", "posix"} {
		t.Run(platform, func(t *testing.T) {
			source := filepath.Join("..", "installer", "declarations")
			if platform == "posix" {
				source = filepath.Join("..", "installer", "posix", "declarations")
			}
			entries, err := os.ReadDir(source)
			if os.IsNotExist(err) && !privateWorkspace(t) {
				t.Skip("authored declarations are absent from the published serve checkout")
			}
			if err != nil {
				t.Fatal(err)
			}
			tools := t.TempDir()
			dir := filepath.Join(tools, installationDeclarationsDir)
			if err := os.Mkdir(dir, 0o700); err != nil {
				t.Fatal(err)
			}
			count := 0
			for _, entry := range entries {
				if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
					continue
				}
				raw, err := os.ReadFile(filepath.Join(source, entry.Name()))
				if err != nil {
					t.Fatal(err)
				}
				var file providerFile
				if err := json.Unmarshal(raw, &file); err != nil {
					t.Fatal(err)
				}
				if program := file.Declaration.Program; program != "" {
					if err := os.WriteFile(filepath.Join(tools, program), nil, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				if err := os.WriteFile(filepath.Join(dir, entry.Name()), raw, 0o600); err != nil {
					t.Fatal(err)
				}
				count++
			}
			if count == 0 {
				t.Fatal("installer ships no declarations")
			}
			report, reported := reportCollector()
			p := &runtimeProviders{report: report}
			loaded, err := p.readDeclarationDir(dir, nil, true)
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded) != count || len(reported()) != 0 {
				t.Fatalf("loaded %d of %d installer declarations; errors: %v", len(loaded), count, reported())
			}
		})
	}
}

// reportCollector gathers the errors openProviders reports, so a test can
// assert on the invalid-program message without a live runtime.
func reportCollector() (func(error), func() []string) {
	var mu sync.Mutex
	var messages []string
	return func(err error) {
			mu.Lock()
			defer mu.Unlock()
			messages = append(messages, err.Error())
		}, func() []string {
			mu.Lock()
			defer mu.Unlock()
			return append([]string(nil), messages...)
		}
}

// An installation declaration naming its program as a bare file name resolves
// it against the tools directory the declarations directory sits beside, and
// the resolved absolute path is what the runtime launches (facade
// CONTRACT.md FAC-R7, VISION 2026-09-22 on optional features).
func TestInstallationBundledProgramResolvesAndLaunches(t *testing.T) {
	name := fmt.Sprintf("bundled-%d", time.Now().UnixNano()%1_000_000_000)
	pidDir := t.TempDir()
	tools, dir := newInstallationDir(t)
	program := copyTestBinaryTo(t, tools, "oa-fixture-provider")
	shipped := filepath.Base(program)
	writeBundledDeclaration(t, dir, name, shipped, name, []string{providerFixtureArg, "chat", providerEndpointToken, pidDir})

	report, reported := reportCollector()
	p, err := openProviders(t.TempDir(), false, report)
	if err != nil {
		t.Fatal(err)
	}
	installed := p.installedDeclarations(nil)
	if len(installed) != 1 || installed[0].Declaration.Name != name {
		t.Fatalf("installed declarations: %+v (reported %v)", installed, reported())
	}
	got := installed[0]
	if got.Declaration.Program != program {
		t.Fatalf("resolved program = %q, want %q", got.Declaration.Program, program)
	}
	if got.ShippedProgram != shipped {
		t.Fatalf("shipped program = %q, want %q", got.ShippedProgram, shipped)
	}
	if msgs := reported(); len(msgs) != 0 {
		t.Fatalf("a resolving bundled program was reported: %v", msgs)
	}

	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- p.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		<-served
	})
	s := p.lookup(name)
	if s == nil {
		t.Fatalf("no supervisor for %q", name)
	}
	s.activate()
	eventually(t, 10*time.Second, "the bundled program launching", func() bool {
		return len(pids(t, pidDir)) == 1
	})
}

// A bundled name that resolves to no file beside the tools directory is
// invalid, and the report names the shipped name and the resolved path.
func TestInstallationMissingBundledProgramIsRefused(t *testing.T) {
	name := fmt.Sprintf("missing-%d", time.Now().UnixNano()%1_000_000_000)
	shipped := "no-such-program"
	if runtime.GOOS == "windows" {
		shipped += ".exe"
	}
	tools, dir := newInstallationDir(t)
	writeBundledDeclaration(t, dir, name, shipped, name, nil)
	wantResolved := filepath.Join(tools, shipped)

	report, reported := reportCollector()
	p, err := openProviders(t.TempDir(), false, report)
	if err != nil {
		t.Fatal(err)
	}
	if installed := p.installedDeclarations(nil); len(installed) != 0 {
		t.Fatalf("a missing bundled program was accepted: %+v", installed)
	}
	msgs := reported()
	found := false
	for _, m := range msgs {
		if strings.Contains(m, "invalid program") && strings.Contains(m, shipped) && strings.Contains(m, wantResolved) {
			found = true
		}
	}
	if !found {
		t.Fatalf("no report named %q and %q: %v", shipped, wantResolved, msgs)
	}
	if s := p.lookup(name); s != nil {
		t.Fatalf("a missing bundled program still has a supervisor: %+v", s)
	}
}

// An operator's own declaration keeps the absolute-path rule: a bare program
// name written directly into the operator's directory is invalid and left
// out, the same as before bundled resolution existed for the installation's
// directory.
func TestProviderOperatorBareProgramIsRefused(t *testing.T) {
	state := t.TempDir()
	dir := filepath.Join(state, providersDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("operator-%d", time.Now().UnixNano()%1_000_000_000)
	bare := "provider"
	if runtime.GOOS == "windows" {
		bare += ".exe"
	}
	f := providerFile{Version: providerFileVersion, Declaration: providerDeclaration{
		Name: name, Program: bare, Arguments: []string{}, Endpoint: name, Transport: transportNative,
		Contracts: []string{inference.Contract}, Activation: "on_demand",
	}}
	raw, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name+".json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}

	report, reported := reportCollector()
	p, err := openProviders(state, false, report)
	if err != nil {
		t.Fatal(err)
	}
	files, _, err := p.read()
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range files {
		if got.Declaration.Name == name {
			t.Fatalf("a bare operator program was accepted: %+v", got)
		}
	}
	msgs := reported()
	found := false
	for _, m := range msgs {
		if strings.Contains(m, "invalid program") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no invalid program report: %v", msgs)
	}
	if s := p.lookup(name); s != nil {
		t.Fatalf("a bare operator program still has a supervisor: %+v", s)
	}
}
