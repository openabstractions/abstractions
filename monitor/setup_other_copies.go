package main

import (
	"net/http"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// findOtherCopies reads the machine for copies of ComfyUI that
// carry no OpenAbstractions mark (VISION 2026-10-02). Tests replace it, so no
// test reads the machine.
var findOtherCopies = func() ([]setup.OtherCopy, error) { return setup.DefaultCopyFinder().Find() }

type setupOtherCopiesReply struct {
	Copies []setup.OtherCopy `json:"copies"`
	// Incomplete says a source could not be read; the copies listed are the
	// ones Setup found.
	Incomplete bool `json:"incomplete,omitempty"`
}

func otherCopiesNow() setupOtherCopiesReply {
	copies, err := findOtherCopies()
	if copies == nil {
		copies = []setup.OtherCopy{}
	}
	return setupOtherCopiesReply{Copies: copies, Incomplete: err != nil}
}

// setupOtherCopies lists the other copies for the Setup page before any plan
// is reviewed.
func (p *servicePanel) setupOtherCopies(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	panelJSON(w, otherCopiesNow())
}

// refuseOtherCopies writes a conflict when the request would install OA's
// build of an application installed without the OA mark, and the person did
// not choose to install it beside that copy.
func refuseOtherCopies(w http.ResponseWriter, request setupChoiceRequest, result setupOtherCopiesReply) bool {
	if err := setup.CheckOtherCopies(request.Selected, request.Beside, result.Copies, result.Incomplete); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return true
	}
	return false
}
