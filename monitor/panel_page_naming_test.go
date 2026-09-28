package main

import (
	"regexp"
	"testing"
)

// movedSectionNames lists the six sections task 2026-09-23's requirement 1
// split out of the Status page into their own pages (service_page.go's own
// comment on servicePage), each now named by its nav label
// (panel_shell.go's panelNavItems) rather than "on the Status page".
var movedSectionNames = []string{"Work", "Questions and rights", "Questions and permissions", "Rights", "Permissions", "Settings", "Logging", "Identity", "Explore"}

// staleStatusPageReferencePattern is task 2026-09-23's fifth first-time
// visitor, finding 3: credentials_page.go called the rights editor "Rights on
// the Status page" after Rights had already moved to its own "Questions and
// rights" page, naming both a stale location and a name the nav no longer
// uses. This checks every page fragment for the same mistake: one of the
// moved sections named immediately before "on the Status page".
func staleStatusPageReferencePattern() *regexp.Regexp {
	names := ""
	for i, n := range movedSectionNames {
		if i > 0 {
			names += "|"
		}
		names += regexp.QuoteMeta(n)
	}
	return regexp.MustCompile(`(?:` + names + `)\s+on the Status page`)
}

// TestNoStaleStatusPageSectionReference guards against a page naming a
// section that moved off Status as still living "on the Status page"; a
// generic sentence like "check the service on the Status page" (Status
// itself still carries the Check the service button) is untouched, since no
// moved section's name precedes it.
func TestNoStaleStatusPageSectionReference(t *testing.T) {
	pattern := staleStatusPageReferencePattern()
	for page, fragment := range pageFragments {
		if m := pattern.FindString(fragment); m != "" {
			t.Errorf("page %q: %q names a section that moved off the Status page (task 2026-09-23 requirement 1); name it by its own nav label instead", page, m)
		}
	}
}

// graphicsCardAsNamePattern matches "Graphics card" capitalized the way a
// heading or link names a page, never the lowercase "graphics card" the
// Device page's own subtitle uses as an ordinary noun phrase ("The graphics
// card, downloads in progress, and what keeps this device awake").
var graphicsCardAsNamePattern = regexp.MustCompile(`Graphics card`)

// TestNoGraphicsCardAsPageName is task 2026-09-23's sixth first-time
// visitor, finding 1: the nav, the Device page's own heading and its browser
// tab all said "Device", while the Status page's card named the same page
// "Graphics card" and linked to "Open Graphics card". One name wins, the nav
// label ("Device"); every card, link and cross-reference must use it.
func TestNoGraphicsCardAsPageName(t *testing.T) {
	for page, fragment := range pageFragments {
		if m := graphicsCardAsNamePattern.FindString(fragment); m != "" {
			t.Errorf("page %q: %q names the Device page by another name; every card, link and cross-reference must say Device", page, m)
		}
	}
}

var (
	rawActionIDPattern = regexp.MustCompile(`abstraction\.[a-z]+/`)
	sha256IDPattern    = regexp.MustCompile(`sha256:[0-9a-f]{16,}`)
	// jobIDPattern matches abstraction-job/go's own NewID shape (store.go):
	// a millisecond Unix timestamp, "-", and 20 lowercase hex characters,
	// e.g. "1790163884492-044dee6cf7d8509705d2" (task 2026-09-23, eighth
	// first-time visitor, finding 3: Work's Cancel named a finished
	// operation by this raw id instead of its label).
	jobIDPattern = regexp.MustCompile(`\d{10,}-[0-9a-f]{16,}`)
	// sidPattern matches a Windows security identifier's own fixed prefix
	// (task 2026-09-23, eighth first-time visitor, finding 7: Identity's own
	// sentence named the caller's raw SID, "S-1-5-21-…", instead of the
	// account's display name).
	sidPattern = regexp.MustCompile(`S-1-5-`)
	// windowsPathPattern matches a Windows drive letter beginning an absolute
	// path (task 2026-09-23, ninth first-time visitor, finding 5: Explore's
	// own status line named the caller by its full path, "C:\Users\...\
	// Abstraction Panel.exe", instead of its file name).
	windowsPathPattern = regexp.MustCompile(`[A-Z]:\\`)
	// revisionValuePattern matches a revision token following the word
	// "revision": a colon- or dash-joined identifier carrying a run of hex
	// characters, e.g. "revision providers-v2:42a6b3c1" or "revision
	// 2-3a9e13ed" (task 2026-09-23, tenth first-time visitor, finding 1: an
	// outcome line named its own revision directly, "Applied; configuration
	// revision providers-v2:…").
	revisionValuePattern = regexp.MustCompile(`revision [A-Za-z0-9:_.-]*[0-9a-f]{4}[A-Za-z0-9:_.-]*`)
	// rawOutcomeJSONPattern matches a raw job or service result object shown
	// as text, its own "Outcome" field name still quoted JSON (task
	// 2026-09-23, tenth first-time visitor, finding 1: Work's download
	// submit printed `{ "Outcome": "definitely_not_accepted", ... }`
	// unchanged).
	rawOutcomeJSONPattern = regexp.MustCompile(`\{\s*"Outcome"\s*:`)
	goLineComment         = regexp.MustCompile(`//[^\n]*`)
	blockComment          = regexp.MustCompile(`(?s)/\*.*?\*/`)
	doubleQuoted          = regexp.MustCompile(`"[^"\n]*"`)
	singleQuoted          = regexp.MustCompile(`'[^'\n]*'`)
)

