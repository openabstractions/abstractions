package main

import (
	"embed"
	"encoding/json"
	"fmt"
	"html"
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"runtime"
	"strings"
	"sync"
)

// panelCSSFS carries every stylesheet in static/: panel.css, the shared shell
// every page links, and each skin a slice adds as static/skin-<skin>.css
// (SPEC §5, §6). The pattern takes a new skin file without a shell edit.
//
//go:embed static/*.css
var panelCSSFS embed.FS

// panelStatic is the file system the shell serves and links stylesheets
// from; tests replace it to have a skin present or absent.
var panelStatic fs.FS = panelCSSFS

var staticCSSName = regexp.MustCompile(`^[a-z0-9-]+\.css$`)

// servePanelStatic serves /static/<name>.css from the embedded stylesheets.
func servePanelStatic(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/static/")
	if !staticCSSName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	data, err := fs.ReadFile(panelStatic, "static/"+name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/css; charset=utf-8")
	//unchecked: a failed write means the client is already gone
	_, _ = w.Write(data)
}

// skinStylesheet links static/skin-<skin>.css after panel.css when the skin
// is windows or macos and its file is embedded, and links nothing otherwise.
func skinStylesheet(skin string) string {
	if skin != "windows" && skin != "macos" {
		return ""
	}
	if _, err := fs.Stat(panelStatic, "static/skin-"+skin+".css"); err != nil {
		return ""
	}
	return `<link rel="stylesheet" href="/static/skin-` + skin + `.css">`
}

// panelSkin names the skin on the document root's data-skin (SPEC §6):
// "windows" and "macos" on those platforms, "web" for Linux and any other
// browser render. The screenshot tool may name one with ?skin=.
func panelSkin(r *http.Request) string {
	switch skin := r.URL.Query().Get("skin"); skin {
	case "windows", "macos", "web":
		return skin
	}
	switch panelPlatform(r) {
	case "windows":
		return "windows"
	case "macos":
		return "macos"
	default:
		return "web"
	}
}

// panelNativeShell reports a page loaded by a native host that draws its own
// navigation and title (?shell=native, SPEC §6).
func panelNativeShell(r *http.Request) bool {
	return r.URL.Query().Get("shell") == "native"
}

// accentColors is the accent the root carries: the accent itself and the two
// shades the Windows palette pairs with it, Light2 for dark mode and Dark1
// for light mode (the accent fill WinUI uses in each).
type accentColors struct{ Accent, Light2, Dark1 string }

// panelAccent supplies the root's --oa-accent, --oa-accent-light2 and
// --oa-accent-dark1. S1 owns this hook and its default, the Windows default
// blue; the Windows host (S10) sets it from the registry in an init() and
// pushes later changes to an open page through oaSetAccent, as the macOS
// host does from NSColor.controlAccentColor.
var panelAccent = defaultAccent

// defaultAccent is the shell's accent when no host supplies one: the
// Windows default blue and its Light2 and Dark1 shades.
func defaultAccent() accentColors {
	return accentColors{Accent: "#0078D4", Light2: "#60CDFF", Dark1: "#005FB8"}
}

var accentColor = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// accentStyle is the root's inline accent properties, each only for a
// well-formed colour. Only the windows skin carries them: the macOS host
// injects the system accent itself, and the macOS and web skins keep the
// stylesheet's neutral system blue until it does.
func accentStyle(skin string) string {
	if skin != "windows" {
		return ""
	}
	colors := panelAccent()
	var style []string
	for _, p := range []struct{ name, value string }{{"--oa-accent", colors.Accent}, {"--oa-accent-light2", colors.Light2}, {"--oa-accent-dark1", colors.Dark1}} {
		if accentColor.MatchString(p.value) {
			style = append(style, p.name+":"+p.value)
		}
	}
	if len(style) == 0 {
		return ""
	}
	return ` style="` + strings.Join(style, ";") + `"`
}

// panelPlatform names the skin this document renders: the platform this
// binary actually runs on, or, only for the screenshot tool, an explicit
// override so one machine can render every skin.
func panelPlatform(r *http.Request) string {
	switch r.URL.Query().Get("platform") {
	case "windows", "macos", "linux":
		return r.URL.Query().Get("platform")
	}
	switch runtime.GOOS {
	case "windows":
		return "windows"
	case "darwin":
		return "macos"
	default:
		return "linux"
	}
}

// panelNavItem is one row of the navigation table: the single source of the
// HTML sidebar, of /panel/nav.json for the native macOS sidebar, and of the
// search index (research/panel-redesign/SPEC.md §3, §4, §6). Group is "main"
// or "more", the items after the separator. Segoe is the Segoe Fluent Icons
// codepoint a Windows host renders, SF the SF Symbol name a macOS host
// renders; Icon is the SVG the HTML sidebar draws.
type panelNavItem struct {
	ID    string
	Label string
	Path  string
	Group string
	Icon  string
	Segoe string
	SF    string
}

// panelNavItems is the sidebar in order (SPEC §4). Explore keeps its page at
// /explore outside the navigation; Logging and Identity are sections of
// Settings, and a navigation to /logging or /identity lands there
// (research/adopter-boundary/AUDIT-2026-10-06.md, Panel pages). Labels
// follow the Panel vocabulary in research/vocabulary/DECISION.md:
// Permissions (D53), Providers (D41) and Device (D73). Existing page paths
// and aliases keep working.
//
// Segoe Fluent Icons names: Flag E7C1, AllApps E71D, Robot E99A, Shield
// EA18, History E81C, Devices E772, Cloud E753, Permissions (a key) E8D7,
// Setting E713.
var panelNavItems = []panelNavItem{
	{ID: "setup", Label: "Get started", Path: "/setup", Group: "main", Icon: iconSetup, Segoe: "E7C1", SF: "sparkles"},
	{ID: "status", Label: "Apps", Path: "/", Group: "main", Icon: iconStatus, Segoe: "E71D", SF: "square.grid.2x2"},
	{ID: "inference", Label: "Models", Path: "/inference", Group: "main", Icon: iconInference, Segoe: "E99A", SF: "brain"},
	{ID: "rights", Label: "Permissions", Path: "/rights", Group: "main", Icon: iconRights, Segoe: "EA18", SF: "hand.raised"},
	{ID: "work", Label: "Activity", Path: "/work", Group: "main", Icon: iconWork, Segoe: "E81C", SF: "clock.arrow.circlepath"},
	{ID: "card", Label: "Device", Path: "/card", Group: "main", Icon: iconCard, Segoe: "E772", SF: "memorychip"},
	{ID: "registry", Label: "Providers", Path: "/registry", Group: "more", Icon: iconRegistry, Segoe: "E753", SF: "server.rack"},
	{ID: "credentials", Label: "Credentials", Path: "/credentials-page", Group: "more", Icon: iconCredentials, Segoe: "E8D7", SF: "key"},
	{ID: "settings", Label: "Settings", Path: "/settings", Group: "more", Icon: iconSettings, Segoe: "E713", SF: "gearshape"},
}

// navBadgeID is the item carrying the number of questions waiting (SPEC §4).
const navBadgeID = "rights"

// navAliases are task-named paths that redirect to each task item's page,
// so a link may use either name.
var navAliases = map[string]string{
	"/get-started": "/setup",
	"/apps":        "/",
	"/models":      "/inference",
	"/permissions": "/rights",
	"/activity":    "/work",
	"/device":      "/card",
	"/providers":   "/registry",
}

// navItemByID finds one navigation item.
func navItemByID(id string) (panelNavItem, bool) {
	for _, item := range panelNavItems {
		if item.ID == id {
			return item, true
		}
	}
	return panelNavItem{}, false
}

// navItemByPath finds the navigation item a page path or alias belongs to.
// An app page (/app/<name>, app_page.go) belongs to Apps.
// Paths reached by a redirect (/questions, /logging, /identity) land on
// their page's item.
func navItemByPath(path string) (panelNavItem, bool) {
	if target, ok := navAliases[path]; ok {
		path = target
	}
	if strings.HasPrefix(path, "/app/") {
		path = "/"
	}
	switch path {
	case "/questions":
		path = "/rights"
	case "/logging", "/identity", "/explore":
		path = "/settings"
	}
	for _, item := range panelNavItems {
		if item.Path == path {
			return item, true
		}
	}
	return panelNavItem{}, false
}

// apiOrPage serves a path that is both a guarded JSON call and a task-named
// page address: a call carries the X-Panel-Key header, a navigation does
// not and is sent to the page (/questions: the Questions list and its page).
func apiOrPage(key string, api http.HandlerFunc, page string) http.HandlerFunc {
	redirect := redirectAlias(page)
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Panel-Key") == "" && r.Method == http.MethodGet {
			redirect(w, r)
			return
		}
		guard(key, api)(w, r)
	}
}

