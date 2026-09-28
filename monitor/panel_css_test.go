package main

import (
	"regexp"
	"strings"
	"testing"
)

// cssBlocks maps each selector list in monitor/static/panel.css to its own
// declaration block, keyed by the exact selector text as written (multiple
// comma-joined selectors are not split further; a caller that needs one of
// several grouped selectors matches by substring instead). Media queries are
// flattened: a selector inside @media still keys by its own text, since none
// of this file's selectors repeat across media blocks with different rules.
func cssBlocks(t *testing.T) map[string]string {
	t.Helper()
	css, err := panelCSSFS.ReadFile("static/panel.css")
	if err != nil {
		t.Fatal(err)
	}
	// Strip /* ... */ comments first: a comment can itself contain words like
	// "nowrap" or "overflow-x: auto" while explaining why the rule avoids
	// them, and a naive block scan would otherwise see the rule as failing.
	text := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(string(css), "")
	blocks := map[string]string{}
	for _, m := range regexp.MustCompile(`(?s)([^{}]+)\{([^{}]*)\}`).FindAllStringSubmatch(text, -1) {
		selector := strings.TrimSpace(m[1])
		if selector == "" || strings.HasPrefix(selector, "@") {
			continue
		}
		blocks[selector] = m[2]
	}
	if len(blocks) == 0 {
		t.Fatal("found no CSS rule blocks; the comment-stripping regex above may be broken")
	}
	return blocks
}

// declarationFor finds the one declaration block panel.css writes for
// exactly this selector text; the test fails loudly if panel.css's own
// selector spelling has since changed, rather than silently checking
// nothing.
func declarationFor(t *testing.T, blocks map[string]string, selector string) string {
	t.Helper()
	if decl, ok := blocks[selector]; ok {
		return decl
	}
	t.Fatalf("no CSS rule found for selector %q; panel.css may have renamed or restructured it", selector)
	return ""
}

// TestPlainTextElementsWrap is task 2026-09-23's seventh first-time visitor,
// finding 1: an earlier round's blanket "pre { overflow-x: auto }" (meant
// only for a wide JSON dump or hash) put every plain-sentence <pre> — a
// status line, a Questions and rights rule row — in its own one-line box
// with a horizontal scrollbar, since <pre>'s own default white-space: pre
// never let them wrap. A bare <pre> must read like ordinary prose: no
// nowrap, no horizontal scrollbar of its own. Only a genuine code or JSON
// block (every Technical details disclosure in this Panel is a <pre> inside
// a <details>) keeps a formatting-preserving white-space value, and even
// that wraps rather than scrolls.
func TestPlainTextElementsWrap(t *testing.T) {
	blocks := cssBlocks(t)
	forbidden := []string{"nowrap", "overflow-x: auto", "overflow-x:auto"}

	plain := declarationFor(t, blocks, "pre")
	for _, bad := range forbidden {
		if strings.Contains(plain, bad) {
			t.Errorf("selector \"pre\" contains %q; a plain-sentence <pre> (a status line, a rule row) must wrap normally, not scroll sideways: %s", bad, plain)
		}
	}
	if !strings.Contains(plain, "white-space: normal") {
		t.Errorf("selector \"pre\" does not set white-space: normal; got: %s", plain)
	}

	// A cell scrolling itself instead of wrapping was finding 1's second
	// half (Inference's ten-column host list). table itself must carry
	// neither display: block nor overflow-x: auto any more.
	tableDecl := declarationFor(t, blocks, "table")
	for _, bad := range append(forbidden, "display: block", "display:block") {
		if strings.Contains(tableDecl, bad) {
			t.Errorf("selector \"table\" contains %q; a table must wrap its cells, not scroll itself: %s", bad, tableDecl)
		}
	}

	cellDecl := declarationFor(t, blocks, "td, th")
	if !strings.Contains(cellDecl, "word-break: normal") {
		t.Errorf("selector \"td, th\" does not set word-break: normal (a cell should wrap at word boundaries; only overflow-wrap: anywhere should break a genuinely unbreakable token like a sha256 digest): %s", cellDecl)
	}

	// details pre (every Technical details disclosure) keeps its formatting
	// but still wraps rather than scrolls.
	detailsDecl := declarationFor(t, blocks, "details pre")
	for _, bad := range forbidden {
		if strings.Contains(detailsDecl, bad) {
			t.Errorf("selector \"details pre\" contains %q; a Technical details disclosure must wrap a long line, not scroll: %s", bad, detailsDecl)
		}
	}
}

// TestTableHeadersNeverWrap is task 2026-09-23's eighth first-time visitor,
// finding 6: td, th's own overflow-wrap: anywhere (meant for a genuinely
// unbreakable value like a sha256 digest in a data cell) also applied to a
// heading's one plain word, so "Name" or "Credential" broke mid-word
// ("Na/me", "Cre/den/tial") at 600px. th now keeps its word whole; when a
// table's headers still cannot all fit, the table's own div.oa-table
// wrapper scrolls sideways, never the page and never a heading or a cell's
// own text.
func TestTableHeadersNeverWrap(t *testing.T) {
	blocks := cssBlocks(t)

	thDecl := declarationFor(t, blocks, "th")
	if !strings.Contains(thDecl, "white-space: nowrap") {
		t.Errorf("selector \"th\" does not set white-space: nowrap; got: %s", thDecl)
	}

	wrapDecl := declarationFor(t, blocks, ".oa-table")
	if !strings.Contains(wrapDecl, "overflow-x: auto") {
		t.Errorf("selector \".oa-table\" does not set overflow-x: auto; got: %s", wrapDecl)
	}

	tableDecl := declarationFor(t, blocks, "table")
	for _, bad := range []string{"overflow-x", "display: block", "display:block"} {
		if strings.Contains(tableDecl, bad) {
			t.Errorf("selector \"table\" contains %q; only its .oa-table wrapper should scroll, never the table itself", bad)
		}
	}
}

// TestTableCellsNeverWrapInsideOaTable is task 2026-09-23's thirteenth
// first-time visitor, finding 10: td, th's own word-break: normal (meant to
// let an ordinary cell wrap like prose) let table layout narrow a wide
// table, Inference's own ten-column host list among them, to fit its
// container instead of overflowing it and triggering .oa-table's own
// scrollbar; the last column or two lost their width and their content
// entirely at 600px. A cell inside .oa-table now keeps its own line whole,
// the same rule th already gets everywhere, so the table's natural width
// wins and the wrapper scrolls sideways whole instead.
func TestTableCellsNeverWrapInsideOaTable(t *testing.T) {
	blocks := cssBlocks(t)

	cellDecl := declarationFor(t, blocks, ".oa-table td")
	if !strings.Contains(cellDecl, "white-space: nowrap") {
		t.Errorf("selector \".oa-table td\" does not set white-space: nowrap; got: %s", cellDecl)
	}
	if !strings.Contains(cellDecl, "overflow-wrap: normal") {
		t.Errorf("selector \".oa-table td\" does not set overflow-wrap: normal; got: %s", cellDecl)
	}
}
