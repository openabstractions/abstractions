package main

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	fwire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstractions/monitor/internal/setup"
)

// fakeStartMenu keeps the Start menu in a temporary folder whose shortcut
// files hold their fields as JSON, and makes panel the Panel program.
func fakeStartMenu(t *testing.T, panel string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "Programs")
	previous := [...]any{startMenuPrograms, readStartShortcut, writeStartShortcut, panelExecutable}
	t.Cleanup(func() {
		startMenuPrograms = previous[0].(func() (string, error))
		readStartShortcut = previous[1].(func(string) (startShortcut, error))
		writeStartShortcut = previous[2].(func(string, startShortcut) error)
		panelExecutable = previous[3].(func() (string, error))
	})
	startMenuPrograms = func() (string, error) { return dir, nil }
	readStartShortcut = func(path string) (startShortcut, error) {
		raw, err := os.ReadFile(path)
		if err != nil {
			return startShortcut{}, err
		}
		var s startShortcut
		return s, json.Unmarshal(raw, &s)
	}
	writeStartShortcut = func(path string, s startShortcut) error {
		raw, _ := json.Marshal(s)
		return os.WriteFile(path, raw, 0o644)
	}
	panelExecutable = func() (string, error) { return panel, nil }
	return dir
}

func readFakeShortcut(t *testing.T, path string) startShortcut {
	t.Helper()
	s, err := readStartShortcut(path)
	if err != nil {
		t.Fatalf("shortcut %s: %v", filepath.Base(path), err)
	}
	return s
}

// The owner's machine, 2026-11-09: a 2026-10-31 developer build held the
// comfyui entry and Setup's workspace step refused the installed build.
// The step now replaces a stale OpenAbstractions entry, registers the
// installed build through its own register-self and writes the Start-menu
// shortcut that opens it through the owned launch.
func TestComfyWorkspaceStepReplacesAStaleDevEntryAndMakesTheShortcut(t *testing.T) {
	panel := filepath.Join(t.TempDir(), "Abstraction Panel.exe")
	dir := fakeStartMenu(t, panel)
	installed := fwire.ApplicationDescriptor{Name: "comfyui", Program: filepath.Join(t.TempDir(), "OA", "a", "cu", "0.8.0-r17", "launch-helper.exe"), Title: "ComfyUI — OA build"}
	// The developer build's folder is gone.
	dev := fwire.ApplicationDescriptor{Name: "comfyui", Program: filepath.Join(t.TempDir(), "q", "OpenAbstractions ComfyUI.exe"), Title: "ComfyUI — OA build"}
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: dev}}},
		removeReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}}
	selfCalls := 0
	self := func(context.Context) error {
		selfCalls++
		apps.page.Applications = []fwire.ApplicationEntry{{Descriptor: installed}}
		return nil
	}
	var log []string
	created, err := createdOf(officialPicture.completeWorkspace(context.Background(), apps, &orderedReads{log: &log}, installed, nil, &workspaceNative{register: self}))
	if err != nil || !created || apps.removeCalls != 1 || selfCalls != 1 {
		t.Fatalf("stale entry: created=%v remove=%d self=%d err=%v", created, apps.removeCalls, selfCalls, err)
	}
	got := readFakeShortcut(t, filepath.Join(dir, "ComfyUI.lnk"))
	if got != (startShortcut{Target: panel, Arguments: "-open-app comfyui", Description: "ComfyUI with OpenAbstractions image tools"}) {
		t.Fatalf("shortcut %+v", got)
	}
	if !officialPicture.retainedShortcut().current() {
		t.Fatal("the written shortcut does not read current")
	}
	// A second run finds both in place and changes nothing.
	created, err = createdOf(officialPicture.completeWorkspace(context.Background(), apps, &orderedReads{log: &log}, installed, nil, &workspaceNative{register: self}))
	if err != nil || created || apps.removeCalls != 1 || selfCalls != 1 {
		t.Fatalf("second run: created=%v remove=%d self=%d err=%v", created, apps.removeCalls, selfCalls, err)
	}
}

