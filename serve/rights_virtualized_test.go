package main

import (
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-facade/go/bootstrap"
)

// A grant issued from inside a packaged (virtualized) shell for a --program
// under this process's own AppData can name a path the runtime never actually
// sees: the OS redirects a new file there to the package's private LocalCache
// copy, so the operator's nominal path and the peer's physical path diverge.
// warnVirtualizedProgram surfaces that risk, in one sentence naming no
// repository path, and points at the remedy (grant the path status reports)
// without blocking the grant.
func TestWarnVirtualizedProgramWarnsUnderVirtualizedAppData(t *testing.T) {
	virtualized := func() (bootstrap.ProfileView, error) {
		return bootstrap.ProfileView{Virtualized: true, Family: "Claude_pzs8sxrjxfjjc"}, nil
	}
	var diagnostics strings.Builder
	warnVirtualizedProgram(&diagnostics, `C:\Users\someone\AppData\Local\oa-refusal-test\openabstractions-mcp.exe`, `C:\Users\someone\AppData\Local`, virtualized)
	got := diagnostics.String()
	for _, want := range []string{"warning", "Claude_pzs8sxrjxfjjc", "openabstractions status", "physical path"} {
		if !strings.Contains(got, want) {
			t.Fatalf("warning %q does not mention %q", got, want)
		}
	}
	if strings.Contains(got, "research/packaged-activation") {
		t.Fatalf("warning %q names a repository path, want none", got)
	}
}

// No warning: the profile view is real (not virtualized), even though the
// program is under AppData.
func TestWarnVirtualizedProgramSilentWhenProfileIsReal(t *testing.T) {
	real := func() (bootstrap.ProfileView, error) { return bootstrap.ProfileView{}, nil }
	var diagnostics strings.Builder
	warnVirtualizedProgram(&diagnostics, `C:\Users\someone\AppData\Local\oa-refusal-test\openabstractions-mcp.exe`, `C:\Users\someone\AppData\Local`, real)
	if got := diagnostics.String(); got != "" {
		t.Fatalf("warning %q for a real (non-virtualized) profile view, want none", got)
	}
}

// No warning: the program is outside AppData, even though the profile view is
// virtualized elsewhere.
func TestWarnVirtualizedProgramSilentOutsideAppData(t *testing.T) {
	virtualized := func() (bootstrap.ProfileView, error) {
		return bootstrap.ProfileView{Virtualized: true, Family: "Claude_pzs8sxrjxfjjc"}, nil
	}
	var diagnostics strings.Builder
	warnVirtualizedProgram(&diagnostics, `C:\Users\someone\oa-tools\openabstractions-mcp.exe`, `C:\Users\someone\AppData\Local`, virtualized)
	if got := diagnostics.String(); got != "" {
		t.Fatalf("warning %q for a program outside AppData, want none", got)
	}
}