// visiblePageText strips everything a page fragment's own id patterns can
// legitimately still appear in, so what remains is only what a reader would
// actually see: a Go or JS comment; an HTML attribute value (every id this
// Panel still names — a title attribute, data-grant-rules — is double-quoted
// HTML, per panel_shell.go's grantScript and every *_page.go edited for task
// 2026-09-23's seventh first-time visitor, finding 2); and a JS single-quoted
// string literal (every remaining one compares or sends an id, e.g.
// credentials_page.go's r.Action==='abstraction.credentials/apply' and its
// request body's action:'abstraction.credentials/apply', never renders one).
// A Technical details block never needs its own exception here: it always
// builds its text from JSON.stringify or string concatenation, so it carries
// no literal id text in the page's own Go source to begin with.
func visiblePageText(fragment string) string {
	t := blockComment.ReplaceAllString(fragment, "")
	t = goLineComment.ReplaceAllString(t, "")
	t = doubleQuoted.ReplaceAllString(t, `""`)
	t = singleQuoted.ReplaceAllString(t, `''`)
	return t
}

// TestNoRawIDInVisibleText is task 2026-09-23's seventh first-time visitor,
// finding 2: an id is never visible page text. It was visible on Device,
// Inference, Registry, Credentials, Questions and rights (a full "Rules
// revision sha256:…" value), Identity (ten "Logging (abstraction.logging/
// sink@1): Ready" lines), Explore (a card described only by its bare
// contract id) and Work (the job id beside Cancel); the plain phrase now
// stands alone everywhere, with the id kept as the element's title attribute
// and, where the page already has one, in that section's Technical details.
func TestNoRawIDInVisibleText(t *testing.T) {
	for page, fragment := range pageFragments {
		visible := visiblePageText(fragment)
		if m := rawActionIDPattern.FindString(visible); m != "" {
			t.Errorf("page %q: raw action id %q is in visible page text (must be a title attribute or behind Technical details)", page, m)
		}
		if m := sha256IDPattern.FindString(visible); m != "" {
			t.Errorf("page %q: raw sha256 value %q is in visible page text (must be truncated, with the full value in a title attribute or behind Technical details)", page, m)
		}
		if m := jobIDPattern.FindString(visible); m != "" {
			t.Errorf("page %q: raw job id %q is in visible page text (name the operation by its label instead; the id belongs in a title attribute or behind Technical details)", page, m)
		}
		if m := sidPattern.FindString(visible); m != "" {
			t.Errorf("page %q: a raw Windows security identifier (%q…) is in visible page text (name the account by its display name instead; the SID belongs in a title attribute or behind Technical details)", page, m)
		}
		if m := windowsPathPattern.FindString(visible); m != "" {
			t.Errorf("page %q: a raw Windows path (%q…) is in visible page text (name the program by its file name instead; the full path belongs in a title attribute or behind Technical details)", page, m)
		}
		if m := revisionValuePattern.FindString(visible); m != "" {
			t.Errorf("page %q: a raw revision value (%q) is in visible page text (the revision belongs in a title attribute or behind Technical details)", page, m)
		}
		if m := rawOutcomeJSONPattern.FindString(visible); m != "" {
			t.Errorf("page %q: a raw outcome object (%q…) is in visible page text (read one plain sentence from it instead; the object belongs behind Technical details)", page, m)
		}
	}
}
