package main

import (
	"context"
	"encoding/json"
	"html"
	"io/fs"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// The Panel holds its operator rules from installation (serve
// runtime_rights.go, installationRules) and asks the person for none of them
// (hostile critic A4): no rendered page carries a grant control, and no
// page's visible text names a rights action or a rule's application resource.
// Ids stay in title attributes and scripts, which this strips.
func TestNoPageGrantsRulesToThePanelOrShowsARuleID(t *testing.T) {
	pages := map[string]string{"setup": setupPage}
	for name, fragment := range pageFragments {
		pages[name] = fragment
	}
	scripts := regexp.MustCompile(`(?s)<script>.*?</script>`)
	tags := regexp.MustCompile(`(?s)<[^>]*>`)
	ruleID := regexp.MustCompile(`abstraction\.[a-z][a-z0-9_-]*/[a-z][a-z0-9_.]*|\bapp:[a-z]`)
	for name, fragment := range pages {
		req := httptest.NewRequest("GET", "/?k=test", nil)
		doc := panelPage(req, "test", name, "OpenAbstractions", fragment)
		for _, control := range []string{"data-grant-rules", `class="oa-grant"`, "oaGrant"} {
			if strings.Contains(doc, control) {
				t.Errorf("%s page carries the grant control %q", name, control)
			}
		}
		text := html.UnescapeString(tags.ReplaceAllString(scripts.ReplaceAllString(doc, " "), " "))
		if id := ruleID.FindString(text); id != "" {
			t.Errorf("%s page text names the rule id %q", name, id)
		}
	}
}

// Every choice stays visible on each page. A person can open Permissions or
// any support page without expanding another navigation control.
func TestNavigationKeepsEveryChoiceVisible(t *testing.T) {
	const key = "panel-test-key"
	nav := panelNav(key, "status")
	// The search hits after the items name section titles, one of which is
	// a page's own Advanced section; the items themselves have no group.
	items := regexp.MustCompile(` data-search="[^"]*"`).ReplaceAllString(nav[:strings.Index(nav, `class="oa-nav-hits"`)], "")
	if strings.Contains(nav, "<details") || strings.Contains(items, "Advanced") {
		t.Fatalf("navigation hides choices in a group: %s", nav)
	}
	labels := regexp.MustCompile(`<span class="oa-nav-label">([^<]*)</span>`)
	var choices []string
	for _, m := range labels.FindAllStringSubmatchIndex(nav, -1) {
		choices = append(choices, nav[m[2]:m[3]])
	}
	if strings.Join(choices, ",") != "Get started,Apps,Models,Permissions,Activity,Device,Providers,Credentials,Settings" {
		t.Fatalf("visible choices %v", choices)
	}
	// One separator, before Providers (SPEC §4).
	if strings.Count(nav, `class="oa-nav-sep"`) != 1 || strings.Index(nav, `class="oa-nav-sep"`) > strings.Index(nav, `data-id="registry"`) || strings.Index(nav, `class="oa-nav-sep"`) < strings.Index(nav, `data-id="card"`) {
		t.Fatalf("the separator is not between Device and Providers: %s", nav)
	}
	if !strings.Contains(nav, `data-badge="rights"`) {
		t.Fatal("Permissions carries no badge for waiting questions")
	}
	for _, path := range []string{"/setup", "/", "/rights", "/inference", "/work", "/card", "/registry", "/credentials-page", "/settings"} {
		if !strings.Contains(nav, `href="`+path+`?k=panel-test-key"`) {
			t.Errorf("navigation lost %s", path)
		}
	}
	for _, path := range []string{"/identity", "/logging", "/explore", "/connect-deepseek"} {
		if strings.Contains(nav, `href="`+path+`?`) {
			t.Errorf("navigation still lists %s", path)
		}
	}
	if selected := panelNav(key, "registry"); !strings.Contains(selected, `class="oa-nav-item active" href="/registry?k=`) {
		t.Fatal("the current provider page is not marked")
	}
	setup := panelPage(httptest.NewRequest("GET", "/setup?k="+key, nil), key, "setup", "OpenAbstractions Setup", setupPage)
	if !strings.Contains(setup, `class="oa-nav-item active" href="/setup?k=`) || !strings.Contains(setup, `aria-label="Panel pages"`) {
		t.Fatal("Get started does not use the shared navigation")
	}
}

// Each task item also answers at a task-named path, and keeps the Panel key.
func TestTaskPathsRedirectToTheirPages(t *testing.T) {
	handler := (&servicePanel{}).handler("panel-test-key")
	for alias, target := range map[string]string{"/apps": "/", "/questions": "/rights", "/models": "/inference", "/activity": "/work", "/device": "/card", "/get-started": "/setup", "/logging": "/settings", "/identity": "/settings", "/permissions": "/rights", "/providers": "/registry"} {
		req := httptest.NewRequest("GET", alias+"?k=panel-test-key", nil)
		req.Host = "127.0.0.1"
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		if got.Code != 302 || got.Header().Get("Location") != target+"?k=panel-test-key" {
			t.Errorf("%s: %d to %q", alias, got.Code, got.Header().Get("Location"))
		}
	}
}

// GET /panel/nav.json is the shell contract S10 and S11 build against
// (research/panel-redesign/SPEC.md §6): exactly these fields, sidebar order,
// group main or more, a badge only on Permissions and only when questions
// wait, and the same key as the pages.
func TestNavJSONFollowsTheShellContract(t *testing.T) {
	const key = "nav-test-key"
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "setup", "plan.json")}
	handler := (&servicePanel{setupJournal: &journal}).handler(key)
	waiting, known := 3, true
	saved := panelWaitingCount
	panelWaitingCount = func(context.Context) (int, bool) { return waiting, known }
	t.Cleanup(func() { panelWaitingCount = saved })
	savedApps := panelRegisteredApps
	panelRegisteredApps = func(context.Context) (int, bool) { return 1, true }
	t.Cleanup(func() { panelRegisteredApps = savedApps })
	get := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("GET", target, nil)
		req.Host = "127.0.0.1"
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		return got
	}
	if got := get("/panel/nav.json"); got.Code != 403 {
		t.Fatalf("nav.json without the key: %d", got.Code)
	}
	got := get("/panel/nav.json?k=" + key)
	if got.Code != 200 {
		t.Fatalf("nav.json: %d %s", got.Code, got.Body.String())
	}
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(got.Body.Bytes(), &raw); err != nil || len(raw) != 2 || raw["items"] == nil || raw["selected"] == nil {
		t.Fatalf("top level %s", got.Body.String())
	}
	var items []map[string]json.RawMessage
	if err := json.Unmarshal(raw["items"], &items); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, item := range items {
		var id, group string
		var glyph map[string]string
		_ = json.Unmarshal(item["id"], &id)
		_ = json.Unmarshal(item["group"], &group)
		_ = json.Unmarshal(item["glyph"], &glyph)
		ids = append(ids, id)
		for field := range item {
			switch field {
			case "id", "label", "path", "group", "glyph", "badge":
			default:
				t.Errorf("%s carries %q, outside the contract", id, field)
			}
		}
		if item["label"] == nil || item["path"] == nil || (group != "main" && group != "more") || len(glyph) != 2 || glyph["segoe"] == "" || glyph["sf"] == "" {
			t.Errorf("item %s: group %q glyph %v", id, group, glyph)
		}
		if _, has := item["badge"]; has != (id == "rights") {
			t.Errorf("item %s badge present = %v", id, has)
		}
	}
	if strings.Join(ids, ",") != "setup,status,inference,rights,work,card,registry,credentials,settings" {
		t.Fatalf("order %v", ids)
	}
	var view navJSON
	_ = json.Unmarshal(got.Body.Bytes(), &view)
	if view.Selected != "status" || view.Items[3].Badge == nil || *view.Items[3].Badge != 3 || view.Items[6].Group != "more" || view.Items[5].Group != "main" {
		t.Fatalf("view %+v", view)
	}
	if err := json.Unmarshal(get("/panel/nav.json?k="+key+"&path=/questions").Body.Bytes(), &view); err != nil || view.Selected != "rights" {
		t.Fatalf("selected for /questions: %q %v", view.Selected, err)
	}
	for _, none := range []struct {
		n  int
		ok bool
	}{{0, true}, {5, false}} {
		waiting, known = none.n, none.ok
		view = navJSON{}
		_ = json.Unmarshal(get("/panel/nav.json?k="+key).Body.Bytes(), &view)
		if len(view.Items) != len(panelNavItems) || view.Items[3].Badge != nil {
			t.Errorf("waiting %d known %v: items %+v", none.n, none.ok, view.Items)
		}
	}
}

