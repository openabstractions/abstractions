package main

import (
	"os"
	"strings"
	"testing"
)

// R19 render check, 2026-11-09: with the runtime down each page shows the
// banner alone. The section sentences that only repeat it carry
// data-runtime-echo, which panel.css hides under data-runtime="down".
func TestRuntimeEchoesAreMarkedOnAppsModelsAndSettings(t *testing.T) {
	// Only lines that hold nothing but their read's outcome are hidden; a
	// line that also carries action results or verdicts writes its echo
	// through oaRuntimeEcho (runtime_down_page_test.js).
	marked := map[string][]string{
		"Apps":     {`id="apps-status"`, `id="apps-providers-status"`},
		"Models":   {`id="models-running-status"`},
		"Settings": {`id="about-runtime"`},
	}
	for _, id := range []string{"identity-status", "settings-status", "typed-settings-status", "selection-status", "models-local-status"} {
		for _, page := range []string{servicePage, inferencePage, settingsPage, identitySection, typedSettingsSection} {
			if strings.Contains(page, `id="`+id+`" data-runtime-echo`) || strings.Contains(page, `data-runtime-echo class="oa-status" id="`+id+`"`) {
				t.Errorf("%s is hidden as a whole; it also carries results", id)
			}
		}
	}
	for _, write := range []struct{ source, call string }{
		{identityScript, "oaRuntimeEcho('identity-status'"}, {settingsPage, "oaRuntimeEcho('settings-status'"}, {typedSettingsBody, "oaRuntimeEcho('typed-settings-status'"},
		{selectionPolicyScript, "oaRuntimeEcho('selection-status'"}, {libraryScript, "oaRuntimeEcho('models-local-status'"},
	} {
		if !strings.Contains(write.source, write.call) {
			t.Errorf("the echo sentence is not written through %s", write.call)
		}
	}
	pages := map[string]string{"Apps": servicePage, "Models": inferencePage, "Settings": settingsPage + identitySection + typedSettingsSection}
	for page, ids := range marked {
		html := pages[page]
		for _, id := range ids {
			at := strings.Index(html, id)
			if at < 0 {
				t.Fatalf("%s has no %s", page, id)
			}
			// The mark is on the element itself, or on the row holding it.
			open := strings.LastIndex(html[:at], "<")
			if id == `id="about-runtime"` {
				open = strings.LastIndex(html[:at], `<div class="oa-row"`)
				if row := strings.LastIndex(html[:at], `<div data-runtime-echo class="oa-row"`); row > open {
					open = row
				}
			}
			end := at + strings.Index(html[at:], ">")
			if !strings.Contains(html[open:end], "data-runtime-echo") {
				t.Errorf("%s: %s is not marked: %s", page, id, html[open:end+1])
			}
		}
	}
	css, err := os.ReadFile("static/panel.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), `html[data-runtime="down"] [data-runtime-echo] { display: none !important; }`) {
		t.Fatal("panel.css does not hide the echoes while the runtime is down")
	}
	if got := runtimeEcho(`<p class="oa-status" id="x"></p>`); got != `<p data-runtime-echo class="oa-status" id="x"></p>` {
		t.Fatalf("runtimeEcho = %s", got)
	}
}

// R19b render check, 2026-11-09: a section read only from the runtime is
// hidden whole while the runtime is down, so no heading stands over an
// emptied list. The mark follows the section's id, where help.go finds it.
func TestRuntimeGroupsAreHiddenWholeWhileTheRuntimeIsDown(t *testing.T) {
	for _, want := range []struct{ page, section string }{
		{inferencePage, `<section id="models-local" data-runtime-echo class="oa-group">`},
		{inferencePage, `<section id="models-running" data-runtime-echo class="oa-group">`},
		{inferencePage, `<section id="program-review-section" data-runtime-echo class="oa-group" hidden>`},
		{cardPage, `<section id="awake-section" data-runtime-echo class="oa-group">`},
		{typedSettingsSection, `<section data-runtime-echo class="oa-group"><h2>Preferences</h2>`},
		{messageActivitySection, `<section id="messages" data-runtime-echo class="oa-group">`},
		{workPage, `<section id="account-work" data-runtime-echo class="oa-group">`},
		{rightsPage, `<section id="questions" data-runtime-echo class="oa-group">`},
		{rightsPage, `<section id="defaults" data-runtime-echo class="oa-group">`},
		{rightsPage, `<section id="rights-advanced" data-runtime-echo class="oa-group">`},
		{registryPage, `<section id="remote-section" data-runtime-echo class="oa-group">`},
		{credentialsPage, `<section id="connect" data-runtime-echo class="oa-group">`},
		{credentialsPage, `<section id="saved" data-runtime-echo class="oa-group">`},
		// Journey 2026-11-10: these still showed their own errors, an empty
		// table and an Off gateway beside the banner.
		{rightsPage, `<section id="rights-section" data-runtime-echo class="oa-group">`},
		{rightsPage, `<section id="agent-messages" data-runtime-echo class="oa-group">`},
		{rightsPage, `<section id="assistant" data-runtime-echo class="oa-group">`},
		{rightsPage, `<section id="app-questions" data-runtime-echo class="oa-group">`},
		{registryPage, `<section id="hosts-section" data-runtime-echo class="oa-group">`},
		{registryPage, `<section id="gateway" data-runtime-echo class="oa-group">`},
		{registryPage, `<section id="keys-section" data-runtime-echo class="oa-group">`},
		{registryPage, `<section id="providers-section" data-runtime-echo class="oa-group">`},
		{machinesPage, `<section id="pair-connections" data-runtime-echo>`},
		{workPage, `<section id="timeline" data-runtime-echo class="oa-group">`},
		{workPage, `<section id="model-requests" data-runtime-echo class="oa-group">`},
		{workPage, `<section id="work" data-runtime-echo class="oa-group">`},
	} {
		if !strings.Contains(want.page, want.section) {
			t.Errorf("not hidden whole while the runtime is down: %s", want.section)
		}
	}
	if got := runtimeGroup(oaGroup("x", "X", "")); got != `<section id="x" data-runtime-echo class="oa-group"><h2>X</h2></section>` {
		t.Fatalf("runtimeGroup = %s", got)
	}
	// Use is a row only once its uses are read: no empty chooser.
	if !strings.Contains(selectionPolicySection, `<div id="models-profile-row" hidden><div class="oa-row">`) {
		t.Error("the Use row is shown before its uses are read")
	}
}
