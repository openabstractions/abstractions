package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"html"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// The Panel rules lint (research/panel-redesign/SPEC.md §4 and §6, S1; the
// owner's Panel rules of 2026-10-07). It reads every page's rendered HTML
// as served on arrival and reports:
//
//   - empty-input: a visible text input or textarea with no value. A secret
//     field (type=password, a password autocomplete or data-oa-secret), the
//     search field and an "Other…" disclosure's input (the second argument
//     of oaProgramChooser or oaAccountChooser, or data-oa-other) are
//     excepted.
//   - button-status: a button with no status line to write its one sentence
//     into: none inside its row (oa-row), else none inside its section, and
//     no data-status naming one. A status line is role="status", aria-live,
//     class oa-status or an <output>.
//   - caption: a second (third, …) caption in one section, or on the page
//     outside its sections. A caption is a paragraph with its own text that
//     is no status line and no empty state.
//   - raw-value: visible text or a placeholder shaped like a file path, a
//     Windows security identifier or a hash. A title attribute and text
//     inside a <details> disclosure (Technical details) are not visible
//     page text.
//   - class:<name>: the page class contract. A page's markup, className
//     assignments and classList calls use only the classes SPEC.md §6
//     lists (panelShellClasses); each other class is ruled by its name.
//
// The raw-value shapes come from static/panel_rules.js, which page tests
// apply to the DOM their script rendered (assertPanelRules); this test reads
// the HTML the server sends, before any script runs.
//
// testdata/panel_lint.allowlist records the violations each page had when
// the lint landed, per page and rule; the page slices S2–S9 remove them.
// Like scripts/app_names.py, the counts must equal the allowlist (NEW, GREW,
// SHRANK and GONE fail), and a row may not exceed the same row at the merge
// base with origin/main (RAISED). Set PANEL_LINT_LIST=1 to log every
// violation with go test -v.

const panelLintAllowlist = "testdata/panel_lint.allowlist"

type panelLintViolation struct {
	Rule   string
	Detail string
}

func (v panelLintViolation) String() string { return v.Rule + ": " + v.Detail }

// lintNode is one element or text node of the small HTML tree the lint
// reads. Text nodes have an empty tag.
type lintNode struct {
	tag      string
	attrs    map[string]string
	text     string
	parent   *lintNode
	children []*lintNode
}

func (n *lintNode) has(attr string) bool { _, ok := n.attrs[attr]; return ok }

func (n *lintNode) class(name string) bool {
	for _, c := range strings.Fields(n.attrs["class"]) {
		if c == name {
			return true
		}
	}
	return false
}

var (
	lintToken = regexp.MustCompile(`(?is)<!--.*?-->|<!doctype[^>]*>|<(script|style|textarea)\b([^>]*)>(.*?)</(?:script|style|textarea)\s*>|</?[a-zA-Z][^>]*>|[^<]+|<`)
	lintTag   = regexp.MustCompile(`(?s)^<(/?)([a-zA-Z][a-zA-Z0-9-]*)(.*?)(/?)>$`)
	lintAttr  = regexp.MustCompile(`([^\s"'=<>/]+)(?:\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s"'=<>` + "`" + `]+)))?`)
	lintVoid  = map[string]bool{"area": true, "base": true, "br": true, "col": true, "embed": true, "hr": true, "img": true, "input": true, "link": true, "meta": true, "source": true, "track": true, "wbr": true}
	// Opening one of these closes an open paragraph (HTML's implied </p>).
	lintClosesP       = map[string]bool{"address": true, "article": true, "aside": true, "blockquote": true, "details": true, "div": true, "dl": true, "fieldset": true, "footer": true, "form": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "header": true, "hr": true, "main": true, "nav": true, "ol": true, "p": true, "pre": true, "section": true, "table": true, "ul": true}
	lintSiblingCloses = map[string][]string{"li": {"li"}, "option": {"option"}, "tr": {"tr", "td", "th"}, "td": {"td", "th"}, "th": {"td", "th"}}
)

func lintAttrs(raw string) map[string]string {
	attrs := map[string]string{}
	for _, m := range lintAttr.FindAllStringSubmatch(raw, -1) {
		attrs[strings.ToLower(m[1])] = html.UnescapeString(m[2] + m[3] + m[4])
	}
	return attrs
}

// parseLintHTML builds the tree with HTML's few implied end tags; it is a
// reader for this Panel's own markup, not a general HTML parser.
func parseLintHTML(doc string) *lintNode {
	root := &lintNode{tag: "#root", attrs: map[string]string{}}
	stack := []*lintNode{root}
	top := func() *lintNode { return stack[len(stack)-1] }
	add := func(n *lintNode) { n.parent = top(); top().children = append(top().children, n) }
	for _, m := range lintToken.FindAllStringSubmatchIndex(doc, -1) {
		token := doc[m[0]:m[1]]
		switch {
		case strings.HasPrefix(token, "<!"):
			continue
		case m[2] >= 0: // script, style or textarea with its raw content
			n := &lintNode{tag: strings.ToLower(doc[m[2]:m[3]]), attrs: lintAttrs(doc[m[4]:m[5]])}
			n.children = []*lintNode{{text: html.UnescapeString(doc[m[6]:m[7]]), parent: n}}
			add(n)
			continue
		}
		t := lintTag.FindStringSubmatch(token)
		if t == nil {
			add(&lintNode{text: html.UnescapeString(token)})
			continue
		}
		name := strings.ToLower(t[2])
		if t[1] == "/" {
			for i := len(stack) - 1; i > 0; i-- {
				if stack[i].tag == name {
					stack = stack[:i]
					break
				}
			}
			continue
		}
		if lintClosesP[name] && top().tag == "p" {
			stack = stack[:len(stack)-1]
		}
		for _, sibling := range lintSiblingCloses[name] {
			if top().tag == sibling {
				stack = stack[:len(stack)-1]
				break
			}
		}
		n := &lintNode{tag: name, attrs: lintAttrs(t[3])}
		add(n)
		if !lintVoid[name] && t[4] != "/" {
			stack = append(stack, n)
		}
	}
	return root
}

func lintWalk(n *lintNode, visit func(*lintNode)) {
	visit(n)
	for _, c := range n.children {
		lintWalk(c, visit)
	}
}

