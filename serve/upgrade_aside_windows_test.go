package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/fileid"
	"golang.org/x/sys/windows"
)

const upgradeAsideHelper = "OA_TEST_UPGRADE_ASIDE_HELPER"

// TestUpgradeAsideHelperProcess is the running installed image of
// TestUpgradeAsideLetsANewFileReplaceARunningImage.
func TestUpgradeAsideHelperProcess(t *testing.T) {
	if os.Getenv(upgradeAsideHelper) != "1" {
		t.Skip("helper process only")
	}
	time.Sleep(30 * time.Second)
}

func moveFixtureUpgradeImagesAside(env exclusionEnv, scope string, folders []string, incoming string, predecessors []relatedProduct) []string {
	tools, release := holdSameUpgradeTools(incoming, predecessors, folders)
	if release != nil {
		defer release()
	}
	record, path, _ := env.load(scope)
	var caller processIdentity
	if record != nil {
		caller, _ = env.admittedUpgradeInstaller(path, record)
	}
	return moveUpgradeImagesAsideFor(env, scope, folders, tools, caller)
}

func settleUpgradeAside(env exclusionEnv, scope string, rollback bool) []string {
	record, path, err := env.load(scope)
	if err != nil {
		return []string{err.Error()}
	}
	var caller processIdentity
	if record != nil {
		caller, _ = env.admittedUpgradeInstaller(path, record)
	}
	notes, _, err := settleUpgradeAsideOperation(env, scope, rollback, caller, false)
	if err != nil {
		notes = append(notes, err.Error())
	}
	return notes
}

func newUpgradeLibraryExclusion(t *testing.T) *testExclusion {
	t.Helper()
	x := newTestExclusion(t)
	x.env.lock = lockUpgradeScope
	x.env.read = readExclusionFile
	x.env.writeContext = writeExclusionFileContext
	x.env.remove = removeExclusionFile
	x.env.trusted = trustedExclusionFile
	x.env.ended = upgradeInstallerEnded
	return x
}

func writeAsideFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readAsideFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// The R6 to R7 upgrade required a restart because a process ran
// tools\openabstractions.exe when Windows Installer reached it. A running
// image refuses a new file at its name until begin-upgrade moves it aside.
func TestUpgradeAsideLetsANewFileReplaceARunningImage(t *testing.T) {
	folder := t.TempDir()
	installed := filepath.Join(folder, "tools", "openabstractions.exe")
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(installed), 0o755); err != nil {
		t.Fatal(err)
	}
	target, err := os.Create(installed)
	if err != nil {
		t.Fatal(err)
	}
	_, copyErr := io.Copy(target, source)
	source.Close()
	if err := target.Close(); copyErr != nil || err != nil {
		t.Fatal(copyErr, err)
	}
	helper := exec.Command(installed, "-test.run=^TestUpgradeAsideHelperProcess$")
	helper.Env = append(os.Environ(), upgradeAsideHelper+"=1")
	helper.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	stopped := false
	stop := func() {
		if !stopped {
			stopped = true
			helper.Process.Kill()
			helper.Wait()
		}
	}
	defer stop()
	time.Sleep(200 * time.Millisecond)
	if err := os.WriteFile(installed, []byte("new"), 0o644); err == nil {
		t.Fatal("a running image accepted a new file; the test does not reproduce the in-use condition")
	}

	record := &upgradeExclusion{Installer: holderFor(9), Begun: time.Date(2026, 11, 7, 9, 12, 21, 0, time.UTC)}
	if notes := moveUpgradeImagesAside([]string{folder}, record, nil, nil); len(notes) != 0 {
		t.Fatalf("move aside: %q", notes)
	}
	if regularFileExists(installed) {
		t.Fatal("the running image kept its installed name")
	}
	if err := os.WriteFile(installed, []byte("new"), 0o644); err != nil {
		t.Fatalf("the installer could not write the new image: %v", err)
	}
	dir := filepath.Join(upgradeAsideRoot(folder), upgradeAsideName(record))
	if notes := finishUpgradeAsideDir(folder, dir); len(notes) != 1 || !strings.Contains(notes[0], "delete previous image") {
		t.Fatalf("a still-running previous image must stay and be reported: %q", notes)
	}
	stop()
	if notes := finishUpgradeAsideDir(folder, dir); len(notes) != 0 {
		t.Fatalf("finish after exit: %q", notes)
	}
	if got := readAsideFile(t, installed); got != "new" {
		t.Fatalf("installed image %q", got)
	}
	if _, err := os.Lstat(upgradeAsideRoot(folder)); !os.IsNotExist(err) {
		t.Fatalf("aside directory left after commit: %v", err)
	}
}

