package main

import (
	"context"
	"sync"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	"github.com/openabstractions/abstraction-identity/listen"
	inference "github.com/openabstractions/abstraction-inference/go"
	iwire "github.com/openabstractions/abstraction-inference/go/abstraction/inference/api"
	inferenceservice "github.com/openabstractions/abstraction-inference/go/service"
	router "github.com/openabstractions/abstraction-router/go"
)

// registryOperator answers hosts from three sources and one provider.
type registryOperator struct{ *panelOperator }

func (o registryOperator) Hosts(_ context.Context, s inference.Subject) iwire.HostList {
	o.seen("hosts", s)
	return iwire.HostList{Outcome: iwire.ListOutcomePage, Revision: "hosts-v1:r", Hosts: []iwire.HostState{
		{Entry: iwire.HostEntry{Name: "ollama", Kind: "ollama", Base: "http://127.0.0.1:11500", Profiles: []string{"chat", "embed"}, DeclaredBy: "ollama"}, Up: true},
		{Entry: iwire.HostEntry{Name: "lemonade", Kind: "lemonade", Base: "http://127.0.0.1:13305", Profiles: []string{"chat"}, DeclaredBy: "default"}, Why: "unreachable"},
	}}
}

// fixedRegistry answers registry@1 with one declaration and records each call.
type fixedRegistry struct {
	mu    sync.Mutex
	calls int
}

func (r *fixedRegistry) Declarations() (fwire.DeclarationList, error) {
	r.mu.Lock()
	r.calls++
	r.mu.Unlock()
	return fwire.DeclarationList{Outcome: fwire.DeclarationListOutcomePage, Revision: "providers-v2:r", Declarations: []fwire.DeclarationState{{
		Declaration: fwire.Declaration{Name: "local-stores", Program: "/opt/inventoryd", Arguments: []string{}, Endpoint: "inventoryd-v1", Transport: fwire.DeclarationTransportNative,
			Contracts: []string{"abstraction.storage/inventory-source@1"}, Activation: fwire.ActivationOnDemand, Resources: []string{"store:ollama", "store:huggingface"}},
		DeclaredBy: "/opt/oa/openabstractions", Readiness: fwire.DeclarationReadinessReady, Restarts: 2, Accepted: []string{"store:ollama"},
		Described: []fwire.ServiceState{{Contract: "abstraction.storage/inventory-source@1", Readiness: fwire.ServiceReadinessReady, Guarantees: []string{}, Capabilities: map[string]string{}}}}}}, nil
}
func (r *fixedRegistry) Declare(string, fwire.Declaration) (fwire.DeclarationChange, error) {
	return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeForbidden}, nil
}
func (r *fixedRegistry) Withdraw(string, string) (fwire.DeclarationChange, error) {
	return fwire.DeclarationChange{Outcome: fwire.DeclarationEditOutcomeForbidden}, nil
}
func (r *fixedRegistry) Observe(string, int64) (fwire.DeclarationObservation, error) {
	return fwire.DeclarationObservation{Outcome: fwire.DeclarationListOutcomeForbidden, Declarations: []fwire.DeclarationState{}}, nil
}

// registryService serves a fixedRegistry on its endpoint.
type registryService struct {
	listener listen.Listener
	handler  *fixedRegistry
}

func (s registryService) Serve(ctx context.Context) error {
	stop := context.AfterFunc(ctx, func() { s.listener.Close() })
	defer stop()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return nil
		}
		go func() {
			defer conn.Close()
			call, err := listen.ReceiveFramed(ctx, conn, listen.Program, 1<<20)
			if call != nil {
				defer call.Close()
			}
			if err != nil {
				return
			}
			if reply, err := (&fwire.RegistryDispatcher{Handler: s.handler}).ExchangeFrame(call.Frame); err == nil {
				call.Reply(reply)
			}
		}()
	}
}
func (s registryService) Close() error { return s.listener.Close() }

// The registry view carries hosts with who declared each and their profiles,
// providers with readiness and accepted stores, and reports the router it
// could not resolve without hiding the rest.
func TestPanelRegistryReadsHostsProvidersAndRemotes(t *testing.T) {
	own(t)
	operator := registryOperator{&panelOperator{}}
	registry := &fixedRegistry{}
	panelConfigRuntimeWith(t, func(o *host.Options) {
		provider, err := inference.New(inference.Config{Router: router.New(),
			Decide: func(context.Context, inference.Subject, string, string) (string, error) { return "not_granted", nil },
			Apply: func(context.Context, inference.Subject, string, string, string) (map[string]string, string) {
				return nil, "unknown"
			}})
		if err != nil {
			t.Fatal(err)
		}
		endpoint := o.ConfigEndpoint + "-inference"
		h, err := inferenceservice.Listen(endpoint, provider)
		if err != nil {
			t.Fatal(err)
		}
		h.Operator = operator
		o.Inference, o.InferenceEndpoint = panelInference{Host: h, provider: provider}, endpoint
		registryAt := o.ConfigEndpoint + "-registry"
		l, err := listen.Listen(registryAt)
		if err != nil {
			t.Fatal(err)
		}
		o.Registry, o.RegistryEndpoint = registryService{listener: l, handler: registry}, registryAt
	})
	h := newTestPanel(t).handler("test-key")
	if r := panelRequest(t, h, "/registry?k=wrong", nil); r.Code != 403 {
		t.Fatalf("page without the key: %d", r.Code)
	}
	if r := panelRequest(t, h, "/registry?k=test-key", nil); r.Code != 200 {
		t.Fatalf("page: %d", r.Code)
	}
	view := decodePanel[registryView](t, 200, panelRequest(t, h, "/registry/view", nil).Body.Bytes())
	if view.HostsError != "" || len(view.Hosts.Hosts) != 2 || view.Hosts.Hosts[0].Entry.DeclaredBy != "ollama" {
		t.Fatalf("hosts %+v", view)
	}
	p := view.Providers.Declarations
	if view.ProviderError != "" || len(p) != 1 || p[0].Readiness != fwire.DeclarationReadinessReady || p[0].Accepted[0] != "store:ollama" || p[0].Restarts != 2 ||
		len(p[0].Described) != 1 {
		t.Fatalf("providers %+v %s", view.Providers, view.ProviderError)
	}
	registry.mu.Lock()
	if registry.calls != 1 {
		t.Fatalf("registry@1 Declarations calls: %d", registry.calls)
	}
	registry.mu.Unlock()
	if view.RemoteError == "" || len(view.Remote) != 0 {
		t.Fatalf("a runtime without router@1 reads as an error beside the rest: %+v", view)
	}
	operator.mu.Lock()
	defer operator.mu.Unlock()
	for _, s := range operator.callers {
		if s.Program == "" || s.Account == "" {
			t.Fatalf("an operator call was not bound: %+v", s)
		}
	}
}