// lintHidden reports a node not shown on arrival: under hidden, a template,
// a script or style, or the document head.
func lintHidden(n *lintNode) bool {
	for p := n; p != nil; p = p.parent {
		if p.has("hidden") || p.tag == "template" || p.tag == "script" || p.tag == "style" || p.tag == "head" {
			return true
		}
	}
	return false
}

// lintTechnicalSummary is the text of a details' summary. Only an expander
// whose summary reads "Technical details" may carry data-oa-technical (SPEC
// §6, the same rule as panel_rules.js).
func lintTechnicalSummary(n *lintNode) string {
	for _, c := range n.children {
		if c.tag == "summary" {
			return lintOwnText(c)
		}
	}
	return ""
}

// lintInTechnicalBody reports a node in the body of a Technical details
// disclosure: a <details> marked data-oa-technical, or oaShowError's
// details.oa-error-details (SPEC §6, the same rule as panel_rules.js). Only
// raw-value spares it. Its summary, and every other expander's body, open
// or closed, is judged like the rest of the page.
func lintInTechnicalBody(n *lintNode) bool {
	prev := n
	for p := n.parent; p != nil; p = p.parent {
		if p.tag == "details" && (p.has("data-oa-technical") || p.class("oa-error-details")) && prev.tag != "summary" {
			return true
		}
		prev = p
	}
	return false
}

func lintInside(n *lintNode, tag string) bool {
	for p := n.parent; p != nil; p = p.parent {
		if p.tag == tag {
			return true
		}
	}
	return false
}

// lintSecretScopeValid reports whether data-oa-secret may stand on this
// element: an input, textarea, code or output with no child elements (the
// same rule as static/panel_rules.js; SPEC §6: an exemption attribute
// applies to its own element and only to the shape it exists for).
func lintSecretScopeValid(n *lintNode) bool {
	switch n.tag {
	case "input", "textarea", "code", "output":
	default:
		return false
	}
	for _, c := range n.children {
		if c.tag != "" {
			return false
		}
	}
	return true
}

// lintSecret reports a secret field, the same definition as panel_rules.js's
// isSecret: a valid data-oa-secret, a password input, or an input or
// textarea with a password or one-time-code autocomplete. For a text node it
// asks of the element holding the text. A secret field is exempt from
// empty-input, and its own text and placeholder from the hash shape only.
func lintSecret(n *lintNode) bool {
	if n.tag == "" {
		n = n.parent
	}
	if n == nil {
		return false
	}
	if n.has("data-oa-secret") {
		return lintSecretScopeValid(n)
	}
	if n.tag == "input" && n.attrs["type"] == "password" {
		return true
	}
	auto := n.attrs["autocomplete"]
	return (n.tag == "input" || n.tag == "textarea") && (auto == "current-password" || auto == "new-password" || auto == "one-time-code")
}

func lintIsStatus(n *lintNode) bool {
	live, hasLive := n.attrs["aria-live"]
	return n.attrs["role"] == "status" || (hasLive && live != "off") || n.class("oa-status") || n.tag == "output"
}

func lintHasStatus(n *lintNode) bool {
	found := false
	lintWalk(n, func(c *lintNode) { found = found || (c.tag != "" && lintIsStatus(c)) })
	return found
}