func TestUpgradeAsideMovesOnlyInstalledMappedLibraries(t *testing.T) {
	system, err := windows.GetSystemDirectory()
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(system, "version.dll"))
	if err != nil {
		t.Fatal(err)
	}
	predecessor, incoming := t.TempDir(), t.TempDir()
	folders := []string{predecessor, incoming}
	x := newTestExclusion(t)
	holder := holderFor(46)
	x.alive[holder.PID] = true
	if err := x.env.begin("machine", folders, holder); err != nil {
		t.Fatal(err)
	}

	tools := filepath.Join(predecessor, "tools")
	if err := os.MkdirAll(tools, 0o755); err != nil {
		t.Fatal(err)
	}
	libraries := []string{"abstraction_ipc.dll", "abstraction_facade_appmodel.dll"}
	var modules []windows.Handle
	originalFiles := make(map[string]string)
	closeModules := func() {
		for _, module := range modules {
			if err := windows.FreeLibrary(module); err != nil {
				t.Error(err)
			}
		}
		modules = nil
	}
	t.Cleanup(closeModules)
	for _, name := range libraries {
		path := filepath.Join(tools, name)
		if err := os.WriteFile(path, original, 0o644); err != nil {
			t.Fatal(err)
		}
		originalFiles[name], err = fileid.FileKey(path)
		if err != nil {
			t.Fatal(err)
		}
		module, err := windows.LoadLibraryEx(path, 0, windows.LOAD_LIBRARY_SEARCH_DLL_LOAD_DIR|windows.LOAD_LIBRARY_SEARCH_SYSTEM32)
		if err != nil {
			t.Fatal(err)
		}
		modules = append(modules, module)
		if err := os.WriteFile(path, []byte("new"), 0o644); err == nil {
			t.Fatalf("%s did not reproduce an in-use image", name)
		}
		writeAsideFile(t, filepath.Join(incoming, "tools", name), "same name in incoming folder")
	}
	foreign := filepath.Join(tools, "unrelated.dll")
	writeAsideFile(t, foreign, "foreign")
	for _, name := range upgradeProcessNames {
		if filepath.Ext(name) == ".dll" {
			t.Fatalf("library role became a process target: %s", name)
		}
	}
	// Distinct registered/incoming folders leave the mapped predecessor
	// untouched. Nothing may be returned to that root at commit.
	if notes := moveFixtureUpgradeImagesAside(x.env, "machine", folders, incoming, []relatedProduct{{location: predecessor}}); len(notes) != 0 {
		t.Fatalf("cross-root move: %q", notes)
	}
	if notes := settleUpgradeAside(x.env, "machine", false); len(notes) != 0 {
		t.Fatalf("cross-root commit: %q", notes)
	}
	for _, name := range libraries {
		raw, err := os.ReadFile(filepath.Join(tools, name))
		if err != nil || !bytes.Equal(raw, original) {
			t.Fatalf("cross-root move changed predecessor %s: %v", name, err)
		}
	}
	if _, err := os.Lstat(upgradeAsideRoot(predecessor)); !os.IsNotExist(err) {
		t.Fatalf("cross-root upgrade left a library for commit to re-expose: %v", err)
	}
	if notes := moveFixtureUpgradeImagesAside(x.env, "machine", folders, predecessor, []relatedProduct{{location: predecessor}}); len(notes) != 0 {
		t.Fatalf("mapped predecessor move: %q", notes)
	}
	for _, name := range libraries {
		path := filepath.Join(tools, name)
		if regularFileExists(path) {
			t.Fatalf("mapped predecessor kept the installed name: %s", name)
		}
		writeAsideFile(t, path, "new")
		if got := readAsideFile(t, filepath.Join(incoming, "tools", name)); got != "same name in incoming folder" {
			t.Fatalf("unregistered folder's %s changed: %q", name, got)
		}
	}
	if got := readAsideFile(t, foreign); got != "foreign" {
		t.Fatalf("unrelated file changed: %q", got)
	}
	if notes := settleUpgradeAside(x.env, "machine", true); len(notes) != 0 {
		t.Fatalf("rollback while mapped: %q", notes)
	}
	for _, name := range libraries {
		path := filepath.Join(tools, name)
		key, err := fileid.FileKey(path)
		if err != nil || key != originalFiles[name] {
			t.Fatalf("rollback changed the identity of %s: %v", name, err)
		}
		raw, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(raw, original) {
			t.Fatalf("rollback changed the contents of %s: %v", name, err)
		}
	}

	if notes := moveFixtureUpgradeImagesAside(x.env, "machine", folders, predecessor, []relatedProduct{{location: predecessor}}); len(notes) != 0 {
		t.Fatalf("mapped predecessor move for commit: %q", notes)
	}
	for _, name := range libraries {
		writeAsideFile(t, filepath.Join(tools, name), "new")
	}
	if notes := settleUpgradeAside(x.env, "machine", false); len(notes) != len(libraries) {
		t.Fatalf("commit while mapped did not retain both predecessors: %q", notes)
	}
	for _, name := range libraries {
		if got := readAsideFile(t, filepath.Join(tools, name)); got != "new" {
			t.Fatalf("commit while mapped changed replacement %s: %q", name, got)
		}
	}
	if notes := x.env.release("machine"); len(notes) != 0 {
		t.Fatalf("release committed exclusion with mapped predecessors: %q", notes)
	}
	closeModules()
	if err := x.env.begin("machine", folders, holderFor(47)); err != nil {
		t.Fatal(err)
	}
	if notes := moveFixtureUpgradeImagesAside(x.env, "machine", folders, predecessor, nil); len(notes) != 0 {
		t.Fatalf("later upgrade after module release: %q", notes)
	}
	for _, name := range libraries {
		if got := readAsideFile(t, filepath.Join(tools, name)); got != "new" {
			t.Fatalf("commit restored old %s over replacement: %q", name, got)
		}
	}
	if _, err := os.Lstat(upgradeAsideRoot(predecessor)); !os.IsNotExist(err) {
		t.Fatalf("commit left predecessor library custody: %v", err)
	}
}

