package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/openabstractions/abstraction-facade/go-core/redact/redacttest"
	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/client"
	storageservice "github.com/openabstractions/abstraction-storage/go/service"
)

// runtimeLeaseEndpoint answers where the runtime itself serves leases@1. An
// isolated runtime derives its resource table from --isolated while its
// resolver endpoint is another name; the live walkthrough of 2026-10-02 (D8)
// declared <runtime endpoint>-resource-table, which nothing served, and the
// application saw only engine:unavailable. models host compares a declared
// endpoint with this answer to drop one an earlier models host wrote.
func TestRuntimeLeaseEndpointIsTheOneTheRuntimeServes(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	options, _ := isolatedRuntime(t)
	options.isolated = fmt.Sprintf("oatest-lease-%d-%d", os.Getpid(), endpointSeq.Add(1))
	served, err := resourceTableEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	if served == options.endpoint+"-resource-table" {
		t.Fatalf("the fixture does not separate the two names: %s", served)
	}
	startInferenceRuntime(t, options)

	call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	got, err := runtimeLeaseEndpoint(call, client.New(options.endpoint))
	if err != nil {
		t.Fatal(err)
	}
	if got != served {
		t.Fatalf("the runtime named %s; it serves leases@1 at %s", got, served)
	}
	if !answersAt(got) {
		t.Fatalf("nothing answers at %s", got)
	}
}

// The models host command itself, against an isolated runtime, declares no
// lease endpoint: the runtime that starts the model host names its own in
// the environment, which a declared one would override (FAC-R8). One an
// earlier models host wrote, equal to the runtime's own, is dropped on the
// next run. An inventory source lists the object; a stand-in model host
// passes the engine check.
func TestModelsHostCommandDeclaresNoLeaseEndpoint(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	// Copied first, so the runtime, stopped by a later cleanup, has ended
	// the source before its folder is removed.
	source := copyTestBinary(t, "oa-fixture-inventory")
	options, _ := isolatedRuntime(t)
	options.isolated = fmt.Sprintf("oatest-host-%d-%d", os.Getpid(), endpointSeq.Add(1))
	served, err := resourceTableEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}

	name := fmt.Sprintf("object-source-%d", time.Now().UnixNano()%1_000_000_000)
	if out, err := runProvider(t, append([]string{"add", name, "--program", source, "--provider-endpoint", name,
		"--contract", storageInventorySource, "--store", "ollama",
		"--arg", providerFixtureArg, "--arg", "inventory-object", "--arg", providerEndpointToken, "--arg", t.TempDir()}, endpoint...)...); err != nil {
		t.Fatalf("provider add: %v\n%s", err, out)
	}
	eventually(t, 60*time.Second, "the source's store being accepted", func() bool {
		return len(providerStates(t, endpoint)[name].Accepted) > 0
	})

	// models host reads the inventory as this program, which the person
	// would allow once.
	if granted, err := runRightsCommand(t, "grant", "--program", probeSelf().Program, "--action", ActionInventoryRead,
		"--resource", storageservice.InventoryResource, "--endpoint", options.endpoint); err != nil || granted.Outcome != "applied" {
		t.Fatalf("grant inventory read: %+v %v", granted, err)
	}
	host := recordingModelHost(t, t.TempDir())
	declared := func(extra ...string) (hostSettings, []hostedModel, []string) {
		t.Helper()
		var out, diagnostics strings.Builder
		args := append([]string{"ollama/" + fixtureObject, "--engine", unpinnedEngine(t), "--program", host,
			"--endpoint", options.endpoint, "--timeout", "30s"}, extra...)
		if err := modelsHostCommand("models host", args, &out, &diagnostics); err != nil {
			t.Fatalf("models host: %v\n%s%s", err, out.String(), diagnostics.String())
		}
		call, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		registry, err := client.New(options.endpoint).ResolveRegistry(call, client.Requirements{})
		if err != nil {
			t.Fatal(err)
		}
		list, err := registry.Declarations(call)
		if err != nil {
			t.Fatal(err)
		}
		settings, models := readHostDeclaration(list)
		var arguments []string
		if d := existingHostDeclaration(list); d != nil {
			arguments = d.Arguments
		}
		return settings, models, arguments
	}
	settings, models, arguments := declared()
	if len(models) != 1 || settings.resource != "" || slices.Contains(arguments, argResource) {
		t.Fatalf("models host declared %q for %v (%q); it declares no lease endpoint", settings.resource, models, arguments)
	}
	// An earlier models host wrote the runtime's own endpoint; the next run
	// drops it.
	if settings, _, _ := declared("--resource-endpoint", served); settings.resource != served {
		t.Fatalf("a named lease endpoint is not declared: %q", settings.resource)
	}
	if settings, _, arguments := declared(); settings.resource != "" || slices.Contains(arguments, argResource) {
		t.Fatalf("the runtime's own lease endpoint %s stayed declared: %q", served, arguments)
	}
}

