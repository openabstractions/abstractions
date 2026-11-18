package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	inference "github.com/openabstractions/abstraction-inference/go"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The Activity timeline's Setup permissions (CONV-U1): each chat permission
// the setup step wrote (setup_step_chat.go) is one timeline row, told once
// while the rule stands, with Undo. The rights service owns the record: a
// rule this Panel set with Setup's provenance (RuleRecord.Why). Undo removes
// exactly that rule; the app then asks at its next chat, and the setup step
// does not write it again for the same answer.

// setupGrantRules is the rights operator surface the rows read and Undo
// changes.
type setupGrantRules interface {
	ListPolicyContext(ctx context.Context, cursor string, limit int64) (rightsclient.PolicyPage, error)
	ReadRuleContext(ctx context.Context, subject rightsclient.Subject, action, resource string) (rightsclient.RuleRead, error)
	RevokeRuleContext(ctx context.Context, expected string, subject rightsclient.Subject, action, resource string) (rightsclient.PolicyEdit, error)
}

// setupGrantOperator resolves the rights operator per request and this
// Panel's own subject, the setter of the rules Setup wrote. Tests replace
// it.
var setupGrantOperator = func(ctx context.Context) (setupGrantRules, rightsclient.Subject, error) {
	caller, err := panelMachine().ObserveCaller(ctx)
	if err != nil || caller.Outcome.String() != "observed" || caller.Account == "" || caller.Program == "" {
		return nil, rightsclient.Subject{}, errors.New("the Panel's own account could not be verified")
	}
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, rightsclient.Subject{}, err
	}
	return operator, rightsclient.Subject{Account: caller.Account, Program: caller.Program}, nil
}

// activityUndo names what a row's Undo reverses: the one rule a Setup
// permission's Undo removes, or the apps a Setup Keep line's Undo asks about
// again (setup_step_keep.go), with that line's time.
type activityUndo struct {
	Program  string   `json:"program,omitempty"`
	Action   string   `json:"action,omitempty"`
	Resource string   `json:"resource,omitempty"`
	At       int64    `json:"at,omitempty"`
	Apps     []string `json:"apps,omitempty"`
	// Line names a rights change line, with At its record time; its Undo is
	// read again from the runtime's own log (activity_rights_changes.go).
	Line string `json:"line,omitempty"`
}

// setupGrantsLimit bounds the rules one read lists and reads back.
const setupGrantsLimit = 8

// setupGrantRecords lists the standing chat permissions Setup wrote, with
// their records, and the policy revision they were read at: permits this
// Panel (self) set with Setup's provenance (setupWrote). A rule the person
// set, whatever its reason text, is none of them.
func setupGrantRecords(ctx context.Context, rules setupGrantRules, self rightsclient.Subject) ([]rightsclient.RuleRecord, string, error) {
	var found []rightsclient.RuleRecord
	cursor, revision := "", ""
	for pages := 0; pages < setupGrantsLimit; pages++ {
		page, err := rules.ListPolicyContext(ctx, cursor, activityPageSize)
		if err != nil {
			return nil, "", err
		}
		if page.Outcome != rightsclient.PolicyPageOutcomePage {
			return nil, "", errors.New("the permission list is " + page.Outcome.String())
		}
		revision = page.Revision
		for _, rule := range page.Rules {
			if !rule.Permit || rule.Action != inference.ActionComplete {
				continue
			}
			read, err := rules.ReadRuleContext(ctx, rule.Subject, rule.Action, rule.Resource)
			if err != nil {
				return nil, "", err
			}
			if read.Outcome == rightsclient.RuleReadOutcomeFound && setupWrote(read.Record, self) {
				found = append(found, *read.Record)
			}
		}
		if page.Complete || page.Next == "" {
			break
		}
		cursor = page.Next
	}
	return found, revision, nil
}

// activitySetupGrants answers GET with the standing Setup permissions as
// timeline rows, and POST {program, action, resource} with Undo of one.
func (p *servicePanel) activitySetupGrants(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := panelCall(r)
	defer cancel()
	rules, self, err := setupGrantOperator(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		records, _, err := setupGrantRecords(ctx, rules, self)
		if err != nil {
			panelError(w, err)
			return
		}
		out := activityPage{Outcome: "page", Events: []activityEvent{}, Complete: true}
		names := p.programTitler(ctx)
		for _, record := range records {
			at, _ := time.Parse(time.RFC3339Nano, record.SetAt)
			program := record.Rule.Subject.Program
			out.Events = append(out.Events, activityEvent{At: at.UnixMilli(), App: names.name(program), Program: program,
				What: "Setup let it use the coding model on this computer", Result: "Allowed",
				Undo: &activityUndo{Program: program, Action: record.Rule.Action, Resource: record.Rule.Resource}})
		}
		panelJSON(w, out)
	case http.MethodPost:
		var undo activityUndo
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<12)).Decode(&undo); err != nil || undo.Program == "" || undo.Action != inference.ActionComplete || undo.Resource == "" {
			http.Error(w, "choose one Setup permission to undo", http.StatusBadRequest)
			return
		}
		records, revision, err := setupGrantRecords(ctx, rules, self)
		if err != nil {
			panelError(w, err)
			return
		}
		for _, record := range records {
			rule := record.Rule
			if rule.Subject.Program != undo.Program || rule.Action != undo.Action || rule.Resource != undo.Resource {
				continue
			}
			edit, err := rules.RevokeRuleContext(ctx, revision, rule.Subject, rule.Action, rule.Resource)
			if err != nil {
				panelError(w, err)
				return
			}
			if edit.Outcome != rightsclient.PolicyEditOutcomeApplied {
				http.Error(w, "The permission changed while it was being removed. Try again.", http.StatusConflict)
				return
			}
			p.logAction(ctx, "setup.grant_undo", map[string]string{"rights.program": rule.Subject.Program, "rights.resource": rule.Resource})
			panelJSON(w, struct {
				Message string `json:"message"`
			}{"Undone: the app asks before its next chat."})
			return
		}
		http.Error(w, "This Setup permission is no longer in force.", http.StatusConflict)
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
	}
}
