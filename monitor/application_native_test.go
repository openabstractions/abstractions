package main

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
)

// intakePanel is a Panel run an installer started with manifest: its
// directory, its own request records and Activity record, and the parsed
// request.
func intakePanel(t *testing.T, apps setupApplications, manifest string) (*servicePanel, applicationStart) {
	t.Helper()
	request, _, err := parseApplicationStart(writeApplicationManifest(t, manifest), "", "overview")
	if err != nil {
		t.Fatal(err)
	}
	p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{},
		appRequests: applicationRequestsFor(t, nil), appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
	return p, request
}

// A fresh install's request registers the app with no question: the run has
// no question surface at all, the entry is connected, and nothing waits in
// Apps or Activity.
func TestAnInstallersRequestConnectsTheAppWithoutAQuestion(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p, request := intakePanel(t, apps, manifestText(applicationTestProgram(t), "OpenCode with OA"))
	if err := p.applyApplicationRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if apps.registers != 1 || apps.entries["example"].Title != "OpenCode with OA" {
		t.Fatalf("not connected: %+v", apps.entries)
	}
	if reads := p.appReadRules.(*applicationReadFake); len(reads.allowed) != 1 || reads.allowed[0] != "example" {
		t.Fatalf("the Panel may not list it: %+v", reads.allowed)
	}
	if len(p.appRequests.recorded()) != 0 || len(p.appActivity.lines()) != 0 {
		t.Fatal("a connected app waits or reports")
	}
}

// The same program asking again with a changed manifest is updated in place
// by the service; nothing waits in Apps.
func TestTheSameProgramAskingAgainUpdatesSilently(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p, request := intakePanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	if err := p.applyApplicationRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	_, again := intakePanel(t, apps, manifestText(applicationTestProgram(t), "Example 2"))
	if err := p.applyApplicationRequest(context.Background(), again); err != nil {
		t.Fatal(err)
	}
	if apps.entries["example"].Title != "Example 2" || apps.removes != 0 || len(p.appRequests.recorded()) != 0 {
		t.Fatalf("same program: %+v removes=%d", apps.entries, apps.removes)
	}
}

// Another program under a connected name meets conflict: the connected
// entry is untouched, and the newcomer waits in Apps as Not connected with
// the title it would replace. Connect replaces only on the click, through
// the existing replace, and only the entry the row named.
func TestAnotherProgramUnderAConnectedNameWaitsAndConnectReplacesOnTheClick(t *testing.T) {
	connected := fwire.ApplicationDescriptor{Name: "example", Program: filepath.Join(t.TempDir(), "first.exe"), Title: "First"}
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"example": connected}}
	p, request := intakePanel(t, apps, manifestText(applicationTestProgram(t), "Newcomer"))
	if err := p.applyApplicationRequest(context.Background(), request); !errors.Is(err, errApplicationNotChanged) {
		t.Fatal("conflict reported as", err)
	}
	if apps.entries["example"] != connected || apps.removes != 0 {
		t.Fatal("the conflict changed the connected entry")
	}
	h := p.handler(applicationTestKey)
	rows := pendingRows(t, h)
	if len(rows) != 1 || rows[0].Title != "Newcomer" || rows[0].Replaces != "First" || rows[0].Shown != applicationDigest(&connected) {
		t.Fatalf("Not connected row: %+v", rows)
	}
	// Listing and reading the row change nothing.
	if apps.entries["example"] != connected || apps.removes != 0 {
		t.Fatal("listing replaced the entry")
	}
	stale := connectRow(t, h, pendingApplication{Name: rows[0].Name, Digest: rows[0].Digest})
	if stale.Code != 409 || apps.entries["example"] != connected {
		t.Fatalf("a Connect that did not see the replaced entry replaced it: %d %s", stale.Code, stale.Body.String())
	}
	if rec := connectRow(t, h, rows[0]); rec.Code != 200 || !strings.Contains(rec.Body.String(), "Newcomer is connected") {
		t.Fatalf("Connect: %d %s", rec.Code, rec.Body.String())
	}
	if apps.entries["example"].Title != "Newcomer" || len(pendingRows(t, h)) != 0 {
		t.Fatalf("not replaced: %+v", apps.entries)
	}
}

