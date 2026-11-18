package main

import (
	"net/http/httptest"
	"regexp"
	"testing"
)

// No served Panel page opens a browser script dialog. A destructive button
// asks for a second select in the page instead (registry_page.go,
// explore_page.go), and the WebView2 host turns default script dialogs off.
func TestNoPageOpensAScriptDialog(t *testing.T) {
	pages := map[string]string{"setup": setupPage}
	for name, fragment := range pageFragments {
		pages[name] = fragment
	}
	dialog := regexp.MustCompile(`(^|[^\w.$])(confirm|alert|prompt)\(|window\.(confirm|alert|prompt)\b`)
	for name, fragment := range pages {
		doc := panelPage(httptest.NewRequest("GET", "/?k=test", nil), "test", name, "OpenAbstractions", fragment)
		if call := dialog.FindString(doc); call != "" {
			t.Errorf("%s page calls a script dialog: %q", name, call)
		}
	}
}