// lintOwnText is a node's text without the text of the controls inside it.
func lintOwnText(n *lintNode) string {
	var b strings.Builder
	var walk func(*lintNode)
	walk = func(c *lintNode) {
		if c.tag == "" {
			b.WriteString(c.text)
			return
		}
		switch c.tag {
		case "button", "select", "option", "textarea", "label", "script", "style":
			return
		}
		for _, k := range c.children {
			walk(k)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

func lintIsCaption(n *lintNode) bool {
	return n.tag == "p" && !lintIsStatus(n) && !n.class("oa-empty") && !n.has("hidden") && lintOwnText(n) != ""
}

var (
	otherInputCall = regexp.MustCompile(`oa(?:Program|Account)Chooser\(\s*'[^']*'\s*,\s*'([^']+)'`)
	jsShapesBlock  = regexp.MustCompile(`(?s)var SHAPES = \{\n(.*?)\n\s*\};`)
	jsShapeKey     = regexp.MustCompile(`(?m)^\s*([A-Za-z_$][A-Za-z0-9_$]*)\s*:`)
	jsShapeLine    = regexp.MustCompile(`(?m)^\s*([A-Za-z_$][A-Za-z0-9_$]*): /(.+)/([A-Za-z]*),\s*$`)
)

type rawValueShape struct {
	name    string
	pattern *regexp.Regexp
}

// matches is the shape's matches in text, with panel_rules.js's shapeGuard:
// a program match followed by "/" (a URL path), by ":" and a digit (a
// port), or right after "://" (a URL's host) is an address, not a file name.
// The Go and JS cross-check holds the two equal.
func (s rawValueShape) matches(text string) []string {
	var out []string
	for _, loc := range s.pattern.FindAllStringIndex(text, -1) {
		if s.name == "program" {
			rest := text[loc[1]:]
			if strings.HasPrefix(rest, "/") || (len(rest) > 1 && rest[0] == ':' && rest[1] >= '0' && rest[1] <= '9') ||
				strings.HasSuffix(text[:loc[0]], "://") {
				continue
			}
		}
		out = append(out, text[loc[0]:loc[1]])
	}
	return out
}

// parseRawValueShapes reads the raw-value shapes from panel_rules.js's
// source: every key of its SHAPES object must be one regex literal line,
// flagged with g and i only. A JS literal's escaped slash is a plain one in
// Go, and its i flag becomes (?i).
func parseRawValueShapes(source string) ([]rawValueShape, error) {
	block := jsShapesBlock.FindStringSubmatch(source)
	if block == nil {
		return nil, fmt.Errorf("no SHAPES object")
	}
	var keys []string
	for _, m := range jsShapeKey.FindAllStringSubmatch(block[1], -1) {
		keys = append(keys, m[1])
	}
	var shapes []rawValueShape
	for _, m := range jsShapeLine.FindAllStringSubmatch(block[1], -1) {
		if strings.Trim(m[3], "gi") != "" {
			return nil, fmt.Errorf("shape %s has flags %q; only g and i are read", m[1], m[3])
		}
		pattern := strings.ReplaceAll(m[2], `\/`, `/`)
		if strings.Contains(m[3], "i") {
			pattern = "(?i)" + pattern
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("shape %s: %v", m[1], err)
		}
		shapes = append(shapes, rawValueShape{m[1], compiled})
	}
	var names []string
	for _, s := range shapes {
		names = append(names, s.name)
	}
	if len(keys) == 0 || strings.Join(keys, ",") != strings.Join(names, ",") {
		return nil, fmt.Errorf("SHAPES keys %v, parsed shapes %v; write each shape as one `name: /pattern/flags,` line", keys, names)
	}
	return shapes, nil
}

// loadRawValueShapes reads static/panel_rules.js, the module page tests
// apply to rendered DOM, so the served-HTML lint and the rendered-DOM lint
// share one definition.
func loadRawValueShapes(t *testing.T) []rawValueShape {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("static", "panel_rules.js"))
	if err != nil {
		t.Fatal(err)
	}
	shapes, err := parseRawValueShapes(string(raw))
	if err != nil {
		t.Fatalf("static/panel_rules.js: %v", err)
	}
	return shapes
}

// lintPanelPage applies the Panel rules to one rendered document.
func lintPanelPage(doc string, rawValueShapes []rawValueShape) []panelLintViolation {
	root := parseLintHTML(doc)
	var found []panelLintViolation
	add := func(rule, format string, args ...any) {
		found = append(found, panelLintViolation{rule, fmt.Sprintf(format, args...)})
	}
	others := map[string]bool{}
	lintWalk(root, func(n *lintNode) {
		if n.tag == "script" && len(n.children) == 1 {
			for _, m := range otherInputCall.FindAllStringSubmatch(n.children[0].text, -1) {
				others[m[1]] = true
			}
		}
	})
	describe := func(n *lintNode) string {
		for _, attr := range []string{"id", "name", "placeholder", "aria-label"} {
			if v := n.attrs[attr]; v != "" {
				return n.tag + " " + attr + "=" + strconv.Quote(v)
			}
		}
		if text := lintOwnText(n); text != "" {
			return n.tag + " " + strconv.Quote(text)
		}
		return n.tag
	}
	lintWalk(root, func(n *lintNode) {
		if n.tag != "" && n.has("data-oa-technical") {
			if said := lintTechnicalSummary(n); said != "Technical details" {
				add("technical-summary", "data-oa-technical on an expander whose summary reads %q: only a \"Technical details\" expander may carry it", said)
			}
		}
		if n.tag != "" && n.has("data-oa-secret") && !lintSecretScopeValid(n) {
			add("secret-scope", "data-oa-secret on %s: valid only on an input, textarea, code or output with no child elements", describe(n))
		}
		switch n.tag {
		case "input":
			kind := strings.ToLower(n.attrs["type"])
			switch kind {
			case "", "text", "url", "email", "tel", "number":
			default:
				return
			}
			if lintHidden(n) || strings.TrimSpace(n.attrs["value"]) != "" || lintSecret(n) || n.has("data-oa-other") || others[n.attrs["id"]] {
				return
			}
			add("empty-input", "%s", describe(n))
		case "textarea":
			if !lintHidden(n) && strings.TrimSpace(lintOwnText(&lintNode{tag: "x", children: n.children})) == "" && !lintSecret(n) {
				add("empty-input", "%s", describe(n))
			}
		case "button":
			if lintInside(n, "nav") {
				return
			}
			if target := n.attrs["data-status"]; target != "" {
				ok := false
				lintWalk(root, func(c *lintNode) { ok = ok || (c.attrs["id"] == target && lintIsStatus(c)) })
				if ok {
					return
				}
			}
			var container *lintNode
			for p := n.parent; p != nil && container == nil; p = p.parent {
				if p.class("oa-row") {
					container = p
				}
			}
			for p := n.parent; p != nil && container == nil; p = p.parent {
				if p.tag == "section" {
					container = p
				}
			}
			if container == nil || !lintHasStatus(container) {
				add("button-status", "%s", describe(n))
			}
		case "section", "main":
			var captions []string
			for _, c := range n.children {
				if lintIsCaption(c) {
					captions = append(captions, lintOwnText(c))
				}
				if n.tag == "main" && c.class("oa-pagehead") {
					lintWalk(c, func(k *lintNode) {
						if k.class("oa-caption") && lintOwnText(k) != "" {
							captions = append(captions, lintOwnText(k))
						}
					})
				}
			}
			where := "the page"
			if n.tag == "section" {
				where = "section " + strconv.Quote(n.attrs["id"])
			}
			for _, extra := range captions[min(1, len(captions)):] {
				add("caption", "%s: %q", where, extra)
			}
		case "":
			if lintHidden(n) || lintInTechnicalBody(n) || lintInside(n, "title") {
				return
			}
			secret := lintSecret(n)
			for _, shape := range rawValueShapes {
				if secret && shape.name == "hash" {
					continue // a secret field's own text: the key itself
				}
				for _, m := range shape.matches(n.text) {
					add("raw-value", "%s %q in %q", shape.name, strings.TrimSpace(m), strings.Join(strings.Fields(n.text), " "))
				}
			}
		}
		if placeholder := n.attrs["placeholder"]; placeholder != "" && n.tag != "" && !lintHidden(n) && !lintInTechnicalBody(n) {
			secret := lintSecret(n)
			for _, shape := range rawValueShapes {
				if secret && shape.name == "hash" {
					continue
				}
				for _, m := range shape.matches(placeholder) {
					add("raw-value", "%s %q in the placeholder of %s", shape.name, strings.TrimSpace(m), describe(n))
				}
			}
		}
	})
	return found
}

// lintedPages renders every page of the Panel as it arrives: each page in
// the navigation, Explore, and the components alone.
func lintedPages() map[string]string {
	docs := map[string]string{}
	for name, fragment := range lintedFragments() {
		docs[name] = panelPage(httptest.NewRequest("GET", "/x?k=lint&platform=windows", nil), "lint", name, "", fragment)
	}
	return docs
}

// lintedFragments are the pages' own fragments: markup and script, without
// the shell around them.
func lintedFragments() map[string]string {
	pages := map[string]string{"setup": setupPage}
	for name, fragment := range pageFragments {
		pages[name] = fragment
	}
	return pages
}

var (
	pageMarkupClass = regexp.MustCompile(`\bclass=(?:"([^"]*)"|'([^']*)'|\\"([^"\\]*)\\")`)
	pageClassName   = regexp.MustCompile(`\.className\s*(\+=|=)([^=][^;\n}]*)`)
	pageClassAttr   = regexp.MustCompile(`setAttribute\(\s*['"]class['"]\s*,([^)]*)\)`)
	pageClassList   = regexp.MustCompile(`classList\.(add|remove|toggle|contains|replace)\(([^)]*)\)`)
	jsQuoted        = regexp.MustCompile(`'([^'\n]*)'|"([^"\n]*)"|` + "`([^`]*)`")
	classToken      = regexp.MustCompile(`^-?[A-Za-z_][A-Za-z0-9_-]*$`)
)

// pageClassViolations is the page class contract: a page's markup and its
// script's className (= and +=), setAttribute('class', …) and classList
// strings use only the classes SPEC.md §6 lists (panelShellClasses), which
// the skins style. Each other class is one violation per use, ruled
// "class:<name>". A string ending in "-" is a prefix the script completes,
// and passes only when panelShellClassPrefixes lists it (oa-icon-).
func pageClassViolations(fragment string) []panelLintViolation {
	var found []panelLintViolation
	check := func(where, text string) {
		for _, token := range strings.Fields(text) {
			if !classToken.MatchString(token) || slices.Contains(panelShellClasses, token) || slices.Contains(panelShellClassPrefixes, token) {
				continue
			}
			found = append(found, panelLintViolation{Rule: "class:" + token, Detail: where})
		}
	}
	quoted := func(text string, first bool) []string {
		var out []string
		for _, m := range jsQuoted.FindAllStringSubmatch(text, -1) {
			out = append(out, m[1]+m[2]+m[3])
			if first {
				break
			}
		}
		return out
	}
	for _, m := range pageMarkupClass.FindAllStringSubmatch(fragment, -1) {
		check("markup class", m[1]+m[2]+m[3])
	}
	for _, m := range pageClassName.FindAllStringSubmatch(fragment, -1) {
		for _, s := range quoted(m[2], false) {
			check("script className "+m[1], s)
		}
	}
	for _, m := range pageClassAttr.FindAllStringSubmatch(fragment, -1) {
		for _, s := range quoted(m[1], false) {
			check("script setAttribute('class')", s)
		}
	}
	for _, m := range pageClassList.FindAllStringSubmatch(fragment, -1) {
		for _, s := range quoted(m[2], m[1] == "toggle" || m[1] == "contains") {
			check("script classList."+m[1], s)
		}
	}
	return found
}

func parsePanelLintAllowlist(text string) (map[string]int, error) {
	rows := map[string]int{}
	scanner := bufio.NewScanner(strings.NewReader(text))
	for scanner.Scan() {
		line := scanner.Text()
		if strings.TrimSpace(line) == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			return nil, fmt.Errorf("allowlist row %q is not page<TAB>rule<TAB>count", line)
		}
		n, err := strconv.Atoi(fields[2])
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("allowlist row %q has no positive count", line)
		}
		rows[fields[0]+"\t"+fields[1]] = n
	}
	return rows, scanner.Err()
}

