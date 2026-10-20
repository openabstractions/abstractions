package main

import (
	"context"
	"net/http"
	"time"

	askclient "github.com/openabstractions/abstraction-asks/go/client"
	facade "github.com/openabstractions/abstraction-facade/go"
)

// navJSONGlyph is one item's icon for a native host: a Segoe Fluent Icons
// codepoint on Windows, an SF Symbol name on macOS.
type navJSONGlyph struct {
	Segoe string `json:"segoe"`
	SF    string `json:"sf"`
}

// navJSONItem is one sidebar row of the shell contract (SPEC §6).
type navJSONItem struct {
	ID    string       `json:"id"`
	Label string       `json:"label"`
	Path  string       `json:"path"`
	Group string       `json:"group"`
	Glyph navJSONGlyph `json:"glyph"`
	Badge *int         `json:"badge,omitempty"`
}

// navJSON is GET /panel/nav.json: the navigation table in sidebar order and
// the selected item.
type navJSON struct {
	Items    []navJSONItem `json:"items"`
	Selected string        `json:"selected"`
}

// navBadgeBudget bounds the waiting-question count, so a slow runtime leaves
// the badge absent instead of delaying the sidebar.
const navBadgeBudget = 2 * time.Second

// panelWaitingCount reports how many questions wait for an answer: first-use
// questions not yet answered, app choices still pending and typed permission
// requests not yet answered, from the first page of each list. ok is false when the runtime could not tell. Tests
// replace it.
var panelWaitingCount = func(ctx context.Context) (int, bool) {
	operator, err := panelMachine().ResolveAsksOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return 0, false
	}
	questions, err := operator.ListQuestionsContext(ctx, "", 64)
	if err != nil || questions.Outcome != askclient.OperatorPageOutcomePage {
		return 0, false
	}
	waiting := 0
	for _, record := range questions.Records {
		if record.Option == "" {
			waiting++
		}
	}
	if choices, err := operator.ListChoicesContext(ctx, "", 64); err == nil && choices.Outcome == askclient.OperatorPageOutcomePage {
		now := time.Now()
		for _, record := range choices.Records {
			if choicePendingAt(record, now) {
				waiting++
			}
		}
	}
	// Typed permission requests wait in the same list (asks ASK-A10).
	if typed, err := defaultPresenterPermissions(ctx); err == nil {
		if page, err := typed.ListPermissionsContext(ctx, "", 64); err == nil && page.Outcome == askclient.OperatorPageOutcomePage {
			for _, record := range page.Records {
				if record.Option == "" {
					waiting++
				}
			}
		}
	}
	return waiting, true
}

// navTable builds the contract's item list; badge is the waiting count, or
// negative for none.
func navTable(badge int, selected string) navJSON {
	view := navJSON{Items: make([]navJSONItem, 0, len(panelNavItems)), Selected: selected}
	for _, item := range panelNavItems {
		row := navJSONItem{ID: item.ID, Label: item.Label, Path: item.Path, Group: item.Group, Glyph: navJSONGlyph{Segoe: item.Segoe, SF: item.SF}}
		if item.ID == navBadgeID && badge >= 0 {
			n := badge
			row.Badge = &n
		}
		view.Items = append(view.Items, row)
	}
	return view
}

// navJSONHandler serves the navigation table to a native host's sidebar.
// ?path= names the page the host shows, so selected follows it; without one,
// selected is the landing page.
func (p *servicePanel) navJSONHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), navBadgeBudget)
	defer cancel()
	selected := ""
	if path := r.URL.Query().Get("path"); path != "" {
		if item, ok := navItemByPath(path); ok {
			selected = item.ID
		}
	}
	if selected == "" {
		item, _ := navItemByPath(p.landingPath(ctx))
		selected = item.ID
	}
	badge := -1
	if n, ok := panelWaitingCount(ctx); ok {
		badge = n
	}
	w.Header().Set("Cache-Control", "no-store")
	panelJSON(w, navTable(badge, selected))
}

// landingPath is where the Panel opens (P-J1a): Get started while a setup
// component the person chose is unfinished, and Apps otherwise, also on a
// fresh install. Get started leads with this device's pages before the AI
// choices; Apps names the failure in its health banner when the runtime
// cannot list apps.
func (p *servicePanel) landingPath(ctx context.Context) string {
	if journal, err := p.setupJournalForPanel(); err == nil {
		if state, err := journal.Load(ctx); err == nil && state.Plan.ID != "" {
			if state.Ready() {
				return "/"
			}
			return "/setup"
		}
	}
	return "/"
}

// landing serves /start, the address the Panel opens at: it sends the
// window to the landing page with the query, and with it the Panel key.
func (p *servicePanel) landing(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), navBadgeBudget)
	defer cancel()
	redirectAlias(p.landingPath(ctx))(w, r)
}

// navRoutes registers the shell contract's routes, both behind the Panel
// key (guard: the X-Panel-Key header or ?k=, compared in constant time).
func (p *servicePanel) navRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/panel/nav.json", guard(key, p.navJSONHandler))
	mux.HandleFunc("/start", guard(key, p.landing))
}