func TestUpgradeAsideRefusesALibraryLinkedOutsideTheInstallation(t *testing.T) {
	folder := t.TempDir()
	external := filepath.Join(t.TempDir(), "foreign.dll")
	writeAsideFile(t, external, "foreign")
	path := filepath.Join(folder, "tools", "abstraction_ipc.dll")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(external, path); err != nil {
		t.Fatal(err)
	}
	x := newTestExclusion(t)
	holder := holderFor(48)
	x.alive[holder.PID] = true
	if err := x.env.begin("machine", []string{folder}, holder); err != nil {
		t.Fatal(err)
	}
	notes := moveFixtureUpgradeImagesAside(x.env, "machine", []string{folder}, folder, []relatedProduct{{location: folder}})
	if len(notes) != 1 || !strings.Contains(notes[0], "regular file with one installed name") ||
		readAsideFile(t, path) != "foreign" || readAsideFile(t, external) != "foreign" {
		t.Fatalf("hard-linked library was not preserved: %q", notes)
	}
}

func TestUpgradeAsideLibraryFailureRetainsSiblingRollback(t *testing.T) {
	folder := t.TempDir()
	x := newTestExclusion(t)
	holder := holderFor(47)
	x.alive[holder.PID] = true
	if err := x.env.begin("machine", []string{folder}, holder); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(folder, "tools")
	ipc := filepath.Join(tools, "abstraction_ipc.dll")
	appmodel := filepath.Join(tools, "abstraction_facade_appmodel.dll")
	writeAsideFile(t, ipc, "old IPC")
	writeAsideFile(t, appmodel, "old app model")
	name, err := windows.UTF16PtrFromString(appmodel)
	if err != nil {
		t.Fatal(err)
	}
	// A handle denying delete sharing can prevent relocation independently
	// of loading an image. Its exact file must stay at the original name.
	held, err := windows.CreateFile(name, windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = windows.CloseHandle(held) })
	notes := moveFixtureUpgradeImagesAside(x.env, "machine", []string{folder}, folder, []relatedProduct{{location: folder}})
	if len(notes) != 1 || !strings.Contains(notes[0], "move "+appmodel+" aside") {
		t.Fatalf("failed sibling move: %q", notes)
	}
	if got := readAsideFile(t, appmodel); got != "old app model" {
		t.Fatalf("failed sibling move changed the original: %q", got)
	}
	if regularFileExists(ipc) {
		t.Fatal("successful first move did not free the replacement name")
	}
	writeAsideFile(t, ipc, "new IPC")
	if notes := settleUpgradeAside(x.env, "machine", true); len(notes) != 0 {
		t.Fatalf("rollback with failed sibling move: %q", notes)
	}
	if got := readAsideFile(t, ipc); got != "old IPC" {
		t.Fatalf("failed sibling move lost first rollback: %q", got)
	}
}