// checkKnownValueRule holds a known-value row to its one shape:
// known-value:program:<file name>, where the program shape matches the whole
// name once (no bare extension, no prefix, no address). Only the program
// shape takes known values.
func checkKnownValueRule(rule string, shapes []rawValueShape) error {
	rest, ok := strings.CutPrefix(rule, "known-value:")
	if !ok {
		return fmt.Errorf("not a known-value rule")
	}
	shape, value, ok := strings.Cut(rest, ":")
	if !ok || shape != "program" {
		return fmt.Errorf("only the program shape takes known values, not %q", shape)
	}
	for _, s := range shapes {
		if s.name != "program" {
			continue
		}
		if m := s.matches(value); len(m) == 1 && m[0] == value {
			return nil
		}
	}
	return fmt.Errorf("%q is no whole program file name", value)
}

// checkKnownSummary holds a technical-summary row to its shape: a summary
// other than "Technical details", as a person reads it (no surrounding or
// doubled spaces).
func checkKnownSummary(summary string) error {
	if summary == "" || summary == "Technical details" || strings.Join(strings.Fields(summary), " ") != summary {
		return fmt.Errorf("%q is no other summary", summary)
	}
	return nil
}

// lintRuleFamily is a rule's family: "class" for class:<name>, else the
// rule itself.
func lintRuleFamily(rule string) string {
	family, _, _ := strings.Cut(rule, ":")
	return family
}

// lintRuleFamilies are the rule families an allowlist governs: those named
// on its "# governs:" line and those its rows use. A family new at this
// commit is seeded, not raised; once the merge base governs it, its rows
// only shrink.
func lintRuleFamilies(text string) map[string]bool {
	families := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, "# governs:"); ok {
			for _, f := range strings.Fields(rest) {
				families[f] = true
			}
			continue
		}
		if fields := strings.Split(line, "\t"); len(fields) == 3 && !strings.HasPrefix(line, "#") {
			families[lintRuleFamily(fields[1])] = true
		}
	}
	return families
}

// lintGoverned is the "# governs:" line alone: the scope an allowlist
// declares, which may only grow from the merge base on.
func lintGoverned(text string) map[string]bool {
	governed := map[string]bool{}
	for _, line := range strings.Split(text, "\n") {
		if rest, ok := strings.CutPrefix(line, "# governs:"); ok {
			for _, f := range strings.Fields(rest) {
				governed[f] = true
			}
		}
	}
	return governed
}

// panelLintFamilies are the rule families the lint can emit; the allowlist's
// "# governs:" line names every one.
var panelLintFamilies = []string{"empty-input", "button-status", "caption", "raw-value", "class", "secret-scope", "technical-summary"}

// baseLintFamilies and baseLintGoverned are set by baseLintAllowlist: the
// families the merge base's allowlist governs (its rows and its governs
// line), and its governs line alone.
var baseLintFamilies, baseLintGoverned map[string]bool

// baseLintAllowlist is the allowlist at the merge base with origin/main, or
// nil when this checkout cannot tell (no git, no origin/main, or no file
// there yet); the RAISED rule then judges nothing, as app_names.py does.
func baseLintAllowlist(t *testing.T) map[string]int {
	git := func(args ...string) (string, bool) {
		out, err := exec.Command("git", args...).Output()
		return strings.TrimSpace(string(out)), err == nil
	}
	prefix, ok := git("rev-parse", "--show-prefix")
	if !ok {
		t.Log("not a Git checkout; raised allowlist rows are not judged")
		return nil
	}
	base, ok := git("merge-base", "origin/main", "HEAD")
	if !ok {
		t.Log("no merge base with origin/main; raised allowlist rows are not judged")
		return nil
	}
	text, ok := git("show", base+":"+filepath.ToSlash(prefix)+panelLintAllowlist)
	if !ok {
		t.Log("the merge base holds no Panel lint allowlist; raised rows are not judged")
		return nil
	}
	rows, err := parsePanelLintAllowlist(text)
	if err != nil {
		t.Fatalf("allowlist at the merge base: %v", err)
	}
	baseLintFamilies, baseLintGoverned = lintRuleFamilies(text), lintGoverned(text)
	return rows
}

