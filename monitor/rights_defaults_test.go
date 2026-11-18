package main

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go"
	wire "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
)

// panelDefaultsRuntime serves a rights policy with the runtime's five
// account-wide defaults, the agent-messages switch, and this test as its
// operator.
func panelDefaultsRuntime(t *testing.T, allow *atomic.Bool) *rights.DecisionPolicy {
	t.Helper()
	var outage atomic.Bool
	policy := panelRightsRuntime(t, allow, &outage)
	defaults := append(grants.RuntimeAccountDefaults(), rights.AccountDefault{Name: agentMessagesDefault, Action: "abstraction.facade/application.message", EveryResource: true, Off: true, Covers: func(context.Context, wire.Subject, string) bool { return false }})
	if err := policy.ConfigureDefaults(rightsSubject(exploreSelf()).Account, defaults, nil); err != nil {
		t.Fatal(err)
	}
	return policy
}

// rights RIGHTS-O13: the Panel lists every default with its switch, turns one
// at the listed revision, conflicts on a stale one, and reports the
// operator's refusal.
func TestServicePanelDefaultsListAndTurnASwitch(t *testing.T) {
	own(t)
	var allow atomic.Bool
	panelDefaultsRuntime(t, &allow)
	h := (&servicePanel{}).handler("test-key")
	list := func() defaultsView {
		r := panelRequest(t, h, "/rights/defaults", nil)
		return decodePanel[defaultsView](t, r.Code, r.Body.Bytes())
	}
	if v := list(); v.Outcome != "forbidden" || v.Revision != "" || len(v.Defaults) != 0 {
		t.Fatalf("unauthorized defaults %+v", v)
	}
	allow.Store(true)
	fresh := list()
	if fresh.Outcome != "page" || len(fresh.Defaults) != 5 || fresh.Agents == nil || fresh.Agents.On || fresh.Agents.Label != defaultLabels[agentMessagesDefault] {
		t.Fatalf("defaults %+v", fresh)
	}
	for _, d := range fresh.Defaults {
		if d.Name == agentMessagesDefault {
			t.Fatal("the agents switch is listed among the defaults for all apps")
		}
	}
	for _, d := range fresh.Defaults {
		if !d.On || d.SetBy != nil || d.Label != defaultLabels[d.Name] || d.Label == "" {
			t.Fatalf("fresh default %+v", d)
		}
	}
	r := panelRequest(t, h, "/rights/defaults", defaultEdit{Revision: fresh.Revision, Name: "download", On: false})
	off := decodePanel[defaultsView](t, r.Code, r.Body.Bytes())
	if off.Outcome != "applied" || off.Current == nil || off.Current.On || off.Current.SetBy == nil || off.Revision == fresh.Revision {
		t.Fatalf("turn off %+v", off)
	}
	for _, d := range list().Defaults {
		if d.On == (d.Name == "download") {
			t.Fatalf("after turning download off: %+v", d)
		}
	}
	r = panelRequest(t, h, "/rights/defaults", defaultEdit{Revision: fresh.Revision, Name: "download", On: true})
	if stale := decodePanel[defaultsView](t, r.Code, r.Body.Bytes()); stale.Outcome != "conflict" || stale.Current == nil || stale.Current.On {
		t.Fatalf("stale edit %+v", stale)
	}
	if r = panelRequest(t, h, "/rights/defaults", map[string]any{"revision": off.Revision, "name": "download", "on": true, "extra": 1}); r.Code != 400 {
		t.Fatalf("unknown field accepted: %d", r.Code)
	}
}

// A switch turned off and on again within the first write's save keeps the
// second choice: the switch stays usable, the second write follows the first
// and the saved state ends on (panel audit 2026-11-01: "Send you
// notifications" off then on within about three seconds lost the second
// change).
func TestRightsPageDefaultsQuickSecondChangeIsSaved(t *testing.T) {
	own(t)
	var allow atomic.Bool
	allow.Store(true)
	panelDefaultsRuntime(t, &allow)
	report := runRightsPageMode(t, "defaults-twice")
	for _, row := range report.Defaults {
		if row.Disabled {
			t.Fatalf("a switch is unusable while it saves: %+v", report.Defaults)
		}
	}
	for _, row := range report.DefaultsAfter {
		if !row.On || row.Disabled {
			t.Fatalf("after off then on: %+v (status %q)", report.DefaultsAfter, report.DefaultsAfterStatus)
		}
	}
	if report.DefaultsAfterStatus != "Every app may now download in the background." {
		t.Fatalf("status after off then on: %q", report.DefaultsAfterStatus)
	}
	h := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, h, "/rights/defaults", nil)
	for _, d := range decodePanel[defaultsView](t, r.Code, r.Body.Bytes()).Defaults {
		if !d.On {
			t.Fatalf("saved default %+v is off after off then on", d)
		}
	}
}

// The Permissions page's "Defaults for all apps" group shows each switch's
// state on arrival and writes one sentence when one is turned.
func TestRightsPageDefaultsGroupTurnsASwitch(t *testing.T) {
	own(t)
	var allow atomic.Bool
	allow.Store(true)
	panelDefaultsRuntime(t, &allow)
	report := runRightsPageMode(t, "defaults")
	if len(report.Defaults) != 5 {
		t.Fatalf("defaults shown %+v; status %q", report.Defaults, report.DefaultsStatus)
	}
	for _, row := range report.Defaults {
		if !row.On || row.Disabled || len(row.Lines) == 0 || strings.Contains(row.Lines[0], "abstraction.") {
			t.Fatalf("arrival row %+v", row)
		}
	}
	if report.DefaultsAfterStatus != "No app may download in the background unless you allowed that app." {
		t.Fatalf("status after turning off: %q", report.DefaultsAfterStatus)
	}
	for _, row := range report.DefaultsAfter {
		if row.On == (row.Lines[0] == "Download in the background") {
			t.Fatalf("after turning download off: %+v", report.DefaultsAfter)
		}
	}
}

