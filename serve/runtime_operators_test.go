package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	identity "github.com/openabstractions/abstraction-identity"
)

// The installed Panel sets and revokes rights rules and manages credentials, so
// the runtime must name it an operator when it is installed beside the runtime;
// an absent sibling is never named.
func TestOperatorSiblingsNameTheInstalledPanel(t *testing.T) {
	dir := t.TempDir()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	exe := filepath.Join(dir, "openabstractions"+suffix)
	panel := filepath.Join(dir, "Abstraction Panel"+suffix)
	for _, path := range []string{exe, panel} {
		if err := os.WriteFile(path, nil, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	exe = identity.CanonicalProgramPath(filepath.Clean(exe))
	panel = identity.CanonicalProgramPath(filepath.Clean(panel))
	got := operatorSiblings(exe)
	if !slices.Contains(got, panel) {
		t.Fatalf("operators %v do not name the installed Panel %s", got, panel)
	}
	if slices.Contains(got, filepath.Join(dir, "openabstractionsw"+suffix)) {
		t.Fatalf("operators %v name an absent windowless link", got)
	}
	if n := len(got); n != 2 {
		t.Fatalf("operators %v: want the runtime and the Panel only", got)
	}
}

// The runtime admits an operator by the subject-program comparison used
// everywhere else (identity.SameSubjectProgram): case-insensitively on
// Windows, exactly elsewhere.
func TestOperatorComparisonFollowsSubjectProgramRules(t *testing.T) {
	operator, spelled, other := "/opt/oa/openabstractions", "/opt/oa/openabstractions", "/opt/oa/OpenAbstractions"
	if runtime.GOOS == "windows" {
		operator, spelled, other = `C:\OA\openabstractions.exe`, `c:\oa\OPENABSTRACTIONS.EXE`, `C:\OA\openabstractionsx.exe`
	}
	r := &runtimeRights{operators: []string{operator}}
	if !r.isOperator(spelled) {
		t.Fatalf("%s is the operator %s", spelled, operator)
	}
	if r.isOperator(other) {
		t.Fatalf("%s is not the operator %s", other, operator)
	}
}

// operatorProgramFixture writes an executable stand-in for a Panel built from
// source, under a name the runtime never looks for beside itself.
func operatorProgramFixture(t *testing.T) string {
	t.Helper()
	suffix := ""
	if runtime.GOOS == "windows" {
		suffix = ".exe"
	}
	panel := filepath.Join(t.TempDir(), "openabstractions-monitor"+suffix)
	if err := os.WriteFile(panel, nil, 0o700); err != nil {
		t.Fatal(err)
	}
	return panel
}

func isolatedName() string { return fmt.Sprintf("oaop-%d", time.Now().UnixNano()%1_000_000_000) }

// An isolated runtime admits the program its operator named with
// --operator-program as one of its operators, so a Panel built from source
// can answer its questions and edit its rules (live walkthrough 2026-10-02,
// D7).
func TestIsolatedRuntimeAdmitsItsNamedOperatorProgram(t *testing.T) {
	panel := operatorProgramFixture(t)
	options, err := parseRuntime([]string{"--isolated", isolatedName(), "--state-dir", t.TempDir(), "--operator-program", panel}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	r, err := composeRights(options)
	if err != nil {
		t.Fatal(err)
	}
	want := identity.CanonicalProgramPath(filepath.Clean(panel))
	if !slices.Contains(r.operators, want) {
		t.Fatalf("operators %v do not name the isolated runtime's operator program %s", r.operators, want)
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	if r.operators[0] != identity.CanonicalProgramPath(filepath.Clean(exe)) {
		t.Fatalf("the runtime's own program must stay first, the setter of installation rules: %v", r.operators)
	}
}

// The installed runtime never takes an operator program, and an isolated one
// takes only an existing executable with a state directory of its own.
func TestOperatorProgramIsRefusedOutsideAnIsolatedRuntime(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	panel := operatorProgramFixture(t)
	installed, err := runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	absentAfterRefusal := func(path string) {
		t.Helper()
		if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
			t.Errorf("a refused --operator-program left %s behind: %v", path, err)
		}
	}
	pipe := filepath.Join(t.TempDir(), "rt.sock")
	if runtime.GOOS == "windows" {
		pipe = `\\.\pipe\oa-operator-program-test`
	}
	for name, args := range map[string][]string{
		"installed runtime":         {"--operator-program", panel},
		"explicit endpoints":        {"--endpoint", pipe, "--log-endpoint", pipe + "l", "--config-endpoint", pipe + "c", "--jobs-endpoint", pipe + "j", "--state-dir", t.TempDir(), "--out", filepath.Join(t.TempDir(), "log.jsonl"), "--operator-program", panel},
		"installed-parent mode":     {"--isolated", isolatedName(), "--state-dir", t.TempDir(), "--supervised", "--operator-program", panel},
		"relative path":             {"--isolated", isolatedName(), "--state-dir", t.TempDir(), "--operator-program", "monitor.exe"},
		"missing program":           {"--isolated", isolatedName(), "--state-dir", t.TempDir(), "--operator-program", panel + ".absent"},
		"installed runtime's state": {"--isolated", isolatedName(), "--state-dir", installed, "--operator-program", panel},
	} {
		if _, err := parseRuntime(args, io.Discard); err == nil || !strings.Contains(err.Error(), "--operator-program") {
			t.Errorf("%s: --operator-program accepted, or refused for another reason: %v", name, err)
		}
	}
	absentAfterRefusal(installed)
	// A share spelling of the absent installed folder resolves apart from
	// its drive path, so it is refused before anything is created.
	for name, share := range shareAliases(t, installed) {
		if _, err := parseRuntime([]string{"--isolated", isolatedName(), "--state-dir", share, "--operator-program", panel}, io.Discard); err == nil || !strings.Contains(err.Error(), "--operator-program") {
			t.Errorf("%s (%s): --operator-program accepted, or refused for another reason: %v", name, share, err)
		}
		absentAfterRefusal(installed)
	}
	// Installed XPC keeps the installed operator set. Only a separately named
	// isolated runtime may add a development operator.
	if _, err := isolatedOperatorProgram(runtimeFlags{stateDir: t.TempDir(), xpc: true, operatorProgram: panel}); err == nil {
		t.Error("installed --xpc: --operator-program accepted")
	}

	// Another spelling of the installed runtime's state folder is the same
	// folder by file identity: a junction or link, an 8.3 short name, the
	// \\?\ prefix.
	refused := func(name, state string) {
		t.Helper()
		if _, err := parseRuntime([]string{"--isolated", isolatedName(), "--state-dir", state, "--operator-program", panel}, io.Discard); err == nil || !strings.Contains(err.Error(), "--operator-program") {
			t.Errorf("%s (%s): --operator-program accepted, or refused for another reason: %v", name, state, err)
		}
	}
	if err := os.MkdirAll(installed, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, alias := range stateAliases(t, installed) {
		refused(name, alias)
	}
	// A state folder that is the installed one only once it exists: a link
	// in the installed folder's ancestry leads into it.
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	absent, err := runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	// In name order: a dangling junction is checked while the installed
	// folder is still absent.
	aliases := absentStateAliases(t, absent)
	for _, name := range slices.Sorted(maps.Keys(aliases)) {
		refused(name, aliases[name])
	}
	absentAfterRefusal(absent)
	// A folder of its own is accepted beside the faked installed one.
	if _, err := parseRuntime([]string{"--isolated", isolatedName(), "--state-dir", t.TempDir(), "--operator-program", panel}, io.Discard); err != nil {
		t.Fatalf("a state directory of its own: %v", err)
	}
}

func TestIsolatedXPCKeepsOperatorStateAndModeGuards(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	panel := operatorProgramFixture(t)
	options := runtimeFlags{xpc: true, isolated: isolatedName(), stateDir: t.TempDir(), operatorProgram: panel}
	if program, err := isolatedOperatorProgram(options); err != nil || program != identity.CanonicalProgramPath(filepath.Clean(panel)) {
		t.Fatalf("isolated native operator = %q, %v", program, err)
	}
	installed, err := runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	for name, change := range map[string]func(*runtimeFlags){
		"installed namespace": func(o *runtimeFlags) { o.isolated = "" },
		"installed state":     func(o *runtimeFlags) { o.stateDir = installed },
		"supervised":          func(o *runtimeFlags) { o.supervised = true },
		"missing state":       func(o *runtimeFlags) { o.stateDir = "" },
	} {
		refused := options
		change(&refused)
		if _, err := isolatedOperatorProgram(refused); err == nil {
			t.Errorf("%s admitted a development operator", name)
		}
	}
}

// An isolated state folder that does not exist yet is accepted and created,
// beside an installed runtime whose state folder exists.
func TestOperatorProgramAcceptsAStateFolderNotYetMade(t *testing.T) {
	t.Setenv("LOCALAPPDATA", t.TempDir())
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	installed, err := runtimeStateDir(runtime.GOOS, os.Getenv, os.UserHomeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(installed, 0o700); err != nil {
		t.Fatal(err)
	}
	panel := operatorProgramFixture(t)
	state := filepath.Join(t.TempDir(), "new", "state")
	options, err := parseRuntime([]string{"--isolated", isolatedName(), "--state-dir", state, "--operator-program", panel}, io.Discard)
	if err != nil {
		t.Fatalf("a state folder not yet made: %v", err)
	}
	if program, err := isolatedOperatorProgram(options); err != nil || program != identity.CanonicalProgramPath(filepath.Clean(panel)) {
		t.Fatalf("operator program %q %v", program, err)
	}
	if info, err := os.Stat(state); err != nil || !info.IsDir() {
		t.Fatalf("the accepted state folder was not created: %v", err)
	}
}