func TestUpgradeAsideCommitReturnsUnreplacedImagesAndRollbackRestoresAll(t *testing.T) {
	x := newTestExclusion(t)
	folder := t.TempDir()
	holder := holderFor(41)
	x.alive[holder.PID] = true
	if err := x.env.begin("machine", []string{folder}, holder); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(folder, "tools")
	core := filepath.Join(tools, "openabstractions.exe")
	inventory := filepath.Join(tools, "inventoryd.exe")
	unrelated := filepath.Join(tools, "notes.txt")
	reset := func() {
		writeAsideFile(t, core, "old core")
		writeAsideFile(t, inventory, "old inventory")
		writeAsideFile(t, unrelated, "kept")
	}

	reset()
	if notes := moveFixtureUpgradeImagesAside(x.env, "machine", []string{folder}, folder, nil); len(notes) != 0 {
		t.Fatalf("move aside: %q", notes)
	}
	if regularFileExists(core) || regularFileExists(inventory) || !regularFileExists(unrelated) {
		t.Fatal("begin moved the wrong files")
	}
	writeAsideFile(t, core, "new core")
	if notes := settleUpgradeAside(x.env, "machine", false); len(notes) != 0 {
		t.Fatalf("commit: %q", notes)
	}
	if readAsideFile(t, core) != "new core" || readAsideFile(t, inventory) != "old inventory" {
		t.Fatal("commit must keep the new file and return the image the transaction left out")
	}
	if _, err := os.Lstat(upgradeAsideRoot(folder)); !os.IsNotExist(err) {
		t.Fatalf("aside directory left after commit: %v", err)
	}

	reset()
	if notes := moveFixtureUpgradeImagesAside(x.env, "machine", []string{folder}, folder, nil); len(notes) != 0 {
		t.Fatalf("move aside: %q", notes)
	}
	writeAsideFile(t, core, "new core")
	if notes := settleUpgradeAside(x.env, "machine", true); len(notes) != 0 {
		t.Fatalf("rollback: %q", notes)
	}
	if readAsideFile(t, core) != "old core" || readAsideFile(t, inventory) != "old inventory" {
		t.Fatal("rollback must restore every previous image")
	}
	if _, err := os.Lstat(upgradeAsideRoot(folder)); !os.IsNotExist(err) {
		t.Fatalf("aside directory left after rollback: %v", err)
	}
}

func TestUpgradeAsideRetainsFilesOutsideItsExactImageRoles(t *testing.T) {
	folder := t.TempDir()
	aside := filepath.Join(upgradeAsideRoot(folder), "prior")
	for _, name := range []string{"foreign.exe", "unrecognized.dll", "ABSTRACTION_IPC.DLL"} {
		writeAsideFile(t, filepath.Join(aside, name), "not owned")
	}
	notes := finishUpgradeAsideDir(folder, aside)
	if len(notes) != 3 {
		t.Fatalf("expected three unowned files to be retained: %q", notes)
	}
	for _, name := range []string{"foreign.exe", "unrecognized.dll", "ABSTRACTION_IPC.DLL"} {
		if got := readAsideFile(t, filepath.Join(aside, name)); got != "not owned" {
			t.Fatalf("%s changed: %q", name, got)
		}
		if regularFileExists(filepath.Join(folder, "tools", name)) {
			t.Fatalf("unowned %s moved into the install", name)
		}
	}
}

func TestUpgradeAsideUnverifiedInstallerRequiresExactSnapshotLeaf(t *testing.T) {
	admitted := processIdentity{PID: 41, Image: "msiexec.exe"}
	for _, image := range []string{"", "MSIEXEC.EXE", "other.exe"} {
		if sameUpgradeInstaller(admitted, processIdentity{PID: admitted.PID, Image: image}) {
			t.Fatalf("unverified snapshot %q inherited custody", image)
		}
	}
	if !sameUpgradeInstaller(admitted, admitted) {
		t.Fatal("same legacy snapshot no longer matches")
	}
}

// An earlier transaction's leftovers are settled with commit rules and never
// restored by this transaction's rollback.
func TestUpgradeAsideSettlesEarlierLeftoversBeforeMoving(t *testing.T) {
	x := newTestExclusion(t)
	folder := t.TempDir()
	holder := holderFor(42)
	x.alive[holder.PID] = true
	if err := x.env.begin("machine", []string{folder}, holder); err != nil {
		t.Fatal(err)
	}
	tools := filepath.Join(folder, "tools")
	stale := filepath.Join(upgradeAsideRoot(folder), "7-1")
	writeAsideFile(t, filepath.Join(tools, "openabstractions.exe"), "current core")
	writeAsideFile(t, filepath.Join(stale, "openabstractions.exe"), "stale core")
	writeAsideFile(t, filepath.Join(stale, "modelhostd.exe"), "only modelhost")
	if notes := moveFixtureUpgradeImagesAside(x.env, "machine", []string{folder}, folder, nil); len(notes) != 0 {
		t.Fatalf("move aside: %q", notes)
	}
	if _, err := os.Lstat(stale); !os.IsNotExist(err) {
		t.Fatalf("earlier leftovers remain: %v", err)
	}
	writeAsideFile(t, filepath.Join(tools, "openabstractions.exe"), "new core")
	if notes := settleUpgradeAside(x.env, "machine", true); len(notes) != 0 {
		t.Fatalf("rollback: %q", notes)
	}
	if got := readAsideFile(t, filepath.Join(tools, "openabstractions.exe")); got != "current core" {
		t.Fatalf("rollback restored %q", got)
	}
	if got := readAsideFile(t, filepath.Join(tools, "modelhostd.exe")); got != "only modelhost" {
		t.Fatalf("an image only an earlier transaction held was lost: %q", got)
	}
}

