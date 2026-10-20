// Package startprofiletest supplies test fixtures for registered native starts.
package startprofiletest

import (
	"testing"

	"github.com/openabstractions/abstractions/mcp-gateway/startprofile"
)

// ClearProviderSelectors sets every refused provider selector to "" for the
// test, so a check started from a Claude session exercises the fixture rather
// than the production refusal.
func ClearProviderSelectors(t testing.TB) {
	t.Helper()
	for _, name := range startprofile.ProviderSelectors() {
		t.Setenv(name, "")
	}
}
