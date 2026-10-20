package main

import (
	"context"
	facade "github.com/openabstractions/abstraction-facade/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	watch "github.com/openabstractions/abstraction-watch/go"
	"net/http"
	"time"
	"unicode/utf8"
)

const rightsObserveWait = 2 * time.Second

type rightsRevisionReader interface {
	ObservePolicyPagesContext(context.Context, string, int64) func(func(rightsclient.PolicyObservation, error) bool)
}
type rightsObservation struct {
	Outcome  string
	Revision string `json:"Revision,omitempty"`
	Cursor   string `json:"Cursor,omitempty"`
}

// The source owns the cursor and expiry deadline. This presenter receives one
// generated observation through Follow, including a fresh snapshot after gap.
// Its request cancellation closes and joins the active native observation.
func followRightsRevision(ctx context.Context, reader rightsRevisionReader, after string, wait time.Duration) (rightsObservation, error) {
	follow := watch.Follow(ctx, after, func(ctx context.Context, cursor string) func(func(rightsclient.PolicyObservation, error) bool) {
		return reader.ObservePolicyPagesContext(ctx, cursor, wait.Milliseconds())
	}, func(page rightsclient.PolicyObservation) (string, bool) {
		return page.Next, page.Outcome == wire.PolicyObservationOutcomeGap
	})
	defer follow.Close()
	page, err := follow.Next(ctx)
	if err != nil {
		return rightsObservation{}, err
	}
	if err := ctx.Err(); err != nil {
		return rightsObservation{}, err
	}
	return rightsObservation{Outcome: page.Outcome.String(), Revision: page.Revision, Cursor: page.Next}, nil
}

func (p *servicePanel) observeRights(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	revision := r.URL.Query().Get("revision")
	after := r.URL.Query().Get("after")
	if (revision != "" && !rightsText(revision, 128)) || len(after) > 256 || !utf8.ValidString(after) {
		http.Error(w, "invalid policy observation", http.StatusBadRequest)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), rightsObserveWait+5*time.Second)
	defer cancel()
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		if r.Context().Err() == nil {
			panelJSON(w, rightsObservation{Outcome: "unavailable"})
		}
		return
	}
	observation, err := followRightsRevision(ctx, operator, after, rightsObserveWait)
	if err != nil {
		if r.Context().Err() == nil {
			panelJSON(w, rightsObservation{Outcome: "unavailable"})
		}
		return
	}
	if r.Context().Err() == nil {
		panelJSON(w, observation)
	}
}
