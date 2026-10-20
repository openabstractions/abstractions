package main

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	"github.com/openabstractions/abstraction-identity/listen"
)

// unservedRuntimeEndpoint is an endpoint no process serves.
var unservedRuntimeEndpoint = listen.Endpoint(fmt.Sprintf("oa-monitor-test-unserved-%d", os.Getpid()))

// TestMain keeps every test away from the runtime installed on the machine
// running the tests. The Panel's lookup reads two sources:
//
//   - panelMachine, facade.Discover in production, here an endpoint nobody
//     serves. macOS installed selection ignores the environment, so this seam
//     is what covers it.
//   - ABSTRACTION_RUNTIME_ENDPOINT, which installed selection on Windows and
//     Linux and the unverified default endpoint read. Set, it also stops
//     Windows discovery from starting an installed runtime that is not
//     running (bootstrap.ActivateInstalled).
//
// The MSI registration (Windows) and the installed program (macOS) remain
// readable; they name a server expectation and contact nothing. A test that
// needs a runtime starts its fixture and binds it (trustPanelRuntime,
// t.Setenv), which this default leaves in place for that test only.
func TestMain(m *testing.M) {
	if err := os.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", unservedRuntimeEndpoint); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	panelMachine = func() *facade.Machine { return facade.New(unservedRuntimeEndpoint) }
	os.Exit(m.Run())
}

// A test without a fixture finds no runtime; with the rule writer left at its
// production default, a confirmed registration reports that it could not
// record the Panel's read and writes nowhere.
func TestATestWithoutAFixtureReachesNoRuntime(t *testing.T) {
	if got := os.Getenv("ABSTRACTION_RUNTIME_ENDPOINT"); got != unservedRuntimeEndpoint {
		t.Fatalf("ABSTRACTION_RUNTIME_ENDPOINT is %q", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := panelMachine().ObserveCaller(ctx); err == nil {
		t.Fatal("the default Panel lookup reached a runtime")
	}
	if err := (panelApplicationReads{}).allowPanelRead(ctx, "example"); err == nil {
		t.Fatal("the default rule writer reached a runtime")
	}
}