// ?shell=native leaves navigation and the page h1 to the host; the document
// still names its page label, carries data-skin and data-shell, and keeps
// the parameter on its own links (SPEC §6).
func TestNativeShellLeavesNavigationAndTitleToTheHost(t *testing.T) {
	const key = "native-test-key"
	pages := map[string]string{"setup": setupPage}
	for name, fragment := range pageFragments {
		pages[name] = fragment
	}
	for name, fragment := range pages {
		native := panelPage(httptest.NewRequest("GET", "/x?k="+key+"&shell=native&platform=macos", nil), key, name, "fallback", fragment)
		if strings.Contains(native, `class="oa-nav"`) || strings.Contains(native, "<h1") || !strings.Contains(native, `data-shell="native"`) || !strings.Contains(native, `data-skin="macos"`) || !strings.Contains(native, "oaSetAccent") {
			t.Errorf("%s: native shell kept the navigation or the h1, or lost its attributes", name)
		}
		plain := panelPage(httptest.NewRequest("GET", "/x?k="+key+"&platform=linux", nil), key, name, "fallback", fragment)
		if !strings.Contains(plain, `class="oa-nav"`) || !strings.Contains(plain, `data-skin="web"`) || !strings.Contains(plain, `data-shell="html"`) {
			t.Errorf("%s: the HTML shell lost its navigation or skin", name)
		}
		if item, ok := navItemByID(name); ok {
			for _, doc := range []string{native, plain} {
				if !strings.Contains(doc, "<title>"+item.Label+"</title>") {
					t.Errorf("%s: document title is not the page label %q", name, item.Label)
				}
			}
		}
	}
	if doc := panelPage(httptest.NewRequest("GET", "/x?k="+key+"&platform=windows", nil), key, "card", "", cardPage); !strings.Contains(doc, `data-skin="windows"`) {
		t.Error("Windows renders without the windows skin")
	}
	if doc := panelPage(httptest.NewRequest("GET", "/x?k="+key+"&platform=windows&skin=web", nil), key, "card", "", cardPage); !strings.Contains(doc, `data-skin="web"`) {
		t.Error("the screenshot tool cannot name the web skin")
	}
	head := oaPageHead("card", "Device", "This device's graphics card and sleep.")
	native := nativeFragment(head + "<p>rest</p>")
	if strings.Contains(native, "<h1") || !strings.Contains(native, `<div class="oa-pagehead-title">Device</div>`) {
		t.Errorf("a page head in the native shell: %s", native)
	}
}

