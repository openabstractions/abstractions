package main

import (
	"runtime"
	"slices"
	"testing"
)

// The four sites that name OA's programs read these two sets; the Panel and the
// Asks presenter are in the operator set and outside the runtime set.
func TestOAProgramSets(t *testing.T) {
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	if got, want := oaProgramFiles(oaRuntimeProgramStems), []string{"openabstractions" + suffix, "openabstractionsw" + suffix}; !slices.Equal(got, want) {
		t.Fatalf("runtime programs = %q, want %q", got, want)
	}
	if got, want := oaProgramFiles(oaProgramStems), []string{"openabstractions" + suffix, "openabstractionsw" + suffix, "Abstraction Panel" + suffix, "OpenAbstractions Presenter" + suffix}; !slices.Equal(got, want) {
		t.Fatalf("OA programs = %q, want %q", got, want)
	}
	if len(oaRuntimeProgramStems) != 2 {
		t.Fatalf("building the operator set changed the runtime set: %q", oaRuntimeProgramStems)
	}
}