// redirectAlias sends a task-named path to its page, keeping the query and
// with it the Panel key.
func redirectAlias(target string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		to := target
		if r.URL.RawQuery != "" {
			to += "?" + r.URL.RawQuery
		}
		http.Redirect(w, r, to, http.StatusFound)
	}
}

// 24x24 glyphs, one per nav item, rendered on colored tiles by panel.css.
const (
	iconSetup       = `<path d="M12 2l2.9 6.6 7.1.6-5.4 4.7 1.6 7L12 17.2 5.8 20.9l1.6-7L2 9.2l7.1-.6L12 2z"/>`
	iconStatus      = `<path d="M12 2a10 10 0 100 20 10 10 0 000-20zm-1.2 14.6l-4.4-4.4 1.4-1.4 3 3 6-6 1.4 1.4-7.4 7.4z"/>`
	iconWork        = `<path d="M10 4h4a2 2 0 012 2v1h3a2 2 0 012 2v9a2 2 0 01-2 2H5a2 2 0 01-2-2V9a2 2 0 012-2h3V6a2 2 0 012-2zm0 3h4V6h-4v1zM5 9v9h14V9H5z"/>`
	iconCard        = `<path d="M7 2v2H5v2H3v2h2v8H3v2h2v2h2v-2h10v2h2v-2h2v-2h-2V8h2V6h-2V4h-2V2h-2v2H9V2H7zm0 6h10v8H7V8z"/>`
	iconInference   = `<path d="M4 4h16a1 1 0 011 1v11a1 1 0 01-1 1H9l-5 4V17a1 1 0 01-1-1V5a1 1 0 011-1z"/>`
	iconRegistry    = `<path d="M12 2l10 6-10 6L2 8l10-6zm-8 9.2L12 16l8-4.8V16l-8 4.8L4 16z"/>`
	iconCredentials = `<path d="M14 2a6 6 0 00-5.65 8.06L2 16.4V20a1 1 0 001 1h3.6l1.35-1.35H10v-2h2v-2h1.94A6 6 0 1014 2zm2 3a2 2 0 110 4 2 2 0 010-4z"/>`
	iconRights      = `<path d="M12 2l8 3v6c0 5-3.5 8.5-8 11-4.5-2.5-8-6-8-11V5l8-3z"/>`
	iconSettings    = `<path d="M12 8a4 4 0 100 8 4 4 0 000-8zm9 4a7.4 7.4 0 01-.14 1.4l2.1 1.6-2 3.4-2.5-1a7.6 7.6 0 01-2.4 1.4L18 22H8.4l-.46-2.8a7.6 7.6 0 01-2.4-1.4l-2.5 1-2-3.4 2.1-1.6A7.4 7.4 0 013 12c0-.5.05-1 .14-1.4L1.14 9l2-3.4 2.5 1c.7-.6 1.5-1.05 2.4-1.4L8.4 2h7.2l.46 2.8c.9.35 1.7.8 2.4 1.4l2.5-1 2 3.4-2.1 1.6c.1.45.14.95.14 1.4z"/>`
)

// navSection is one section title the sidebar search finds: the section's
// element id on its page and its heading text.
type navSection struct {
	ID    string
	Title string
}

var (
	navSectionsOnce sync.Once
	navSectionsByID map[string][]navSection
	sectionHeading  = regexp.MustCompile(`(?s)<section id="([A-Za-z0-9_-]+)"([^>]*)>\s*<h2[^>]*>(.*?)</h2>`)
	markupTag       = regexp.MustCompile(`(?s)<[^>]*>`)
	hiddenAttr      = regexp.MustCompile(`\bhidden\b`)
)

// navPageFragments names each navigation item's page fragment, read for
// the search index's section titles.
func navPageFragments() map[string]string {
	return map[string]string{
		"setup": setupPage, "status": servicePage, "inference": inferencePage, "rights": rightsPage,
		"work": workPage, "card": cardPage, "registry": registryPage, "credentials": credentialsPage, "settings": settingsPage,
	}
}

// navSections lists, per navigation item, the titles of the sections its
// page shows on arrival (a section rendered hidden is skipped), in page
// order. The search field filters these and the page labels together.
func navSections() map[string][]navSection {
	navSectionsOnce.Do(func() {
		navSectionsByID = map[string][]navSection{}
		for id, fragment := range navPageFragments() {
			for _, m := range sectionHeading.FindAllStringSubmatch(fragment, -1) {
				if hiddenAttr.MatchString(m[2]) {
					continue
				}
				title := strings.Join(strings.Fields(html.UnescapeString(markupTag.ReplaceAllString(m[3], " "))), " ")
				if title != "" {
					navSectionsByID[id] = append(navSectionsByID[id], navSection{ID: m[1], Title: title})
				}
			}
		}
	})
	return navSectionsByID
}

// navSearchText is what one item's search matches: its label and its
// section titles; navSearchScript compares them in any case.
func navSearchText(item panelNavItem) string {
	parts := []string{item.Label}
	for _, section := range navSections()[item.ID] {
		parts = append(parts, section.Title)
	}
	return strings.Join(parts, " ")
}