// The agents switch (Permissions › Messages between agents) is the
// agent-messages default: off on a fresh install, turned only by the operator
// Panel, and listed apart from the defaults for all apps.
func TestServicePanelAgentsSwitchTurnsTheAgentMessagesDefault(t *testing.T) {
	own(t)
	var allow atomic.Bool
	panelDefaultsRuntime(t, &allow)
	h := (&servicePanel{}).handler("test-key")
	list := func() defaultsView {
		r := panelRequest(t, h, "/rights/defaults", nil)
		return decodePanel[defaultsView](t, r.Code, r.Body.Bytes())
	}
	if v := list(); v.Outcome != "forbidden" || v.Agents != nil {
		t.Fatalf("a non-operator read the agents switch %+v", v)
	}
	allow.Store(true)
	fresh := list()
	if fresh.Agents == nil || fresh.Agents.On {
		t.Fatalf("fresh agents switch %+v", fresh.Agents)
	}
	allow.Store(false)
	r := panelRequest(t, h, "/rights/defaults", defaultEdit{Revision: fresh.Revision, Name: agentMessagesDefault, On: true})
	if refused := decodePanel[defaultsView](t, r.Code, r.Body.Bytes()); refused.Outcome != "forbidden" {
		t.Fatalf("a non-operator turned the agents switch %+v", refused)
	}
	allow.Store(true)
	r = panelRequest(t, h, "/rights/defaults", defaultEdit{Revision: fresh.Revision, Name: agentMessagesDefault, On: true})
	on := decodePanel[defaultsView](t, r.Code, r.Body.Bytes())
	if on.Outcome != "applied" || on.Current == nil || !on.Current.On || on.Current.SetBy == nil {
		t.Fatalf("turn on %+v", on)
	}
	if after := list(); after.Agents == nil || !after.Agents.On || len(after.Defaults) != 5 {
		t.Fatalf("after turning on %+v", after)
	}
}

// AB-1w Panel ruling (2026-11-09): the agents setting is its own Permissions
// group, "Messages between agents", with one "Ask me first" switch; Apps ›
// Connections holds only its navigation rows.
func TestAgentMessagesSwitchLivesOnThePermissionsPage(t *testing.T) {
	if strings.Contains(servicePage, "agent-messages") || strings.Contains(servicePage, `class="switch"`) {
		t.Fatal("Apps still carries the agents switch")
	}
	// No On or Off shows before the first read: the switch is hidden and its
	// status line loading.
	group := runtimeGroup(oaGroup("agent-messages", "Messages between agents", agentMessagesCaption, strings.Replace(oaRow("Ask me first", "", `<input type="checkbox" class="switch" id="agent-messages-ask" aria-label="Ask before an agent messages another" disabled hidden>`, "agent-messages-status"), oaStatusLine("agent-messages-status", false), oaStatus("agent-messages-status"), 1)))
	if !strings.Contains(group, `data-state="loading"`) {
		t.Fatal("the agents status line does not start loading")
	}
	if !strings.Contains(rightsPage, group) || agentMessagesCaption != "Your agents can send each other messages and questions." {
		t.Fatal("the Permissions page lacks the Messages between agents group with its ruled strings")
	}
	if strings.Index(rightsPage, `id="defaults"`) > strings.Index(rightsPage, `id="agent-messages"`) || strings.Index(rightsPage, `id="agent-messages"`) > strings.Index(rightsPage, `id="assistant"`) {
		t.Fatal("Messages between agents is not right after Defaults for all apps")
	}
	if _, ok := helpURL("rights", "agent-messages"); !ok {
		t.Fatal("Messages between agents has no Learn more")
	}
}

// The Permissions page shows "Ask me first" on after a fresh install, turns
// the agent-messages default with the inverted value and says what changed;
// a non-operator Panel hides the switch, says why and turns nothing.
func TestRightsPageAgentsSwitchAsksFirst(t *testing.T) {
	own(t)
	var allow atomic.Bool
	allow.Store(true)
	panelDefaultsRuntime(t, &allow)
	report := runRightsPageMode(t, "agents")
	if len(report.Agents) != 3 {
		t.Fatalf("agents switch %+v", report.Agents)
	}
	if a := report.Agents[0]; !a.On || a.Disabled || a.Hidden || a.Status != "" {
		t.Fatalf("arrival %+v", a)
	}
	if a := report.Agents[1]; a.On || a.Disabled || a.Status != "Your agents now message each other without asking you." {
		t.Fatalf("after Ask me first off %+v", a)
	}
	if s := report.AgentsSaved; s == nil || !s.On || s.SetBy == nil {
		t.Fatalf("saved default after Ask me first off %+v", s)
	}
	if a := report.Agents[2]; !a.On || a.Status != "Your agents now ask you before messaging each other." {
		t.Fatalf("after Ask me first on %+v", a)
	}
	h := (&servicePanel{}).handler("test-key")
	r := panelRequest(t, h, "/rights/defaults", nil)
	if v := decodePanel[defaultsView](t, r.Code, r.Body.Bytes()); v.Agents == nil || v.Agents.On {
		t.Fatalf("saved default after Ask me first on %+v", v.Agents)
	}
	allow.Store(false)
	refused := runRightsPageMode(t, "agents")
	if len(refused.Agents) != 1 || !refused.Agents[0].Hidden || refused.Agents[0].Status == "" {
		t.Fatalf("non-operator agents switch %+v", refused.Agents)
	}
}
