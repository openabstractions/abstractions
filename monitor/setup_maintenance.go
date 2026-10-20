package main

import (
	"net/http"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

func (p *servicePanel) setupMaintenance(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		if !p.setupTryStart(w) {
			return
		}
		defer p.setupActionMu.Unlock()
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	if r.Method == http.MethodGet {
		review := setup.ReviewMaintenance
		if p.maintenanceReview != nil {
			review = p.maintenanceReview
		}
		panelJSON(w, review(ctx))
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	request, ok := decodeSetupChoice(w, r)
	if !ok {
		return
	}
	if !request.Confirm || request.PlanID == "" || len(request.Selected) != 0 || len(request.Beside) != 0 || request.ComponentID != "" || request.ModelPlanID != "" || request.CodingModelPlanID != "" || request.PermissionsID != "" || request.Engine != "" {
		http.Error(w, "review and confirm opening the installed Setup", http.StatusBadRequest)
		return
	}
	open := setup.OpenMaintenance
	if p.maintenanceOpen != nil {
		open = p.maintenanceOpen
	}
	if err := open(ctx, request.PlanID); err != nil {
		http.Error(w, err.Error(), http.StatusConflict)
		return
	}
	// The normal installer owns every later choice and operation. No archive
	// journal or installed application status changes at this handoff.
	panelJSON(w, map[string]any{"opened": true, "message": "Setup opened. Review its app choices and apply the changes there. Installation has not been confirmed here."})
}
