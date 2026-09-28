package main

import (
	"regexp"
	"strings"
	"testing"
)

// grantRuleActionPattern finds every action token a data-grant-rules
// attribute names (gResolveRules, panel_shell.go, splits each comma-separated
// token on "|" and takes the action half); the resource half, when present,
// is a resource name or another action union, never itself checked here.
var grantRuleActionPattern = regexp.MustCompile(`data-grant-rules="([^"]*)"`)

// parenthesizedActionIDPattern finds every raw action id a page names in the
// "plain phrase (raw id)" shape task 2026-09-23's fifth first-time visitor
// asked every page to use (Inference's data-grant-rules sentence,
// gRewriteSentence in panel_shell.go, and identity_page.go's capability rows
// already used this shape; Device, Registry and Credentials did not, and
// showed a bare id or a raw id fragment with no plain phrase at all instead).
var parenthesizedActionIDPattern = regexp.MustCompile(`\((abstraction\.[a-z][a-zA-Z0-9_.-]*/[a-zA-Z0-9_.]+)\)`)

// emittedRuleActions collects every rights action id a page fragment names,
// through either shape above.
func emittedRuleActions(fragment string) []string {
	var actions []string
	for _, attr := range grantRuleActionPattern.FindAllStringSubmatch(fragment, -1) {
		for _, token := range strings.Split(attr[1], ",") {
			token = strings.TrimSpace(token)
			if token == "" {
				continue
			}
			action := strings.SplitN(token, "|", 2)[0]
			actions = append(actions, action)
		}
	}
	for _, m := range parenthesizedActionIDPattern.FindAllStringSubmatch(fragment, -1) {
		actions = append(actions, m[1])
	}
	return actions
}

// TestRightsActionPlainCoversEmittedActions is task 2026-09-23's fifth
// first-time visitor, finding 2: every action id a Panel page actually names
// (a grant widget's data-grant-rules, or a "plain phrase (id)" sentence) must
// have a plain phrase in rightsActionPlain, so the page never has to fall
// back to showing the raw id alone.
func TestRightsActionPlainCoversEmittedActions(t *testing.T) {
	found := false
	for page, fragment := range pageFragments {
		for _, action := range emittedRuleActions(fragment) {
			found = true
			if rightsActionPlain[action] == "" {
				t.Errorf("page %q names action %q with no rightsActionPlain entry", page, action)
			}
		}
	}
	if !found {
		t.Fatal("found no action ids to check across pageFragments; the patterns above may no longer match the pages' markup")
	}
}