// Setup replaces its own entries and stale OpenAbstractions ones; another
// program's entry with the same name stays, and the step reports it.
func TestComfyWorkspaceStepRefusesAThirdPartyEntry(t *testing.T) {
	dir := fakeStartMenu(t, filepath.Join(t.TempDir(), "Abstraction Panel.exe"))
	third := filepath.Join(t.TempDir(), "ComfyUI", "ComfyUI.exe")
	if err := os.MkdirAll(filepath.Dir(third), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(third, []byte("someone else's program"), 0o755); err != nil {
		t.Fatal(err)
	}
	foreign := fwire.ApplicationDescriptor{Name: "comfyui", Program: third, Title: "ComfyUI"}
	if comfyEntryReplaceable(foreign) {
		t.Fatal("an existing unsigned program outside Setup's folders read as replaceable")
	}
	installed := fwire.ApplicationDescriptor{Name: "comfyui", Program: filepath.Join(t.TempDir(), "launch-helper.exe"), Title: "ComfyUI — OA build"}
	apps := &setupApplicationsFake{page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: foreign}}},
		removeReply: fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}}
	self := func(context.Context) error { t.Fatal("register-self ran over another program's entry"); return nil }
	var log []string
	_, err := officialPicture.completeWorkspace(context.Background(), apps, &orderedReads{log: &log}, installed, nil, &workspaceNative{register: self})
	if err == nil || !strings.Contains(err.Error(), "another program is already registered; it was preserved") || apps.removeCalls != 0 {
		t.Fatalf("third-party entry: remove=%d err=%v", apps.removeCalls, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "ComfyUI.lnk")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("a shortcut was written beside another program's entry: %v", err)
	}

	// A program in a folder Setup installs versions into is Setup's own.
	home, err := setup.CurrentHome()
	if err != nil {
		t.Fatal(err)
	}
	own := filepath.Join(home.Data, "OA", "a", "cu", "0.8.0", "launch-helper.exe")
	if err := os.MkdirAll(filepath.Dir(own), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(own, []byte("helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	if !comfyEntryReplaceable(fwire.ApplicationDescriptor{Name: "comfyui", Program: own}) {
		t.Fatal("an entry for an earlier version Setup installed read as a third party's")
	}
	if comfyEntryReplaceable(fwire.ApplicationDescriptor{Name: "comfyui", Program: filepath.Join(home.Data, "OA", "a", "cu")}) {
		t.Fatal("the install folder itself read as an installed program")
	}
}

// A developer build of ComfyUI signed by this Panel's own signer
// (oaSameSigner) is OpenAbstractions' own and is replaced; the same program
// under another signer is a third party's and stays.
func TestComfyEntrySignedByThePanelsSignerIsReplaceable(t *testing.T) {
	dev := filepath.Join(t.TempDir(), "q", "OpenAbstractions ComfyUI.exe")
	if err := os.MkdirAll(filepath.Dir(dev), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dev, []byte("developer build"), 0o755); err != nil {
		t.Fatal(err)
	}
	saved, savedSigned := oaSameSigner, panelSigned
	t.Cleanup(func() { oaSameSigner, panelSigned = saved, savedSigned })
	panelSigned = func() bool { return true }
	entry := fwire.ApplicationDescriptor{Name: "comfyui", Program: dev, Title: "ComfyUI — OA build"}
	oaSameSigner = func(program string) bool { return program == dev }
	if !comfyEntryReplaceable(entry) {
		t.Fatal("a build signed by the Panel's signer read as a third party's")
	}
	oaSameSigner = func(string) bool { return false }
	if comfyEntryReplaceable(entry) {
		t.Fatal("a build under another signer read as OpenAbstractions' own")
	}
}

// Owner report on R20c, 2026-11-10: the unsigned preview Panel kept a signed
// developer build's entry from .forks\q as "another program's", Setup's
// reinstall stopped at the workspace step, and ComfyUI got no Start-menu
// shortcut. An unsigned Panel replaces an entry registered under
// OpenAbstractions' ComfyUI build title and keeps every other title.
func TestComfyEntryWithTheOABuildTitleIsReplaceableUnderAnUnsignedPanel(t *testing.T) {
	dev := filepath.Join(t.TempDir(), "q", "OpenAbstractions ComfyUI.exe")
	if err := os.MkdirAll(filepath.Dir(dev), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dev, []byte("developer build"), 0o755); err != nil {
		t.Fatal(err)
	}
	saved, savedSigned := oaSameSigner, panelSigned
	t.Cleanup(func() { oaSameSigner, panelSigned = saved, savedSigned })
	oaSameSigner = func(string) bool { return false }
	panelSigned = func() bool { return false }
	if !comfyEntryReplaceable(fwire.ApplicationDescriptor{Name: "comfyui", Program: dev, Title: setup.ComfyApplicationTitle}) {
		t.Fatal("an unsigned Panel kept a developer build of OpenAbstractions' ComfyUI")
	}
	if comfyEntryReplaceable(fwire.ApplicationDescriptor{Name: "comfyui", Program: dev, Title: "ComfyUI"}) {
		t.Fatal("an unsigned Panel replaced a third party's ComfyUI entry")
	}
	// The registration step removes the developer entry and registers the
	// installed build in its place.
	installed := fwire.ApplicationDescriptor{Name: "comfyui", Program: filepath.Join(t.TempDir(), "launch-helper.exe"), Title: setup.ComfyApplicationTitle}
	applied := fwire.ApplicationChange{Outcome: fwire.ApplicationOutcomeApplied}
	apps := &setupApplicationsFake{removeReply: applied, registerReply: applied, page: fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage, Applications: []fwire.ApplicationEntry{{Descriptor: fwire.ApplicationDescriptor{Name: "comfyui", Program: dev, Title: setup.ComfyApplicationTitle}}}}}
	created, err := createdOf(officialPicture.retainedWorkspaceRegistration().ensureRegistered(context.Background(), apps, installed, nil, nil))
	if err != nil || !created || apps.removeCalls != 1 || apps.registerCalls != 1 || apps.registered != installed {
		t.Fatalf("replace: created %v err %v removed %d registered %d %+v", created, err, apps.removeCalls, apps.registerCalls, apps.registered)
	}
}