// No program chooser is fed the graphics card's holders: those are running
// processes, csrss and dwm among them (SPEC §1, §7). oaProgramChooser reads
// the registered applications itself (panel_plain_page_test.js).
func TestProgramChoosersReadNoRunningProcesses(t *testing.T) {
	feed := regexp.MustCompile(`\.holders\|\|\[\]\)\)if\(h\.program\)[A-Za-z]*\.add|for\(const h of\(r\.holders[^\n]*Chooser\.rebuild`)
	pages := map[string]string{"setup": setupPage, "shell": plainScript}
	for name, fragment := range pageFragments {
		pages[name] = fragment
	}
	for name, fragment := range pages {
		if m := feed.FindString(fragment); m != "" {
			t.Errorf("%s feeds running processes to a program chooser: %s", name, m)
		}
	}
	if strings.Contains(plainScript, "/card/table") || !strings.Contains(plainScript, "/apps/registrations?source=applications") {
		t.Error("oaProgramChooser does not read the registered applications")
	}
}

// The floating help button never covers the last row: where it shows, the
// content column reserves its size and gap twice over below the last row,
// and the button is drawn from the same two sizes. Every page with help
// anchors puts it directly in the content column, where the rule finds it.
func TestHelpFloatLeavesRoomForTheLastRow(t *testing.T) {
	blocks := cssBlocks(t)
	reserve := declarationFor(t, blocks, `html:is([data-platform="macos"], [data-platform="linux"], [data-skin="macos"]) .oa-content:has(> .oa-help-float)`)
	if !strings.Contains(reserve, "padding-bottom: calc(var(--oa-help-float-size) + 2 * var(--oa-help-float-gap))") {
		t.Errorf("the content column reserves no room for the help button: %s", reserve)
	}
	float := declarationFor(t, blocks, "html[data-platform=\"macos\"] .oa-help-float,\nhtml[data-platform=\"linux\"] .oa-help-float")
	for _, want := range []string{"width: var(--oa-help-float-size)", "bottom: var(--oa-help-float-gap)", "right: var(--oa-help-float-gap)"} {
		if !strings.Contains(float, want) {
			t.Errorf("the help button is not drawn from the reserved sizes (%s): %s", want, float)
		}
	}
	doc := panelPage(httptest.NewRequest("GET", "/rights?k=k&platform=macos", nil), "k", "rights", "", rightsPage)
	if !regexp.MustCompile(`<a class="oa-help-float"[^>]*>\?</a></main>`).MatchString(doc) {
		t.Error("the help button is not the content column's own last child")
	}
	// The macOS skin draws a 22 px button 20 px from the corner; the reserve
	// (20 + 2 × 24 = 68 px) covers it (22 + 2 × 20 = 62 px).
}