func panelNav(key, active string) string {
	// Every WriteString below targets a strings.Builder, which never errors.
	var b strings.Builder
	keyed := func(path string) string { return path + "?k=" + url.QueryEscape(key) }
	item := func(entry panelNavItem) {
		class := "oa-nav-item"
		if entry.ID == active {
			class += " active"
		}
		//unchecked: strings.Builder never errors
		b.WriteString(`<a class="` + class + `" href="` + html.EscapeString(keyed(entry.Path)) + `" data-id="` + entry.ID + `" data-search="` + html.EscapeString(navSearchText(entry)) + `">`)
		//unchecked: strings.Builder never errors
		b.WriteString(`<span class="oa-nav-icon oa-icon-` + entry.ID + `"><svg viewBox="0 0 24 24" aria-hidden="true">` + entry.Icon + `</svg></span>`)
		//unchecked: strings.Builder never errors
		b.WriteString(`<span class="oa-nav-label">` + html.EscapeString(entry.Label) + `</span>`)
		if entry.ID == navBadgeID {
			//unchecked: strings.Builder never errors
			b.WriteString(`<span class="oa-nav-badge" data-badge="` + entry.ID + `" hidden></span>`)
		}
		//unchecked: strings.Builder never errors
		b.WriteString(`</a>`)
	}
	//unchecked: strings.Builder never errors
	b.WriteString(`<nav class="oa-nav" aria-label="Panel pages">`)
	//unchecked: strings.Builder never errors
	b.WriteString(`<div class="oa-brand">OpenAbstractions<span>Panel</span></div>`)
	//unchecked: strings.Builder never errors
	b.WriteString(`<div class="oa-nav-search" role="search"><input type="search" id="oa-nav-search" placeholder="Find a setting" aria-label="Find a page or section" autocomplete="off" spellcheck="false"></div>`)
	group := ""
	for _, entry := range panelNavItems {
		if entry.Group != group {
			if group != "" {
				//unchecked: strings.Builder never errors
				b.WriteString(`</div><div class="oa-nav-sep" role="separator"></div>`)
			}
			//unchecked: strings.Builder never errors
			b.WriteString(`<div class="oa-nav-group" data-group="` + entry.Group + `">`)
			group = entry.Group
		}
		item(entry)
	}
	//unchecked: strings.Builder never errors
	b.WriteString(`</div><div class="oa-nav-hits" hidden>`)
	for _, entry := range panelNavItems {
		for _, section := range navSections()[entry.ID] {
			//unchecked: strings.Builder never errors
			b.WriteString(`<a class="oa-nav-hit" href="` + html.EscapeString(keyed(entry.Path)+"#"+section.ID) + `" data-search="` + html.EscapeString(section.Title) + `" hidden>` +
				html.EscapeString(section.Title) + `<span class="oa-nav-hit-page">` + html.EscapeString(entry.Label) + `</span></a>`)
		}
	}
	//unchecked: strings.Builder never errors
	b.WriteString(`</div><p class="oa-nav-nohits" hidden>No page or section matches.</p></nav>`)
	return b.String()
}

// panelClassesJSON is the one source of the shell's class names
// (static/panel_classes.json): this file reads it into panelShellClasses
// and panelShellClassPrefixes, and static/panel_rules.js reads the same file
// for its rendered-DOM class rule.
//
//go:embed static/panel_classes.json
var panelClassesJSON []byte

// panelShellClasses are the class names the shell and the shared components
// emit, the list in research/panel-redesign/SPEC.md §6. S1 owns them; the
// skins (static/skin-*.css) style these and no others
// (panel_selector_contract_test.go), and pages use these and no others (the
// page class contract in panel_lint_test.go). oa-icon-* is one name per
// navigation id, and oa-icon-logging for the Logging section's tile.
// panelShellClassPrefixes are the prefixes a page script may complete into
// a class: oa-icon- plus a navigation id.
var panelShellClasses, panelShellClassPrefixes = loadPanelClasses()

func loadPanelClasses() ([]string, []string) {
	var list struct {
		Classes  []string `json:"classes"`
		Prefixes []string `json:"prefixes"`
	}
	if err := json.Unmarshal(panelClassesJSON, &list); err != nil {
		panic(fmt.Sprintf("static/panel_classes.json: %v", err))
	}
	if err := checkPanelClasses(list.Classes, list.Prefixes); err != nil {
		panic("static/panel_classes.json: " + err.Error())
	}
	return list.Classes, list.Prefixes
}

// checkPanelClasses holds the class list to the shape panel_rules.js's
// setClasses accepts: at least one class, no empty name, and each prefix
// ending in "-" after at least one character.
func checkPanelClasses(classes, prefixes []string) error {
	if len(classes) == 0 {
		return fmt.Errorf("classes must be a non-empty list")
	}
	for _, c := range classes {
		if c == "" {
			return fmt.Errorf("an empty class name")
		}
	}
	for _, p := range prefixes {
		if len(p) < 2 || !strings.HasSuffix(p, "-") {
			return fmt.Errorf("prefix %q must end in \"-\" after at least one character", p)
		}
	}
	return nil
}

// panelShellAttributes are the attributes a skin's selectors may key on,
// the list in SPEC.md §6: those the shell and components set, those a host
// sets on the root (data-backdrop, data-host), and standard HTML state.
var panelShellAttributes = []string{
	"data-platform", "data-skin", "data-shell", "data-id", "data-group", "data-badge", "data-state",
	"aria-pressed", "data-evidence", "data-ready",
	"data-backdrop", "data-host",
	"hidden", "open", "disabled", "type", "aria-current", "aria-busy", "href", "tabindex",
}

// The shared components (SPEC §5). Each returns markup with the class
// static/panel.css styles; labels, titles and captions are escaped, while
// controls, rows and bodies are markup the page already built. Every row
// that holds a control has its own status line for the one sentence a
// click writes (SPEC §4, the Panel rules lint).

// oaPageHead is the page's head: its icon tile, title and one caption.
// page is a navigation id, for the tile's colour and glyph.
func oaPageHead(page, title, caption string) string {
	icon := ""
	if item, ok := navItemByID(page); ok {
		icon = item.Icon
	}
	head := `<header class="oa-pagehead"><span class="oa-pagehead-tile oa-icon-` + html.EscapeString(page) + `"><svg viewBox="0 0 24 24" aria-hidden="true">` + icon + `</svg></span><div class="oa-pagehead-text"><h1 class="oa-pagehead-title">` + html.EscapeString(title) + `</h1>`
	if caption != "" {
		head += `<p class="oa-caption">` + html.EscapeString(caption) + `</p>`
	}
	return head + `</div></header>`
}

// oaButton is a button; primary marks the one primary action of its row or
// group (oa-primary).
func oaButton(id, label string, primary bool) string {
	class := ""
	if primary {
		class = ` class="oa-primary"`
	}
	return `<button id="` + html.EscapeString(id) + `"` + class + `>` + html.EscapeString(label) + `</button>`
}

// oaGroup is one group of rows: a section with its heading and at most one
// caption. id is the section's anchor (help.go links it).
func oaGroup(id, title, caption string, rows ...string) string {
	group := `<section id="` + html.EscapeString(id) + `" class="oa-group"><h2>` + html.EscapeString(title) + `</h2>`
	if caption != "" {
		group += `<p class="oa-caption">` + html.EscapeString(caption) + `</p>`
	}
	return group + strings.Join(rows, "") + `</section>`
}

// oaRow is one row: label, optional caption, the trailing control and, when
// statusID is set, the row's status line.
func oaRow(label, caption, control, statusID string) string {
	row := `<div class="oa-row"><div class="oa-row-text"><span class="oa-row-label">` + html.EscapeString(label) + `</span>`
	if caption != "" {
		row += `<span class="oa-row-caption">` + html.EscapeString(caption) + `</span>`
	}
	row += `</div><div class="oa-row-control">` + control + `</div>`
	if statusID != "" {
		row += oaStatusLine(statusID, false)
	}
	return row + `</div>`
}