// TestPanelRulesLint holds every page to the Panel rules, with the
// violations each page had when the lint landed allowed until its slice
// removes them. The allowlist only shrinks.
func TestPanelRulesLint(t *testing.T) {
	counts := map[string]int{}
	details := map[string][]string{}
	pages, fragments, shapes := lintedPages(), lintedFragments(), loadRawValueShapes(t)
	names := make([]string, 0, len(pages))
	for name := range pages {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		found := append(lintPanelPage(pages[name], shapes), pageClassViolations(fragments[name])...)
		for _, v := range found {
			if !slices.Contains(panelLintFamilies, lintRuleFamily(v.Rule)) {
				t.Errorf("the lint emits rule %q, whose family panelLintFamilies does not name", v.Rule)
			}
			row := name + "\t" + v.Rule
			counts[row]++
			details[row] = append(details[row], v.Detail)
		}
	}
	if os.Getenv("PANEL_LINT_LIST") != "" {
		var listed []string
		for row := range details {
			listed = append(listed, row)
		}
		sort.Strings(listed)
		for _, row := range listed {
			for _, d := range details[row] {
				t.Logf("%s\t%s", row, d)
			}
		}
		var rows []string
		for row, n := range counts {
			rows = append(rows, row+"\t"+strconv.Itoa(n))
		}
		sort.Strings(rows)
		t.Logf("allowlist rows:\n%s", strings.Join(rows, "\n"))
	}
	raw, err := os.ReadFile(panelLintAllowlist)
	if err != nil {
		t.Fatal(err)
	}
	allowed, err := parsePanelLintAllowlist(string(raw))
	if err != nil {
		t.Fatal(err)
	}
	governed := lintGoverned(string(raw))
	for _, family := range panelLintFamilies {
		if !governed[family] {
			t.Errorf("the allowlist's # governs: line does not name %q, a family the lint emits", family)
		}
	}
	seen := map[string]bool{}
	var rows []string
	for row := range counts {
		seen[row] = true
		rows = append(rows, row)
	}
	for row := range allowed {
		_, rule, _ := strings.Cut(row, "\t")
		if lintRuleFamily(rule) == "known-value" {
			// A known value is a rendered-DOM allowance: page tests and the
			// screenshot tool count it (panel_rules.js knownValues); here
			// only its shape is judged.
			if err := checkKnownValueRule(rule, shapes); err != nil {
				t.Errorf("%s: %v", strings.Replace(row, "\t", " ", 1), err)
			}
			continue
		}
		if summary, ok := strings.CutPrefix(rule, "technical-summary:"); ok {
			// A known summary is a rendered-DOM allowance too, counted by its
			// page test (panel_rules.js knownSummaries). A served page's own
			// markup takes none: its violations are ruled technical-summary.
			if err := checkKnownSummary(summary); err != nil {
				t.Errorf("%s: %v", strings.Replace(row, "\t", " ", 1), err)
			}
			continue
		}
		if !seen[row] {
			rows = append(rows, row)
		}
	}
	sort.Strings(rows)
	for _, row := range rows {
		have, may := counts[row], allowed[row]
		label := strings.Replace(row, "\t", " ", 1)
		switch {
		case may == 0:
			t.Errorf("NEW %s: %d violation(s) on a page the allowlist holds clean:\n  %s", label, have, strings.Join(details[row], "\n  "))
		case have > may:
			t.Errorf("GREW %s: %d violation(s), allowlisted %d:\n  %s", label, have, may, strings.Join(details[row], "\n  "))
		case have == 0:
			t.Errorf("GONE %s: no violation left; drop the row from %s", label, panelLintAllowlist)
		case have < may:
			t.Errorf("SHRANK %s: %d violation(s), allowlisted %d; lower the row to %d", label, have, may, have)
		}
	}
	if earlier := baseLintAllowlist(t); earlier != nil {
		for family := range baseLintGoverned {
			if !governed[family] {
				t.Errorf("NARROWED: the merge base governs %q and this allowlist's # governs: line drops it; the governed scope only grows", family)
			}
		}
		for row, n := range allowed {
			_, rule, _ := strings.Cut(row, "\t")
			if !baseLintFamilies[lintRuleFamily(rule)] {
				continue // a rule family this commit seeds
			}
			if n > earlier[row] {
				t.Errorf("RAISED %s: allowlisted %d, %d at the merge base with origin/main; the allowlist only shrinks", strings.Replace(row, "\t", " ", 1), n, earlier[row])
			}
		}
	}
}

// The lint catches each rule's shape, and leaves alone what the rules allow.
func TestPanelRulesLintCatchesEachRule(t *testing.T) {
	doc := `<html><head><title>C:\x</title></head><body><nav><input type="search"><button>Nav</button></nav><main>
<p>Page caption.</p><p>Second page caption.</p>
<section id="a"><h2>A</h2><p>One.</p><p>Two.</p><p id="s" role="status"></p><p class="oa-empty">Nothing yet.</p>
<input id="path" placeholder="C:\Program Files\App\app.exe"><input type="password"><input id="other"><input type="text" value="kept">
<input hidden><div hidden><input></div><textarea></textarea><textarea>filled</textarea>
<select id="pick"></select><button>Save</button></section>
<section id="c"><h2>C</h2><div class="oa-row"><button>Row</button><p class="oa-status" id="r"></p></div></section>
<section id="b"><h2>B</h2><button>Lost</button><button data-status="s">Pointed</button>
<p>Program C:\Users\me\app.exe for S-1-5-21-1-2-3, digest 0123456789abcdef0123.</p>
<details data-oa-technical><summary>Technical details</summary><pre>C:\Users\me S-1-5-21-9 abcdefabcdefabcdef</pre></details>
<span title="C:\x">app.exe</span><span title="Abstraction Panel.exe">Abstraction Panel</span></section>
<button>Outside</button>
<script>oaProgramChooser('pick','other',()=>[]);var p='C:\\Users\\x';</script>
</main></body></html>`
	got := map[string][]string{}
	for _, v := range lintPanelPage(doc, loadRawValueShapes(t)) {
		got[v.Rule] = append(got[v.Rule], v.Detail)
	}
	want := map[string]int{"empty-input": 2, "button-status": 2, "caption": 2, "raw-value": 7}
	for rule, n := range want {
		if len(got[rule]) != n {
			t.Errorf("%s: %d, want %d: %q", rule, len(got[rule]), n, got[rule])
		}
	}
	if len(got) != len(want) {
		t.Errorf("rules %v", got)
	}
}

