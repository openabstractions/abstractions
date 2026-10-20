package main

import (
	"encoding/json"
	"net/http"
)

// setupBusyReply is the refusal of a Setup request that would start a step
// while another step runs. It carries the running steps so the page shows
// the running step with its progress.
type setupBusyReply struct {
	Error    string                  `json:"error"`
	Busy     bool                    `json:"busy"`
	Actions  map[string]setupAction  `json:"actions"`
	Outcomes map[string]setupOutcome `json:"outcomes"`
}

// setupTryStart takes the Setup action lock at once or refuses the request
// with 409. No request waits on the lock for the length of a step.
func (p *servicePanel) setupTryStart(w http.ResponseWriter) bool {
	if p.setupActionMu.TryLock() {
		return true
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusConflict)
	_ = json.NewEncoder(w).Encode(setupBusyReply{
		Error:    "Another Catalog step is running.",
		Busy:     true,
		Actions:  p.setupActionsNow(),
		Outcomes: p.setupOutcomesNow(),
	})
	return false
}
