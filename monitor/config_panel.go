package main

import (
	"net/http"
	"unicode/utf8"

	configclient "github.com/openabstractions/abstraction-config/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// Observe effective configuration through its owner. The editor draft remains
// browser-owned until an explicit revision-checked replacement is submitted.
func (p *servicePanel) observeSettings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	cursor := r.URL.Query().Get("cursor")
	if len(cursor) > 512 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid settings cursor", http.StatusBadRequest)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	observer, err := panelMachine().ResolveConfigObserver(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	value, err := observer.ObserveContext(ctx, configclient.RunOverrides{}, cursor, 2000)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, value)
}