// oaRowNav is a row that opens another page or section, ending in a chevron.
func oaRowNav(label, caption, href string) string {
	row := `<a class="oa-row oa-row-nav" href="` + html.EscapeString(href) + `"><span class="oa-row-text"><span class="oa-row-label">` + html.EscapeString(label) + `</span>`
	if caption != "" {
		row += `<span class="oa-row-caption">` + html.EscapeString(caption) + `</span>`
	}
	return row + `</span><span class="oa-row-chevron" aria-hidden="true"></span></a>`
}

// oaExpander holds expert content one level deep, closed on arrival.
func oaExpander(summary, body string) string {
	return `<details class="oa-expander"><summary>` + html.EscapeString(summary) + `<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body">` + body + `</div></details>`
}

// oaTechnicalDetails is the expander that holds raw values a page keeps out
// of its sentences (an id, a path, the raw reply): summary "Technical
// details", marked data-oa-technical, so the Panel rules spare its body from
// the raw-value rule only (SPEC §6). Every other expander is judged fully.
func oaTechnicalDetails(body string) string {
	return `<details class="oa-expander" data-oa-technical><summary>Technical details<span class="oa-row-chevron" aria-hidden="true"></span></summary><div class="oa-expander-body">` + body + `</div></details>`
}

// oaStatus is a status line in its loading state: it reads "Loading…" until
// the page's script writes the first sentence (oaSetStatus), so a page never
// shows an error before its data has had the chance to arrive.
func oaStatus(id string) string {
	return oaStatusLine(id, true)
}

func oaStatusLine(id string, loading bool) string {
	if loading {
		return `<p class="oa-status" id="` + html.EscapeString(id) + `" role="status" aria-live="polite" data-state="loading" aria-busy="true">Loading…</p>`
	}
	return `<p class="oa-status" id="` + html.EscapeString(id) + `" role="status" aria-live="polite" data-state="idle"></p>`
}

// oaEmpty is an empty list's one sentence.
func oaEmpty(sentence string) string {
	return `<p class="oa-empty">` + html.EscapeString(sentence) + `</p>`
}

// oaTable is a table inside its scrolling wrapper; only Activity and the
// Providers server list use one (SPEC §5). The page's script fills tbody.
func oaTable(id string, headers ...string) string {
	table := `<div class="oa-table"><table id="` + html.EscapeString(id) + `"><thead><tr>`
	for _, header := range headers {
		table += `<th>` + html.EscapeString(header) + `</th>`
	}
	return table + `</tr></thead><tbody></tbody></table></div>`
}

var firstH1 = regexp.MustCompile(`(?s)<h1([^>]*)>(.*?)</h1>`)

// nativeFragment drops the page's h1 for a native host, which shows the
// title in its own toolbar (SPEC §6). A page head's title (oaPageHead) stays
// as the head's text, without the h1 element.
func nativeFragment(fragment string) string {
	m := firstH1.FindStringSubmatchIndex(fragment)
	if m == nil {
		return fragment
	}
	attrs, text := fragment[m[2]:m[3]], fragment[m[4]:m[5]]
	if strings.Contains(attrs, `class="oa-pagehead-title"`) {
		return fragment[:m[0]] + `<div class="oa-pagehead-title">` + text + `</div>` + fragment[m[1]:]
	}
	return fragment[:m[0]] + fragment[m[1]:]
}

// panelPage assembles one full document around a page's own fragment (its
// existing markup and unchanged <script>): doctype, the platform and skin
// attributes, the shared stylesheet, and the shell's navigation pane. It also
// links every section to its reference anchor (help.go): a mark inside each
// section heading, a Learn-more link after each section's first paragraph,
// and one page-level floating mark, shown per platform by static/panel.css.
// A page in the navigation table takes its label as the document title. With
// ?shell=native the navigation and the page's h1 are left to the host.
func panelPage(r *http.Request, key, active, title, fragment string) string {
	fragment = injectSectionHelp(active, fragment)
	if item, ok := navItemByID(active); ok {
		title = item.Label
	}
	native := panelNativeShell(r)
	shell, nav := "html", panelNav(key, active)+navScript+navSearchScript
	if native {
		shell, nav, fragment = "native", nativeShellScript, nativeFragment(fragment)
	}
	// The viewport meta tag was missing (task 2026-09-23, fifth first-time
	// visitor, finding 8): with none, a narrow render of this page falls back
	// to the 980px layout a touch browser assumes for a page never designed
	// for a small screen, then shrinks that to fit — the two-column layout
	// stays two columns, shrunk down and unreadable, well past where
	// static/panel.css's own max-width: 860px stacking rule would otherwise
	// take over. width=device-width makes the layout viewport track the
	// window's real width, so that rule engages at the width it names.
	skin := panelSkin(r)
	return `<!doctype html><html lang="en" data-platform="` + panelPlatform(r) + `" data-skin="` + skin + `" data-shell="` + shell + `"` + accentStyle(skin) + `><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>` + html.EscapeString(title) + `</title><link rel="stylesheet" href="/static/panel.css">` + skinStylesheet(skin) + `</head><body><div class="oa-shell">` + nav + actionPlainScript + plainScript + `<main class="oa-content">` + fragment + pageHelpFloat(active) + `</main></div>` + readyScript + `</body></html>`
}

// readyScript is the shell's readiness fallback (SPEC §6). After load it sets
// data-ready="load" on the root, unless the page declared that it owns its
// readiness with an element carrying data-ready-owner="page" in its markup,
// or the page already called oaReady. The check reads the parsed document,
// so a comment or a script string naming the marker does not count.
const readyScript = `<script>window.addEventListener('load',function(){var root=document.documentElement;if(!root||root.hasAttribute('data-ready')||document.querySelector('[data-ready-owner="page"]'))return;root.setAttribute('data-ready','load')});</script>`

