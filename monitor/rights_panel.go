package main

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	facade "github.com/openabstractions/abstraction-facade/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	identity "github.com/openabstractions/abstraction-identity"
	inference "github.com/openabstractions/abstraction-inference/go"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
	routerservice "github.com/openabstractions/abstraction-router/go/service"
)

// rightsActionPlain names every rights action a plain phrase, for the Status
// page's "Show rules" list and its second list of actions no rule names yet
// (task 2026-09-23, third first-time-visitor walkthrough: the list otherwise
// prints raw action names like "abstraction.credentials/holder.manage"). It
// covers every action installationRules (serve/runtime_rights.go) writes,
// worded as capability then action in the decided words (RENAME-PLAN §3,
// Panel step 7: S11 and D11, so "inference server manage" and "router
// servers" where the wire still says host), and every action serve/runtime_*.go registers into
// o.RightsActions beyond those (rights_catalogue_test.go enumerates that
// full catalogue and fails when this map falls behind it, and fails when a
// page's <span title="action">phrase</span> says anything else). An action
// missing here still shows a phrase, oaActionFallback's generic one
// (panel_shell.go), with the raw id in parentheses; the raw name is also
// always carried as a title attribute, so nothing this runtime knows is
// ever shown as only an id.
var rightsActionPlain = map[string]string{
	host.ConfigEditAction:                   "config rewrite",
	host.LogHistoryAction:                   "logging history",
	host.JobSubmitAction:                    "jobs submit",
	host.JobCancelAction:                    "jobs cancel",
	host.JobInventoryAction:                 "jobs all",
	host.ModelLookupAction:                  "model resolve",
	routerservice.ActionInventory:           "router servers",
	routerservice.ActionRoute:               "router pick",
	host.ResourceTableReadAction:            "resource table read",
	resourceservice.ActionHold:              "resource hold",
	resourceservice.ActionYield:             "resource yield",
	credentials.ActionManage:                "credentials manage",
	credentials.ActionRead:                  "credentials list",
	inference.ActionHostManage:              "inference server manage",
	inference.ActionKeyIssue:                "inference key issue",
	inference.ActionAuditRead:               "inference audit read",
	inference.ActionComplete:                "inference complete",
	"abstraction.facade/application.manage": "application manage",
	"abstraction.facade/provider.manage":    "provider manage",
	// abstraction.credentials/apply, abstraction.storage/inventory.provide,
	// the three actions below (task 2026-09-23, sixth first-time visitor,
	// finding 2) and every abstraction.facade/application.* and
	// abstraction.storage/* action below it are named by their exact string
	// for the same reason as the two facade actions above: each is declared
	// in serve, a main package the monitor package cannot import
	// (credentials.go's setApplyRule, provider_inventory.go's
	// ActionInventoryProvide, applications.go's ActionApplication*,
	// runtime_inventory.go's ActionInventoryRead, runtime_lending.go's
	// ActionLend). rights_catalogue_test.go's servePackageActions repeats
	// this same list, in sync by hand for the same reason.
	"abstraction.credentials/apply":           "credentials apply",
	"abstraction.storage/inventory.provide":   "storage inventory provide",
	"abstraction.storage/inventory.read":      "storage inventory read",
	"abstraction.storage/lend":                "storage lend",
	"abstraction.facade/application.read":     "application read",
	"abstraction.facade/application.announce": "application announce",
	"abstraction.facade/application.activate": "application activate",
	// The four actions below are abstraction-rights/go's own generic
	// catalogue (rwire.ResourceActions, rights_catalogue_test.go), imported
	// there rather than named by a per-capability constant: no capability
	// package here exports these four as named constants of its own.
	"abstraction.storage/content.read":    "storage content read",
	"abstraction.storage/content.write":   "storage content write",
	"abstraction.storage/content.observe": "storage content observe",
	"abstraction.storage/content.remove":  "storage content remove",
	// abstraction.asks/question.ask (abstraction-asks/go's own
	// ResourceActions) has no named Go constant of its own either.
	"abstraction.asks/question.ask": "questions ask",
}

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

// policyRuleView is one listed rule plus who set it, read individually
// through the same operator: ListPolicyContext's bulk page carries no
// provenance of its own (rightsclient.PolicyRule has no SetBy), while
// ReadRuleContext's single-rule record does. Field names match
// rightsclient.PolicyRule/PolicyPage's own (untagged, so their Go field name
// is the wire name) so the existing list rendering keeps working unchanged.
type policyRuleView struct {
	Subject  rightsclient.Subject
	Action   string
	Resource string
	Permit   bool
	SetBy    *rightsclient.Subject `json:"SetBy,omitempty"`
	SetAt    string                `json:"SetAt,omitempty"`
	Why      string                `json:"Why,omitempty"`
}

type policyPageView struct {
	Outcome  string
	Revision string
	Catalog  []string
	// ActionPlain names known actions in plain words (rightsActionPlain),
	// sent alongside Catalog and Rules so the page never has to guess a
	// phrase for a raw action name it did not itself invent.
	ActionPlain map[string]string
	Rules       []policyRuleView
	Next        string
	Complete    bool
}

// presentPolicyPage enriches a listed page with who set each rule (task
// 2026-09-23, "Show rules": each rule renders as "<plain phrase>: allowed for
// <program>, set at installation"). One extra read per listed rule, bounded to
// the page's own limit (at most 16 here), over the same resolved operator.
func presentPolicyPage(ctx context.Context, operator *rightsclient.Operator, page rightsclient.PolicyPage) policyPageView {
	view := policyPageView{Outcome: page.Outcome.String(), Revision: page.Revision, Catalog: page.Catalog, ActionPlain: rightsActionPlain, Next: page.Next, Complete: page.Complete, Rules: []policyRuleView{}}
	for _, rule := range page.Rules {
		row := policyRuleView{Subject: rule.Subject, Action: rule.Action, Resource: rule.Resource, Permit: rule.Permit}
		if read, err := operator.ReadRuleContext(ctx, rule.Subject, rule.Action, rule.Resource); err == nil && read.Record != nil {
			setBy := read.Record.SetBy
			row.SetBy, row.SetAt, row.Why = &setBy, read.Record.SetAt, read.Record.Why
		}
		view.Rules = append(view.Rules, row)
	}
	return view
}

func (e rightsEdit) valid() bool {
	target := rightsText(e.Revision, 128) && rightsText(e.Account, 128) && rightsText(e.Program, 4096) &&
		identity.ValidSubjectProgram(e.Program) && rightsText(e.Action, 128) && rightsText(e.Resource, 1024)
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
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		panelError(w, err)
		return
	}
	if edit.Edit != "" {
		p.logAction(ctx, "rights."+edit.Edit, map[string]string{"rights.revision": edit.Revision, "rights.action": edit.Action})
	}
	subject := rightsclient.Subject{Account: edit.Account, Program: edit.Program}
	if edit.Edit == "" {
		page, err := operator.ListPolicyContext(ctx, r.URL.Query().Get("cursor"), 16)
		if err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, presentPolicyPage(ctx, operator, page))
		return
	}
	var result any
	switch edit.Edit {
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
