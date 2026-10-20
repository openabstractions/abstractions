package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"runtime"
	"sync/atomic"
	"testing"

	asks "github.com/openabstractions/abstraction-asks/go"
	credentials "github.com/openabstractions/abstraction-credentials/go"
	cwire "github.com/openabstractions/abstraction-credentials/go/abstraction/credentials/api"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	router "github.com/openabstractions/abstraction-router/go"
	routerclient "github.com/openabstractions/abstraction-router/go/client"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

type providerListingMemory struct {
	*privateReleaseMemory
	reads atomic.Int64
}

func (m *providerListingMemory) Get(key string) ([]byte, error) {
	m.reads.Add(1)
	return m.privateReleaseMemory.Get(key)
}

func TestProviderCallerHostListingNeverAsksOrReadsSavedSecret(t *testing.T) {
	rights := testRights(t, t.TempDir())
	if err := rights.install([]string{inference.ActionComplete, credentials.ActionApply}, nil); err != nil {
		t.Fatal(err)
	}
	definitions, err := grants.SelectorPermissionDefinitions(grants.SelectorServices{Credentials: true})
	if err != nil {
		t.Fatal(err)
	}
	rights.policy.Definitions = definitions
	var questions atomic.Int64
	rights.firstUse = func(rwire.Subject, func() asks.Publisher, string, string) string {
		questions.Add(1)
		return "unexpected-question"
	}
	backend := &providerListingMemory{privateReleaseMemory: &privateReleaseMemory{values: map[string][]byte{}}}
	holder, err := credentials.Open(credentials.Config{Backend: backend, Namespace: "provider-listing-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	stored := holder.Store(cwire.Subject{Account: rights.owner, Program: rights.operators[0]}, "", cwire.Registration{Name: "deepseek", Kind: "bearer", Scope: cwire.Scope{Targets: []string{"127.0.0.1"}, Consumers: []string{router.CredentialConsumer}}, Secret: []byte("synthetic-only")})
	if stored.Outcome != cwire.StoreOutcomeStored {
		t.Fatal(stored)
	}
	c := &runtimeCredentials{runtimeRights: rights, holder: holder}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(`{"data":[{"id":"fixture-model"}]}`)) }))
	defer upstream.Close()
	routes := router.New(router.NewHosted("deepseek", upstream.URL, router.WireOpenAICompatible, "deepseek"))
	// The synthetic listing has no authenticated upstream; Check still sees
	// the saved key's real scope and current native caller's policy below.
	routes.UseCredentials(func(context.Context, string, string, string) (map[string]string, error) { return nil, nil })
	endpoint := listen.Endpoint(fmt.Sprintf("provider-quiet-listing-%d-%d", os.Getpid(), endpointSeq.Add(1)))
	h, err := routerservice.Listen(endpoint, routes)
	if runtime.GOOS == "darwin" && errors.Is(err, identity.ErrNotProven) {
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	if err = h.EnablePolicy(func(context.Context, *identity.Peer, string, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	var reached atomic.Int64
	if err = h.EnableHostListing(func(ctx context.Context, peer *identity.Peer, hosts []router.HostState) ([]router.HostState, error) {
		subject, err := inferenceservice.SubjectFromPeer(peer)
		if err != nil {
			return nil, err
		}
		bound := rwire.Subject{Account: subject.Account, Program: subject.Program}
		if err := rights.policy.Set(bound, inference.ActionComplete, inference.ResourceHost("deepseek"), true); err != nil {
			return nil, err
		}
		if c.useDecision(ctx, bound, credentials.ResourceFor("deepseek")) != rwire.DecisionOutcomeNotGranted {
			return nil, fmt.Errorf("credential fixture did not reach not-granted")
		}
		reached.Add(1)
		return c.callerHostListing(ctx, peer, hosts)
	}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- h.Serve(ctx) }()
	defer func() { cancel(); <-done }()
	for range 2 {
		view, err := routerclient.New(endpoint).Hosts(true)
		if err != nil || len(view.Hosts) != 1 || view.Hosts[0].Servable || view.Hosts[0].AvailabilityCause == nil || string(*view.Hosts[0].AvailabilityCause) != "credential" {
			t.Fatal(view, err)
		}
	}
	if reached.Load() != 2 || questions.Load() != 0 || backend.reads.Load() != 0 {
		t.Fatal("read-only listing effects", reached.Load(), questions.Load(), backend.reads.Load())
	}
}

func TestProviderCallerHostProjectionPreservesBindingsAndSharedState(t *testing.T) {
	hosts := []router.HostState{{Host: "custom", Base: "https://person.example/v1", Wire: "openai-compatible", Credential: "personal", Hosted: true, Servable: true}, {Host: "deepseek", Base: "https://api.deepseek.com/v1", Wire: "openai-compatible", Credential: "deepseek", Hosted: true, Servable: true}}
	want := append([]router.HostState(nil), hosts...)
	seen := 0
	view := projectCallerHosts(hosts, func(host router.HostState) router.AvailabilityCause {
		seen++
		if host.Host == "deepseek" {
			return router.AvailabilityCredential
		}
		return ""
	})
	if seen != 2 || !reflect.DeepEqual(hosts, want) || !reflect.DeepEqual(view[0], hosts[0]) || view[1].Servable || view[1].AvailabilityCause != router.AvailabilityCredential || view[1].Base != hosts[1].Base || view[1].Credential != hosts[1].Credential {
		t.Fatal("caller projection changed binding/shared inventory", view, hosts)
	}
	var c *runtimeCredentials
	if _, err := c.callerHostListing(t.Context(), nil, hosts); err == nil {
		t.Fatal("unbound caller admitted")
	}
}

func TestProviderCallerHostProjectionRefusesStaleOrAmbiguousBinding(t *testing.T) {
	host := router.HostState{Host: "deepseek", Base: "https://api.deepseek.com/v1", Wire: "openai-compatible", Credential: "deepseek", Hosted: true, Servable: true}
	file := providerFile{Declaration: providerDeclaration{Name: host.Host, Host: &providerHost{Base: host.Base, Kind: host.Wire, Credential: host.Credential, Hosted: true}}}
	if got := projectCallerHostBindings([]router.HostState{host}, []providerFile{file}); !got[0].Servable {
		t.Fatal(got)
	}
	for _, kind := range []string{"missing", "disabled", "endpoint", "wire", "credential", "ambiguous"} {
		changed := file
		h := *file.Declaration.Host
		changed.Declaration.Host = &h
		files := []providerFile{changed}
		switch kind {
		case "missing":
			files = nil
		case "disabled":
			files[0].Disabled = true
		case "endpoint":
			h.Base = "https://person.example/v1"
		case "wire":
			h.Kind = "anthropic-messages"
		case "credential":
			h.Credential = "personal"
		case "ambiguous":
			files = append(files, changed)
		}
		got := projectCallerHostBindings([]router.HostState{host}, files)
		if got[0].Servable || got[0].AvailabilityCause != router.AvailabilityConfiguration || !host.Servable {
			t.Fatal(kind, got)
		}
	}
}
