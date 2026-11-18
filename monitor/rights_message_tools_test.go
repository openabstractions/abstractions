package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/grants"
	rwire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	rightsclient "github.com/openabstractions/abstraction-rights/go/client"
)

// messageToolsPolicy is the rights operator under the switches: one policy
// with a revision that changes on every applied edit.
type messageToolsPolicy struct {
	rules    []rightsclient.PolicyRule
	revision int
	edits    []string
}

func (m *messageToolsPolicy) rev() string { return "r" + string(rune('0'+m.revision)) }

func (m *messageToolsPolicy) ListPolicyContext(context.Context, string, int64) (rightsclient.PolicyPage, error) {
	return rightsclient.PolicyPage{Outcome: rightsclient.PolicyPageOutcomePage, Revision: m.rev(), Rules: append([]rightsclient.PolicyRule(nil), m.rules...), Complete: true}, nil
}

func (m *messageToolsPolicy) SetRuleContext(_ context.Context, expected string, rule rightsclient.PolicyRule) (rwire.PolicyEdit, error) {
	if expected != m.rev() {
		return rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeConflict, Revision: m.rev()}, nil
	}
	m.rules = append(m.rules, rule)
	m.revision++
	m.edits = append(m.edits, "set "+rule.Subject.Account+" "+rule.Subject.Program+" "+rule.Action+" "+rule.Resource)
	return rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeApplied, Revision: m.rev(), Current: &rule}, nil
}

func (m *messageToolsPolicy) RevokeRuleContext(_ context.Context, expected string, subject rightsclient.Subject, action, resource string) (rwire.PolicyEdit, error) {
	if expected != m.rev() {
		return rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeConflict, Revision: m.rev()}, nil
	}
	kept := m.rules[:0]
	for _, r := range m.rules {
		if r.Subject != subject || r.Action != action || r.Resource != resource {
			kept = append(kept, r)
		}
	}
	m.rules = kept
	m.revision++
	m.edits = append(m.edits, "revoke "+subject.Program+" "+resource)
	return rwire.PolicyEdit{Outcome: rwire.PolicyEditOutcomeApplied, Revision: m.rev()}, nil
}

// S5 ruling 2026-11-22: each agent pair has "Allow full tool use when
// <sender> messages <receiver>", off by default. Turning it on writes the
// receiver's own rule on the sender and an Activity line; so does turning it
// off. Only a request carrying the Panel's key reaches the switch.
func TestMessageToolsSwitchPerPairWritesReceiverRuleAndActivity(t *testing.T) {
	policy := &messageToolsPolicy{}
	operator, agents := messageToolsOperator, messageToolsAgents
	t.Cleanup(func() { messageToolsOperator, messageToolsAgents = operator, agents })
	messageToolsOperator = func(context.Context) (messageToolsRules, string, error) { return policy, "person", nil }
	integrations := filepath.Join(t.TempDir(), "OA", "agent-integrations")
	claude := messageToolsAgent{Name: "oa-claude-integration", Title: "Claude", Program: filepath.Join(integrations, "oa-claude-integration.exe")}
	codex := messageToolsAgent{Name: "oa-codex-integration", Title: "Codex", Program: filepath.Join(integrations, "oa-codex-integration.exe")}
	messageToolsAgents = func(context.Context, *servicePanel) []messageToolsAgent { return []messageToolsAgent{claude, codex} }
	p := &servicePanel{appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
	mux := http.NewServeMux()
	mux.HandleFunc("/rights/message-tools", guard("panel-key", p.rightsMessageTools))
	do := func(method, body string, key bool) (int, messageToolsView) {
		t.Helper()
		r := httptest.NewRequest(method, "/rights/message-tools", strings.NewReader(body))
		if key {
			r.Header.Set("X-Panel-Key", "panel-key")
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		var view messageToolsView
		if w.Code == http.StatusOK {
			if err := json.Unmarshal(w.Body.Bytes(), &view); err != nil {
				t.Fatal(err, w.Body.String())
			}
		}
		return w.Code, view
	}
	find := func(view messageToolsView, sender, receiver string) messageToolsPair {
		for _, pair := range view.Pairs {
			if pair.Sender == sender && pair.Receiver == receiver {
				return pair
			}
		}
		t.Fatal("no pair", sender, receiver, view.Pairs)
		return messageToolsPair{}
	}

	code, view := do(http.MethodGet, "", true)
	if code != http.StatusOK || view.Outcome != "page" || len(view.Pairs) != 4 {
		t.Fatal(code, view)
	}
	pair := find(view, "Codex", "Claude")
	if pair.On || pair.Label != "Allow full tool use when Codex messages Claude" {
		t.Fatal("the pair is not off by default with the ruling's words", pair)
	}

	// No key: an agent or a page elsewhere never reaches the switch.
	if code, _ := do(http.MethodPost, `{"revision":"r0","sender":"oa-codex-integration","receiver":"oa-claude-integration","on":true}`, false); code != http.StatusForbidden || len(policy.edits) != 0 {
		t.Fatal("a request without the Panel key changed the switch", code, policy.edits)
	}

	code, view = do(http.MethodPost, `{"revision":"r0","sender":"oa-codex-integration","receiver":"oa-claude-integration","on":true}`, true)
	if code != http.StatusOK || view.Outcome != "applied" || len(policy.edits) != 1 ||
		policy.edits[0] != "set person "+claude.Program+" "+grants.ApplicationMessageFullToolsAction+" app:oa-codex-integration" {
		t.Fatal("turning the switch on", code, view, policy.edits)
	}
	lines := p.appActivity.lines()
	if len(lines) != 1 || lines[0].What != "Allowed full tool use when Codex messages Claude" || lines[0].App != "Claude" || lines[0].Program != claude.Program {
		t.Fatal("turning it on wrote no Activity line", lines)
	}
	_, view = do(http.MethodGet, "", true)
	if !find(view, "Codex", "Claude").On || find(view, "Claude", "Codex").On || find(view, "Claude", "Claude").On {
		t.Fatal("the switch did not apply to exactly that pair", view.Pairs)
	}

	code, view = do(http.MethodPost, `{"revision":"`+view.Revision+`","sender":"oa-codex-integration","receiver":"oa-claude-integration","on":false}`, true)
	if code != http.StatusOK || view.Outcome != "applied" || len(policy.rules) != 0 {
		t.Fatal("turning the switch off", code, view, policy.rules)
	}
	if lines := p.appActivity.lines(); len(lines) != 2 || lines[1].What != "Turned off full tool use when Codex messages Claude" {
		t.Fatal("turning it off wrote no Activity line", lines)
	}

	// A stale revision changes nothing and writes no line.
	if _, view := do(http.MethodPost, `{"revision":"r0","sender":"oa-codex-integration","receiver":"oa-claude-integration","on":true}`, true); view.Outcome != "conflict" || len(p.appActivity.lines()) != 2 {
		t.Fatal("a conflicting write was recorded", view)
	}
	// An app that is not one of the person's agents is never named.
	if _, view := do(http.MethodPost, `{"revision":"r2","sender":"some-app","receiver":"oa-claude-integration","on":true}`, true); view.Outcome != "invalid" || len(policy.rules) != 0 {
		t.Fatal("a switch named an unknown sender", view)
	}
}
