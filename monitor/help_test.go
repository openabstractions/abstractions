package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// pageFragments mirrors panel_shell.go's routing: which page constant each
// help.go "page" key names.
var pageFragments = map[string]string{
	"status":      servicePage,
	"work":        workPage,
	"card":        cardPage,
	"inference":   inferencePage,
	"registry":    registryPage,
	"credentials": credentialsPage,
	"rights":      rightsPage,
	"settings":    settingsPage,
	"logging":     loggingPage,
	"identity":    identityPage,
	"explore":     explorePage,
}

var sectionIDPattern = regexp.MustCompile(`<section id="([a-zA-Z0-9_-]+)"`)
var introH1IDPattern = regexp.MustCompile(`<h1 id="([a-zA-Z0-9_-]+)"`)

// pageSectionIDs lists every id a page's fragment marks as one of its own
// sections: every <section id="..."> and the page's own <h1 id="..."> intro,
// when it has one.
func pageSectionIDs(fragment string) []string {
	var ids []string
	for _, m := range sectionIDPattern.FindAllStringSubmatch(fragment, -1) {
		ids = append(ids, m[1])
	}
	for _, m := range introH1IDPattern.FindAllStringSubmatch(fragment, -1) {
		ids = append(ids, m[1])
	}
	return ids
}

// TestHelpAnchorsCoverEverySection is requirement 1's test: every section id
// in the five Panel pages has a help.go entry, and every entry names a
// section id that actually exists in that page.
func TestHelpAnchorsCoverEverySection(t *testing.T) {
	mapped := map[string]map[string]bool{}
	for _, e := range helpAnchors {
		if mapped[e.Page] == nil {
			mapped[e.Page] = map[string]bool{}
		}
		if mapped[e.Page][e.ID] {
			t.Errorf("duplicate help.go entry for page %q section %q", e.Page, e.ID)
		}
		mapped[e.Page][e.ID] = true
	}

	for page, fragment := range pageFragments {
		ids := pageSectionIDs(fragment)
		if len(ids) == 0 {
			t.Errorf("page %q: found no section ids to check against help.go", page)
		}
		seen := map[string]bool{}
		for _, id := range ids {
			if seen[id] {
				t.Errorf("page %q: id %q used more than once (a duplicate DOM id)", page, id)
			}
			seen[id] = true
			if !mapped[page][id] {
				t.Errorf("page %q section %q has no help.go entry", page, id)
			}
		}
	}

	for _, e := range helpAnchors {
		fragment, ok := pageFragments[e.Page]
		if !ok {
			t.Errorf("help.go entry names unknown page %q", e.Page)
			continue
		}
		found := false
		for _, id := range pageSectionIDs(fragment) {
			if id == e.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("help.go entry for page %q section %q: no such section id in %s_page.go", e.Page, e.ID, e.Page)
		}
	}
}

// siteRoot locates openabstractions-flat/openabstractions.github.io from the
// monitor module, walking up to the repository root.
func siteRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		candidate := filepath.Join(dir, "openabstractions-flat", "openabstractions.github.io")
		if info, err := os.Stat(candidate); err == nil {
			if !info.IsDir() {
				t.Fatalf("site tree %s is not a directory", candidate)
			}
			return candidate
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		private := filepath.Join(dir, "openabstractions-flat")
		if info, err := os.Stat(private); err == nil {
			if !info.IsDir() {
				t.Fatalf("private workspace marker %s is not a directory", private)
			}
			t.Fatalf("private workspace is missing its site tree at %s", candidate)
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// The published monitor module is tested on its own. The site-target
	// integration assertion runs in the private workspace that owns both trees.
	t.Skip("site-target integration requires the private openabstractions.github.io tree")
	return ""
}

var htmlIDPattern = regexp.MustCompile(`\bid="([a-zA-Z0-9_-]+)"`)

// siteAnchors collects every id="..." attribute out of every .html file
// under root, keyed by the file's base name (e.g. "reference.html").
func siteAnchors(t *testing.T, root string) map[string]map[string]bool {
	t.Helper()
	anchors := map[string]map[string]bool{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || filepath.Ext(path) != ".html" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name := filepath.Base(path)
		set := anchors[name]
		if set == nil {
			set = map[string]bool{}
			anchors[name] = set
		}
		for _, m := range htmlIDPattern.FindAllStringSubmatch(string(data), -1) {
			set[m[1]] = true
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return anchors
}

// TestHelpAnchorsResolveOnSite is the other half of requirement 1: every
// help.go URL names a file and #anchor that exist in this tree's copy of the
// website.
func TestHelpAnchorsResolveOnSite(t *testing.T) {
	root := siteRoot(t)
	anchors := siteAnchors(t, root)
	urlPattern := regexp.MustCompile(`^https://openabstractions\.github\.io/([a-zA-Z0-9_.-]+\.html)#([a-zA-Z0-9_-]+)$`)
	for _, e := range helpAnchors {
		m := urlPattern.FindStringSubmatch(e.URL)
		if m == nil {
			t.Errorf("page %q section %q: URL %q is not a plain reference.html/adopt.html anchor", e.Page, e.ID, e.URL)
			continue
		}
		file, anchor := m[1], m[2]
		set, ok := anchors[file]
		if !ok {
			t.Errorf("page %q section %q: %s does not exist under %s", e.Page, e.ID, file, root)
			continue
		}
		if !set[anchor] {
			t.Errorf("page %q section %q: %s has no id=%q", e.Page, e.ID, file, anchor)
		}
	}
}

// TestRetiredHelpAnchorsStillLand keeps older Panel builds' Learn-more links
// working: every id in helpRetiredAnchors is still on reference.html, and
// none of them is a current helpAnchors target.
func TestRetiredHelpAnchorsStillLand(t *testing.T) {
	anchors := siteAnchors(t, siteRoot(t))["reference.html"]
	current := map[string]bool{}
	for _, e := range helpAnchors {
		current[e.URL[strings.LastIndex(e.URL, "#")+1:]] = true
	}
	for _, id := range helpRetiredAnchors {
		if !anchors[id] {
			t.Errorf("reference.html lost the retired anchor %q; keep it as an empty <span id> before its section's heading", id)
		}
		if current[id] {
			t.Errorf("%q is both retired and a current help anchor", id)
		}
	}
}