// Another program's "ComfyUI" shortcut keeps its name; Setup's own takes the
// second name, an earlier one of Setup's is rewritten, and removal deletes
// only Setup's.
func TestComfyShortcutLeavesAnotherProgramsShortcut(t *testing.T) {
	panel := filepath.Join(t.TempDir(), "Abstraction Panel.exe")
	dir := fakeStartMenu(t, panel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	desktop := startShortcut{Target: platformProgram(`C:\Program Files\ComfyUI\ComfyUI.exe`), Description: "ComfyUI Desktop"}
	if err := writeStartShortcut(filepath.Join(dir, "ComfyUI.lnk"), desktop); err != nil {
		t.Fatal(err)
	}
	if officialPicture.retainedShortcut().current() {
		t.Fatal("another program's shortcut read as Setup's")
	}
	if err := officialPicture.retainedShortcut().ensure(); err != nil {
		t.Fatal(err)
	}
	if got := readFakeShortcut(t, filepath.Join(dir, "ComfyUI.lnk")); got != desktop {
		t.Fatalf("another program's shortcut changed: %+v", got)
	}
	second := filepath.Join(dir, "ComfyUI (OpenAbstractions).lnk")
	if got := readFakeShortcut(t, second); got.Target != panel || got.Arguments != "-open-app comfyui" {
		t.Fatalf("Setup's shortcut %+v", got)
	}
	// A shortcut that copies the arguments but starts another program is not
	// Setup's: it is neither rewritten nor removed.
	saved := oaSameSigner
	t.Cleanup(func() { oaSameSigner = saved })
	oaSameSigner = func(string) bool { return false }
	if ownStartShortcut(startShortcut{Target: platformProgram(`C:\Tools\launcher.exe`), Arguments: "-open-app comfyui"}, startShortcut{Target: panel, Arguments: "-open-app comfyui"}) {
		t.Fatal("another program's shortcut with copied arguments read as Setup's")
	}
	// Setup's shortcut from an earlier Panel path is rewritten in place.
	if err := writeStartShortcut(second, startShortcut{Target: filepath.Join(t.TempDir(), "Abstraction Panel.exe"), Arguments: "-open-app comfyui"}); err != nil {
		t.Fatal(err)
	}
	if officialPicture.retainedShortcut().current() {
		t.Fatal("a shortcut to an earlier Panel path read current")
	}
	if err := officialPicture.retainedShortcut().ensure(); err != nil || readFakeShortcut(t, second).Target != panel {
		t.Fatalf("earlier shortcut not rewritten: %v", err)
	}
	if err := officialPicture.retainedShortcut().remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(second); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("Setup's shortcut stayed: %v", err)
	}
	if got := readFakeShortcut(t, filepath.Join(dir, "ComfyUI.lnk")); got != desktop {
		t.Fatalf("removal touched another program's shortcut: %+v", got)
	}
}

