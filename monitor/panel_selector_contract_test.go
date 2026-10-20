package main

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strings"
	"testing"
)

// The selector contract (research/panel-redesign/SPEC.md §6): S1 owns the
// class names, the skins (static/skin-*.css, S10 and S11) style only those.
// panelShellClasses is the list; SPEC.md §6 carries the same list between
// its shell-classes markers; the shell and the components emit nothing
// outside it.

var (
	cssComment     = regexp.MustCompile(`(?s)/\*.*?\*/`)
	cssRule        = regexp.MustCompile(`(?s)([^{}]+)\{[^{}]*\}`)
	cssQuoted      = regexp.MustCompile(`"[^"]*"|'[^']*'`)
	cssClass       = regexp.MustCompile(`\.([A-Za-z_][A-Za-z0-9_-]*)`)
	markupClass    = regexp.MustCompile(`class="([^"]*)"`)
	scriptClass    = regexp.MustCompile(`(?:className\s*=\s*|classList\.(?:add|remove|toggle)\()\s*'([^']*)'`)
	specClassBlock = regexp.MustCompile(`(?s)<!-- shell-classes -->(.*?)<!-- /shell-classes -->`)
	specClass      = regexp.MustCompile("`\\.([A-Za-z_][A-Za-z0-9_-]*)`")
	cssAttribute   = regexp.MustCompile(`\[\s*([A-Za-z_][A-Za-z0-9_:.-]*)`)
	specAttrBlock  = regexp.MustCompile(`(?s)<!-- shell-attributes -->(.*?)<!-- /shell-attributes -->`)
	specAttribute  = regexp.MustCompile("`\\[([A-Za-z_][A-Za-z0-9_:.-]*)\\]`")
)

