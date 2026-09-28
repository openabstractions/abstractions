package main

import (
	"context"
	"net/http"
	"time"
	"unicode/utf8"

	configclient "github.com/openabstractions/abstraction-config/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// settingsObserveWait is the server-side wait Observe asks for. The request's
// own context and the Observer's client transport both need a longer deadline
// than this: an idle wait that runs the full budget, plus the round trip and
// the service's own processing, must still finish inside the client timeout,
// or the client cancels a call the service was about to answer "unchanged"
// and this handler reports an error for what is a normal idle window.
const settingsObserveWait = 2 * time.Second

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
	ctx, cancel := context.WithTimeout(r.Context(), settingsObserveWait+5*time.Second)
	defer cancel()
	observer, err := panelMachine().ResolveConfigObserver(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	if observer, err = observer.WithTimeout(settingsObserveWait + 3*time.Second); err != nil {
		panelError(w, err)
		return
	}
	value, err := observer.ObserveContext(ctx, configclient.RunOverrides{}, cursor, settingsObserveWait.Milliseconds())
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, value)
}
