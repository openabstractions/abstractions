package main

import (
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	credentials "github.com/openabstractions/abstraction-credentials/go"
	host "github.com/openabstractions/abstraction-facade/go/runtime"
	resourceservice "github.com/openabstractions/abstraction-resource/go/service"
)

// TestGrantEntriesNameExistingSections is requirement 1's guard: every
// grantEntry names a page and section id that actually exists in that page's
// fragment, using the same id pattern help_test.go checks against. A new
// entry that misspells an id, or a page rename that drops one, fails here
// instead of silently rendering no control.
func TestGrantEntriesNameExistingSections(t *testing.T) {
	for _, g := range grantEntries {
		fragment, ok := pageFragments[g.Page]
		if !ok {
			t.Errorf("grantEntry names unknown page %q", g.Page)
			continue
		}
		found := false
		for _, id := range pageSectionIDs(fragment) {
			if id == g.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("grantEntry for page %q section %q: no such section id in %s_page.go", g.Page, g.ID, g.Page)
		}
		if len(g.Rules) == 0 {
			t.Errorf("grantEntry for page %q section %q: no rules", g.Page, g.ID)
		}
		if g.Resource == "" {
			t.Errorf("grantEntry for page %q section %q: no default resource", g.Page, g.ID)
		}
		if len(g.Rerun) == 0 {
			t.Errorf("grantEntry for page %q section %q: no rerun button", g.Page, g.ID)
		}
	}
}

// TestInjectGrantControlsRendersWidget checks the shape requirement 1 asks
// for: a button rendered from the section's own data, right after the
// paragraph that already names the rules, carrying the exact rules and
// resource as data attributes.
func TestInjectGrantControlsRendersWidget(t *testing.T) {
	fragment := injectGrantControls("work", workPage)
	m := regexp.MustCompile(`<section id="account-work">.*?(<div class="oa-grant"[^>]*>.*?</div>).*?</section>`).FindStringSubmatch(fragment)
	if m == nil {
		t.Fatal("no oa-grant widget found inside the account-work section")
	}
	widget := m[1]
	if !strings.Contains(widget, `id="account-work-grant"`) {
		t.Errorf("widget carries no id: %s", widget)
	}
	wantRules := host.JobInventoryAction + "," + host.JobCancelAction
	if !strings.Contains(widget, `data-grant-rules="`+wantRules+`"`) {
		t.Errorf("widget rules = %s, want %s in %s", wantRules, wantRules, widget)
	}
	if !strings.Contains(widget, `data-grant-resource="`+host.JobResource+`"`) {
		t.Errorf("widget resource missing %s in %s", host.JobResource, widget)
	}
	if !strings.Contains(widget, "Grant these to this Panel") {
		t.Errorf("widget carries no button label: %s", widget)
	}
	// The bootstrap call names the section's own read button.
	if !strings.Contains(fragment, `oaGrant('account-work-grant','account-start')`) {
		t.Errorf("no bootstrap call for account-work-grant in fragment")
	}
}

// TestInjectGrantControlsBootstrapAfterOwnScript is the ordering guarantee
// monitor/*_page_test.js relies on: every bootstrap <script> lands after the
// page's own <script>...</script> block, so extracting the first script tag
// in a page's Go source still gets that page's own script untouched.
func TestInjectGrantControlsBootstrapAfterOwnScript(t *testing.T) {
	for _, page := range []struct {
		name     string
		fragment string
	}{
		{"status", servicePage},
		{"work", workPage},
		{"card", cardPage},
		{"credentials", credentialsPage},
		{"inference", inferencePage},
		{"registry", registryPage},
		{"rights", rightsPage},
		{"settings", settingsPage},
		{"logging", loggingPage},
		{"identity", identityPage},
		{"explore", explorePage},
	} {
		rendered := injectGrantControls(page.name, page.fragment)
		firstScript := strings.Index(rendered, "<script>")
		firstScriptClose := strings.Index(rendered, "</script>")
		if firstScript < 0 || firstScriptClose < 0 || firstScriptClose < firstScript {
			t.Fatalf("%s: page has no well-formed <script> block", page.name)
		}
		extracted := rendered[firstScript+len("<script>") : firstScriptClose]
		if strings.Contains(extracted, "oaGrant(") {
			t.Errorf("%s: the first <script>...</script> block (what monitor/*_page_test.js extracts) contains a grant bootstrap call: %s", page.name, extracted)
		}
	}
}

// TestPanelPageEmbedsGrantScriptOnce checks the shared control panelPage
// wires in: grantScript appears exactly once, and it precedes the page's own
// fragment so window.oaGrant exists before any bootstrap call runs.
func TestPanelPageEmbedsGrantScriptOnce(t *testing.T) {
	req := httptest.NewRequest("GET", "/work?k=test", nil)
	doc := panelPage(req, "test", "work", "OpenAbstractions work", workPage)
	if n := strings.Count(doc, "window.oaGrant=function"); n != 1 {
		t.Errorf("grantScript embedded %d times, want 1", n)
	}
	shellIdx := strings.Index(doc, "window.oaGrant=function")
	widgetIdx := strings.Index(doc, `id="account-work-grant"`)
	if shellIdx < 0 || widgetIdx < 0 || widgetIdx < shellIdx {
		t.Errorf("grantScript (at %d) must precede the account-work widget (at %d)", shellIdx, widgetIdx)
	}
}

// TestGrantResourceConstantsResolve pins the exact strings the widgets embed
// to the packages that define them, so a future rename in
// abstraction-resource or abstraction-credentials fails this test instead of
// silently drifting the Panel's own rule text.
func TestGrantResourceConstantsResolve(t *testing.T) {
	if resourceservice.ResourceAccount != credentials.ResourceAccount {
		t.Errorf("resourceservice.ResourceAccount = %q, credentials.ResourceAccount = %q; grantEntries assumes both name the same account resource", resourceservice.ResourceAccount, credentials.ResourceAccount)
	}
}