// skinSelectorAttributes lists the attributes a stylesheet's selectors key
// on ([name], [name="value"], [name$=…]), sorted and without repeats.
func skinSelectorAttributes(css string) []string {
	seen := map[string]bool{}
	for _, m := range cssRule.FindAllStringSubmatch(cssComment.ReplaceAllString(css, ""), -1) {
		selector := strings.TrimSpace(m[1])
		if strings.HasPrefix(selector, "@") {
			continue
		}
		for _, a := range cssAttribute.FindAllStringSubmatch(cssQuoted.ReplaceAllString(selector, `""`), -1) {
			seen[a[1]] = true
		}
	}
	var out []string
	for a := range seen {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}

// strayAttributes are the attributes a stylesheet keys on outside the
// contract.
func strayAttributes(css string) []string {
	var stray []string
	for _, a := range skinSelectorAttributes(css) {
		if !slices.Contains(panelShellAttributes, a) {
			stray = append(stray, a)
		}
	}
	return stray
}

// skinSelectorClasses lists the class names a stylesheet's selectors use,
// sorted and without repeats; declarations, comments, at-rule preludes and
// quoted attribute values are not selectors.
func skinSelectorClasses(css string) []string {
	seen := map[string]bool{}
	for _, m := range cssRule.FindAllStringSubmatch(cssComment.ReplaceAllString(css, ""), -1) {
		selector := strings.TrimSpace(m[1])
		if strings.HasPrefix(selector, "@") {
			continue
		}
		for _, c := range cssClass.FindAllStringSubmatch(cssQuoted.ReplaceAllString(selector, `""`), -1) {
			seen[c[1]] = true
		}
	}
	var out []string
	for c := range seen {
		out = append(out, c)
	}
	sort.Strings(out)
	return out
}

// strayClasses are the classes a stylesheet styles outside the contract.
func strayClasses(css string) []string {
	var stray []string
	for _, c := range skinSelectorClasses(css) {
		if !slices.Contains(panelShellClasses, c) {
			stray = append(stray, c)
		}
	}
	return stray
}

// Every skin styles only the contract's classes. With no skin on main yet
// it checks nothing; a skin that rebases onto it is held to the list.
func TestSkinsStyleOnlyContractClasses(t *testing.T) {
	skins, err := filepath.Glob(filepath.Join("static", "skin-*.css"))
	if err != nil {
		t.Fatal(err)
	}
	if len(skins) == 0 {
		t.Log("no static/skin-*.css yet; nothing to judge")
	}
	for _, skin := range skins {
		raw, err := os.ReadFile(skin)
		if err != nil {
			t.Fatal(err)
		}
		if stray := strayClasses(string(raw)); len(stray) != 0 {
			t.Errorf("%s styles classes outside the shell contract (SPEC.md §6): %s; use a listed class, or ask S1 to add one", skin, strings.Join(stray, ", "))
		}
		if stray := strayAttributes(string(raw)); len(stray) != 0 {
			t.Errorf("%s keys on attributes outside the shell contract (SPEC.md §6): %s; use a listed attribute, or ask S1 to add one", skin, strings.Join(stray, ", "))
		}
	}
}

// The class list holds the shape setClasses accepts: classes, no empty name,
// and prefixes ending in "-" after at least one character.
func TestPanelClassesShape(t *testing.T) {
	if err := checkPanelClasses(panelShellClasses, panelShellClassPrefixes); err != nil {
		t.Fatalf("static/panel_classes.json: %v", err)
	}
	for name, c := range map[string]struct{ classes, prefixes []string }{
		"no classes":       {nil, nil},
		"an empty class":   {[]string{"oa-row", ""}, nil},
		"a bare prefix":    {[]string{"oa-row"}, []string{"-"}},
		"no trailing dash": {[]string{"oa-row"}, []string{"oa-icon"}},
	} {
		if checkPanelClasses(c.classes, c.prefixes) == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// The check itself catches a guessed class and ignores what is no selector.
func TestSelectorContractCatchesAGuess(t *testing.T) {
	css := `/* .oa-in-a-comment */
html[data-skin="windows"] .oa-nav-item.active::before, .oa-row :is(.oa-row-label, .oa-guess) { color: red; background: url(x.png); }
@media (prefers-color-scheme: dark) { .oa-settings-pinned { margin: 0.5em; } }
a[href$=".css"] { content: ".oa-quoted"; }
@keyframes spin { to { transform: rotate(1turn); } }`
	if got := strings.Join(skinSelectorClasses(css), ","); got != "active,oa-guess,oa-nav-item,oa-row,oa-row-label,oa-settings-pinned" {
		t.Fatalf("selector classes %s", got)
	}
	if got := strings.Join(strayClasses(css), ","); got != "oa-guess,oa-settings-pinned" {
		t.Fatalf("stray classes %s", got)
	}
	attrs := `html[data-skin="windows"][data-backdrop="mica"] .oa-nav-item[aria-current], a[href$=".css"], button[data-primary] { color: red; }
input[type="checkbox"]:checked, details[open] > summary { content: "[data-quoted]"; }`
	if got := strings.Join(skinSelectorAttributes(attrs), ","); got != "aria-current,data-backdrop,data-primary,data-skin,href,open,type" {
		t.Fatalf("selector attributes %s", got)
	}
	if got := strings.Join(strayAttributes(attrs), ","); got != "data-primary" {
		t.Fatalf("stray attributes %s", got)
	}
}

// panelShellAttributes and SPEC.md §6 name the same attributes.
func TestShellAttributeListMatchesSpec(t *testing.T) {
	raw, err := os.ReadFile(privateInput(t, "..", "research", "panel-redesign", "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	block := specAttrBlock.FindStringSubmatch(string(raw))
	if block == nil {
		t.Fatal("SPEC.md §6 has no shell-attributes block")
	}
	var spec []string
	for _, m := range specAttribute.FindAllStringSubmatch(block[1], -1) {
		spec = append(spec, m[1])
	}
	want := slices.Clone(panelShellAttributes)
	sort.Strings(spec)
	sort.Strings(want)
	if !slices.Equal(spec, want) {
		t.Fatalf("SPEC.md §6 lists %v, panelShellAttributes %v", spec, want)
	}
}

// panelShellClasses and SPEC.md §6 name the same classes. The public tree
// carries no research/ folder; there the comparison is skipped.
func TestShellClassListMatchesSpec(t *testing.T) {
	raw, err := os.ReadFile(privateInput(t, "..", "research", "panel-redesign", "SPEC.md"))
	if err != nil {
		t.Fatal(err)
	}
	block := specClassBlock.FindStringSubmatch(string(raw))
	if block == nil {
		t.Fatal("SPEC.md §6 has no shell-classes block")
	}
	var spec []string
	for _, m := range specClass.FindAllStringSubmatch(block[1], -1) {
		spec = append(spec, m[1])
	}
	want := slices.Clone(panelShellClasses)
	sort.Strings(spec)
	sort.Strings(want)
	if !slices.Equal(spec, want) {
		t.Fatalf("SPEC.md §6 lists %v, panelShellClasses %v", spec, want)
	}
}

// The shell and the components emit only listed classes, in markup and in
// the shared scripts, so the list is complete.
func TestShellEmitsOnlyListedClasses(t *testing.T) {
	fragment := oaPageHead("card", "Device", "One caption.") +
		oaGroup("g", "Group", "Caption.",
			oaRow("Label", "Caption", oaButton("b", "Go", true), "s"),
			oaRowNav("Next", "Caption", "/card"),
			oaExpander("More", oaStatus("t")),
			oaEmpty("Nothing."),
			oaTable("table", "A"),
		)
	var emitted []string
	for _, page := range []string{"card", "settings", "status"} {
		doc := panelPage(httptest.NewRequest("GET", "/?k=k", nil), "k", page, "", fragment)
		for _, m := range markupClass.FindAllStringSubmatch(doc, -1) {
			emitted = append(emitted, strings.Fields(m[1])...)
		}
	}
	for _, script := range []string{plainScript, navScript, navSearchScript, nativeShellScript} {
		for _, m := range scriptClass.FindAllStringSubmatch(script, -1) {
			emitted = append(emitted, strings.Fields(m[1])...)
		}
	}
	for _, c := range emitted {
		if !slices.Contains(panelShellClasses, c) {
			t.Errorf("the shell emits %q, which SPEC.md §6 does not list", c)
		}
	}
	for _, c := range []string{"oa-primary", "oa-row-chevron", "oa-nav-badge", "oa-nav-sep", "oa-row-label", "oa-row-caption"} {
		if !slices.Contains(emitted, c) {
			t.Errorf("the shell no longer emits %q", c)
		}
	}
}