func TestExtensionShortcutDoesNotClaimAnotherRegistration(t *testing.T) {
	panel := filepath.Join(t.TempDir(), "Abstraction Panel.exe")
	dir := fakeStartMenu(t, panel)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	spec := extensionShortcut{application: "example-app", title: "Example App", description: "Example App tools"}
	names := spec.names()
	foreignPath := filepath.Join(dir, names[0])
	foreign := startShortcut{Target: panel, Arguments: "-open-app another-app", Description: spec.description}
	if err := writeStartShortcut(foreignPath, foreign); err != nil {
		t.Fatal(err)
	}
	if err := spec.ensure(); err != nil {
		t.Fatal(err)
	}
	ownedPath := filepath.Join(dir, names[1])
	if got := readFakeShortcut(t, ownedPath); got.Arguments != "-open-app example-app" || !spec.current() {
		t.Fatalf("incorrect owned shortcut: %+v", got)
	}
	if err := spec.remove(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ownedPath); !errors.Is(err, fs.ErrNotExist) || readFakeShortcut(t, foreignPath) != foreign {
		t.Fatalf("removal did not preserve the other registration: %v", err)
	}
	if _, ok := declaredExtensionShortcut("unlisted-extension", ""); ok {
		t.Fatal("unknown extension supplied a shortcut")
	}
	for _, title := range []string{"../outside", "CON", "LPT1", "NUL.txt"} {
		spec.title = title
		if err := spec.ensure(); err == nil {
			t.Errorf("shortcut accepted unsafe title %q", title)
		}
	}
}

// An install whose packages finished reads as not finished while its
// ComfyUI is not in Apps, is another program's or an earlier entry, or has
// no Start-menu shortcut.
func TestComfyWorkspaceStateReadsAMissingRegistrationAsUnfinished(t *testing.T) {
	panel := filepath.Join(t.TempDir(), "Abstraction Panel.exe")
	fakeStartMenu(t, panel)
	journal := setup.Journal{Path: filepath.Join(t.TempDir(), "plan.json")}
	state := `{"plan":{"id":"plan-owner","selected":["comfyui","pytorch"],"components":[]},"approved_plan_id":"plan-owner","effects":{"comfyui-app":{"revision":7,"phase":"installed"}},"configuration":{}}`
	if err := os.WriteFile(journal.Path, []byte(state), 0o644); err != nil {
		t.Fatal(err)
	}
	program := filepath.Join(t.TempDir(), "OA", "a", "cu", "0.8.0-r17", "launch-helper.exe")
	previous := comfyInstalledProgram
	t.Cleanup(func() { comfyInstalledProgram = previous })
	comfyInstalledProgram = func(context.Context, setup.Journal) (string, error) { return program, nil }
	plan := setup.Plan{ID: "plan-owner", Selected: []setup.AppID{setup.ComfyUI, "pytorch"}}
	readNote := func(entries ...fwire.ApplicationDescriptor) (string, string) {
		page := fwire.ApplicationPage{Outcome: fwire.ApplicationOutcomePage}
		for _, d := range entries {
			page.Applications = append(page.Applications, fwire.ApplicationEntry{Descriptor: d})
		}
		p := &servicePanel{comfyApps: &setupApplicationsFake{page: page}}
		return officialPicture.workspaceState(p, context.Background(), journal, plan)
	}
	read := func(entries ...fwire.ApplicationDescriptor) string {
		state, _ := readNote(entries...)
		return state
	}
	if got := read(); got != "unregistered" {
		t.Fatalf("no entry: %q", got)
	}
	saved := oaSameSigner
	t.Cleanup(func() { oaSameSigner = saved })
	third := filepath.Join(t.TempDir(), "ComfyUI.exe")
	if err := os.WriteFile(third, []byte("another program"), 0o755); err != nil {
		t.Fatal(err)
	}
	oaSameSigner = func(program string) bool { return false }
	other := fwire.ApplicationDescriptor{Name: "comfyui", Program: third, Title: "ComfyUI"}
	if got, note := readNote(other); got != "other" || note != "A picture workspace entry for another program is already registered; it was preserved." {
		t.Fatalf("another program's entry: %q %q", got, note)
	}
	oaSameSigner = func(program string) bool { return program == third }
	if got := read(other); got != "stale" {
		t.Fatalf("an entry signed by the Panel's signer: %q", got)
	}
	installed := fwire.ApplicationDescriptor{Name: "comfyui", Program: program, Title: "ComfyUI — OA build"}
	if got := read(installed); got != "no_shortcut" {
		t.Fatalf("entry without a shortcut: %q", got)
	}
	if err := officialPicture.retainedShortcut().ensure(); err != nil {
		t.Fatal(err)
	}
	if got := read(installed); got != "ready" {
		t.Fatalf("entry and shortcut: %q", got)
	}
	// A plan without ComfyUI, or a ComfyUI not yet installed, says nothing.
	if got, _ := officialPicture.workspaceState(&servicePanel{comfyApps: &setupApplicationsFake{}}, context.Background(), journal, setup.Plan{ID: "plan-owner", Selected: []setup.AppID{"pytorch"}}); got != "" {
		t.Fatalf("plan without ComfyUI: %q", got)
	}
}