// The shared components carry the structure SPEC §5 names: a page head
// with tile, title and one caption; groups of rows, each row with its
// status line; navigation rows with a chevron; expanders; a status line
// that starts loading; one-sentence empty states; tables in their wrapper.
func TestComponentsCarryTheirStructure(t *testing.T) {
	fragment := oaPageHead("card", "Device", "This device's graphics card and sleep.") +
		oaGroup("awake", "Keeping this device awake", "Apps keeping this device from sleeping.",
			oaRow("Keep awake while downloading", "", `<input type="checkbox" class="switch" id="keep">`, "keep-status"),
			oaRowNav("Graphics card details", "Who uses it now.", "/card#holders"),
			oaExpander("Technical details", oaStatus("tech-status")+`<button id="read">Read again</button>`),
			oaEmpty("No app keeps this device awake."),
			oaTable("awake-table", "App", "Since"),
		)
	for _, want := range []string{`<header class="oa-pagehead"><span class="oa-pagehead-tile oa-icon-card">`, `<h1 class="oa-pagehead-title">Device</h1>`,
		`<section id="awake" class="oa-group"><h2>Keeping this device awake</h2><p class="oa-caption">`, `<div class="oa-row">`,
		`<p class="oa-status" id="keep-status" role="status"`, `<a class="oa-row oa-row-nav" href="/card#holders">`, `class="oa-row-chevron"`,
		`<details class="oa-expander"><summary>Technical details<span class="oa-row-chevron" aria-hidden="true"></span></summary>`, `id="tech-status" role="status" aria-live="polite" data-state="loading" aria-busy="true">Loading…`,
		`<p class="oa-empty">No app keeps this device awake.</p>`, `<div class="oa-table"><table id="awake-table"><thead><tr><th>App</th><th>Since</th>`} {
		if !strings.Contains(fragment, want) {
			t.Errorf("components lack %s", want)
		}
	}
	if got := oaRow("<b>", "", "", ""); strings.Contains(got, "<b>") || strings.Contains(got, "oa-status") {
		t.Errorf("a row escapes its label and has no status line without an id: %s", got)
	}
	blocks := cssBlocks(t)
	if decl := declarationFor(t, blocks, "[hidden]"); !strings.Contains(decl, "display: none !important") {
		t.Errorf("[hidden] does not win over a component's display: %s", decl)
	}
	for _, selector := range []string{".oa-pagehead", ".oa-row", ".oa-row-text", ".oa-row-control", "details.oa-expander", ".oa-status", `.oa-status[data-state="loading"]`, "p.oa-empty", ".oa-table"} {
		declarationFor(t, blocks, selector)
	}
}

