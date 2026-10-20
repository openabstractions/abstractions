package main

import (
	"regexp"
	"sort"
	"testing"
)

// rawActionID finds a raw rights action id, abstraction.<layer>/<action>,
// anywhere in a page fragment, markup or script. An id followed by @<n> is a
// service contract, never an action, and is skipped.
var rawActionID = regexp.MustCompile(`abstraction\.[a-z][a-z0-9_-]*/[a-z][a-zA-Z0-9_.-]*[a-zA-Z0-9_]`)

// TestRightsActionPlainCoversEmittedActions is task 2026-09-23's fifth
// first-time visitor, finding 2: every rights action id a Panel page names
// has a plain phrase in rightsActionPlain, so the page never has to show the
// raw id alone. A page that names none passes.
func TestRightsActionPlainCoversEmittedActions(t *testing.T) {
	missing := map[string][]string{}
	for page, fragment := range pageFragments {
		for _, m := range rawActionID.FindAllStringIndex(fragment, -1) {
			if m[1] < len(fragment) && fragment[m[1]] == '@' {
				continue
			}
			action := fragment[m[0]:m[1]]
			if rightsActionPlain[action] == "" {
				missing[action] = append(missing[action], page)
			}
		}
	}
	actions := make([]string, 0, len(missing))
	for action := range missing {
		actions = append(actions, action)
	}
	sort.Strings(actions)
	for _, action := range actions {
		t.Errorf("pages %v name action %q with no rightsActionPlain entry", missing[action], action)
	}
}