func TestUpgradeAsideReleaseWithoutRecordDoesNotCreateScope(t *testing.T) {
	x := newTestExclusion(t)
	x.env.lock = lockUpgradeScope
	notes, released, err := settleUpgradeAsideOperation(x.env, "user", false, processIdentity{}, true)
	if err != nil || len(notes) != 0 || len(released) != 1 {
		t.Fatalf("release absent record: notes=%q released=%q err=%v", notes, released, err)
	}
	if _, err := os.Stat(filepath.Join(x.dir, "user")); !os.IsNotExist(err) {
		t.Fatalf("release created absent scope: %v", err)
	}
}

func TestUpgradeAsideReleaseReadErrorDoesNotCreateScope(t *testing.T) {
	x := newTestExclusion(t)
	x.env.lock = lockUpgradeScope
	x.env.read = func(string) ([]byte, error) { return nil, os.ErrPermission }
	_, _, err := settleUpgradeAsideOperation(x.env, "user", false, processIdentity{}, true)
	if err == nil || !strings.Contains(err.Error(), "permission") {
		t.Fatalf("unreadable record accepted: %v", err)
	}
	if _, err := os.Stat(filepath.Join(x.dir, "user")); !os.IsNotExist(err) {
		t.Fatalf("release created scope for unreadable record: %v", err)
	}
}

func TestUpgradeAsideWithoutRecordMovesNothing(t *testing.T) {
	x := newTestExclusion(t)
	folder := t.TempDir()
	core := filepath.Join(folder, "tools", "openabstractions.exe")
	writeAsideFile(t, core, "core")
	if notes := moveFixtureUpgradeImagesAside(x.env, "machine", []string{folder}, folder, nil); len(notes) != 1 || !strings.Contains(notes[0], "no exclusion record") {
		t.Fatalf("notes %q", notes)
	}
	if !regularFileExists(core) {
		t.Fatal("moved an image without a record for commit and rollback")
	}
	if notes := settleUpgradeAside(x.env, "machine", false); len(notes) != 0 {
		t.Fatalf("settle without record: %q", notes)
	}
}