// Expander scope (SPEC §6): an expander's body, open or closed, is judged
// like the page; only a Technical details body (details marked
// data-oa-technical, or .oa-error-details) is spared, and only from
// raw-value. Every summary is visible text.
func TestExpanderScope(t *testing.T) {
	kinds := func(body string) []string {
		doc := `<html><body><main><section id="s"><h2>S</h2><p role="status"></p>` + body + `</section></main></body></html>`
		var got []string
		for _, v := range lintPanelPage(doc, loadRawValueShapes(t)) {
			got = append(got, v.Rule+": "+strings.SplitN(v.Detail, " in ", 2)[0])
		}
		return got
	}
	for _, open := range []string{" open", ""} {
		got := kinds(`<details class="oa-expander"` + open + `><summary>Abstraction Panel.exe: Rules (16)</summary><div class="oa-expander-body"><p>C:\Users\me</p><input id="folder"></div></details>`)
		if strings.Join(got, ",") != `raw-value: program "Panel.exe",raw-value: path "C:\\",empty-input: input id="folder"` {
			t.Errorf("expander%s: %q", open, got)
		}
	}
	for _, marker := range []string{`data-oa-technical`, `class="oa-error-details"`} {
		got := kinds(`<details ` + marker + `><summary>Technical details</summary><pre>C:\Users\me S-1-5-21-1-2 abstraction.model/lookup sha256:0123456789abcdef</pre><input id="raw"></details>`)
		if strings.Join(got, ",") != `empty-input: input id="raw"` {
			t.Errorf("Technical details (%s): %q", marker, got)
		}
	}
	if got := kinds(`<details class="oa-error-details"><summary>C:\Users\me</summary></details>`); strings.Join(got, ",") != `raw-value: path "C:\\"` {
		t.Errorf("a Technical details summary: %q", got)
	}
	if !strings.Contains(oaTechnicalDetails("<pre>x</pre>"), `<details class="oa-expander" data-oa-technical><summary>Technical details`) {
		t.Error("oaTechnicalDetails carries no data-oa-technical marker")
	}
}

var callsOaReady = regexp.MustCompile(`\boaReady\(`)

// readyOwnerProblem reports a page whose own script calls oaReady( while its
// served HTML carries no element with data-ready-owner="page": the shell's
// load fallback then marks it ready before its reads finish (SPEC §6). The
// marker counts only as served markup; a script that writes it, or a
// comment naming it, does not.
func readyOwnerProblem(fragment, doc string) string {
	var script strings.Builder
	for _, m := range regexp.MustCompile(`(?s)<script\b[^>]*>(.*?)</script>`).FindAllStringSubmatch(fragment, -1) {
		script.WriteString(m[1])
	}
	if !callsOaReady.MatchString(script.String()) {
		return ""
	}
	owned := false
	lintWalk(parseLintHTML(doc), func(n *lintNode) {
		owned = owned || (n.tag != "" && n.tag != "script" && n.attrs["data-ready-owner"] == "page")
	})
	if owned {
		return ""
	}
	return `its script calls oaReady() but its served HTML has no element with data-ready-owner="page"; put <div data-ready-owner="page" hidden></div> in the page markup (not from script), or the shell marks the page ready at load, before its first reads finish`
}

// Every page whose script calls oaReady() declares data-ready-owner="page"
// in its served HTML (Device and Activity do today).
func TestPagesCallingOaReadyOwnTheirReadiness(t *testing.T) {
	fragments, docs := lintedFragments(), lintedPages()
	owners := 0
	for name, fragment := range fragments {
		if problem := readyOwnerProblem(fragment, docs[name]); problem != "" {
			t.Errorf("%s: %s", name, problem)
		} else if callsOaReady.MatchString(fragment) {
			owners++
		}
	}
	if owners == 0 {
		t.Error("no page calls oaReady; the check judged nothing")
	}
	for name, c := range map[string]struct {
		fragment string
		problem  bool
	}{
		"marker in markup":    {`<div data-ready-owner="page" hidden></div><script>load().then(()=>oaReady())</script>`, false},
		"marker from script":  {`<div id="x"></div><script>el('x').setAttribute('data-ready-owner','page');document.body.insertAdjacentHTML('beforeend','<i data-ready-owner="page"></i>');oaReady()</script>`, true},
		"marker in a comment": {`<!-- data-ready-owner="page" --><script>oaReady()</script>`, true},
		"no oaReady call":     {`<p>Static.</p>`, false},
	} {
		doc := panelPage(httptest.NewRequest("GET", "/card?k=k", nil), "k", "card", "", c.fragment)
		if got := readyOwnerProblem(c.fragment, doc) != ""; got != c.problem {
			t.Errorf("%s: problem %v, want %v", name, got, c.problem)
		}
	}
}

// Only an expander whose summary reads "Technical details" may carry
// data-oa-technical; a technical-summary row names another summary.
func TestTechnicalSummaryRule(t *testing.T) {
	rules := func(body string) []string {
		doc := `<html><body><main><section id="s"><h2>S</h2><p role="status"></p>` + body + `</section></main></body></html>`
		var got []string
		for _, v := range lintPanelPage(doc, loadRawValueShapes(t)) {
			got = append(got, v.Rule)
		}
		return got
	}
	if got := rules(`<details data-oa-technical class="oa-expander"><summary>Technical details<span class="oa-row-chevron"></span></summary><pre>x</pre></details>`); len(got) != 0 {
		t.Errorf("a Technical details expander: %v", got)
	}
	if got := rules(`<details data-oa-technical><summary>Details</summary><pre>x</pre></details>`); strings.Join(got, ",") != "technical-summary" {
		t.Errorf("a marked Details expander: %v", got)
	}
	if got := rules(oaTechnicalDetails("<pre>x</pre>")); len(got) != 0 {
		t.Errorf("oaTechnicalDetails: %v", got)
	}
	for summary, ok := range map[string]bool{"Details": true, "Exact permission details": true, "Technical details": false, "": false, " Details": false, "Two  spaces": false} {
		if (checkKnownSummary(summary) == nil) != ok {
			t.Errorf("summary %q accepted %v", summary, !ok)
		}
	}
}

