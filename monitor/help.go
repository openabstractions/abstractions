package main

import "regexp"

// helpSite is the published site this Panel links out to. Every anchor below
// resolves on the site under openabstractions-flat/openabstractions.github.io
// in this tree; help_test.go parses that tree's HTML to prove each anchor
// still exists there.
const helpSite = "https://openabstractions.github.io/"

// helpEntry names the reference anchor documenting one Panel section: Page
// matches the "active" key panelPage already receives (status, card,
// inference, registry, credentials) and ID matches the section's element id
// inside that page's fragment (service_page.go, card_page.go, and so on).
type helpEntry struct {
	Page string
	ID   string
	URL  string
}

// helpAnchors is the anchor map (task 2026-09-22: every Panel section gets a
// help link to a precise anchor on the website). Order is display order
// within each page; the first entry for a page is also that page's anchor
// for the macOS/Linux floating "?" button (help.go's helpPageURL).
//
// Every anchor lives on reference.html, the developer reference; a Panel
// section that is not itself a generated schema page gets its own heading
// there ("The Panel, section by section") rather than being folded into the
// schema index, which documents generated contract pages specifically.
// Credentials used to point at adopt.html, the page written for people who
// use applications, while every other Panel page's Learn-more pointed at
// reference.html; the mismatch was never a deliberate split (task
// 2026-09-23, fourth first-time visitor, finding 3), so Credentials now
// anchors on reference.html like the rest.
//
// Work, Questions and rights, Settings, Logging, Identity and Explore left
// the Status page for their own pages (requirement 1); their anchor ids on
// reference.html kept the old "panel-status-…" prefix until task 2026-09-23,
// fifth first-time visitor, finding 4, so they now read "panel-<page>-…"
// like every other page's. reference.html keeps each old id as an empty
// <span> alias right before its heading, so a link to the old anchor still
// lands on the same section.
var helpAnchors = []helpEntry{
	{"status", "service-check", helpSite + "reference.html#panel-status-service-check"},
	{"work", "work", helpSite + "reference.html#panel-work-work"},
	{"work", "account-work", helpSite + "reference.html#panel-work-account-work"},
	{"work", "download", helpSite + "reference.html#panel-work-download"},
	{"rights", "questions", helpSite + "reference.html#panel-rights-questions"},
	{"rights", "rights-section", helpSite + "reference.html#panel-rights-permissions"},
	{"settings", "settings", helpSite + "reference.html#panel-settings-settings"},
	{"logging", "logging", helpSite + "reference.html#panel-logging-logging"},
	{"identity", "identity", helpSite + "reference.html#panel-identity-identity"},
	{"explore", "explore", helpSite + "reference.html#panel-explore-explore"},

	{"card", "holders", helpSite + "reference.html#panel-card-holders"},
	{"card", "downloads", helpSite + "reference.html#panel-card-downloads"},
	{"card", "awake-section", helpSite + "reference.html#panel-card-awake"},

	{"inference", "hosts-section", helpSite + "reference.html#panel-inference-servers"},
	{"inference", "gateway", helpSite + "reference.html#panel-inference-gateway"},
	{"inference", "keys-section", helpSite + "reference.html#panel-inference-keys"},
	{"inference", "audit-section", helpSite + "reference.html#panel-inference-audit"},

	{"registry", "hosts-section", helpSite + "reference.html#panel-providers-servers"},
	{"registry", "providers-section", helpSite + "reference.html#panel-providers-registered"},
	{"registry", "remote-section", helpSite + "reference.html#panel-providers-remote"},

	{"credentials", "saved", helpSite + "reference.html#panel-credentials-saved"},
	{"credentials", "new", helpSite + "reference.html#panel-credentials-add"},
	{"credentials", "rotate", helpSite + "reference.html#panel-credentials-rotate"},
	{"credentials", "revoke", helpSite + "reference.html#panel-credentials-revoke"},
	{"credentials", "access", helpSite + "reference.html#panel-credentials-access"},
}

// helpRetiredAnchors are reference.html ids an older Panel build links to.
// The site keeps each as an empty <span id> before the section's current
// heading, so those links still land (RENAME-PLAN §3, Panel and command line
// step 14). help_test.go proves every one still resolves.
var helpRetiredAnchors = []string{
	"panel-status-work", "panel-status-account-work", "panel-status-download",
	"panel-status-questions", "panel-status-rights", "panel-status-settings",
	"panel-status-logging", "panel-status-identity", "panel-status-explore",
	"panel-rights-rights", "panel-inference-hosts", "panel-registry-hosts",
	"panel-registry-providers", "panel-registry-remote", "panel-credentials-new",
}

