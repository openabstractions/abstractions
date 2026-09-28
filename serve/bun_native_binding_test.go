package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// The C ABI statuses this fixture names. OA_IPC_UNTRUSTED refuses a call whose
// server expectation the peer does not satisfy, before any application byte.
const ipcStatusUntrusted = 8

type bunProbeStep struct {
	OK            bool     `json:"ok"`
	Status        *int     `json:"status"`
	Transferred   *int     `json:"transferred"`
	Message       string   `json:"message"`
	Value         string   `json:"value"`
	Outcome       string   `json:"outcome"`
	Program       string   `json:"program"`
	Services      []string `json:"services"`
	PrincipalKind int      `json:"principalKind"`
	Principal     string   `json:"principal"`
}

type bunProbeResult struct {
	Select       bunProbeStep `json:"select"`
	Endpoint     bunProbeStep `json:"endpoint"`
	Verified     bunProbeStep `json:"verified"`
	WrongProgram bunProbeStep `json:"wrongProgram"`
	Unverified   bunProbeStep `json:"unverified"`
}

// The Bun binding reaches a running runtime through the shared C ABI library,
// and the server expectation it marshals decides the call: the isolated
// runtime's own program is answered, a different program is refused before any
// byte is sent, and a call without an expectation keeps the unverified
// compatibility mode. Both variables must name existing absolute files: this
// test runs a real Bun runtime and a built shared library, neither of which the
// Go toolchain produces.
func TestBunBindingCallsAnIsolatedRuntimeThroughTheSharedLibrary(t *testing.T) {
	bun, library := os.Getenv("OA_BUN_BINARY"), os.Getenv("OA_IPC_LIBRARY")
	if bun == "" || library == "" {
		t.Skip("set OA_BUN_BINARY to a bun executable and OA_IPC_LIBRARY to the built shared C ABI library to run the Bun binding")
	}
	for name, path := range map[string]string{"OA_BUN_BINARY": bun, "OA_IPC_LIBRARY": library} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s must be an absolute path, got %q", name, path)
		}
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}

	script := stageBunProbe(t)
	options, _ := isolatedRuntime(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	ready := make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("startup: %v", err)
	case <-time.After(runtimeWait):
		t.Fatalf("runtime not ready within %v", runtimeWait)
	}
	defer func() {
		cancel()
		if err := awaitStopped(t, done, "runtime"); err != nil {
			t.Error(err)
		}
	}()

	_, who, err := principal()
	if err != nil {
		t.Fatal(err)
	}
	kind := "2"
	if runtime.GOOS == "windows" {
		kind = "1"
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	program := filepath.Clean(executable)
	wrong := filepath.Join(filepath.Dir(program), "not-the-isolated-runtime"+filepath.Ext(program))

	call, finish := context.WithTimeout(ctx, runtimeWait)
	defer finish()
	command := exec.CommandContext(call, bun, script)
	command.Dir = filepath.Dir(script)
	command.Env = append(environmentWithout(os.Environ(), "ABSTRACTION_IPC_LIBRARY", "ABSTRACTION_IPC_NODE"),
		"ABSTRACTION_IPC_LIBRARY="+library,
		"OA_PROBE_ENDPOINT="+options.endpoint,
		"OA_PROBE_PRINCIPAL_KIND="+kind,
		"OA_PROBE_PRINCIPAL="+who,
		"OA_PROBE_PROGRAM="+program,
		"OA_PROBE_WRONG_PROGRAM="+wrong)
	var out, errors bytes.Buffer
	command.Stdout, command.Stderr = &out, &errors
	if err := command.Run(); err != nil {
		t.Fatalf("bun probe: %v\nstdout:\n%s\nstderr:\n%s", err, out.String(), errors.String())
	}
	lines := strings.Split(strings.ReplaceAll(strings.TrimSpace(out.String()), "\r\n", "\n"), "\n")
	if len(lines) == 0 {
		t.Fatalf("bun probe produced no output; stderr:\n%s", errors.String())
	}
	var result bunProbeResult
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &result); err != nil {
		t.Fatalf("bun probe output %q: %v\nstderr:\n%s", out.String(), err, errors.String())
	}

	// This machine's installation decides the selection outcome. Either answer
	// is a real measurement of the shared selector through the Bun worker; the
	// evidence records which one ran.
	switch {
	case result.Select.OK:
		if result.Select.PrincipalKind != 1 && result.Select.PrincipalKind != 2 {
			t.Fatalf("selectRuntime identity %+v", result.Select)
		}
		t.Logf("selectRuntime selected the installed runtime: kind=%d principal=%q program=%q",
			result.Select.PrincipalKind, result.Select.Principal, result.Select.Program)
	case result.Select.Status != nil && *result.Select.Status == ipcStatusUntrusted:
		t.Logf("selectRuntime refused a missing or ambiguous installation: %s", result.Select.Message)
	default:
		t.Fatalf("selectRuntime %+v, want the installed identity or Status.Untrusted", result.Select)
	}

	if !result.Endpoint.OK || result.Endpoint.Value == "" {
		t.Fatalf("runtimeEndpoint %+v, want a non-empty endpoint", result.Endpoint)
	}
	t.Logf("runtimeEndpoint answered %q", result.Endpoint.Value)

	hosted := []string{
		(&wire.ResolverDispatcher{}).ServiceContract(),
		(&wire.CallerDispatcher{}).ServiceContract(),
	}
	for _, described := range []struct {
		name string
		step bunProbeStep
	}{{"verified", result.Verified}, {"unverified", result.Unverified}} {
		if !described.step.OK || described.step.Outcome != wire.DescriptionOutcomeDescribed.String() ||
			!slices.Equal(described.step.Services, hosted) {
			t.Fatalf("%s Describe %+v, want outcome described and contracts %v", described.name, described.step, hosted)
		}
		t.Logf("%s Describe answered %s by %q hosting %v",
			described.name, described.step.Outcome, described.step.Program, described.step.Services)
	}

	if result.WrongProgram.OK || result.WrongProgram.Status == nil || *result.WrongProgram.Status != ipcStatusUntrusted ||
		result.WrongProgram.Transferred == nil || *result.WrongProgram.Transferred != 0 {
		t.Fatalf("Describe under a wrong program expectation %+v, want Status.Untrusted with zero bytes sent",
			result.WrongProgram)
	}
	t.Logf("a wrong program expectation refused the call with status %d and %d bytes sent",
		*result.WrongProgram.Status, *result.WrongProgram.Transferred)
}

// stageBunProbe copies the generated JavaScript packages and the fixture script
// into one resolvable tree, as the OpenCode provider staging does.
func stageBunProbe(t *testing.T) string {
	t.Helper()
	sources, err := findOpenCodeProviderSources(filepath.Clean(".."))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	modules := filepath.Join(root, "node_modules", "@openabstractions")
	copyTree(t, sources.ipc, filepath.Join(modules, "ipc"))
	copyTree(t, sources.facade, filepath.Join(modules, "facade"))
	fixture, err := os.ReadFile(filepath.Join("testdata", "bun-native-binding.js"))
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "bun-native-binding.js")
	if err := os.WriteFile(script, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	return script
}
