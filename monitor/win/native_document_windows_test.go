//go:build windows

package win

import "testing"

func TestRetainedNativeDocumentNavigation(t *testing.T) {
	root := "https://panel.invalid/native-host/shell?k=fixture"
	for _, uri := range []string{root, "https://panel.invalid/credentials-page?k=fixture", "https://external.invalid/", "", "about:blank"} {
		if got := resourceDocumentAllows(&Resources{DocumentURL: root}, uri); got != (uri == root) {
			t.Fatalf("retained navigation %q: %v", uri, got)
		}
		if !resourceDocumentAllows(nil, uri) || !resourceDocumentAllows(&Resources{}, uri) {
			t.Fatalf("ordinary navigation changed: %q", uri)
		}
	}
}