// A known-value row names one whole program file name; a bare extension, a
// prefix, an address or another shape is refused.
func TestKnownValueRowsTakeOneShape(t *testing.T) {
	shapes := loadRawValueShapes(t)
	for rule, ok := range map[string]bool{
		"known-value:program:openabstractions.exe":  true,
		"known-value:program:Abstraction Panel.exe": false, // two words: the shape matches "Panel.exe"
		"known-value:program:.exe":                  false,
		"known-value:program:openabstractions":      false,
		"known-value:program:openabstractions.ex":   false,
		"known-value:program:example.web.app/x":     false,
		"known-value:path:C:\\Users":                false,
		"known-value:program":                       false,
	} {
		if err := checkKnownValueRule(rule, shapes); (err == nil) != ok {
			t.Errorf("%s: accepted %v, want %v (%v)", rule, err == nil, ok, err)
		}
	}
	raw, err := os.ReadFile(panelLintAllowlist)
	if err != nil {
		t.Fatal(err)
	}
	if !lintGoverned(string(raw))["known-value"] {
		t.Error("the allowlist's # governs: line does not name known-value, so its rows could grow")
	}
}

// The governed families come from the "# governs:" line and the rows, so a
// family seeded now is judged for raised rows from the next merge base on,
// even after its last row goes.
func TestLintRuleFamilies(t *testing.T) {
	got := lintRuleFamilies("# a comment\n# governs: class empty-input\ncard\tclass:oa-note\t1\nrights\tcaption\t2\n")
	if len(got) != 3 || !got["class"] || !got["empty-input"] || !got["caption"] {
		t.Fatalf("families %v", got)
	}
	if lintRuleFamily("class:oa-note") != "class" || lintRuleFamily("raw-value") != "raw-value" {
		t.Fatal("rule family")
	}
	if g := lintGoverned("# governs: class caption\ncard\tempty-input\t1\n"); len(g) != 2 || !g["class"] || !g["caption"] {
		t.Fatalf("governed %v", g)
	}
}

// The allowlist's # governs: line names every family the lint can emit: a
// page with one violation of each rule, through both checks, emits exactly
// panelLintFamilies, and the line lists them all.
func TestGovernsLineNamesEveryFamily(t *testing.T) {
	doc := `<html><body><main><section id="s"><h2>S</h2><p>One.</p><p>Two.</p>
<input id="empty"><button>Lost</button><p>C:\Users\me</p><div data-oa-secret></div>
<details data-oa-technical><summary>Details</summary></details></section></main></body></html>`
	emitted := map[string]bool{}
	for _, v := range lintPanelPage(doc, loadRawValueShapes(t)) {
		emitted[lintRuleFamily(v.Rule)] = true
	}
	for _, v := range pageClassViolations(`<p class="stray">x</p>`) {
		emitted[lintRuleFamily(v.Rule)] = true
	}
	var got []string
	for f := range emitted {
		got = append(got, f)
	}
	want := slices.Clone(panelLintFamilies)
	sort.Strings(got)
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("the lint emits families %v, panelLintFamilies %v", got, want)
	}
	raw, err := os.ReadFile(panelLintAllowlist)
	if err != nil {
		t.Fatal(err)
	}
	governed := lintGoverned(string(raw))
	for _, f := range panelLintFamilies {
		if !governed[f] {
			t.Errorf("# governs: does not name %q", f)
		}
	}
}

// A secret field is exempt from empty-input and raw-value in the served-HTML
// lint as in panel_rules.js: data-oa-secret, a password input, a password
// or one-time-code autocomplete, and what such a field holds.
func TestSecretFieldsAreExempt(t *testing.T) {
	key := "sk-0123456789abcdef0123456789abcdef"
	page := func(body string) string {
		return `<html><body><main><section id="k"><h2>K</h2><p role="status"></p>` + body + `</section></main></body></html>`
	}
	rulesOf := func(body string) []string {
		var rules []string
		for _, v := range lintPanelPage(page(body), loadRawValueShapes(t)) {
			rules = append(rules, v.Rule+": "+v.Detail)
		}
		return rules
	}
	kinds := func(rules []string) string {
		var out []string
		for _, r := range rules {
			rule, detail, _ := strings.Cut(r, ": ")
			if rule == "raw-value" {
				rule += " " + strings.Fields(detail)[0]
			}
			out = append(out, rule)
		}
		return strings.Join(out, ",")
	}
	// Secret fields: no empty-input, and a hash in their own text or
	// placeholder is the key itself.
	if found := rulesOf(`<input data-oa-secret placeholder="` + key + `"><input type="password"><input autocomplete="one-time-code">
<code data-oa-secret>` + key + `</code><output data-oa-secret>` + key + `</output>`); len(found) != 0 {
		t.Fatalf("secret fields were judged: %v", found)
	}
	// The same fields outside a secret are caught.
	if got := kinds(rulesOf(`<input placeholder="` + key + `"><div><code>` + key + `</code></div>`)); got != "empty-input,raw-value hash,raw-value hash" {
		t.Fatalf("the same fields outside a secret: %s", got)
	}
	// A path, SID or action id in a secret field is still caught.
	if got := kinds(rulesOf(`<code data-oa-secret>C:\Users\me S-1-5-21-1-2 abstraction.model/lookup ` + key + `</code>`)); got != "raw-value path,raw-value SID,raw-value action" {
		t.Fatalf("other shapes in a secret: %s", got)
	}
	// data-oa-secret on a wrapper, or on a code with child elements, is a
	// secret-scope violation and exempts nothing.
	if got := kinds(rulesOf(`<div data-oa-secret><p>C:\Users\me</p></div>`)); got != "secret-scope,raw-value path" {
		t.Fatalf("a secret wrapper: %s", got)
	}
	if got := kinds(rulesOf(`<code data-oa-secret><span>` + key + `</span></code>`)); got != "secret-scope,raw-value hash" {
		t.Fatalf("a secret code with a child element: %s", got)
	}
}

