package main

import (
	"bytes"
	"context"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	registration "github.com/openabstractions/abstraction-facade/go/abstraction/facade/registration"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	logging "github.com/openabstractions/abstraction-logging/go"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
)

func TestRegistryV3NativeResolvedHostedLifecycle(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current native transport")
	}
	r, caller := registryObserverFixture(t)
	d := registration1459Directory(t, t.TempDir())
	d.owner = r.operator.credentials.owner
	r.operator.credentials.runtimeRights.applications = d
	r.providers.registrationDirectory = func() *applicationDirectory { return d }
	r.providers.installation = ""
	endpoint := func() string {
		return listen.Endpoint(fmt.Sprintf("registry3-native-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	}
	registryEndpoint := endpoint()
	registry, err := listenRegistry(registryEndpoint, r, caller.Account, func(err error) { t.Log(err) })
	if err != nil {
		t.Fatal(err)
	}
	options := host.Options{Endpoint: endpoint(), LogEndpoint: endpoint(), ConfigEndpoint: endpoint(),
		Registry: registry, RegistryEndpoint: registryEndpoint, Sink: remoteTestSink(func(logging.Record) error { return nil })}
	options.ConfigStore, options.ConfigUserKey = isolatedConfigStore(t.TempDir())
	options.ConfigWithoutMachine = true
	runtime, err := host.Listen(options)
	if err != nil {
		_ = registry.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	done := make(chan error, 1)
	go func() { done <- runtime.Serve(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = runtime.Close()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	principal, err := currentPrincipal()
	if err != nil {
		t.Fatal(err)
	}
	program, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	frames := func(endpoint string) wire.EndpointTransport {
		return (listen.FrameClient{Endpoint: endpoint, Timeout: 3 * time.Second, MaxFrame: 1 << 20, Server: &listen.ServerExpectation{Principal: principal, Program: program}}).WithContext(ctx)
	}
	contract := (&registration.RegistryV3Dispatcher{}).ServiceContract()
	advertised, err := wire.NewEndpointClient(frames(registryEndpoint)).Describe()
	if err != nil || advertised.Outcome != wire.DescriptionOutcomeDescribed || !slices.ContainsFunc(advertised.Services, func(s wire.ServiceState) bool {
		return s.Contract == contract && s.Readiness == wire.ServiceReadinessReady
	}) {
		t.Fatalf("native registry@3 advertisement: %+v %v", advertised, err)
	}
	resolved, err := wire.NewResolverClient(frames(options.Endpoint)).Resolve(wire.ResolveRequest{Capability: "abstraction.facade", Contracts: []string{contract}, Guarantees: []string{}, Scope: wire.ScopeLocal})
	if err != nil || resolved.Status.String() != "resolved" || resolved.Reference.Contract != contract || resolved.Reference.Endpoint != registryEndpoint {
		t.Fatalf("native registry@3 resolution: %+v %v", resolved, err)
	}
	client := registration.NewRegistryV3Client(frames(resolved.Reference.Endpoint))
	legacy := wire.NewRegistryClient(frames(resolved.Reference.Endpoint))
	page, err := client.Registrations("", 64)
	if err != nil || page.Outcome != registration.OutcomePage || len(page.Registrations) != 0 {
		t.Fatal(page, err)
	}
	spec := registration.RegistrationSpec{Program: "", Providers: []registration.ProviderFacet{}, Servers: []registration.ServerFacet{{Name: "native-journey-host", Hosted: true, Kind: router.WireOpenAICompatible, Base: "https://first.fixture.invalid", Modalities: []string{"chat"}, Credential: ""}}, Remotes: []registration.RemoteFacet{}, Applications: []registration.ApplicationFacet{}}
	created, err := client.Register(spec)
	if err != nil || created.Outcome != registration.OutcomeApplied || created.Revision == "" {
		t.Fatalf("native Register: %+v %v", created, err)
	}
	page, err = client.Registrations("", 64)
	if err != nil || page.Outcome != registration.OutcomePage || len(page.Registrations) != 1 {
		t.Fatal(page, err)
	}
	row := page.Registrations[0]
	if row.Key != spec.Servers[0].Name || row.Revision != created.Revision || row.IdentityEvidence != registration.IdentityEvidenceNone || row.Source != registration.SourceOperator || len(row.Spec.Servers) != 1 || row.Spec.Servers[0].Base != spec.Servers[0].Base {
		t.Fatal(row)
	}
	old, err := legacy.Declarations()
	if err != nil || old.Outcome != wire.DeclarationListOutcomePage || len(old.Declarations) != 1 || old.Declarations[0].Declaration.Name != row.Key || old.Declarations[0].Declaration.Host == nil || old.Declarations[0].Declaration.Host.Base != spec.Servers[0].Base {
		t.Fatalf("canonical record legacy projection: %+v %v", old, err)
	}
	spec.Servers[0].Base = "https://second.fixture.invalid"
	replaced, err := client.Replace(created.Revision, spec)
	if err != nil || replaced.Outcome != registration.OutcomeApplied || replaced.Revision == created.Revision {
		t.Fatal(replaced, err)
	}
	// The canonical publication owner keeps its index and immutable program/
	// receipt payloads here. HTTP-only records have no application descriptor.
	snapshot := func() map[string][]byte {
		t.Helper()
		entries, err := os.ReadDir(d.v3.dir)
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][]byte{}
		for _, entry := range entries {
			if !strings.HasSuffix(entry.Name(), ".json") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(d.v3.dir, entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			out[entry.Name()] = raw
		}
		if _, ok := out["index.json"]; !ok {
			t.Fatal("canonical registration index absent")
		}
		return out
	}
	before := snapshot()
	stale := spec
	stale.Servers = slices.Clone(spec.Servers)
	stale.Servers[0].Base = "https://stale.fixture.invalid"
	conflict, err := client.Replace(created.Revision, stale)
	if err != nil || conflict.Outcome != registration.OutcomeConflict {
		t.Fatalf("native stale Replace: %+v %v", conflict, err)
	}
	after := snapshot()
	if !maps.EqualFunc(before, after, bytes.Equal) {
		t.Fatal("stale native edit wrote canonical index or payload")
	}
	page, err = client.Observe("", 0)
	if err != nil || page.Outcome != registration.OutcomePage || len(page.Registrations) != 1 || page.Registrations[0].Revision != replaced.Revision || page.Registrations[0].Spec.Servers[0].Base != spec.Servers[0].Base {
		t.Fatal(page, err)
	}
	removed, err := client.Remove(replaced.Revision, registration.FacetReference{Kind: registration.FacetKindHost, Name: spec.Servers[0].Name})
	if err != nil || removed.Outcome != registration.OutcomeApplied || removed.Removal == nil {
		t.Fatalf("native Remove: %+v %v", removed, err)
	}
	page, err = client.Registrations("", 64)
	if err != nil || page.Outcome != registration.OutcomePage || len(page.Registrations) != 0 {
		t.Fatal(page, err)
	}
	old, err = legacy.Declarations()
	if err != nil || old.Outcome != wire.DeclarationListOutcomePage || len(old.Declarations) != 0 {
		t.Fatalf("legacy removal projection: %+v %v", old, err)
	}
	before = snapshot()
	subject := rights.Subject{Account: caller.Account, Program: caller.Program}
	if err := r.operator.credentials.policy.Set(subject, ActionProviderManage, "account", false); err != nil {
		t.Fatal(err)
	}
	denied, err := client.Register(spec)
	if err != nil || denied.Outcome != registration.OutcomeForbidden || denied.Refusal == nil || denied.Refusal.Source != rights.RefusalSourceRights || denied.Refusal.Decision != rights.RefusalDecisionDenied {
		t.Fatalf("receiving native authority refusal: %+v %v", denied, err)
	}
	after = snapshot()
	if !maps.EqualFunc(before, after, bytes.Equal) {
		t.Fatal("authority-refused native edit wrote canonical index or payload")
	}
}