// The same path holding another image meets conflict "identity": the entry
// stays as it is, and the refused request waits in Apps as Not connected
// with the title it would replace, so Connect can replace the entry.
func TestTheSamePathWithAnotherImageWaitsAsNotConnected(t *testing.T) {
	program := applicationTestProgram(t)
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p, request := intakePanel(t, apps, manifestText(program, "Example"))
	if err := p.applyApplicationRequest(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	connected := apps.entries["example"]
	apps.identity = map[string]bool{"example": true}
	_, again := intakePanel(t, apps, manifestText(program, "Example 2"))
	if err := p.applyApplicationRequest(context.Background(), again); !errors.Is(err, errApplicationNotChanged) {
		t.Fatal("identity conflict reported as", err)
	}
	if apps.entries["example"] != connected {
		t.Fatal("the conflict changed the entry")
	}
	rows := pendingRows(t, p.handler(applicationTestKey))
	if len(rows) != 1 || rows[0].Title != "Example 2" || rows[0].Replaces != "Example" {
		t.Fatalf("the refused same-path request does not wait in Apps: %+v", rows)
	}
}

// A title that reads as OpenAbstractions is refused with no question: it is
// not registered, waits in no row, and Activity names the refusal.
func TestAReservedTitleIsRefusedAndListedInActivity(t *testing.T) {
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}
	p, request := intakePanel(t, apps, manifestText(applicationTestProgram(t), "Open Abstractions"))
	if err := p.applyApplicationRequest(context.Background(), request); !errors.Is(err, errApplicationNotChanged) {
		t.Fatal(err)
	}
	if apps.registers != 0 || len(p.appRequests.recorded()) != 0 {
		t.Fatal("a reserved title was registered or waits")
	}
	lines := p.appActivity.lines()
	if len(lines) != 1 || lines[0].Outcome != "Not connected: the title is a name OpenAbstractions uses for itself" || lines[0].At == 0 {
		t.Fatalf("Activity: %+v", lines)
	}
	rec := applicationCall(t, p.handler(applicationTestKey), "GET", "/activity/app-requests", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Its installer asked to connect it") {
		t.Fatalf("Activity route: %d %s", rec.Code, rec.Body.String())
	}
}

// With the runtime unreachable, the request waits in Apps as Not connected.
func TestAnUnreachableRuntimeLeavesTheAppNotConnected(t *testing.T) {
	apps := unreachableDirectory{&applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{}}}
	p, request := intakePanel(t, apps, manifestText(applicationTestProgram(t), "Example"))
	if err := p.applyApplicationRequest(context.Background(), request); !errors.Is(err, errApplicationNotChanged) {
		t.Fatal(err)
	}
	if recorded := p.appRequests.recorded(); recorded["example"] != request.manifest {
		t.Fatalf("not recorded: %+v", recorded)
	}
}

// An uninstaller removes its own program's entry with no question and
// Activity lists it; an entry another program holds stays, silently.
func TestAnUninstallerRemovesOnlyItsOwnProgramsEntry(t *testing.T) {
	program := applicationTestProgram(t)
	manifest := writeApplicationManifest(t, manifestText(program, "Example"))
	foreign := fwire.ApplicationDescriptor{Name: "example", Program: filepath.Join(t.TempDir(), "foreign.exe"), Title: "Foreign"}
	apps := &applicationDirectoryFake{entries: map[string]fwire.ApplicationDescriptor{"example": foreign}}
	p := &servicePanel{comfyApps: apps, appReadRules: &applicationReadFake{},
		appRequests: applicationRequestsFor(t, map[string]string{"example": manifest}), appActivity: applicationActivityLog{path: filepath.Join(t.TempDir(), "activity.json")}}
	// The uninstaller's record named its manifest's program at intake.
	removal := applicationStart{remove: "example", recorded: program}
	if err := p.applyApplicationRequest(context.Background(), removal); !errors.Is(err, errApplicationNotChanged) {
		t.Fatal(err)
	}
	if apps.removes != 0 || apps.entries["example"] != foreign || len(p.appActivity.lines()) != 0 {
		t.Fatal("another program's entry was removed or reported")
	}
	apps.entries["example"] = fwire.ApplicationDescriptor{Name: "example", Program: program, Title: "Example"}
	if err := p.applyApplicationRequest(context.Background(), removal); err != nil {
		t.Fatal(err)
	}
	if _, ok := apps.entries["example"]; ok {
		t.Fatal("its own entry was kept")
	}
	if reads := p.appReadRules.(*applicationReadFake); len(reads.cleared) != 1 {
		t.Fatalf("read rule kept: %+v", reads.cleared)
	}
	if lines := p.appActivity.lines(); len(lines) != 1 || lines[0].App != "Example" || lines[0].Outcome != "Removed" {
		t.Fatalf("Activity: %+v", lines)
	}
	// An entry already gone is no error.
	if err := p.applyApplicationRequest(context.Background(), applicationStart{remove: "example"}); err != nil {
		t.Fatal(err)
	}
}

// unreachableDirectory is the directory a client sees when the runtime it
// pins is not the program serving the endpoint: every call fails with the
// resolution error (owner's PC, 2026-11-01).
type unreachableDirectory struct{ *applicationDirectoryFake }

var errUnreachableRuntime = errors.New(`service resolution: runtime_unavailable: abstraction.facade/applications@2 (capability abstraction.facade) at the installed runtime at \\.\pipe\openabstractions-user-S-1-5-21-1-runtime-v1: listen: connected server is not trusted: server program mismatch`)

func (unreachableDirectory) Observe(context.Context, string, int64) (fwire.ApplicationPage, error) {
	return fwire.ApplicationPage{}, errUnreachableRuntime
}

func (unreachableDirectory) Register(context.Context, fwire.ApplicationDescriptor) (fwire.ApplicationChange, error) {
	return fwire.ApplicationChange{}, errUnreachableRuntime
}