// The page class contract finds unlisted classes in markup, in className
// assignments (= and +=), in setAttribute('class', …) and in classList
// calls, and passes listed classes, the listed oa-icon- prefix, comparisons
// and what is no class literal. Any other prefix (oa-row-) is a violation.
func TestPageClassContractCatchesEachForm(t *testing.T) {
	fragment := `<section id="a" class="oa-group"><p class="oa-note">x</p><div class='oa-row stray-one'></div>
<script>
row.className='oa-row '+(ok?'oa-empty':'stray-two');
icon.className='oa-nav-icon oa-icon-'+id;
el.classList.add('oa-status','stray-three');
el.classList.toggle('stray-four', mode==='oa-unrelated');
el.classList.contains('active');
html='<span class=\"stray-five\">'+name+'</span>';
var note="class=x";
row.className+=' stray-six';
cell.setAttribute('class','oa-row-label stray-seven');
tile.className='oa-row-'+kind;
if(row.className==='stray-compared')done();
</script>`
	var got []string
	for _, v := range pageClassViolations(fragment) {
		got = append(got, strings.TrimPrefix(v.Rule, "class:"))
	}
	sort.Strings(got)
	if strings.Join(got, ",") != "oa-note,oa-row-,stray-five,stray-four,stray-one,stray-seven,stray-six,stray-three,stray-two" {
		t.Fatalf("page class violations %v", got)
	}
	if found := pageClassViolations(oaPageHead("card", "Device", "Caption.") + oaGroup("g", "G", "C.", oaRow("L", "", oaButton("b", "Go", true), "s"), oaExpander("More", oaStatus("t")))); len(found) != 0 {
		t.Fatalf("the components break the page class contract: %v", found)
	}
}

// The shape reader takes every SHAPES key or refuses: a key it cannot read,
// a flag other than g and i, a pattern Go cannot compile.
func TestRawValueShapesAreReadWhole(t *testing.T) {
	good := "var SHAPES = {\n    path: /\\/home\\//gi,\n    SID: /\\bS-1-\\d+/g,\n  };"
	shapes, err := parseRawValueShapes(good)
	if err != nil || len(shapes) != 2 || shapes[0].pattern.String() != `(?i)/home/` {
		t.Fatalf("good shapes: %v %v", shapes, err)
	}
	for name, source := range map[string]string{
		"a key on two lines":          "var SHAPES = {\n    path: /x/g,\n    hash:\n      /y/g,\n  };",
		"a flag other than g and i":   "var SHAPES = {\n    path: /x/gm,\n  };",
		"a pattern Go cannot compile": "var SHAPES = {\n    path: /(?<=x)y/g,\n  };",
		"no SHAPES":                   "var OTHER = {};",
	} {
		if _, err := parseRawValueShapes(source); err == nil {
			t.Errorf("%s was read", name)
		}
	}
}

// The served-HTML lint and the rendered-DOM rules find the same raw values
// in the same samples: node runs panel_rules.js's rawValues, Go the shapes
// it read from the same file.
func TestRawValueShapesMatchInGoAndJS(t *testing.T) {
	samples := []string{
		`Program C:\Users\me\app.exe`, `c:\tools`, `\\server\share`, `%APPDATA%\OpenAbstractions`,
		`stored at /home/me/x and (~/models)`, `/Users/me/Library/x`, `see /rights for that`,
		`account S-1-5-21-3833002460-1001`, `S-1-5`, `digest 0123456789abcdef0123 and sha256:ab`,
		`deadbeefdeadbee`, `app.exe on the graphics card, 12 GB, today at 6:14 PM`, ``,
		`needs abstraction.model/lookup on hf`, `contract abstraction.job/acceptance@1.`, `abstraction.credentials/apply, abstraction.storage/content.read`,
		`the abstraction layer`, `abstraction./x`, `Abstraction.Model/Lookup`,
		`Abstraction Panel.exe is running`, `Open Notes.app.`, `kernel32.DLL loaded`, `llama-server.exe, ollama.exe`,
		`the app. Next`, `an .exe file`, `Apps and the Apps page`, `huggingface.co · model.safetensors`, `setup.exeX`,
		`see example.web.app`, `https://example.web.app`, `example.web.app/path`, `example.web.app:8443`, `Abstraction Panel.exe: Rules`,
	}
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node is not on PATH")
	}
	input, _ := json.Marshal(samples)
	script := `const {rawValues}=require(process.argv[1]);const s=JSON.parse(process.argv[2]);process.stdout.write(JSON.stringify(s.map(x=>rawValues(x).map(v=>v.shape+' '+v.match))))`
	modulePath, err := filepath.Abs(filepath.Join("static", "panel_rules.js"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command(node, "-e", script, modulePath, string(input)).Output()
	if err != nil {
		t.Fatalf("node: %v", err)
	}
	var fromJS [][]string
	if err := json.Unmarshal(out, &fromJS); err != nil {
		t.Fatalf("node output %q: %v", out, err)
	}
	shapes := loadRawValueShapes(t)
	for i, sample := range samples {
		fromGo := []string{}
		for _, shape := range shapes {
			for _, m := range shape.matches(sample) {
				fromGo = append(fromGo, shape.name+" "+strings.TrimSpace(m))
			}
		}
		if strings.Join(fromGo, "|") != strings.Join(fromJS[i], "|") {
			t.Errorf("%q: Go %q, JS %q", sample, fromGo, fromJS[i])
		}
	}
}

// The shared components (oaPageHead, oaGroup, oaRow, oaRowNav, oaExpander,
// oaStatus, oaEmpty, oaTable) pass the Panel rules on their own.
func TestComponentsPassThePanelRules(t *testing.T) {
	fragment := oaPageHead("card", "Device", "This device's graphics card and sleep.") +
		oaGroup("awake", "Keeping this device awake", "Apps keeping this device from sleeping.",
			oaRow("Keep awake while downloading", "", `<input type="checkbox" class="switch" id="keep">`, "keep-status"),
			oaRow("Check now", "Reads the device again.", `<button id="check">Check now</button>`, "check-status"),
			oaRowNav("Graphics card details", "Who uses it now.", "/card#holders"),
			oaExpander("Technical details", oaStatus("tech-status")+`<button id="read">Read again</button>`),
			oaEmpty("No app keeps this device awake."),
			oaTable("awake-table", "App", "Since"),
		)
	doc := panelPage(httptest.NewRequest("GET", "/card?k=k", nil), "k", "card", "", fragment)
	if found := lintPanelPage(doc, loadRawValueShapes(t)); len(found) != 0 {
		t.Fatalf("components break the Panel rules: %v", found)
	}
}
