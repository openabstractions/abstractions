package main

import (
	"context"
	"net/http"
	"slices"
	"time"

	asks "github.com/openabstractions/abstraction-asks/go"
	facade "github.com/openabstractions/abstraction-facade/go"
	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/probe"
	identity "github.com/openabstractions/abstraction-identity"
	iclient "github.com/openabstractions/abstraction-inference/go/client"
	rights "github.com/openabstractions/abstraction-rights/go"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// The app page's facts (GET /app/<name>/facts): what one registered app
// did and may do, filtered here, on the server, by program identity. A call,
// a rule or an answer belongs to the app when its program is the registered
// program by identity.SameSubjectProgram, so a different spelling or an 8.3
// short alias of the same file still matches. The page shows no program
// path and no raw host: a call names its model and its server's registered
// title, or "Model server".

// appCall is one model call the app made, as the page shows it.
type appCall struct {
	Model   string `json:"model"`
	Server  string `json:"server"`
	UnixMs  int64  `json:"unixMs"`
	Outcome string `json:"outcome"`
}

// appFacts is the reply. Each part carries its own outcome, so one service
// being unavailable leaves the others shown.
type appFacts struct {
	Outcome           string            `json:"outcome"`
	RulesOutcome      string            `json:"rulesOutcome"`
	Revision          string            `json:"revision,omitempty"`
	ActionPlain       map[string]string `json:"actionPlain"`
	Rules             []policyRuleView  `json:"rules"`
	CallsOutcome      string            `json:"callsOutcome"`
	Calls             []appCall         `json:"calls"`
	Assistant         *assistantEntry   `json:"assistant,omitempty"`
	AssistantRevision string            `json:"assistantRevision,omitempty"`
	// The account-wide defaults that let this app act today, as the runtime
	// explains its decision (rights RIGHTS-O14). ScriptHost: the runtime
	// keeps the defaults from this program. Subject is the rule a Block for
	// this app writes.
	DefaultsOutcome string                `json:"defaultsOutcome"`
	Defaults        []appDefault          `json:"defaults"`
	ScriptHost      bool                  `json:"scriptHost"`
	Subject         *rightsclient.Subject `json:"subject,omitempty"`
}

// appDefault is one default that applies to the app: its words and the
// exact action and resource a Block for this app denies.
type appDefault struct {
	Name     string `json:"name"`
	Label    string `json:"label"`
	Action   string `json:"action"`
	Resource string `json:"resource"`
}

// appDefaultsRead is the rights operator surface the defaults read. Tests
// replace appDefaultsReader.
type appDefaultsRead interface {
	ListDefaultsContext(ctx context.Context) (rwire.DefaultPage, error)
	ExplainForContext(ctx context.Context, subject rightsclient.Subject, action, resource string) (rwire.DecisionExplanation, error)
}

var appDefaultsReader = func(ctx context.Context) (appDefaultsRead, error) {
	return panelMachine().ResolveRightsOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// appInferenceRead is the inference operator surface the facts read. Tests
// replace appInferenceReader.
type appInferenceRead interface {
	Audit(ctx context.Context, cursor, maxEntries int64) (iclient.AuditPage, error)
	Hosts(ctx context.Context) (iclient.HostList, error)
}

var appInferenceReader = func(ctx context.Context) (appInferenceRead, error) {
	return panelMachine().ResolveInferenceOperator(ctx, facade.Requirements{Scope: facade.ScopeLocal})
}

// appAuditPages bounds the audit read: 32 pages of 64 entries.
const appAuditPages = 32

// sameProgram memoizes identity.SameSubjectProgram against one program, so
// a long audit compares each distinct spelling once.
type sameProgram struct {
	program string
	seen    map[string]bool
}

func (s *sameProgram) is(other string) bool {
	if other == "" {
		return false
	}
	if same, ok := s.seen[other]; ok {
		return same
	}
	same := identity.SameSubjectProgram(s.program, other)
	s.seen[other] = same
	return same
}

func (p *servicePanel) appFacts(w http.ResponseWriter, r *http.Request, name string) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET required", http.StatusMethodNotAllowed)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	apps, err := p.setupApplicationsForPanel(ctx)
	if err != nil {
		panelError(w, err)
		return
	}
	registry, err := apps.Observe(ctx, "", 0)
	if err != nil {
		panelError(w, err)
		return
	}
	var entry *fwire.ApplicationEntry
	for _, listed := range registry.Applications {
		if listed.Descriptor.Name == name && listed.Scope == fwire.ScopeLocal {
			found := listed
			entry = &found
		}
	}
	facts := appFacts{Outcome: "page", ActionPlain: rightsActionPlain, Rules: []policyRuleView{}, Calls: []appCall{}, Defaults: []appDefault{}}
	if entry == nil {
		facts.Outcome = "missing"
		panelJSON(w, facts)
		return
	}
	mine := &sameProgram{program: entry.Descriptor.Program, seen: map[string]bool{}}
	names := newProgramTitler(registry.Applications)
	p.appRuleFacts(ctx, mine, names, &facts)
	appDefaultFacts(ctx, *entry, &facts)
	p.appCallFacts(ctx, mine, registry, &facts)
	panelJSON(w, facts)
}

// appDefaultFacts lists the account-wide defaults that apply to the app as
// the runtime decides them. For each configured default it asks the rights
// operator to explain the app's decision on that default's target
// (RIGHTS-O14, ExplainFor): the same evaluation a decision makes, so a typed
// Block, the app's own rule under any spelling of its program and the switch
// all count as they do when the app acts. A default is listed only where the
// runtime says it is the step that permits; a default the runtime keeps from
// the app (the script-host exclusion) sets ScriptHost. An every-resource
// default is the app's for its own entry, "app:<name>", the resource the
// runtime decides it on (serve applications.go). With the app's rules unread
// the page has no revision for a Block, so no default is listed.
func appDefaultFacts(ctx context.Context, entry fwire.ApplicationEntry, facts *appFacts) {
	d := entry.Descriptor
	facts.Subject = appsRegistrationSubject(probe.Self().Account, d.Program)
	if facts.RulesOutcome != "page" || facts.Subject == nil {
		facts.DefaultsOutcome = "unavailable"
		return
	}
	reader, err := appDefaultsReader(ctx)
	if err != nil {
		facts.DefaultsOutcome = "unavailable"
		return
	}
	page, err := reader.ListDefaultsContext(ctx)
	switch {
	case selectorUnsupported(err):
		facts.DefaultsOutcome = "unsupported"
		return
	case err != nil:
		facts.DefaultsOutcome = "unavailable"
		return
	}
	facts.DefaultsOutcome = page.Outcome.String()
	if facts.DefaultsOutcome != "page" {
		return
	}
	var listed []appDefault
	for _, dflt := range page.Defaults {
		target := dflt.Resource
		if dflt.EveryResource {
			target = applicationResource(d.Name)
		}
		explained, err := reader.ExplainForContext(ctx, *facts.Subject, dflt.Action, target)
		switch {
		case selectorUnsupported(err):
			facts.DefaultsOutcome = "unsupported"
			return
		case err != nil:
			facts.DefaultsOutcome = "unavailable"
			return
		case explained.Source == nil:
			// A refusal (forbidden, unavailable, invalid) explains nothing.
			facts.DefaultsOutcome = explained.Outcome.String()
			return
		}
		switch *explained.Source {
		case rwire.DecisionSourceAccountDefault:
			if explained.Outcome == rwire.DecisionOutcomePermitted {
				listed = append(listed, appDefault{Name: dflt.Name, Label: presentDefault(dflt).Label, Action: dflt.Action, Resource: target})
			}
		case rwire.DecisionSourceDefaultExcluded:
			facts.ScriptHost = true
		}
	}
	facts.Defaults = append(facts.Defaults, listed...)
	if facts.ScriptHost {
		facts.DefaultsOutcome = "excluded"
	}
}

// appRuleFacts lists this account's rules for the app's program at one
// revision; the answering rule becomes Assistant instead of a row.
func (p *servicePanel) appRuleFacts(ctx context.Context, mine *sameProgram, names *programTitler, facts *appFacts) {
	account := probe.Self().Account
	rules := assistantRuleOperator()
	revision, all, outcome, err := listRules(ctx, rules)
	if err != nil {
		facts.RulesOutcome = "unavailable"
		return
	}
	facts.RulesOutcome = outcome
	if outcome != "page" {
		return
	}
	facts.Revision, facts.AssistantRevision = revision, revision
	for _, rule := range all {
		if rule.Subject.Account != account || !mine.is(rule.Subject.Program) {
			continue
		}
		row := policyRuleView{Subject: rule.Subject, Action: rule.Action, Resource: rule.Resource, Permit: rule.Permit}
		read, readErr := rules.ReadRuleContext(ctx, rule.Subject, rule.Action, rule.Resource)
		if readErr == nil && read.Record != nil {
			setBy := read.Record.SetBy
			row.SetBy, row.SetAt, row.Why, row.Expires = &setBy, read.Record.SetAt, read.Record.Why, read.Record.Expires
			row.Expired = read.Outcome.String() == "expired"
		}
		if rule.Action == asks.AnswerAction && rule.Resource == rights.MetaScopeApps {
			if rule.Permit && !row.Expired {
				facts.Assistant = &assistantEntry{Program: rule.Subject.Program, Name: names.name(rule.Subject.Program), Expires: row.Expires}
			}
			continue
		}
		facts.Rules = append(facts.Rules, row)
	}
}

// appCallFacts reads the model call audit forward from its oldest retained
// entry, keeping the app's calls newest first. A gap restarts at the oldest
// entry still kept.
func (p *servicePanel) appCallFacts(ctx context.Context, mine *sameProgram, registry fwire.ApplicationPage, facts *appFacts) {
	operator, err := appInferenceReader(ctx)
	if err != nil {
		facts.CallsOutcome = "unavailable"
		return
	}
	servers := appServerTitles(ctx, operator, registry)
	var cursor int64
	for range appAuditPages {
		page, err := operator.Audit(ctx, cursor, 64)
		if err != nil {
			facts.CallsOutcome = "unavailable"
			return
		}
		if page.Outcome == iclient.AuditOutcomeGap && page.Next != cursor {
			cursor, facts.Calls = page.Next, facts.Calls[:0]
			continue
		}
		if page.Outcome != iclient.AuditOutcomePage {
			facts.CallsOutcome = page.Outcome.String()
			return
		}
		for _, e := range page.Entries {
			if mine.is(e.Program) {
				server := servers[e.Host]
				if server == "" {
					server = "Model server"
				}
				facts.Calls = append(facts.Calls, appCall{Model: e.Model, Server: server, UnixMs: e.UnixMs, Outcome: e.Outcome})
			}
		}
		if page.AtEnd || page.Next == cursor {
			break
		}
		cursor = page.Next
	}
	facts.CallsOutcome = "page"
	slices.SortStableFunc(facts.Calls, func(a, b appCall) int {
		switch {
		case a.UnixMs > b.UnixMs:
			return -1
		case a.UnixMs < b.UnixMs:
			return 1
		}
		return 0
	})
}

// appServerTitles names each configured model server by the registered
// title of the app whose program declared it. A server no registered app
// declared has no title here, and the page calls it "Model server".
func appServerTitles(ctx context.Context, operator appInferenceRead, registry fwire.ApplicationPage) map[string]string {
	titles := map[string]string{}
	hosts, err := operator.Hosts(ctx)
	if err != nil || hosts.Outcome != iclient.ListOutcomePage {
		return titles
	}
	for _, host := range hosts.Hosts {
		if host.Entry.DeclaredBy == "" {
			continue
		}
		for _, listed := range registry.Applications {
			if listed.Scope == fwire.ScopeLocal && identity.SameSubjectProgram(listed.Descriptor.Program, host.Entry.DeclaredBy) {
				titles[host.Entry.Name] = appEntryTitle(listed)
				break
			}
		}
	}
	return titles
}
