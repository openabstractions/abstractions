package main

import (
	"reflect"
	"strings"
	"testing"
)

// The Panel shows the platform declaration's limits about its own release
// only; sentences about other releases stay in the release record.
func TestCurrentLimitsDropOtherReleases(t *testing.T) {
	limits := []string{
		"Both 0.4.0 Windows MSIs are signed. Manual Setup remains deferred in 0.4.0. ARM64 is cross-built; installation on ARM64 hardware is untested.",
		"Everyone installation requires elevation.",
		"0.4.0 Linux tarballs are unsigned.",
	}
	got := currentLimits(limits, "Development build")
	want := []string{"Everyone installation requires elevation."}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("development build limits = %q, want %q", got, want)
	}
	if own := currentLimits(limits, "v0.4.0"); len(own) != 3 {
		t.Fatalf("0.4.0 keeps its own limits: %q", own)
	}
	views, err := declarationsFor("windows")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range views {
		for _, limit := range v.Limits {
			if strings.Contains(limit, "0.4.0") || strings.Contains(limit, "Manual Setup") {
				t.Fatalf("a development Panel shows another release's limit: %q", limit)
			}
		}
	}
}