// The root carries the accent and its two shades from panelAccent, the
// default blue unless a host sets it, each only as a well-formed colour.
func TestAccentHookTakesOnlyColours(t *testing.T) {
	rootFor := func(skin string) string {
		doc := panelPage(httptest.NewRequest("GET", "/?k=k&skin="+skin, nil), "k", "status", "", servicePage)
		return doc[:strings.Index(doc, "<head>")]
	}
	root := func() string { return rootFor("windows") }
	saved := panelAccent
	t.Cleanup(func() { panelAccent = saved })
	// On Windows the host's init (panel_accent_windows.go) sets the hook from
	// the registry, which TestWindowsAccentReachesTheRoot checks; here the
	// hook is pinned to the shell's own default.
	panelAccent = defaultAccent
	if got := root(); !strings.Contains(got, ` style="--oa-accent:#0078D4;--oa-accent-light2:#60CDFF;--oa-accent-dark1:#005FB8"`) {
		t.Fatalf("default accent: %s", got)
	}
	// The macOS and web skins keep the stylesheet's system blue; the macOS
	// host injects the real accent itself.
	for _, skin := range []string{"macos", "web"} {
		if got := rootFor(skin); strings.Contains(got, "style=") {
			t.Errorf("%s skin carries an inline accent: %s", skin, got)
		}
	}
	for colors, want := range map[accentColors]string{
		{"#A1B2C3", "#D4E5F6", "#102030"}:   ` style="--oa-accent:#A1B2C3;--oa-accent-light2:#D4E5F6;--oa-accent-dark1:#102030"`,
		{"red;}body{", "#D4E5F6", "#12345"}: ` style="--oa-accent-light2:#D4E5F6"`,
		{}:                                  "",
	} {
		panelAccent = func() accentColors { return colors }
		got := root()
		if want == "" && strings.Contains(got, "style=") || want != "" && !strings.Contains(got, want) {
			t.Errorf("accent %+v gave root %s", colors, got)
		}
	}
}

// Every page carries the shell's fallback; whether it marks the page is
// decided in the browser from the parsed document (panel_ready_test.js), so
// neither a comment nor a script string naming oaReady or the owner marker
// changes the served page. oaReady sets "1"; the fallback sets "load".
func TestShellReadyFallbackIsAlwaysServed(t *testing.T) {
	req := httptest.NewRequest("GET", "/card?k=k", nil)
	for _, fragment := range []string{
		`<p>Static.</p>`,
		`<!-- oaReady() data-ready-owner="page" --><p>Static.</p>`,
		`<div data-ready-owner="page"></div><script>load().then(()=>oaReady())</script>`,
	} {
		if doc := panelPage(req, "k", "card", "", fragment); strings.Count(doc, readyScript) != 1 {
			t.Errorf("fragment %q: readyScript served %d times", fragment, strings.Count(doc, readyScript))
		}
	}
	if !strings.Contains(plainScript, "if(root&&root.dataset)root.dataset.ready='1';") {
		t.Error("oaReady does not set data-ready=1 on the root")
	}
	if !strings.Contains(readyScript, "setAttribute('data-ready','load')") || strings.Contains(readyScript, "oaReady()") {
		t.Error(`the fallback must set data-ready="load", never call oaReady`)
	}
}

