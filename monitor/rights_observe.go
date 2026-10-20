package main

import (
	"context"
	facade "github.com/openabstractions/abstraction-facade/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	watch "github.com/openabstractions/abstraction-watch/go"
	"net/http"
	"time"
)

const rightsObserveEvery = time.Second
const rightsObserveWait = 2 * time.Second

type rightsRevisionReader interface {
	ListPolicyContext(context.Context, string, int64) (rightsclient.PolicyPage, error)
}
type rightsObservation struct {
	Outcome  string
	Revision string `json:"Revision,omitempty"`
}
type rightsWatchState struct {
	outcome  string
	revision string
	deadline bool
}

// Observe is a Panel adapter over public, freshly authorized reads. The expiry
// deadline is only an invalidation hint; the subsequent ReadRule confirms expiry.
func followRightsRevision(ctx context.Context, reader rightsRevisionReader, revision string, refreshAt time.Time, every, quiet time.Duration) (rightsObservation, error) {
	sub := watch.Poll(func() (rightsWatchState, string, error) {
		if err := ctx.Err(); err != nil {
			return rightsWatchState{}, "", err
		}
		read, cancel := context.WithTimeout(ctx, time.Second)
		page, err := reader.ListPolicyContext(read, "", 1)
		cancel()
		if err := ctx.Err(); err != nil {
			return rightsWatchState{}, "", err
		}
		state := rightsWatchState{outcome: "unavailable"}
		if err == nil {
			state.outcome = page.Outcome.String()
			if state.outcome == "page" {
				state.revision = page.Revision
				state.deadline = !refreshAt.IsZero() && !time.Now().Before(refreshAt)
			}
		}
		// Availability is visible data. A Source error retains the last good
		// state, which must not report fresh editable policy during an outage.
		stamp := state.outcome + ":" + state.revision
		if state.deadline {
			stamp += ":expiry"
		}
		return state, stamp, nil
	}, every, quiet)
	defer sub.Close()
	for {
		notice, err := sub.Next(ctx)
		if err != nil {
			return rightsObservation{}, err
		}
		if err := ctx.Err(); err != nil {
			return rightsObservation{}, err
		}
		state := notice.Now
		if state.outcome != "page" {
			return rightsObservation{Outcome: state.outcome}, nil
		}
		if state.revision != revision || state.deadline {
			return rightsObservation{Outcome: "changed", Revision: state.revision}, nil
		}
		if notice.Quiet {
			return rightsObservation{Outcome: "unchanged", Revision: state.revision}, nil
		}
	}
}

func (p *servicePanel) observeRights(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	revision := r.URL.Query().Get("revision")
	if revision != "" && !rightsText(revision, 128) {
		http.Error(w, "invalid policy revision", http.StatusBadRequest)
		return
	}
	var refreshAt time.Time
	if value := r.URL.Query().Get("refresh_at"); value != "" {
		const format = "2006-01-02T15:04:05.000Z"
		var err error
		refreshAt, err = time.Parse(format, value)
		if err != nil || refreshAt.Format(format) != value {
			http.Error(w, "invalid refresh deadline", http.StatusBadRequest)
			return
		}
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
	observation, err := followRightsRevision(ctx, operator, revision, refreshAt, rightsObserveEvery, rightsObserveWait)
	if err != nil {
		if r.Context().Err() != nil {
			return
		}
		panelJSON(w, rightsObservation{Outcome: "unavailable"})
		return
	}
	if r.Context().Err() == nil {
		panelJSON(w, observation)
	}
}
