package main

import (
	"io"
	"net/http"

	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	rclient "github.com/openabstractions/abstraction-router/go/client"
)

// registryView is one reading of the runtime's catalogue sources: the
// configured and declared hosts, the provider declarations of
// abstraction.facade/registry@1, and the hosts remote runtimes report. Each
// part carries its own outcome or error, so one refusal leaves the others
// readable.
type registryView struct {
	Hosts         iclient.HostList      `json:"hosts"`
	HostsError    string                `json:"hosts_error,omitempty"`
	Providers     fwire.DeclarationList `json:"providers"`
	ProviderError string                `json:"providers_error,omitempty"`
	Remote        []rclient.HostState   `json:"remote"`
	RemoteError   string                `json:"remote_error,omitempty"`
}

// registryRoutes serves the registry page and its reading. Hosts come from the
// resolved abstraction.inference/operator@1, decided as host.manage for this
// Panel; declarations from abstraction.facade/registry@1, decided as
// provider.manage; remote runtimes' hosts from abstraction.router/router@1,
// decided as inventory.read.
func (p *servicePanel) registryRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/registry", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("k") != key {
			http.Error(w, "panel key required", 403)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, registryPage)
	})
	mux.HandleFunc("/registry/view", guard(key, p.registryView))
}

func (p *servicePanel) registryView(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.Error(w, "GET required", 405)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	view := registryView{Hosts: iclient.HostList{Hosts: []iclient.HostState{}}, Providers: fwire.DeclarationList{Declarations: []fwire.DeclarationState{}},
		Remote: []rclient.HostState{}}
	machine := panelMachine()
	if operator, err := machine.ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal}); err != nil {
		view.HostsError = err.Error()
	} else if hosts, err := operator.Hosts(ctx); err != nil {
		view.HostsError = err.Error()
	} else {
		view.Hosts = hosts
	}
	if registry, err := machine.ResolveRegistry(ctx, facade.Requirements{Scope: facade.ScopeLocal}); err != nil {
		view.ProviderError = err.Error()
	} else if declarations, err := registry.Declarations(ctx); err != nil {
		view.ProviderError = err.Error()
	} else {
		view.Providers = declarations
	}
	if routes, err := machine.ResolveRouter(ctx, facade.Requirements{Scope: facade.ScopeLocal}); err != nil {
		view.RemoteError = err.Error()
	} else if snapshot, err := routes.HostsContext(ctx, false); err != nil {
		view.RemoteError = err.Error()
	} else {
		for _, h := range snapshot.Hosts {
			if h.Domain != "" && h.Domain != h.Host {
				view.Remote = append(view.Remote, h)
			}
		}
	}
	panelJSON(w, view)
}
