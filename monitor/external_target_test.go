package main

import "testing"

// A Panel link leaves for a web page or Windows Settings at Apps, and for
// nothing else.
func TestExternalTargetAllowsWebAndWindowsAppsSettings(t *testing.T) {
	for url, want := range map[string]bool{
		"https://github.com/openabstractions/redist/releases/latest": true,
		"http://example.com/":        true,
		"ms-settings:appsfeatures":   true,
		"ms-settings:privacy-webcam": false,
		"ms-settings:":               false,
		"file:///C:/Windows":         false,
		"javascript:alert(1)":        false,
	} {
		if got := externalTarget(url); got != want {
			t.Errorf("externalTarget(%q) = %v, want %v", url, got, want)
		}
	}
}
