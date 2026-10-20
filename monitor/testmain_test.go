package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	facade "github.com/openabstractions/abstraction-facade/go"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	"github.com/openabstractions/abstractions/monitor/internal/personfolders"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
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
//
// TestMain also keeps every test away from the person's own folders, where
// the Panel's Setup page keeps its journals and installs apps: the process's
// per-user folders lie in testPerson, a fresh temporary folder removed
// afterwards (personfolders.Enter). A test that inspects them takes its own
// (personFolders).
func TestMain(m *testing.M) {
	if err := os.Setenv("ABSTRACTION_RUNTIME_ENDPOINT", unservedRuntimeEndpoint); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	panelMachine = func() *facade.Machine { return facade.New(unservedRuntimeEndpoint) }
	// The apps this account's Setup packages recorded are the person's own.
	installedRecords = func() map[string]string { return map[string]string{} }
	nativeApplicationNamesRead = func(context.Context) (map[string]bool, error) { return map[string]bool{}, nil }
	person, err := personfolders.Enter()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	testPerson = person
	// The Start menu Setup writes shortcuts into lies inside the stand-in.
	if _, err := startMenuPrograms(); !errors.Is(err, errors.ErrUnsupported) {
		startMenuPrograms = func() (string, error) {
			return filepath.Join(person, "AppData", "Roaming", "Microsoft", "Windows", "Start Menu", "Programs"), nil
		}
	}
	code := m.Run()
	personfolders.Leave(person)
	os.Exit(code)
}

// testPerson holds the per-user folders of a test without its own stand-in.
var testPerson string

// A test without its own stand-in finds Setup's per-user folders inside a
// temporary folder, never the person's own.
func TestATestWithoutAFixtureReachesNoPersonFolder(t *testing.T) {
	home, err := setup.CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	for what, path := range map[string]string{"data": home.Data, "config": home.Config} {
		if testPerson == "" || !identity.Inside(path, testPerson) {
			t.Errorf("Setup's %s folder %s is the person's own, outside the stand-in %q", what, path, testPerson)
		}
	}
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
