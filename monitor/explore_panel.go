package main

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-facade/go/client"
	"github.com/openabstractions/abstraction-facade/go/probe"
	identity "github.com/openabstractions/abstraction-identity"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The Explore section runs the probes of abstraction-facade/go/probe, the list
// `openabstractions probe` runs too, as this Panel, and reads the exact rights
// rule that decides each call through the rights operator. A probe that writes
// runs only with confirm=write, which the page sends after the person confirms.

// exploreRuleView is the exact rule on record for the chosen subject, or why
// the rights operator did not show it.
type exploreRuleView struct {
	Subject  probe.Subject  `json:"subject"`
	Action   string         `json:"action"`
	Resource string         `json:"resource"`
	Outcome  string         `json:"outcome"`
	Revision string         `json:"revision,omitempty"`
	Permit   *bool          `json:"permit,omitempty"`
	SetBy    *probe.Subject `json:"setBy,omitempty"`
	SetAt    string         `json:"setAt,omitempty"`
	Why      string         `json:"why,omitempty"`
	Expires  string         `json:"expires,omitempty"`
	Error    string         `json:"error,omitempty"`
}

var exploreLocal = facade.Requirements{Scope: facade.ScopeLocal}

func exploreSelf() probe.Subject {
	self := probe.Self()
	self.Program = exploreSelfProgram(self.Program)
	return self
}

// exploreConfirmWrite is the confirm value a probe that writes requires.
const exploreConfirmWrite = "write"

func rightsSubject(s probe.Subject) rightsclient.Subject {
	return rightsclient.Subject{Account: s.Account, Program: s.Program}
}

func readExploreRule(ctx context.Context, subject probe.Subject, rule probe.Rule) exploreRuleView {
	view := exploreRuleView{Subject: subject, Action: rule.Action, Resource: rule.Resource}
	operator, err := panelMachine().ResolveRightsOperator(ctx, exploreLocal)
	if err != nil {
		view.Outcome, view.Error = "error", err.Error()
		var resolution *client.ResolutionError
		if errors.As(err, &resolution) {
			view.Outcome = string(resolution.Status)
		}
		return view
	}
	read, err := operator.ReadRuleContext(ctx, rightsSubject(subject), rule.Action, rule.Resource)
	if err != nil {
		view.Outcome, view.Error = "error", err.Error()
		if code := probe.ServiceCode(err); code != "" {
			view.Outcome = code
		}
		return view
	}
	view.Outcome, view.Revision = read.Outcome.String(), read.Revision
	if read.Record != nil {
		permit := read.Record.Rule.Permit
		view.Permit = &permit
		view.SetBy = &probe.Subject{Account: read.Record.SetBy.Account, Program: read.Record.SetBy.Program}
		view.SetAt, view.Why, view.Expires = read.Record.SetAt, read.Record.Why, read.Record.Expires
	}
	return view
}

// explore lists the probes, or runs one as this Panel and reads the rule that
// decides it for the chosen subject (default: this Panel).
func (p *servicePanel) explore(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", 405)
		return
	}
	q := r.URL.Query()
	self := exploreSelf()
	capability, operation, argument := q.Get("capability"), q.Get("operation"), q.Get("argument")
	if capability == "" {
		panelJSON(w, struct {
			Self   probe.Subject `json:"self"`
			Probes []probe.Probe `json:"probes"`
		}{self, probe.List()})
		return
	}
	chosen, _, found := probe.Find(capability, operation)
	if operation == "" || !found {
		http.Error(w, "unknown probe", 400)
		return
	}
	if err := probe.CheckArgument(chosen, argument); err != nil || (argument != "" && !rightsText(argument, 1024)) {
		http.Error(w, "this probe takes "+strconv.Quote(chosen.Argument)+" as its argument", 400)
		return
	}
	if chosen.Writes && q.Get("confirm") != exploreConfirmWrite {
		http.Error(w, capability+" "+operation+" writes ("+chosen.Note+"); confirm it to call it", 400)
		return
	}
	subject := probe.Subject{Account: q.Get("account"), Program: q.Get("program")}
	if subject.Account == "" {
		subject.Account = self.Account
	}
	if subject.Program == "" {
		subject.Program = self.Program
	}
	if !rightsText(subject.Account, 128) || !rightsText(subject.Program, 4096) || !identity.ValidSubjectProgram(subject.Program) {
		http.Error(w, "invalid subject: an account and a clean absolute program path", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if chosen.Writes {
		p.logAction(ctx, "explore."+capability+"."+operation, nil)
	}
	result := probe.Run(ctx, panelMachine(), chosen, argument)
	result.Subject = self
	reply := struct {
		Result probe.Result     `json:"result"`
		Rule   *exploreRuleView `json:"rule,omitempty"`
	}{Result: result}
	if reply.Result.Rule != nil {
		view := readExploreRule(ctx, subject, *reply.Result.Rule)
		reply.Rule = &view
	}
	panelJSON(w, reply)
}
