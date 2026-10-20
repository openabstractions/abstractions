package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	facade "github.com/openabstractions/abstraction-facade/go"
	identity "github.com/openabstractions/abstraction-identity"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The Device page's wake list (VISION 2026-10-02: the person sees who holds
// wake and can allow or stop it per app). One row per app that holds the
// wake lease now or has a rule on it. The lease rows come from the resource
// table (RES-A1); the rules are abstraction.resource/hold on awake, read and
// written through the rights operator. Stop writes a deny rule: a revoke
// would let the runtime's provider sweep write its default permit again
// (serve permitCardRules keeps an existing rule, a deny included), and a
// deny ends the app's wake leases (RES-A1, RES-L7). Allow writes a permit
// for every app: abstraction.resource/hold asks nothing at first use
// (facade grants/definitions.go), so removing a deny would leave the app
// refused. Removing the rule, back to the default, is the Permissions page's
// Remove rule, which each row links to.
//
// Wake has one rule today. The display level is not its own rule yet
// (resource.thrift known_resources has no awake:display); a holder that also
// holds a card lease takes the platform request with the display (RES-A1),
// so its row says the display stays on.

// deviceRoutes registers the Device page's wake and graphics-driver reads.
func (p *servicePanel) deviceRoutes(mux *http.ServeMux, key string) {
	mux.HandleFunc("/card/wake", guard(key, p.cardWake))
	mux.HandleFunc("/card/faults", guard(key, p.cardFaults))
}

// wakeApp is one app the wake list names.
type wakeApp struct {
	Program string `json:"program"`
	Account string `json:"account,omitempty"`
	// Title is the program's Device name (programTitler.deviceName); empty
	// when none names it, and the page then reads its file stem in words.
	Title string `json:"title,omitempty"`
	// Own marks one of OpenAbstractions' own programs; the page shows them
	// as one row, "OpenAbstractions" (architect ruling 2026-11-09, second
	// block).
	Own     bool `json:"own,omitempty"`
	Holding bool `json:"holding"`
	// Display reports a holder that also holds a card lease, whose platform
	// request keeps the display on too (RES-A1).
	Display bool   `json:"display"`
	Since   string `json:"since,omitempty"`
	Why     string `json:"why,omitempty"`
	// Rule is "allowed", "stopped" or "none".
	Rule string `json:"rule"`
}

// wakeView is GET /card/wake. Outcome "unavailable" means the resource
// table could not be read. Rules is "read" when every wake rule was listed,
// else the rights outcome that kept them unread; the rows then name the
// holders alone.
type wakeView struct {
	Outcome string    `json:"outcome"`
	Apps    []wakeApp `json:"apps"`
	Rules   string    `json:"rules"`
	Reason  string    `json:"reason,omitempty"`
}

// wakeRulePages bounds the policy listing: 32 pages of 64 rules.
const wakeRulePages = 32

// wakeRules lists every rule on abstraction.resource/hold for awake. The
// word is "read", or the outcome that stopped the listing.
func wakeRules(ctx context.Context) ([]rightsclient.PolicyRule, string) {
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, "unavailable"
	}
	var out []rightsclient.PolicyRule
	cursor, revision, restarted := "", "", false
	for pages := 0; pages < wakeRulePages; pages++ {
		page, err := operator.ListPolicyContext(ctx, cursor, 64)
		if err != nil {
			return nil, "unavailable"
		}
		if page.Outcome == rwire.PolicyPageOutcomeGap && !restarted {
			out, cursor, revision, restarted = nil, "", "", true
			continue
		}
		if page.Outcome != rwire.PolicyPageOutcomePage {
			return nil, page.Outcome.String()
		}
		if revision != "" && page.Revision != revision && !restarted {
			out, cursor, revision, restarted = nil, "", "", true
			continue
		}
		revision = page.Revision
		for _, rule := range page.Rules {
			if rule.Action == rights.AwakeAction && rule.Resource == rights.AwakeResource {
				out = append(out, rule)
			}
		}
		if page.Complete {
			return out, "read"
		}
		cursor = page.Next
	}
	return nil, "unavailable"
}

// wakeHolders reads the awake lease rows and, for each, whether the same
// holder leases a card.
func wakeHolders(ctx context.Context) ([]cardAwakeHold, []cardHolder, error) {
	table, err := panelMachine().ResolveResourceTable(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return nil, nil, err
	}
	holds, err := awakeFromTable(ctx)
	if err != nil {
		return nil, nil, err
	}
	if len(holds) == 0 {
		return holds, nil, nil
	}
	list, err := table.ResourcesContext(ctx)
	if err != nil {
		return nil, nil, err
	}
	var cards []cardHolder
	for _, name := range list.Resources {
		if !strings.HasPrefix(name, "card:") {
			continue
		}
		state, err := table.HoldersContext(ctx, name, false)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range state.Holders {
			if row.Lease != "" {
				cards = append(cards, row)
			}
		}
	}
	return holds, cards, nil
}

// sameWakeSubject matches a lease row and a rule subject: the same account,
// when the row names one, and the same program file.
func sameWakeSubject(account, program, otherAccount, otherProgram string) bool {
	if account != "" && otherAccount != "" && account != otherAccount {
		return false
	}
	return identity.SameSubjectProgram(program, otherProgram)
}