func TestUpgradeAsideLibraryCustodyRejectsSubstitution(t *testing.T) {
	const role = "abstraction_ipc.dll"
	for _, phase := range []string{"rollback", "commit", "deferred cleanup", "tools replacement", "before rename"} {
		t.Run(phase, func(t *testing.T) {
			x := newUpgradeLibraryExclusion(t)
			folder := t.TempDir()
			tools := filepath.Join(folder, "tools")
			installed := filepath.Join(tools, role)
			writeAsideFile(t, installed, "installed original")
			folders := []string{folder}
			holder := holderFor(49)
			x.alive[holder.PID] = true
			if err := x.env.begin("user", folders, holder); err != nil {
				t.Fatal(err)
			}
			record, path, err := x.env.load("user")
			if err != nil || record == nil {
				t.Fatalf("fixture record: %v", err)
			}
			asideDir := filepath.Join(upgradeAsideRoot(folder), upgradeAsideName(record))
			backup := filepath.Join(asideDir, role)
			ledger := upgradeLibraryLedger{env: x.env, scope: "user", path: path}
			rename := func(source, destination string) {
				t.Helper()
				if err := os.Rename(source, destination); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "before rename" {
				// A destination collision stops the native rename after the
				// intent was recorded. The installed original remains intact.
				writeAsideFile(t, backup, "foreign")
			}
			notes := moveFixtureUpgradeImagesAside(x.env, "user", folders, folder, []relatedProduct{{location: folder}})
			if (len(notes) != 0) != (phase == "before rename") {
				t.Fatalf("begin-upgrade: %q", notes)
			}
			proof, err := ledger.load(upgradeAsideName(record))
			if err != nil || proof == nil || proof.Libraries[role] == "" {
				t.Fatalf("begin-upgrade intent: %v", err)
			}
			expectedInstalled := "installed original"
			if phase != "before rename" {
				expectedInstalled = "new installed"
				writeAsideFile(t, installed, expectedInstalled)
			}
			switch phase {
			case "rollback", "deferred cleanup":
				rename(backup, filepath.Join(asideDir, "original.dll"))
				writeAsideFile(t, backup, "foreign")
			case "commit":
				rename(asideDir, asideDir+"-original")
				writeAsideFile(t, backup, "foreign")
			case "tools replacement":
				rename(tools, tools+"-original")
				writeAsideFile(t, installed, expectedInstalled)
				if err := os.Mkdir(upgradeAsideRoot(folder), 0o755); err != nil {
					t.Fatal(err)
				}
				rename(filepath.Join(tools+"-original", upgradeAsideFolder, filepath.Base(asideDir)), asideDir)
			}
			if phase == "deferred cleanup" {
				// Commit sees the right directories, but the wrong file.
				// A later sweep must independently refuse the same impostor.
				notes = settleUpgradeAside(x.env, "user", false)
				if !strings.Contains(strings.Join(notes, "\n"), "custody file changed") {
					t.Fatalf("commit accepted a substituted file: %q", notes)
				}
				if notes := x.env.release("user"); len(notes) != 0 {
					t.Fatalf("release: %q", notes)
				}
				if err := x.env.begin("user", folders, holderFor(50)); err != nil {
					t.Fatal(err)
				}
				notes = moveFixtureUpgradeImagesAside(x.env, "user", folders, folder, nil)
			} else {
				notes = settleUpgradeAside(x.env, "user", phase != "commit")
			}
			mismatch := "custody file changed"
			expectedBackup := "foreign"
			if phase == "commit" || phase == "tools replacement" {
				mismatch = "custody directories changed"
			}
			if phase == "tools replacement" {
				expectedBackup = "installed original"
			}
			if !strings.Contains(strings.Join(notes, "\n"), mismatch) {
				t.Fatalf("settlement accepted a substituted object: %q", notes)
			}
			if got := readAsideFile(t, installed); got != expectedInstalled {
				t.Fatalf("settlement changed the installed name: %q", got)
			}
			if got := readAsideFile(t, backup); got != expectedBackup {
				t.Fatalf("settlement changed the backup name: %q", got)
			}
		})
	}
}

func TestUpgradeAsideRecoversAbandonedOriginal(t *testing.T) {
	const role = "abstraction_ipc.dll"
	for _, phase := range []string{"before rename", "after rename", "committed without replacement", "live old installer", "existing installed leaf", "cross-root"} {
		t.Run(phase, func(t *testing.T) {
			x := newUpgradeLibraryExclusion(t)
			folder := t.TempDir()
			installed := filepath.Join(folder, "tools", role)
			writeAsideFile(t, installed, "original")
			originalKey, err := fileid.FileKey(installed)
			if err != nil {
				t.Fatal(err)
			}
			self, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			helper := exec.Command(self, "-test.run=^TestUpgradeAsideHelperProcess$")
			helper.Env = append(os.Environ(), upgradeAsideHelper+"=1")
			helper.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
			if err := helper.Start(); err != nil {
				t.Fatal(err)
			}
			old := holderFor(uint32(helper.Process.Pid))
			stop := func() {
				if helper.ProcessState == nil {
					_ = helper.Process.Kill()
					_ = helper.Wait()
				}
			}
			t.Cleanup(stop)
			handle, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, old.PID)
			if err != nil {
				t.Fatal(err)
			}
			old.Created, err = handleCreated(handle)
			windows.CloseHandle(handle)
			if err != nil {
				t.Fatal(err)
			}
			x.alive[old.PID] = true
			incoming := folder
			folders := []string{folder}
			if phase == "cross-root" {
				incoming = t.TempDir()
				writeAsideFile(t, filepath.Join(incoming, "tools", role), "other install")
				folders = append(folders, incoming)
			}
			predecessors := []relatedProduct{{location: folder}}
			if err := x.env.begin("user", folders, old); err != nil {
				t.Fatal(err)
			}
			record, path, err := x.env.load("user")
			if err != nil || record == nil {
				t.Fatalf("old exclusion: %v", err)
			}
			backup := filepath.Join(upgradeAsideRoot(folder), upgradeAsideName(record), role)
			ledger := upgradeLibraryLedger{env: x.env, scope: "user", path: path}
			if phase == "before rename" {
				// Record the intent through begin-upgrade, then remove the
				// collision that stopped the rename before the simulated exit.
				writeAsideFile(t, backup, "collision")
			}
			notes := moveFixtureUpgradeImagesAside(x.env, "user", folders, folder, predecessors)
			if (len(notes) != 0) != (phase == "before rename") {
				t.Fatalf("old move: %q", notes)
			}
			if phase == "before rename" {
				if err := os.Remove(backup); err != nil {
					t.Fatal(err)
				}
			}
			if phase == "existing installed leaf" {
				writeAsideFile(t, installed, "successor")
			}
			if phase == "committed without replacement" {
				proof, err := ledger.load(upgradeAsideName(record))
				if err != nil || proof == nil {
					t.Fatalf("load old proof: %v", err)
				}
				// Simulate commit reaching its durable marker and exiting
				// before it could settle the original to an absent leaf.
				proof.Committed = true
				if err := ledger.store(proof); err != nil {
					t.Fatal(err)
				}
			}
			if phase != "live old installer" {
				stop()
			}
			// Admission may ignore an expired unverified holder. Recovery
			// must still independently prove that its occurrence has ended.
			x.alive[old.PID] = false
			active := holderFor(51)
			x.alive[active.PID] = true
			if err := x.env.begin("user", folders, active); err != nil {
				t.Fatal(err)
			}
			notes = moveFixtureUpgradeImagesAside(x.env, "user", folders, incoming, predecessors)
			if phase == "live old installer" || phase == "existing installed leaf" || phase == "cross-root" {
				if len(notes) == 0 || readAsideFile(t, backup) != "original" {
					t.Fatalf("the stale transaction lost custody: %q", notes)
				}
				if notes := settleUpgradeAside(x.env, "user", true); len(notes) != 0 {
					t.Fatalf("new rollback: %q", notes)
				}
				if phase == "existing installed leaf" && readAsideFile(t, installed) != "successor" {
					t.Fatal("recovery overwrote the existing installed leaf")
				}
				if phase != "existing installed leaf" && fileExists(installed) {
					t.Fatal("the stale original was re-exposed")
				}
				return
			}
			if len(notes) != 0 || fileExists(backup) || fileExists(installed) {
				t.Fatalf("retry did not take custody of the recovered original: %q", notes)
			}
			for range 2 {
				if notes := settleUpgradeAside(x.env, "user", true); len(notes) != 0 {
					t.Fatalf("retry rollback: %q", notes)
				}
				key, err := fileid.FileKey(installed)
				if err != nil || key != originalKey {
					t.Fatalf("retry rollback lost the exact original: %v", err)
				}
			}
		})
	}
}

