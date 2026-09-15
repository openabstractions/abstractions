package main

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"

	facade "github.com/openabstractions/abstraction-facade/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// rightsEdit is one conditional policy change the person made in the panel.
// Account and program name the administrative target; they prove nothing about
// the panel or the target program.
type rightsEdit struct {
	Edit     string `json:"edit"`
	Revision string `json:"revision"`
	Account  string `json:"account"`
	Program  string `json:"program"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
	Permit   bool   `json:"permit"`
}

func rightsText(s string, max int) bool {
	return len(s) > 0 && len(s) <= max && utf8.ValidString(s) && strings.IndexFunc(s, unicode.IsControl) < 0
}

func (e rightsEdit) valid() bool {
	target := rightsText(e.Revision, 128) && rightsText(e.Account, 128) && rightsText(e.Program, 4096) &&
		filepath.IsAbs(e.Program) && filepath.Clean(e.Program) == e.Program && rightsText(e.Action, 128) && rightsText(e.Resource, 1024)
	switch e.Edit {
	case "set":
		return target
	case "revoke":
		return target && !e.Permit
	}
	return false
}

// rights lists the decision policy and applies conditional grants, denies and
// revocations through the resolved operator service. The service's operator
// authorization decides whether this panel may act; typed outcomes are returned
// unchanged. Resolution and transport failures report unavailability.
func (p *servicePanel) rights(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" && r.Method != "POST" {
		http.Error(w, "GET or POST required", 405)
		return
	}
	var edit rightsEdit
	if r.Method == "POST" {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&edit); err != nil || !edit.valid() {
			http.Error(w, "invalid rights edit", 400)
			return
		}
	} else if cursor := r.URL.Query().Get("cursor"); len(cursor) > 256 || !utf8.ValidString(cursor) {
		http.Error(w, "invalid rights cursor", 400)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: "local"})
	if err != nil {
		panelError(w, err)
		return
	}
	subject := rightsclient.Subject{Account: edit.Account, Program: edit.Program}
	var result any
	switch edit.Edit {
	case "":
		result, err = operator.ListPolicyContext(ctx, r.URL.Query().Get("cursor"), 16)
	case "set":
		result, err = operator.SetRuleContext(ctx, edit.Revision, rightsclient.PolicyRule{Subject: subject, Action: edit.Action, Resource: edit.Resource, Permit: edit.Permit})
	case "revoke":
		result, err = operator.RevokeRuleContext(ctx, edit.Revision, subject, edit.Action, edit.Resource)
	}
	if err != nil {
		panelError(w, err)
		return
	}
	panelJSON(w, result)
}