// TestWriteReadyFixtures writes two served shell pages for the composed
// readiness test in scripts/panel_screenshots_test.py when
// PANEL_READY_FIXTURES names a folder: one with a slow fetch and no oaReady,
// one that owns its readiness, holds a long-poll open and calls oaReady,
// and one that owns its readiness but never calls oaReady.
func TestWriteReadyFixtures(t *testing.T) {
	dir := os.Getenv("PANEL_READY_FIXTURES")
	if dir == "" {
		t.Skip("set PANEL_READY_FIXTURES to write the composed readiness pages")
	}
	req := httptest.NewRequest("GET", "/card?k=k", nil)
	pages := map[string]string{
		"fallback.html": `<p class="oa-status" data-state="idle" id="s">Reading.</p>` +
			`<script>fetch('/slow-data').then(function(r){return r.text()}).then(function(){window.fetched=true;document.getElementById('s').textContent='Read.'})</script>`,
		"owner.html": `<div data-ready-owner="page"></div><p id="s">Watching.</p>` +
			`<script>fetch('/hang');setTimeout(function(){oaReady()},300)</script>`,
		"silent-owner.html": `<div data-ready-owner="page"></div><p id="s">Never ready.</p>`,
	}
	for name, fragment := range pages {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(panelPage(req, "k", "card", "", fragment)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The shell links static/skin-<skin>.css after panel.css for the windows and
// macos skins when the file is embedded, links nothing when it is not, and
// serves every embedded stylesheet under /static/.
func TestShellLinksTheSkinWhenItExists(t *testing.T) {
	saved := panelStatic
	t.Cleanup(func() { panelStatic = saved })
	page := func(skin string) string {
		doc := panelPage(httptest.NewRequest("GET", "/card?k=k&skin="+skin, nil), "k", "card", "", cardPage)
		return doc[:strings.Index(doc, "</head>")]
	}
	panelStatic = fstest.MapFS{"static/panel.css": {Data: []byte("p{}")}}
	for _, skin := range []string{"windows", "macos", "web"} {
		if head := page(skin); strings.Contains(head, "skin-") {
			t.Errorf("%s: a skin is linked with no skin file: %s", skin, head)
		}
	}
	panelStatic = fstest.MapFS{"static/panel.css": {Data: []byte("p{}")}, "static/skin-windows.css": {Data: []byte("w{}")}, "static/skin-macos.css": {Data: []byte("m{}")}, "static/skin-web.css": {Data: []byte("x{}")}}
	for skin, want := range map[string]string{"windows": "skin-windows.css", "macos": "skin-macos.css", "web": ""} {
		head := page(skin)
		link := `<link rel="stylesheet" href="/static/panel.css"><link rel="stylesheet" href="/static/` + want + `">`
		if want != "" && !strings.Contains(head, link) || want == "" && strings.Contains(head, "skin-") {
			t.Errorf("%s: head %s", skin, head)
		}
	}
	handler := (&servicePanel{}).handler("k")
	for target, code := range map[string]int{"/static/panel.css": 200, "/static/skin-macos.css": 200, "/static/skin-linux.css": 404, "/static/sub/panel.css": 404, "/static/panel_rules.js": 404} {
		req := httptest.NewRequest("GET", target, nil)
		req.Host = "127.0.0.1"
		got := httptest.NewRecorder()
		handler.ServeHTTP(got, req)
		if got.Code != code {
			t.Errorf("GET %s: %d, want %d", target, got.Code, code)
		}
	}
	panelStatic = saved
	if _, err := fs.Stat(panelStatic, "static/panel.css"); err != nil {
		t.Fatalf("panel.css is not embedded: %v", err)
	}
}

// The sidebar search reads the same table: page labels plus the titles of
// the sections each page shows on arrival, each section a link to its anchor.
func TestSearchIndexHoldsPageAndSectionTitles(t *testing.T) {
	sections := navSections()
	titles := func(id string) (out []string) {
		for _, s := range sections[id] {
			out = append(out, s.Title)
		}
		return out
	}
	if !slices.Contains(titles("rights"), "Saved permissions") || !slices.Contains(titles("card"), "Keeping this device awake") || !slices.Contains(titles("settings"), "About") {
		t.Fatalf("sections %v", sections)
	}
	if slices.Contains(titles("setup"), "Installing") {
		t.Error("a section hidden on arrival is in the search index")
	}
	nav := panelNav("k", "status")
	if !strings.Contains(nav, `href="/rights?k=k#rights-section"`) || !strings.Contains(nav, `type="search"`) || !strings.Contains(nav, `data-search="Permissions Questions waiting`) {
		t.Fatalf("search hits missing: %s", nav)
	}
}

// Every page serves the one plain-phrase table ahead of the shared script,
// so a permission refusal names its action by phrase, never by its id.
func TestPanelPageServesActionPlainTable(t *testing.T) {
	doc := panelPage(httptest.NewRequest("GET", "/models?k=k", nil), "k", "models", "Models", `<p>Static.</p>`)
	table, plain := strings.Index(doc, actionPlainScript), strings.Index(doc, plainScript)
	if table < 0 || plain < 0 || table > plain {
		t.Fatalf("table at %d, shared script at %d: the table must come first", table, plain)
	}
	if !strings.Contains(actionPlainScript, `"abstraction.router/inventory.read":"router servers"`) {
		t.Errorf("the table lacks the router inventory phrase: %s", actionPlainScript)
	}
	if !strings.Contains(plainScript, "' It needs the “'+oaActionPhrase(rule)+'” permission.'") {
		t.Error("the refusal sentence must name its action through oaActionPhrase")
	}
}
