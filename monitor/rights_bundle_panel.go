package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/grants"
	identity "github.com/openabstractions/abstraction-identity"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// rightsAllow is one "Allow downloads for…" or "Allow inference for…" the
// person chose in the panel: a bundle for one program at the listed revision.
type rightsAllow struct {
	For         string   `json:"for"`
	Revision    string   `json:"revision"`
	Account     string   `json:"account"`
	Program     string   `json:"program"`
	Registries  []string `json:"registries"`
	Hosts       []string `json:"hosts"`
	Credentials []string `json:"credentials"`
	Why         string   `json:"why"`
}

// allowReply is the bundle's rules and what the rights service did with each.
type allowReply struct {
	Rules []grants.Rule `json:"rules"`
	grants.Result
}

// rightsBundle writes each exact rule of a bundle for the chosen program, one
// conditional edit at a time from the listed revision, through the resolved
// rights operator. The first rule that does not apply stops the bundle, and
// the reply names the rules that landed.
func (p *servicePanel) rightsBundle(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST required", 405)
		return
	}
	var allow rightsAllow
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&allow); err != nil || !rightsText(allow.Revision, 128) || !rightsText(allow.Account, 128) ||
		!rightsText(allow.Program, 4096) || !identity.ValidSubjectProgram(allow.Program) ||
		len(allow.Why) > 256 || strings.ContainsAny(allow.Why, "\r\n") {
		http.Error(w, "invalid allow: a bundle, the listed revision, an account and a clean absolute program path", 400)
		return
	}
	rules, err := grants.Rules(allow.For, grants.For{Registries: allow.Registries, Hosts: allow.Hosts, Credentials: allow.Credentials})
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if allow.Why == "" {
		allow.Why = grants.DefaultWhy(allow.For)
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	p.logAction(ctx, "rights.allow", map[string]string{"rights.revision": allow.Revision, "rights.for": allow.For})
	subject := rightsclient.Subject{Account: allow.Account, Program: allow.Program}
	result, err := grants.Write(ctx, operator, allow.Revision, subject, rules, 0*time.Second, allow.Why)
	if err != nil {
		panelError(w, errors.Join(errors.New("the outcome of "+result.Stopped.Action+" on "+result.Stopped.Resource+" is uncertain; list the policy before another edit"), err))
		return
	}
	panelJSON(w, allowReply{Rules: rules, Result: result})
}