// An on-demand provider that exits before it answers leaves its output in a
// bounded log under the runtime's state, and its exit reason carries the last
// line and names that log (D8: the walkthrough's provider stderr went
// nowhere).
func TestAnOnDemandProviderExitKeepsItsOutput(t *testing.T) {
	why, reports, logPath, data := runExitingProvider(t, "loud", "stderr-exit")
	for _, want := range []string{"exit status 43", "the lease endpoint does not answer", "log " + logPath} {
		if !strings.Contains(why, want) {
			t.Fatalf("exit reason %q does not carry %q", why, want)
		}
	}
	if !strings.Contains(data, "fixture provider: progress line\n") || !strings.Contains(data, "the lease endpoint does not answer") {
		t.Fatalf("the provider log holds %q", data)
	}
	// The runtime's log records the exit status and the log, never the line.
	if len(reports) != 1 || reports[0] != "provider loud exited:exit status 43; log "+logPath {
		t.Fatalf("the runtime reported %q", reports)
	}
}

// A provider's last line can carry a credential and terminal escapes. The
// exit reason carries it with escapes and control characters removed and
// the credential redacted; the runtime's log carries no line at all; the
// provider's own log keeps the line as written (review of 2026-10-02, D8).
func TestAProvidersExitReasonCarriesNoSecretOrEscape(t *testing.T) {
	why, reports, logPath, data := runExitingProvider(t, "secretive", "stderr-secret")
	if strings.Contains(why, "fixtureSECRET") || strings.ContainsRune(why, 0x1b) || strings.Contains(why, "[31m") {
		t.Fatalf("exit reason %q carries the credential or an escape", why)
	}
	if !strings.Contains(why, "exit status 44") || !strings.Contains(why, "upstream refused Authorization: [redacted]") {
		t.Fatalf("exit reason %q", why)
	}
	if len(reports) != 1 || reports[0] != "provider secretive exited:exit status 44; log "+logPath {
		t.Fatalf("the runtime reported %q", reports)
	}
	if !strings.Contains(data, "Bearer fixtureSECRET.123-abc \x1b[31mfailed") {
		t.Fatalf("the provider log does not keep the line as written: %q", data)
	}
}

// The exit reason carries a provider's last line through the owner of the
// redaction rules: every row of its bypass table (redacttest.Cases) answers
// the same here, and the tail is cut at 240 bytes on a character boundary.
func TestSafeTail(t *testing.T) {
	for _, c := range redacttest.Cases {
		if got := safeTail(c.In); got != c.Want {
			t.Errorf("safeTail(%q) = %q, want %q", c.In, got, c.Want)
		}
	}
	if got := safeTail("tab\there\x00nul sep"); got != "tab here nul sep" {
		t.Errorf("safeTail joined words across controls: %q", got)
	}
	long := strings.Repeat("é", 200)
	if got := safeTail(long); len(got) > providerTailLimit || !utf8.ValidString(got) {
		t.Fatalf("a long tail is %d bytes, valid %v", len(got), utf8.ValidString(got))
	}
}

// runExitingProvider starts the fixture provider kind on demand under a
// supervisor with no probe and no relaunch, waits for its exit, and returns
// the exit reason, what the runtime reported, the provider log's path and
// its contents.
func runExitingProvider(t *testing.T, name, kind string) (string, []string, string, string) {
	t.Helper()
	program := copyTestBinary(t, "oa-fixture-provider")
	state := t.TempDir()
	var mu sync.Mutex
	var reports []string
	p := &runtimeProviders{dir: filepath.Join(state, providersDir),
		report: func(err error) {
			mu.Lock()
			reports = append(reports, err.Error())
			mu.Unlock()
		},
		supervisors: map[string]*providerSupervisor{}, changes: make(chan struct{}), mediated: map[string]mediatedProvider{},
		timing: providerTiming{probeWaiting: time.Hour, probeReady: time.Hour, backoffFirst: time.Hour, backoffMax: time.Hour,
			stable: time.Minute, probeBudget: time.Second, inventoryBudget: time.Second}}
	f := providerFile{Declaration: providerDeclaration{Name: name, Program: program, Endpoint: name, Transport: transportNative,
		Arguments:  []string{providerFixtureArg, kind, providerEndpointToken, t.TempDir()},
		Activation: wire.ActivationOnDemand.String()}}
	s := newProviderSupervisor(p, f)
	s.start()
	s.activate()
	defer s.halt()
	eventually(t, 30*time.Second, "the provider exiting", func() bool {
		return strings.HasPrefix(s.state().Why, "exited:")
	})
	logPath := providerLogPath(p.dir, name)
	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	return s.state().Why, slices.Clone(reports), logPath, string(data)
}

// The provider log is bounded: past its limit the file rotates once and a
// new one starts.
func TestProviderLogRotatesAtItsLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs", "p.log")
	log := &boundedLog{path: path}
	line := strings.Repeat("x", 1000)
	for i := 0; i < 2*providerLogLimit/len(line)+10; i++ {
		if err := log.writeLine(line); err != nil {
			t.Fatal(err)
		}
	}
	log.close()
	for _, p := range []string{path, path + ".1"} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > providerLogLimit {
			t.Fatalf("%s holds %d bytes, above %d", p, info.Size(), providerLogLimit)
		}
	}
}