// navSearchScript filters the sidebar as the person types: page labels and
// section titles from the navigation table, the matching sections listed as
// links to their anchors; Enter opens the first match. It also keeps the
// Permissions badge current from /panel/nav.json while the page is visible.
const navSearchScript = `<script>
(function () {
  var input = document.getElementById('oa-nav-search');
  if (!input) return;
  function all(sel) { return Array.prototype.slice.call(document.querySelectorAll(sel)); }
  // Page labels and section titles match what the person types in any case.
  function words(text) { return String(text || '').trim().toLowerCase(); }
  function filter() {
    var q = words(input.value);
    var items = all('.oa-nav .oa-nav-item'), hits = all('.oa-nav .oa-nav-hit'), shown = 0;
    items.forEach(function (a) { var on = !q || words(a.getAttribute('data-search')).indexOf(q) !== -1; a.hidden = !on; if (on) shown++; });
    hits.forEach(function (a) { var on = !!q && words(a.getAttribute('data-search')).indexOf(q) !== -1; a.hidden = !on; if (on) shown++; });
    all('.oa-nav .oa-nav-sep').forEach(function (s) { s.hidden = !!q; });
    all('.oa-nav .oa-nav-hits').forEach(function (h) { h.hidden = !q || !hits.some(function (a) { return !a.hidden; }); });
    all('.oa-nav .oa-nav-nohits').forEach(function (p) { p.hidden = !q || shown > 0; });
  }
  input.addEventListener('input', filter);
  input.addEventListener('keydown', function (e) {
    if (e.key === 'Escape') { input.value = ''; filter(); return; }
    if (e.key !== 'Enter') return;
    var first = all('.oa-nav .oa-nav-item, .oa-nav .oa-nav-hit').filter(function (a) { return !a.hidden; })[0];
    if (first) { e.preventDefault(); location.href = first.href; }
  });
  var key = new URLSearchParams(location.search).get('k');
  function badge() {
    if (!key || document.visibilityState === 'hidden' || typeof fetch !== 'function') return;
    // path names this page, so nav.json selects it without reading the setup
    // journal for a landing page on every poll.
    var page = location.pathname ? '&path=' + encodeURIComponent(location.pathname) : '';
    fetch('/panel/nav.json?k=' + encodeURIComponent(key) + page).then(function (r) { return r.ok ? r.json() : null; }).then(function (nav) {
      if (!nav) return;
      (nav.items || []).forEach(function (item) {
        all('.oa-nav [data-badge="' + item.id + '"]').forEach(function (b) {
          var n = typeof item.badge === 'number' ? item.badge : 0;
          b.textContent = n > 0 ? String(n) : '';
          b.hidden = !(n > 0);
          b.setAttribute('aria-label', n === 1 ? '1 question waiting' : n + ' questions waiting');
        });
      });
    }).catch(function () {});
  }
  badge();
  document.addEventListener('visibilitychange', badge);
  setInterval(badge, 20000);
})();
</script>`

// nativeShellScript runs in a page a native host loaded with ?shell=native:
// a link to another Panel page keeps the parameter, so the HTML navigation
// never appears inside the host's window (SPEC §6).
const nativeShellScript = `<script>
(function () {
  document.addEventListener('click', function (e) {
    var a = e.target && e.target.closest ? e.target.closest('a[href]') : null;
    if (!a || a.target === '_blank' || a.origin !== location.origin) return;
    var u = new URL(a.href);
    if (u.searchParams.get('shell') !== 'native') { u.searchParams.set('shell', 'native'); a.href = u.toString(); }
  }, true);
})();
</script>`

// navScript keeps the navigation pane's highlight on the item the person
// chose. panelNav marks one item from the page id on the server, and six
// items (Work, Questions and rights, Settings, Logging, Identity, Explore) are
// sections of the Status page reached by a hash, so before this script every
// one of them left "Status" lit after the click (owner, 2026-09-23: "they
// don't stick selected"). The item whose path and hash match the location is
// current; with no hash, the item on this path that carries no hash is. It
// runs at load and on every hashchange, and never touches items on another
// path, which the server already marked from the page id.
const navScript = `<script>
(function () {
  function current() {
    var items = document.querySelectorAll('.oa-nav .oa-nav-item');
    var here = location.pathname, hash = location.hash;
    var chosen = null, plain = null;
    for (var i = 0; i < items.length; i++) {
      var a = items[i];
      if (a.pathname !== here) continue;
      if (hash && a.hash === hash) chosen = a;
      if (!a.hash) plain = a;
    }
    var pick = chosen || plain;
    if (!pick) return;
    for (var j = 0; j < items.length; j++) {
      var b = items[j];
      if (b.pathname !== here) continue;
      if (b === pick) { b.classList.add('active'); b.setAttribute('aria-current', 'page'); }
      else { b.classList.remove('active'); b.removeAttribute('aria-current'); }
    }
  }
  window.addEventListener('hashchange', current);
  current();
})();
</script>`

// plainScript is the one control every page's fragment can call to keep a raw
// backend error off the screen (task 2026-09-23, first-time-user walkthrough:
// a service-resolution failure naming a pipe path and a security identifier
// shown as the Graphics card page's first line before any click; "open
// C:\Users\<name>\...\admin.secret: ..." shown after a click; the same
// generic denial worded three different ways on the Registry page). A page's
// own script calls oaShowError(id, err) wherever it used to write a caught
// error or a raw outcome string straight into an element; oaPlain(text) is
// exposed separately for a page that needs to fold several such texts into
// one sentence of its own (registry_page.go, unifying three denials from one
// click into one line).
//
// oaPlain never repeats the input text in the sentence it returns: no file
// path, pipe name, security identifier, or contract name (an
// "abstraction.<x>/<y>@<n>" carries a runtime version and is never a rule to
// ask for) can survive into it. A rule name of the shape
// "abstraction.<x>/<y>" or "<x>.<y>" found in a permission denial is named
// by its plain phrase (oaActionPhrase), so the reader knows what to ask this
// Panel's operator for; the id itself and the original text remain
// available as the "details" this function also returns, for oaShowError's
// disclosure.
//
// actionPlainScript hands the shared script the one plain-phrase table,
// rightsActionPlain, as oaActionPlainTable; every page shell serves it
// ahead of plainScript.
var actionPlainScript = func() string {
	table, err := json.Marshal(rightsActionPlain)
	if err != nil {
		panic(err)
	}
	return "<script>var oaActionPlainTable=" + string(table) + ";</script>"
}()