// helpURL reports the anchor for one page's section, and whether the map
// carries one.
func helpURL(page, id string) (string, bool) {
	for _, e := range helpAnchors {
		if e.Page == page && e.ID == id {
			return e.URL, true
		}
	}
	return "", false
}

// helpPageURL reports the page-level anchor: the first section listed for
// that page, used by the macOS/Linux floating "?" button.
func helpPageURL(page string) (string, bool) {
	for _, e := range helpAnchors {
		if e.Page == page {
			return e.URL, true
		}
	}
	return "", false
}

// helpMark is the small "?" placed at the right of a section heading on
// macOS and Linux (hidden on Windows by static/panel.css). helpLearnMore is
// the "Learn more" text link at the right of the same heading on Windows
// (hidden on macOS and Linux). Both carry target="_blank"
// rel="noopener" so the click opens the system browser and leaves the Panel
// window in place (desktop_windows.go, macos/PanelLauncher.swift).
func helpMark(url string) string {
	return `<a class="oa-help-mark" href="` + url + `" target="_blank" rel="noopener" aria-label="Help for this section">?</a>`
}

func helpLearnMore(url string) string {
	return `<a class="oa-help-learnmore" href="` + url + `" target="_blank" rel="noopener">Learn more</a>`
}

// pageHelpFloat renders the macOS/Linux page-level "?" button (hidden on
// Windows by static/panel.css), or "" for a page with no anchors.
func pageHelpFloat(page string) string {
	url, ok := helpPageURL(page)
	if !ok {
		return ""
	}
	return `<a class="oa-help-float" href="` + url + `" target="_blank" rel="noopener" aria-label="Help for this page">?</a>`
}

var (
	firstHeading = regexp.MustCompile(`(?s)(<h[1-6][^>]*>)(.*?)(</h[1-6]>)`)
	firstPara    = regexp.MustCompile(`(?s)<p[^>]*>.*?</p>`)
)

// sectionBlock finds the "<section id=\"id\">...</section>" block, or, for a
// page's un-sectioned intro, finds "<h1 id=\"id\">...</h1>" together with the
// intro's own trailing content up to the next <section> (or the end of the
// fragment). Both shapes give a self-contained span the caller can rewrite.
func sectionBlock(fragment, id string) (start, end int, ok bool) {
	if m := regexp.MustCompile(`(?s)<section id="` + regexp.QuoteMeta(id) + `"[^>]*>.*?</section>`).FindStringIndex(fragment); m != nil {
		return m[0], m[1], true
	}
	h1 := regexp.MustCompile(`(?s)<h1 id="` + regexp.QuoteMeta(id) + `"[^>]*>.*?</h1>`)
	m := h1.FindStringIndex(fragment)
	if m == nil {
		return 0, 0, false
	}
	rest := fragment[m[1]:]
	if next := regexp.MustCompile(`<section`).FindStringIndex(rest); next != nil {
		return m[0], m[1] + next[0], true
	}
	return m[0], len(fragment), true
}

// injectSectionHelp rewrites one page's fragment to carry every section's
// help mark (inside its heading) and Learn-more link (after its first
// paragraph, or after its heading when the section has none). It leaves a
// section untouched when the map has no entry, and leaves the fragment
// untouched when a listed section id cannot be found in it — help_test.go
// catches drift between the map and the page templates instead of this
// silently producing a half-linked page.
func injectSectionHelp(page, fragment string) string {
	for _, e := range helpAnchors {
		if e.Page != page {
			continue
		}
		start, end, ok := sectionBlock(fragment, e.ID)
		if !ok {
			continue
		}
		block := fragment[start:end]
		block = injectIntoBlock(block, helpMark(e.URL), helpLearnMore(e.URL))
		fragment = fragment[:start] + block + fragment[end:]
	}
	return fragment
}

// injectIntoBlock places mark and learn inside the block's first heading, so
// every section shows its help link at the same place whatever the section
// holds: the "?" mark on macOS and Linux, the "Learn more" link on Windows.
func injectIntoBlock(block, mark, learn string) string {
	if hm := firstHeading.FindStringSubmatchIndex(block); hm != nil {
		open, text, close := block[hm[2]:hm[3]], block[hm[4]:hm[5]], block[hm[6]:hm[7]]
		block = block[:hm[0]] + open + text + mark + learn + close + block[hm[7]:]
	}
	return block
}
