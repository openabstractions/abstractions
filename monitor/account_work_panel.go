package main

import (
	"encoding/json"
	"net/http"
	"unicode/utf8"

	facade "github.com/openabstractions/abstraction-facade/go"
)

// accountWork lists every program's accepted work in this account, or records
// cancellation intent on one operation by its id, through
// abstraction.job/operator@1. The runtime decides each call by a rights rule:
// abstraction.job/inventory.read to list, abstraction.job/acceptance.cancel to
// cancel (JOB-A13). Typed outcomes are returned unchanged.
func (p *servicePanel) accountWork(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var cancel struct {
		Operation string `json:"operation"`
	}
	if r.Method == "POST" {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&cancel); err != nil || cancel.Operation == "" || len(cancel.Operation) > 128 || !utf8.ValidString(cancel.Operation) {
			http.Error(w, "invalid cancellation: name one operation id", 400)
			return
		}
	} else if cursor := r.URL.Query().Get("cursor"); len(cursor) > 128 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid inventory cursor", 400)
		return
	}
	ctx, done := panelCall(r)
	defer done()
	operator, err := panelMachine().ResolveJobOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	if cancel.Operation != "" {
		p.logAction(ctx, "cancel.operation", map[string]string{"operation.id": cancel.Operation})
		result, err := operator.CancelOperation(ctx, cancel.Operation)
		if err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, result)
		return
	}
	page, err := operator.ListAccountWork(ctx, r.URL.Query().Get("cursor"), 32)
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, page)
}
