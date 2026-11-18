package main

import "testing"

// Each mediation endpoint a test asks for has its own name, however fast
// the calls follow one another.
func TestShortMediationEndpointNamesAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 2000; i++ {
		name := shortMediationEndpoint(t)
		if seen[name] {
			t.Fatalf("endpoint %s repeated after %d calls", name, i)
		}
		seen[name] = true
	}
}