// joinWake builds the rows: every holder, then every rule subject no holder
// matched. A holder with no rule reads as allowed by its lease.
func joinWake(holds []cardAwakeHold, cards []cardHolder, rules []rightsclient.PolicyRule) []wakeApp {
	apps := []wakeApp{}
	used := make([]bool, len(rules))
	for _, hold := range holds {
		merged := false
		for i := range apps {
			if apps[i].Holding && sameWakeSubject(apps[i].Account, apps[i].Program, hold.Account, hold.Program) {
				merged = true
				break
			}
		}
		if merged {
			continue
		}
		app := wakeApp{Program: hold.Program, Account: hold.Account, Holding: true, Since: hold.Since, Why: hold.Why, Rule: "none"}
		for _, card := range cards {
			if sameWakeSubject(hold.Account, hold.Program, card.Account, card.Program) {
				app.Display = true
				break
			}
		}
		for i, rule := range rules {
			if !used[i] && sameWakeSubject(hold.Account, hold.Program, rule.Subject.Account, rule.Subject.Program) {
				used[i] = true
				app.Rule = wakeRuleWord(rule.Permit)
				if app.Account == "" {
					app.Account = rule.Subject.Account
				}
			}
		}
		apps = append(apps, app)
	}
	for i, rule := range rules {
		if !used[i] {
			apps = append(apps, wakeApp{Program: rule.Subject.Program, Account: rule.Subject.Account, Rule: wakeRuleWord(rule.Permit)})
		}
	}
	return apps
}

func wakeRuleWord(permit bool) string {
	if permit {
		return "allowed"
	}
	return "stopped"
}

// wakeEdit is POST /card/wake: allow or stop one app.
type wakeEdit struct {
	Account string `json:"account"`
	Program string `json:"program"`
	Allow   bool   `json:"allow"`
}

// wakeEditResult names the edit's outcome: "applied", "unchanged" (the rule
// already said so) or the rights refusal word.
type wakeEditResult struct {
	Outcome string `json:"outcome"`
}

// wakeEditAttempts bounds retries after a policy revision conflict.
const wakeEditAttempts = 3

// setWakeRule writes the app's rule: a permit to allow, a deny to stop.
func setWakeRule(ctx context.Context, edit wakeEdit) (wakeEditResult, error) {
	operator, err := panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
	if err != nil {
		return wakeEditResult{}, err
	}
	subject := rightsclient.Subject{Account: edit.Account, Program: edit.Program}
	for attempt := 0; attempt < wakeEditAttempts; attempt++ {
		read, err := operator.ReadRuleContext(ctx, subject, rights.AwakeAction, rights.AwakeResource)
		if err != nil {
			return wakeEditResult{}, err
		}
		switch read.Outcome {
		case rwire.RuleReadOutcomeFound:
			if read.Record.Rule.Permit == edit.Allow {
				return wakeEditResult{Outcome: "unchanged"}, nil
			}
		case rwire.RuleReadOutcomeExpired, rwire.RuleReadOutcomeUnknown:
		default:
			return wakeEditResult{Outcome: read.Outcome.String()}, nil
		}
		result, err := operator.SetRuleContext(ctx, read.Revision, rightsclient.PolicyRule{Subject: subject,
			Action: rights.AwakeAction, Resource: rights.AwakeResource, Permit: edit.Allow})
		if err != nil {
			return wakeEditResult{}, err
		}
		if result.Outcome == rwire.PolicyEditOutcomeConflict {
			continue
		}
		return wakeEditResult{Outcome: result.Outcome.String()}, nil
	}
	return wakeEditResult{Outcome: "conflict"}, nil
}

// cardWake answers the wake list (GET) and writes one app's rule (POST).
func (p *servicePanel) cardWake(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
	case http.MethodPost:
		var edit wakeEdit
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<14))
		decoder.DisallowUnknownFields()
		var extra any
		if decoder.Decode(&edit) != nil || decoder.Decode(&extra) != io.EOF || !rightsText(edit.Account, 128) ||
			!rightsText(edit.Program, 4096) || !identity.ValidSubjectProgram(edit.Program) {
			http.Error(w, "invalid wake edit", http.StatusBadRequest)
			return
		}
		ctx, cancel := panelCall(r)
		defer cancel()
		p.logAction(ctx, "device.wake", map[string]string{"wake.allow": strconv.FormatBool(edit.Allow)})
		result, err := setWakeRule(ctx, edit)
		if err != nil {
			panelError(w, err)
			return
		}
		panelJSON(w, result)
		return
	default:
		http.Error(w, "GET or POST required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := panelCall(r)
	defer cancel()
	holds, cards, err := wakeHolders(ctx)
	if err != nil {
		panelJSON(w, wakeView{Outcome: "unavailable", Apps: []wakeApp{}, Rules: "unread", Reason: err.Error()})
		return
	}
	rules, word := wakeRules(ctx)
	view := wakeView{Outcome: "page", Apps: joinWake(holds, cards, rules), Rules: word}
	titles := p.programTitler(ctx)
	for i := range view.Apps {
		view.Apps[i].Title = titles.deviceName(view.Apps[i].Program)
		view.Apps[i].Own = titles.isOwn(view.Apps[i].Program)
	}
	panelJSON(w, view)
}
