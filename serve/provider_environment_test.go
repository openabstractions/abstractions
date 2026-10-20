package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	logclient "github.com/openabstractions/abstraction-logging/go/client"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// A provider the runtime starts learns the runtime's own services from its
// environment, so a hand-written declaration names no endpoint of the
// runtime. On an isolated runtime the lease service is <name>-resource-table,
// a name the runtime's resolver endpoint does not lead to; the live
// walkthrough of 2026-10-02 (D8) declared <runtime endpoint>-resource-table,
// which nothing served.
func TestAHandWrittenDeclarationWorksOnAnIsolatedRuntime(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current shared transport")
	}
	program := copyTestBinary(t, "oa-fixture-env")
	dir := t.TempDir()
	options, _ := isolatedRuntime(t)
	options.isolated = fmt.Sprintf("oatest-env-%d-%d", os.Getpid(), endpointSeq.Add(1))
	served, err := resourceTableEndpoint(options)
	if err != nil {
		t.Fatal(err)
	}
	if served == options.endpoint+"-resource-table" {
		t.Fatalf("the fixture does not separate the two names: %s", served)
	}
	startInferenceRuntime(t, options)
	endpoint := []string{"--endpoint", options.endpoint, "--timeout", "30s"}
	name := fmt.Sprintf("env-source-%d", time.Now().UnixNano()%1_000_000_000)
	out, err := runProvider(t, append([]string{"add", name, "--program", program, "--provider-endpoint", name,
		"--contract", storageInventorySource, "--store", "ollama",
		"--arg", providerFixtureArg, "--arg", "env-inventory", "--arg", providerEndpointToken, "--arg", dir}, endpoint...)...)
	if err != nil {
		t.Fatalf("provider add: %v\n%s", err, out)
	}
	recorded := filepath.Join(dir, "env.json")
	eventually(t, 30*time.Second, "the provider starting", func() bool {
		_, err := os.Stat(recorded)
		return err == nil
	})
	data, err := os.ReadFile(recorded)
	if err != nil {
		t.Fatal(err)
	}
	var seen struct {
		Resource, Runtime, Log string
		Answered               bool
	}
	if err := json.Unmarshal(data, &seen); err != nil {
		t.Fatalf("%s: %v", data, err)
	}
	if seen.Resource != served || !seen.Answered {
		t.Fatalf("the provider found the lease service at %q (answered %v); the runtime serves it at %s", seen.Resource, seen.Answered, served)
	}
	if seen.Runtime != options.endpoint || seen.Log != options.logEndpoint {
		t.Fatalf("the provider was told runtime %q and log %q, want %s and %s", seen.Runtime, seen.Log, options.endpoint, options.logEndpoint)
	}
}

// models host declares no lease endpoint unless the person names one, and
// drops one an earlier models host wrote: derived from the runtime's
// endpoint, or the one the runtime names for leases@1, which on an isolated
// runtime is bootstrap.Endpoint(<name>-resource-table) and no derivation.
func TestModelsHostDeclaresNoLeaseEndpointOfItsOwn(t *testing.T) {
	runtimeEndpoint := `\\.\pipe\example-oawalk-runtime`
	runtimeLease := `\\.\pipe\openabstractions-user-example-oawalk-resource-table`
	for _, earlier := range []string{"", legacyLeaseEndpoint(runtimeEndpoint), legacyLeaseEndpoint(""), runtimeLease} {
		s := hostSettings{program: "modelhostd", engine: "llama-server", resource: earlier}.with("", "", "", "", 0, false, nil, runtimeEndpoint, runtimeLease)
		if s.resource != "" || slices.Contains(hostArguments(s, nil, nil), argResource) {
			t.Fatalf("declaration carried %q from %q: %q", s.resource, earlier, hostArguments(s, nil, nil))
		}
	}
	named := hostSettings{program: "modelhostd", engine: "llama-server"}.with("", "", "", `\\.\pipe\elsewhere`, 0, false, nil, runtimeEndpoint, runtimeLease)
	if args := hostArguments(named, nil, nil); !slices.Contains(args, `\\.\pipe\elsewhere`) {
		t.Fatalf("a named lease endpoint is not declared: %q", args)
	}
	kept := hostSettings{program: "modelhostd", engine: "llama-server", resource: `\\.\pipe\elsewhere`}.with("", "", "", "", 0, false, nil, runtimeEndpoint, runtimeLease)
	if kept.resource != `\\.\pipe\elsewhere` {
		t.Fatalf("a named lease endpoint was dropped on re-hosting: %q", kept.resource)
	}
}

