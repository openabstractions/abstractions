package main

import (
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/grants"
	rights "github.com/openabstractions/abstraction-rights/go"
)

// panelDefaultsRuntime serves a rights policy with the runtime's five
// account-wide defaults and this test as its operator.
func panelDefaultsRuntime(t *testing.T, allow *atomic.Bool) *rights.DecisionPolicy {
	t.Helper()
	var outage atomic.Bool
	policy := panelRightsRuntime(t, allow, &outage)
	if err := policy.ConfigureDefaults(rightsSubject(exploreSelf()).Account, grants.RuntimeAccountDefaults(), nil); err != nil {
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
	if fresh.Outcome != "page" || len(fresh.Defaults) != 5 {
		t.Fatalf("defaults %+v", fresh)
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