func TestUpgradeAsideToolsBindingSurvivesAdmissionWindow(t *testing.T) {
	folder := t.TempDir()
	tools := filepath.Join(folder, "tools")
	installed := filepath.Join(tools, "abstraction_ipc.dll")
	writeAsideFile(t, installed, "registered original")
	held, release := holdSameUpgradeTools(folder, []relatedProduct{{location: folder}}, []string{folder})
	if held == nil {
		t.Fatal("could not hold the registered tools directory")
	}
	defer release()
	if err := os.Rename(tools, tools+"-displaced"); err == nil {
		t.Fatal("the registered tools directory could be displaced during admission")
	}
}

func TestUpgradeAsideSettlementRequiresAdmittedInstaller(t *testing.T) {
	for _, phase := range []string{"delayed action", "joined Burn chain", "unverified legacy user"} {
		t.Run(phase, func(t *testing.T) {
			x := newUpgradeLibraryExclusion(t)
			folder := t.TempDir()
			folders := []string{folder}
			tools := filepath.Join(folder, "tools")
			dll := filepath.Join(tools, "abstraction_ipc.dll")
			exe := filepath.Join(tools, "openabstractions.exe")
			writeAsideFile(t, dll, "original DLL")
			writeAsideFile(t, exe, "original EXE")
			old, current := holderFor(81), holderFor(82)
			switch phase {
			case "delayed action":
				// Same PID with a different creation time still denotes a
				// distinct occurrence and cannot settle the new transaction.
				old.PID = current.PID
				if err := x.env.begin("user", folders, old); err != nil {
					t.Fatal(err)
				}
			case "joined Burn chain":
				x.alive[old.PID] = true
				const token = "0123456789abcdef0123456789abcdef"
				if err := x.env.beginContext(t.Context(), "user", folders, old, token); err != nil {
					t.Fatal(err)
				}
				if err := x.env.beginContext(t.Context(), "user", folders, current, token); err != nil {
					t.Fatal(err)
				}
			case "unverified legacy user":
				current.Created = 0
			}
			if phase != "joined Burn chain" {
				if err := x.env.begin("user", folders, current); err != nil {
					t.Fatal(err)
				}
			}
			x.alive[current.PID] = true
			notes := moveFixtureUpgradeImagesAside(x.env, "user", folders, folder, []relatedProduct{{location: folder}})
			if phase == "unverified legacy user" {
				if len(notes) != 1 || !strings.Contains(notes[0], "creation time is unavailable") {
					t.Fatalf("unverified holder diagnostic: %q", notes)
				}
			} else if len(notes) != 0 {
				t.Fatalf("begin: %q", notes)
			}
			if fileExists(exe) {
				t.Fatal("the legacy EXE was left installed")
			}
			if phase == "unverified legacy user" {
				if got := readAsideFile(t, dll); got != "original DLL" {
					t.Fatalf("unverified parent unexpectedly moved the DLL: %q", got)
				}
			} else {
				if fileExists(dll) {
					t.Fatal("the admitted installer did not move the DLL")
				}
				for _, rollback := range []bool{false, true} {
					if _, _, err := settleUpgradeAsideOperation(x.env, "user", rollback, old, true); err == nil {
						t.Fatal("an old installer occurrence acquired settlement authority")
					}
					if record, _, err := x.env.load("user"); err != nil || record == nil {
						t.Fatalf("an old action released the successor's exclusion: %v", err)
					}
					if fileExists(exe) || fileExists(dll) {
						t.Fatal("an old action moved the successor's staged files")
					}
				}
			}
			if notes, _, err := settleUpgradeAsideOperation(x.env, "user", true, current, true); err != nil || len(notes) != 0 {
				t.Fatalf("authorized rollback and release: %q %v", notes, err)
			}
			if record, _, err := x.env.load("user"); err != nil || record != nil {
				t.Fatalf("the admitted action left the exclusion: %v", err)
			}
			if readAsideFile(t, exe) != "original EXE" || readAsideFile(t, dll) != "original DLL" {
				t.Fatal("authorized rollback did not leave the original tools")
			}
		})
	}
}