// A variable the runtime sets replaces the one it inherited, by name without
// case on Windows; an endpoint variable it does not set is removed; the rest
// of the environment passes through, also when the runtime sets nothing.
func TestChildEnvironmentReplacesTheRuntimesVariables(t *testing.T) {
	base := []string{"PATH=/bin", "ABSTRACTION_RESOURCE_ENDPOINT=stale", "ABSTRACTION_LOG_ENDPOINT=stale", "HOME=/home/a"}
	if runtime.GOOS == "windows" {
		base = append(base, "abstraction_resource_endpoint=stale-lower", "=C:=C:\\work")
	}
	got := childEnvironment(base, []string{"ABSTRACTION_RESOURCE_ENDPOINT=fresh"})
	want := []string{"PATH=/bin", "HOME=/home/a"}
	if runtime.GOOS == "windows" {
		want = append(want, "=C:=C:\\work")
	}
	if !slices.Equal(got, append(slices.Clone(want), "ABSTRACTION_RESOURCE_ENDPOINT=fresh")) {
		t.Fatalf("got %q, want %q and the fresh endpoint", got, want)
	}
	if got := childEnvironment(base, nil); !slices.Equal(got, want) {
		t.Fatalf("with nothing set got %q, want %q", got, want)
	}
}

// An installed runtime started with no --endpoint sets no
// ABSTRACTION_RUNTIME_ENDPOINT, so its child finds the installed runtime and
// never the one the runtime's own parent named.
func TestAnInstalledRuntimesChildDoesNotInheritARuntimeEndpoint(t *testing.T) {
	t.Setenv(runtimeEndpointVar, `\\.\pipe\some-other-runtime`)
	t.Setenv("OA_ENV_PASSES_THROUGH", "kept")
	p := &runtimeProviders{}
	p.setChildEnvironment(providerEnvironment(runtimeFlags{}, host.Options{LogEndpoint: "installed-log"}))
	env := p.providerChildEnvironment()
	if slices.ContainsFunc(env, func(e string) bool { return strings.HasPrefix(strings.ToUpper(e), runtimeEndpointVar+"=") }) {
		t.Fatalf("an installed runtime's child inherited %s: %q", runtimeEndpointVar, env)
	}
	if !slices.Contains(env, "OA_ENV_PASSES_THROUGH=kept") || !slices.Contains(env, logclient.EnvEndpoint+"=installed-log") {
		t.Fatalf("the child environment lost the parent's variables or the runtime's own: %q", env)
	}
}

// A runtime on Mach services (--xpc) names no endpoint, and its child gets
// none of the five endpoint variables its parent carried. Other variables
// remain available to the child (FAC-R8).
func TestAnXPCRuntimesChildGetsNoEndpointVariables(t *testing.T) {
	for _, name := range runtimeEndpointNames {
		t.Setenv(name, "inherited")
	}
	const nativeAddon = "ABSTRACTION_IPC_NODE=non-endpoint-native-addon"
	t.Setenv("ABSTRACTION_IPC_NODE", "non-endpoint-native-addon")
	p := &runtimeProviders{}
	p.setChildEnvironment(providerEnvironment(runtimeFlags{xpc: true, endpoint: "explicit"},
		host.Options{LogEndpoint: "log", ConfigEndpoint: "config", Endpoint: "runtime"}))
	env := p.providerChildEnvironment()
	for _, entry := range env {
		for _, name := range runtimeEndpointNames {
			if strings.HasPrefix(strings.ToUpper(entry), name+"=") {
				t.Fatalf("an XPC runtime's child got endpoint %q", entry)
			}
		}
	}
	if !slices.Contains(env, nativeAddon) {
		t.Fatalf("an XPC runtime's child lost the legitimate non-endpoint variable %q", nativeAddon)
	}
}

func TestIsolatedXPCChildUsesOnlyItsRuntimeServices(t *testing.T) {
	for _, name := range runtimeEndpointNames {
		t.Setenv(name, "installed-or-parent-endpoint")
	}
	options := runtimeFlags{xpc: true, isolated: "permission-demo", endpoint: "derived"}
	endpoint := func(service string) string {
		t.Helper()
		value, err := bootstrap.IsolatedXPCEndpoint(options.isolated, service)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	h := host.Options{Endpoint: endpoint("runtime-v1"), LogEndpoint: endpoint("logging-v1"), ConfigEndpoint: endpoint("config-v1"),
		Router: &router.Router{}, RouterEndpoint: endpoint("router-v1"),
		ResourceTable: &resourceservice.Table{}, ResourceTableEndpoint: endpoint("resource-table-v1")}
	p := &runtimeProviders{}
	p.setChildEnvironment(providerEnvironment(options, h))
	env := p.providerChildEnvironment()
	for _, name := range runtimeEndpointNames {
		entries := 0
		for _, entry := range env {
			if strings.HasPrefix(entry, name+"=") {
				entries++
				if !strings.HasPrefix(strings.TrimPrefix(entry, name+"="), "xpc:com.openabstractions.isolated.permission-demo.") {
					t.Errorf("isolated child inherited another runtime's service: %q", entry)
				}
			}
		}
		if entries != 1 {
			t.Errorf("isolated child has %d values for %s", entries, name)
		}
	}
}