const plainScript = `<script>
function oaRuleName(text){
 var m=/abstraction\.[A-Za-z0-9_-]+\/[A-Za-z0-9_.-]+/.exec(text);
 if(m&&text.charAt(m.index+m[0].length)!=='@')return m[0].replace(/\.+$/,'');
 // A bare "<x>.<y>" token immediately followed by "/" is the head of an
 // "abstraction.<x>/<y>" token (a rule already handled above, or a
 // "abstraction.<x>/<y>@<n>" contract, never a rule on its own): skip it
 // rather than report half of it. A denylisted extension (a file, not a
 // rule) is skipped the same way; the search keeps going past either.
 var skip={secret:1,exe:1,dll:1,json:1,txt:1,log:1,py:1,go:1,js:1,md:1,yml:1,yaml:1,ini:1,cfg:1,dat:1,db:1,pipe:1};
 var re=/\b[a-z][a-z0-9_-]*\.[a-z][a-z0-9_-]*\b/g,b;
 while((b=re.exec(text))){
  if(text.charAt(b.index+b[0].length)==='/')continue;
  var ext=b[0].slice(b[0].lastIndexOf('.')+1);
  if(!skip[ext])return b[0].replace(/\.+$/,'');
 }
 return null;
}
// oaContractName finds the one "abstraction.<layer>/<name>@<n>" contract a
// service-resolution failure names, if any: the runtime version this Panel
// asked for and the layer/name it belongs to, never a rule to ask for (see
// oaRuleName above, which already skips this same shape).
function oaContractName(text){
 var m=/abstraction\.([a-z][a-z0-9_-]*)\/([a-z][a-z0-9_.-]*)@\d+/.exec(text);
 return m?{layer:m[1],name:m[2]}:null;
}
// oaServiceNames maps a contract's "<layer>/<name>" (or, failing that, its
// bare layer) to the plain-language service name a settings-app sentence
// says instead of the contract itself. Two resource-layer contracts name
// different things to the person (the awake-lease service, the graphics-card
// table), so those two are listed by their full "layer/name"; every other
// layer here means the same thing across its contracts, so it is listed by
// layer alone. An unlisted layer falls back to "that service" rather than
// invent a name.
var oaServiceNames=[
 ['resource/leases','the awake-lease service'],
 ['resource/table','the graphics-card table'],
 ['config/observer','settings watching'],
 ['job','background work'],
 ['download','downloads'],
 ['logging','the log'],
 ['storage','storage'],
 ['inference','inference'],
 ['router','the model router'],
 ['credentials','credentials'],
 ['asks','questions'],
 ['model','model lookup'],
];
function oaServiceName(contract){
 if(!contract)return'that service';
 var full=contract.layer+'/'+contract.name;
 for(var i=0;i<oaServiceNames.length;i++)if(oaServiceNames[i][0]===full)return oaServiceNames[i][1];
 for(var j=0;j<oaServiceNames.length;j++)if(oaServiceNames[j][0]===contract.layer)return oaServiceNames[j][1];
 return'that service';
}
// oaActionFallback names a rights action this Panel's rightsActionPlain map
// (rights_panel.go) does not carry yet: "abstraction.<layer>/<rest>" reads
// as "<Layer>: <rest, dots turned to spaces>" (task 2026-09-23, sixth
// first-time visitor, finding 2: a dropdown and a list mixed plain phrases
// with bare ids like "abstraction.storage/content.observe" for the one
// action this runtime knows that the map did not yet name). It is never
// shown alone: every caller appends the raw id after it in parentheses, the
// same shape a known action's "phrase (id)" already uses elsewhere.
// A versioned id ("abstraction.<layer>/<rest>@<n>", a permission set) reads
// the same without its version; anything else reads "This permission",
// never the raw text.
// oaActionPhrase names a rights action in a sentence, never by its id: the
// shared plain-phrase table (rightsActionPlain, served to every page as
// oaActionPlainTable by actionPlainScript), else the id's own words
// ("abstraction.router/inventory.read" reads "router inventory read"). A
// bare "<x>.<y>" from a denial is the tail of an action: the one table
// action ending in it, else its words.
function oaActionPhrase(action){
 var a=String(action==null?'':action),own=Object.prototype.hasOwnProperty;
 var table=typeof oaActionPlainTable==='object'&&oaActionPlainTable?oaActionPlainTable:{};
 if(own.call(table,a)&&table[a])return table[a];
 var m=/^abstraction\.([a-z][a-z0-9_-]*)\/([a-z][a-z0-9_.-]*?)(?:@\d+)?$/.exec(a);
 if(m)return m[1]+' '+m[2].replace(/[._]/g,' ');
 var hits=[];
 for(var k in table)if(own.call(table,k)&&table[k]&&k.slice(k.indexOf('/')+1)===a)hits.push(table[k]);
 if(hits.length===1)return hits[0];
 return a.replace(/[._]/g,' ');
}
function oaActionFallback(action){
 var m=/^abstraction\.([a-z][a-z0-9_-]*)\/([a-z][a-z0-9_.-]*?)(?:@\d+)?$/.exec(String(action==null?'':action));
 if(!m)return 'This permission';
 return m[1].charAt(0).toUpperCase()+m[1].slice(1)+': '+m[2].replace(/\./g,' ');
}
// oaResourceLabel names a graphics-card resource ("card:<n>", the resource
// table's own index for that card) "Graphics card" for the first one and
// "Graphics card <n+1>" for each after it, instead of the raw id (task
// 2026-09-23, eighth first-time visitor, finding 5: "card:0" shown as a
// label on Device and in rule rows, e.g. "Resource hold on card:0"). Any
// other resource (a credential name, a host, "awake") is not a card and
// keeps its own name unchanged.
function oaResourceLabel(resource){
 var m=/^card:(\d+)$/.exec(resource);
 if(m){var n=Number(m[1]);return 'Graphics card'+(n>0?' '+(n+1):'')}
 // A resource shaped like a contract id ("abstraction.job/acceptance@1")
 // names the whole surface a job-shaped rule is scoped to, never a value
 // typed or chosen; the same generic word Explore's own argumentPhrase
 // already reads it as (task 2026-09-23, eleventh first-time visitor,
 // finding 4: a rights confirmation read the raw contract id as its own
 // resource). "service" is the vocabulary decision's own word for this
 // string shape (research/vocabulary/DECISION.md D4), replacing "this
 // capability's own contract" (task 2026-09-23, twelfth first-time visitor,
 // finding 4).
 if(/^abstraction\.[a-z]+\//.test(resource))return "this service";
 // A content digest ("sha256:<hex>") names stored bytes, never something a
 // person reads; it stays on the element's title (SPEC §1, no hash as text).
 if(/^sha256:[0-9a-fA-F]+$/.test(resource))return "Stored content";
 return resource;
}
// oaHostState reads a host's Up flag as one plain word, "Up" or "Not
// reachable"; the raw connection error (Go's own "Get \"http://...\":
// dial tcp ...: connectex: ..." text) never appears as visible text,
// shared by Inference and Registry's own host tables, each of which keeps
// it as that cell's own title (task 2026-09-23, eleventh first-time
// visitor, finding 5).
function oaHostState(up){return up?'Up':'Not reachable'}
// oaLocalTime reads a raw ISO 8601 timestamp (or an epoch millisecond
// number) as a local, readable time: "today at 6:14 PM" for a moment on
// today's date, a locale date ("9/20/2026") for an older one. The raw ISO
// value is never the return value; callers keep it as the element's own
// title, the same convention oaResourceLabel and oaPlain follow for the
// identifiers they read (task 2026-09-23, tenth first-time visitor, finding
// 2: "Issued 2026-09-23T15:09:12.464Z", "Checked at 2026-09-23T18:14:50+03:00"
// and "at 2026-09-23T15:09:12.462974Z" were all a raw timestamp read aloud).
// oaClockText is the one clock format every page shares (task 2026-09-23,
// twelfth first-time visitor, minor: Logging read "20:32:56", a 24-hour
// format built by hand, while Settings and everywhere else read oaLocalTime's
// 12-hour "8:42 PM" through this same function): a caller that needs
// second-level precision (Logging's own rows, several a minute) passes
// seconds=true and gets the same hour:minute shape with :SS appended.
function oaClockText(d,seconds){
 return d.toLocaleTimeString([],seconds?{hour:'numeric',minute:'2-digit',second:'2-digit'}:{hour:'numeric',minute:'2-digit'});
}
// The phrase reads "today at 1:10 PM", "yesterday at 1:10 PM" or "tomorrow
// at 1:10 PM" (an expiry), and a locale date for any other day, so it fits
// after "since", "Until" or in a table cell. oaWhen is the same phrase for a
// verb ("Checked", "registered by app"): "on 9/20/2026" for another day.
function oaLocalTime(value){
 if(value===null||value===undefined||value==='')return '';
 var d=new Date(value);
 if(isNaN(d.getTime()))return String(value);
 var now=new Date();
 var day=function(offset){return new Date(now.getFullYear(),now.getMonth(),now.getDate()+offset).toDateString()};
 var words={};words[day(0)]='today';words[day(-1)]='yesterday';words[day(1)]='tomorrow';
 var word=words[d.toDateString()];
 if(word)return word+' at '+oaClockText(d,false);
 return d.toLocaleDateString();
}
function oaWhen(value){
 var phrase=oaLocalTime(value);
 if(phrase===''||isNaN(new Date(value).getTime())||/^(today|yesterday|tomorrow) at /.test(phrase))return phrase;
 return 'on '+phrase;
}
// oaShortName is the one label a page shows for a path it names: the last
// part, and for a program without its .exe, .app or .dll, so a program
// reads by name, never as a file name (the Panel rules' program shape). The
// full path stays on the element's title. Every page's short-name helper
// is this one.
function oaShortName(path){var parts=String(path||'').split(/[\\/]/).filter(function(x){return x});var last=parts.length?parts[parts.length-1]:String(path||'');return last.replace(/\.(?:exe|app|dll)$/i,'')||last}
// oaModelName is a model's friendly name for page text: the last part of an
// alias or file name, without its model file type. A name that is only a
// hash reads "Unnamed model". The raw name never becomes page text; every
// page that names a model (Models, the app page, Activity) calls this.
function oaModelName(name){
 var s=String(name==null?'':name),parts=s.split(/[\\/]/).filter(function(x){return x}),last=parts.length?parts[parts.length-1]:'';
 var plain=last.replace(/\.(gguf|safetensors|bin|onnx|ggml)$/i,'');
 if(!plain||/[0-9a-f]{16,}/i.test(plain)||/^sha256[-:]/i.test(plain))return 'Unnamed model';
 return plain;
}
// oaRegisteredApps reads the applications registered with the runtime
// (/apps/registrations, the Apps page's own source) once per page: each
// {program, name}. A failed read gives an empty list and is tried again by
// the next caller.
var oaRegisteredRead=null;
function oaRegisteredApps(){
 if(oaRegisteredRead)return oaRegisteredRead;
 try{
  var key=new URLSearchParams(location.search).get('k');
  oaRegisteredRead=fetch('/apps/registrations?source=applications&wait_ms=0',{headers:{'X-Panel-Key':key}}).then(function(r){
   if(!r.ok)throw Error('registrations '+r.status);return r.json();
  }).then(function(page){
   var apps=[],entries=page&&page.entries||[];
   for(var i=0;i<entries.length;i++){var e=entries[i];if(e&&e.role==='application'&&e.program)apps.push({program:e.program,name:e.name||''})}
   return apps;
  }).catch(function(){oaRegisteredRead=null;return []});
 }catch(e){return Promise.resolve([])}
 return oaRegisteredRead;
}
// oaProgramChooser builds and keeps in sync a program chooser: a <select>
// of the applications registered with the runtime, each by its registered
// name with the full path on its own title, plus "Other…" revealing a
// paired hidden <input> so a page's existing code reading that input's own
// .value keeps working unchanged. Rights' own Program field built this
// exact pair on its own; Inference's Issue key and Credentials' App access
// each left their own program field a bare text box instead (task
// 2026-09-23, fourteenth first-time visitor, finding 8). One shared
// implementation now backs all of them. The list comes from the registry,
// never from running processes: fed from the graphics card's holders it
// offered system processes such as csrss and dwm (SPEC §1, §7).
// knownPrograms, optional, returns programs the page already names in a
// saved rule (a rule's subject, an enrolled assistant), called fresh on
// every rebuild.
function oaProgramChooser(selectId,inputId,knownPrograms,nameOf){
 var select=document.getElementById(selectId),input=document.getElementById(inputId);
 var registered=[];
 function programs(){
  var list=[],i;
  for(i=0;i<registered.length;i++)list.push(registered[i].program);
  var extra=knownPrograms?knownPrograms():[];
  for(i=0;i<extra.length;i++)if(extra[i])list.push(extra[i]);
  return list;
 }
 function label(program){
  var named=nameOf&&nameOf(program);if(named)return named;
  for(var i=0;i<registered.length;i++)if(registered[i].program===program&&registered[i].name)return registered[i].name;
  return oaShortName(program);
 }
 function sync(){
  var v=select.value;
  if(v==='__other__'){input.hidden=false;if(programs().indexOf(input.value)!==-1)input.value=''}
  else{input.hidden=true;input.value=v}
 }
 select.onchange=sync;
 function rebuild(){
  var current=select.value;
  var known=[];for(var i=0,list=programs();i<list.length;i++)if(known.indexOf(list[i])===-1)known.push(list[i]);
  known.sort(function(a,b){return label(a).localeCompare(label(b))});
  select.replaceChildren();
  for(var j=0;j<known.length;j++){var o=document.createElement('option');o.value=known[j];o.textContent=label(known[j]);o.title=known[j];select.append(o)}
  var other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);
  select.value=current&&known.indexOf(current)!==-1?current:'__other__';
  sync();
 }
 var ready=oaRegisteredApps().then(function(apps){registered=apps;rebuild()});
 return {rebuild:rebuild,ready:ready};
}
// oaAccountChooser is oaProgramChooser's own twin for an account field: it
// defaults to this Panel's own account, "this account, <name>", the
// moment ensureSelf resolves it, with "Other…" for a different one. Rights'
// own Account field built this pattern first (task 2026-09-23, twelfth
// first-time visitor, finding 6); Credentials' App access left its own
// account field a bare required text box (task 2026-09-23, fourteenth
// first-time visitor, finding 8). selfAccount and selfAccountName are
// functions so the chooser always reads the page's own current values,
// which usually resolve after this chooser first builds.
function oaAccountChooser(selectId,inputId,selfAccount,selfAccountName){
 var select=document.getElementById(selectId),input=document.getElementById(inputId);
 var touched=false;
 function sync(){
  var v=select.value;
  if(v==='__other__'){input.hidden=false;if(input.value===selfAccount())input.value=''}
  else{input.hidden=true;input.value=v}
 }
 select.onchange=function(){touched=true;sync()};
 function rebuild(){
  var current=select.value,self=selfAccount();
  select.replaceChildren();
  if(self){var o=document.createElement('option');o.value=self;o.textContent='this account'+(selfAccountName()?', '+selfAccountName():'');o.title=self;select.append(o)}
  var other=document.createElement('option');other.value='__other__';other.textContent='Other…';select.append(other);
  var fallback=self||'__other__';
  select.value=touched&&current?current:fallback;
  sync();
 }
 return {rebuild:rebuild};
}
function oaPlain(text){
 var t=text==null?'':String(text);var lower=t.toLowerCase();var line;
 if(lower.indexOf('forbidden')!==-1||lower.indexOf('not permitted')!==-1||lower.indexOf('no rule')!==-1){
  line='The runtime did not permit this Panel to do this; nothing was changed.';
  var rule=oaRuleName(t);if(rule)line+=' It needs the “'+oaActionPhrase(rule)+'” permission.';
 }else if(lower.indexOf('incompatible')!==-1){
  var ic=oaContractName(t);
  if(ic)line="The runtime provides "+oaServiceName(ic)+" at a version this Panel doesn't understand; update the runtime or the Panel.";
  else line="This Panel and the runtime don't speak the same version of this service; update one of them.";
 }else if(lower.indexOf('unavailable')!==-1||lower.indexOf('service resolution')!==-1){
  var uc=oaContractName(t);
  if(uc)line="The runtime is running but doesn't provide "+oaServiceName(uc)+" on this account.";
  else line="That service isn't running on this account right now. Start the runtime, or check the service on the Apps page.";
 }else if(lower.indexOf('gap')!==-1){
  line='The list changed while reading; start again.';
 }else if(lower.indexOf('invalid')!==-1){
  line='The runtime rejected this request.';
 }else{
  line="This didn't work.";
 }
 return{line:line,details:t};
}
// oaSetAccent is how a native host pushes a changed accent into an open page
// (panelAccent writes the first one on the root): the accent and its Light2
// and Dark1 shades, each applied only when it is a #rrggbb colour.
function oaSetAccent(accent,light2,dark1){
 var root=document.documentElement,pairs=[['--oa-accent',accent],['--oa-accent-light2',light2],['--oa-accent-dark1',dark1]];
 for(var i=0;i<pairs.length;i++)if(/^#[0-9a-fA-F]{6}$/.test(String(pairs[i][1])))root.style.setProperty(pairs[i][0],pairs[i][1]);
}
// oaReady marks the page settled: data-ready="1" on the root once its first
// reads have finished, which the screenshot tool trusts outright. A page
// that calls it declares data-ready-owner="page" on an element in its
// markup; any other page gets data-ready="load" from readyScript.
function oaReady(){
 var root=document.documentElement;
 if(root&&root.dataset)root.dataset.ready='1';
}
// oaKeyedRows keeps one row per key in a list, so a live reread keeps each
// row's status sentence and focus (SPEC §4): a row whose key stays is
// updated in place by update(row, item, i); a new key gets create(item, i),
// marked with data-key; a gone key's row leaves; the order follows items;
// a child without data-key (an empty-state sentence) stays.
// Rows are placed by oaPlaceRows, so a row that stays is never detached.
function oaKeyedRows(list,items,keyOf,create,update){
 var old=new Map(),children=Array.prototype.slice.call(list.children||[]);
 for(var i=0;i<children.length;i++){var c=children[i];if(c.dataset&&c.dataset.key!=null)old.set(c.dataset.key,c)}
 var rows=[];
 for(var j=0;j<items.length;j++){
  var k=String(keyOf(items[j],j)),r=old.get(k);
  if(!r){r=create(items[j],j);if(!r.dataset)r.dataset={};r.dataset.key=k}else old.delete(k);
  if(update)update(r,items[j],j);
  rows.push(r);
 }
 var gone=[];old.forEach(function(row){gone.push(row)});
 return oaPlaceRows(list,rows,gone);
}
// oaPlaceRows makes list hold rows in this order without detaching a row
// that stays: each gone row (by default every child not in rows) leaves
// with removeChild, and a row is inserted with insertBefore only when it is
// not already in its place. The control that had focus before keeps it: if
// it is still in the list but lost focus (a reorder moved its row), it is
// focused again without scrolling. A list without insertBefore (a minimal
// test fake) has its children replaced when the set or order changed.
// Device and Activity place their cached rows with it; oaKeyedRows uses it.
function oaPlaceRows(list,rows,gone){
 var children=Array.prototype.slice.call(list.children||[]);
 if(typeof list.insertBefore!=='function'){
  var same=rows.length===children.length;
  for(var n=0;same&&n<rows.length;n++)same=children[n]===rows[n];
  if(!same)list.replaceChildren.apply(list,rows);
  return rows;
 }
 var doc=typeof document!=='undefined'?document:null,active=doc&&doc.activeElement;
 if(!gone){gone=[];for(var g=0;g<children.length;g++)if(rows.indexOf(children[g])===-1)gone.push(children[g])}
 for(var i=0;i<gone.length;i++)if(gone[i].parentNode===list)list.removeChild(gone[i]);
 for(var m=0;m<rows.length;m++)if(list.children[m]!==rows[m])list.insertBefore(rows[m],list.children[m]||null);
 if(active&&doc.activeElement!==active&&oaWithin(list,active)&&typeof active.focus==='function')active.focus({preventScroll:true});
 return rows;
}
// oaWithin reports whether node sits inside root.
function oaWithin(root,node){for(var p=node;p;p=p.parentNode)if(p===root)return true;return false}
// oaSetStatus writes one sentence into a status line (oaStatus, oaRow) and
// its state: "loading" until data arrives, then "ok", "error" or "idle".
// oaShowError and oaShowResult set the state the same way.
function oaStatusState(el,state){
 if(!el.setAttribute)return;
 el.setAttribute('data-state',state);
 if(state==='loading')el.setAttribute('aria-busy','true');else if(el.removeAttribute)el.removeAttribute('aria-busy');
}
function oaSetStatus(id,state,sentence){
 var el=document.getElementById(id);
 if(!el)return;
 if(el.replaceChildren)el.replaceChildren();
 el.textContent=sentence==null?(state==='loading'?'Loading…':''):String(sentence);
 oaStatusState(el,state||'ok');
}
// oaTechnical marks a <details> a page builds as Technical details: its body
// holds the raw values kept out of the page's sentences, which the Panel
// rules spare from raw-value only (SPEC §6). It returns the element.
function oaTechnical(details){
 if(!details)return details;
 if(typeof details.setAttribute==='function')details.setAttribute('data-oa-technical','');
 // A page test's fake node that cannot read attributes back keeps the mark
 // as a property, where the Panel rules also look.
 if(typeof details.getAttribute!=='function')details['data-oa-technical']='true';
 return details;
}
function oaShowError(id,err){
 var el=document.getElementById(id);
 if(!el)return;
 oaStatusState(el,'error');
 var text=err&&typeof err==='object'&&'message'in err?err.message:err;
 var p=oaPlain(text);
 if(el.replaceChildren)el.replaceChildren();
 el.textContent=p.line;
 if(p.details&&p.details!==p.line){
  var details=document.createElement('details');details.className='oa-error-details';
  var summary=document.createElement('summary');summary.textContent='Technical details';
  var pre=document.createElement('pre');pre.textContent=p.details;
  details.append(summary,pre);
  el.append(details);
 }
}
// oaShowResult is oaShowError's own shape for a call that succeeded: one
// plain line, with any raw value the call returned (a revision, a receipt,
// a full response) kept off that line, on the element's own title and
// behind a Technical details disclosure instead (task 2026-09-23, tenth
// first-time visitor, finding 1: a raw {"Outcome":...} object, and lines
// like "Stored test-credential at revision 2-3a9e13ed", were visible text).
function oaShowResult(id,line,raw,title){
 var el=document.getElementById(id);
 if(!el)return;
 oaStatusState(el,'ok');
 if(el.replaceChildren)el.replaceChildren();
 el.textContent=line;
 el.title=title||'';
 if(raw!==undefined&&raw!==null){
  var details=document.createElement('details');details.className='oa-error-details';
  var summary=document.createElement('summary');summary.textContent='Technical details';
  var pre=document.createElement('pre');pre.textContent=typeof raw==='string'?raw:JSON.stringify(raw,null,2);
  details.append(summary,pre);
  el.append(details);
 }
}
</script>`