func TestUpgradeAsideBurnFallbackRequiresTheJoinedMSIToHaveExited(t *testing.T) {
	for _, phase := range []string{"exited", "live", "unknown", "wrong Burn", "commit", "successor"} {
		t.Run(phase, func(t *testing.T) {
			x := newTestExclusion(t)
			folder := t.TempDir()
			folders := []string{folder}
			dll := filepath.Join(folder, "tools", "abstraction_ipc.dll")
			writeAsideFile(t, dll, "original DLL")
			burn, msi := holderFor(91), holderFor(92)
			x.alive[burn.PID] = true
			x.alive[msi.PID] = true
			locked := false
			x.env.lock = func(_ context.Context, _, _ string) (func(), error) {
				if locked {
					t.Fatal("scope lock acquired twice")
				}
				locked = true
				return func() { locked = false }, nil
			}
			exitChecks := 0
			if phase != "unknown" {
				x.env.ended = func(holder processIdentity) bool {
					if !locked || holder != msi {
						t.Fatalf("exit check outside scope lock or against wrong MSI: %+v", holder)
					}
					exitChecks++
					return phase != "live"
				}
			}
			const token = "0123456789abcdef0123456789abcdef"
			if err := x.env.beginContext(t.Context(), "machine", folders, burn, token); err != nil {
				t.Fatal(err)
			}
			if err := x.env.beginContext(t.Context(), "machine", folders, msi, token); err != nil {
				t.Fatal(err)
			}
			if notes := moveFixtureUpgradeImagesAside(x.env, "machine", folders, folder, []relatedProduct{{location: folder}}); len(notes) > 0 || fileExists(dll) {
				t.Fatalf("joined MSI did not stage original DLL: %q", notes)
			}
			expectedOwner := burn
			if phase == "successor" {
				x.alive[burn.PID] = false
				expectedOwner = holderFor(93)
				if err := x.env.begin("machine", folders, expectedOwner); err != nil {
					t.Fatal(err)
				}
			}
			caller := burn
			if phase == "wrong Burn" {
				caller.Created++
			}
			notes, _, err := settleUpgradeAsideOperation(x.env, "machine", phase != "commit", caller, true)
			record, _, loadErr := x.env.load("machine")
			if phase == "exited" {
				if err != nil || len(notes) != 0 || loadErr != nil || record != nil || readAsideFile(t, dll) != "original DLL" || exitChecks != 1 {
					t.Fatalf("Burn fallback failed: %q err=%v loadErr=%v record=%+v exit checks=%d", notes, err, loadErr, record, exitChecks)
				}
				return
			}
			if err == nil || loadErr != nil || record == nil || record.Installer != expectedOwner || fileExists(dll) {
				t.Fatalf("unauthorized fallback changed custody: err=%v loadErr=%v record=%+v", err, loadErr, record)
			}
			if (phase == "live" && exitChecks != 1) || (phase != "live" && exitChecks != 0) {
				t.Fatalf("unexpected exit checks: %d", exitChecks)
			}
		})
	}
}
