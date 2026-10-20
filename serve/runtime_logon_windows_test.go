package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	wire "github.com/openabstractions/abstraction-facade/go/abstraction/facade"
	"github.com/openabstractions/abstraction-facade/go/bootstrap"
	identity "github.com/openabstractions/abstraction-identity"
	"github.com/openabstractions/abstraction-identity/listen"
	rights "github.com/openabstractions/abstraction-rights/go/abstraction/rights/api"
	"golang.org/x/sys/windows/registry"
)

type logonFixture struct {
	ops             windowsLogonOps
	values          map[string]logonValue
	writes, removes int
	selection       bootstrap.Selection
	dir             string
}

func newLogonFixture(t *testing.T, panel bool) *logonFixture {
	t.Helper()
	f := &logonFixture{dir: t.TempDir(), values: map[string]logonValue{}}
	for _, name := range []string{"openabstractions.exe", windowlessName} {
		if err := os.WriteFile(filepath.Join(f.dir, name), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if panel {
		for _, name := range []string{"Abstraction Panel.exe", presenterImage} {
			if err := os.WriteFile(filepath.Join(f.dir, name), nil, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	f.selection = bootstrap.Selection{Server: listen.ServerExpectation{Principal: identity.User{Kind: "windows", SID: "S-1-5-21-123"}, Program: filepath.Join(f.dir, "openabstractions.exe")}}
	f.ops = windowsLogonOps{
		selection: func(context.Context) (bootstrap.Selection, error) { return f.selection, nil },
		account:   func() (string, error) { return "S-1-5-21-123", nil }, guard: func() error { return nil },
		executable: func() (string, error) { return filepath.Join(f.dir, windowlessName), nil },
		plain:      func(string) error { return nil }, stat: os.Lstat,
		read:   func(name string) (logonValue, bool, error) { v, ok := f.values[name]; return v, ok, nil },
		write:  func(name string, v logonValue) error { f.writes++; f.values[name] = v; return nil },
		remove: func(name string) error { f.removes++; delete(f.values, name); return nil },
	}
	return f
}

func TestWindowsLogonEmptyNonemptyAndRemoval(t *testing.T) {
	f := newLogonFixture(t, true)
	for _, enabled := range []bool{false, true, true, false, false} {
		if err := reconcileWindowsLogon(context.Background(), enabled, f.ops); err != nil {
			t.Fatal(err)
		}
		if (len(f.values) == 2) != enabled {
			t.Fatalf("enabled=%v values=%v", enabled, f.values)
		}
	}
	if f.writes != 2 || f.removes != 2 {
		t.Fatalf("writes=%d removes=%d", f.writes, f.removes)
	}
}

func TestWindowsConfirmedApplicationsDriveOwnedLoginAndRetainInstallation(t *testing.T) {
	ctx := context.Background()
	f := newLogonFixture(t, true)
	account := f.selection.Server.Principal.SID
	d, err := openApplications(t.TempDir(), account, func(context.Context, rights.Subject, string, string) (bool, error) { return true, nil })
	if err != nil {
		t.Fatal(err)
	}
	calls := make(chan bool, 4)
	stop := startApplicationsLogon(ctx, runtimeFlags{}, &applicationsHost{directory: d}, func(ctx context.Context, enabled bool) error {
		if err := reconcileWindowsLogon(ctx, enabled, f.ops); err != nil {
			return err
		}
		calls <- enabled
		return nil
	}, func(err error) { t.Errorf("login reconciliation: %v", err) })
	defer stop()
	receiveLogin(t, calls, false)
	app := rights.Subject{Account: account, Program: filepath.Join(f.dir, "editor.exe")}
	operator := rights.Subject{Account: account, Program: filepath.Join(f.dir, "Abstraction Panel.exe")}
	descriptor := wire.ApplicationDescriptor{Name: "editor", Program: app.Program, Title: "Editor", StartGuidance: "Open Editor"}
	if result := d.register(ctx, app, descriptor); result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result)
	}
	if result := d.announce(ctx, app, wire.ApplicationPresence{Application: "editor", LeaseMs: 1000}); result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result)
	}
	state, err := d.loginState(ctx)
	if err != nil || state.enabled {
		t.Fatal("pending Keep or presence enabled login", state, err)
	}
	if !d.setQuestion(ctx, "editor", app.Program, "keep-editor") || !d.keepOwned(ctx, "editor", app.Program, "keep-editor") {
		t.Fatal("confirmation did not commit")
	}
	receiveLogin(t, calls, true)
	if len(f.values) != 2 {
		t.Fatal("confirmed application did not enable owned login entries", f.values)
	}
	if result := d.remove(ctx, operator, "editor"); result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result)
	}
	receiveLogin(t, calls, false)
	if len(f.values) != 0 {
		t.Fatal("last confirmed removal retained login entries", f.values)
	}
	for _, name := range []string{"openabstractions.exe", windowlessName, "Abstraction Panel.exe"} {
		if info, err := os.Lstat(filepath.Join(f.dir, name)); err != nil || !info.Mode().IsRegular() {
			t.Fatalf("runtime installation changed: %s: %v", name, err)
		}
	}
	if result := d.register(ctx, operator, descriptor); result.Outcome != wire.ApplicationOutcomeApplied {
		t.Fatal(result)
	}
	receiveLogin(t, calls, true)
	if len(f.values) != 2 {
		t.Fatal("new confirmed application did not re-enable login", f.values)
	}
}

func TestWindowsLogonMissingOptionalPanel(t *testing.T) {
	f := newLogonFixture(t, false)
	if err := reconcileWindowsLogon(context.Background(), true, f.ops); err != nil {
		t.Fatal(err)
	}
	if len(f.values) != 1 || f.writes != 1 {
		t.Fatal(f.values)
	}
	if err := reconcileWindowsLogon(context.Background(), false, f.ops); err != nil {
		t.Fatal(err)
	}
	if len(f.values) != 0 {
		t.Fatal(f.values)
	}
}

func TestWindowsLogonForeignValuesRefuseBeforeMutation(t *testing.T) {
	for _, kind := range []uint32{registry.SZ, registry.EXPAND_SZ, registry.BINARY} {
		for _, enabled := range []bool{false, true} {
			f := newLogonFixture(t, true)
			f.values[presenterLogonName] = logonValue{kind, []byte("foreign")}
			if err := reconcileWindowsLogon(context.Background(), enabled, f.ops); err == nil {
				t.Fatal("foreign accepted")
			}
			if f.writes+f.removes != 0 {
				t.Fatal("mutated before both ownership checks")
			}
		}
	}
}

func TestWindowsLogonRejectsAccountEndpointAndProgram(t *testing.T) {
	for _, mode := range []string{"account", "override", "program", "guard", "plain", "missing-runtime"} {
		t.Run(mode, func(t *testing.T) {
			f := newLogonFixture(t, true)
			switch mode {
			case "account":
				f.selection.Server.Principal.SID = "S-1-5-21-456"
			case "override":
				f.selection.EndpointFromEnvironment = true
			case "program":
				f.selection.Server.Program = filepath.Join(t.TempDir(), "openabstractions.exe")
			case "guard":
				f.ops.guard = func() error { return errors.New("virtualized/elevated") }
			case "plain":
				f.ops.plain = func(string) error { return errors.New("redirected path") }
			case "missing-runtime":
				if err := os.Remove(filepath.Join(f.dir, windowlessName)); err != nil {
					t.Fatal(err)
				}
			}
			if err := reconcileWindowsLogon(context.Background(), true, f.ops); err == nil {
				t.Fatal("unsafe context accepted")
			}
			if f.writes+f.removes != 0 {
				t.Fatal("unsafe mutation")
			}
		})
	}
}

func TestWindowsLogonRechecksBeforeEachMutation(t *testing.T) {
	f := newLogonFixture(t, true)
	read := f.ops.read
	reads := 0
	f.ops.read = func(name string) (logonValue, bool, error) {
		reads++
		if reads == 3 {
			f.values[name] = logonValue{registry.SZ, []byte("changed")}
		}
		return read(name)
	}
	if err := reconcileWindowsLogon(context.Background(), true, f.ops); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatal(err)
	}
	if f.writes+f.removes != 0 {
		t.Fatal("changed value overwritten")
	}
}

func TestWindowsLogonPartialFailureThenReversal(t *testing.T) {
	f := newLogonFixture(t, true)
	write := f.ops.write
	f.ops.write = func(name string, v logonValue) error {
		if name == presenterLogonName {
			return errors.New("denied")
		}
		return write(name, v)
	}
	if err := reconcileWindowsLogon(context.Background(), true, f.ops); err == nil {
		t.Fatal("partial failure hidden")
	}
	if len(f.values) != 1 {
		t.Fatal(f.values)
	}
	if err := reconcileWindowsLogon(context.Background(), false, f.ops); err != nil {
		t.Fatal(err)
	}
	if len(f.values) != 0 || f.removes != 1 {
		t.Fatal("partial runtime value not removed")
	}
}

func TestWindowsLogonCancellationAndCommandLimit(t *testing.T) {
	f := newLogonFixture(t, true)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := reconcileWindowsLogon(ctx, true, f.ops); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if f.writes+f.removes != 0 {
		t.Fatal("canceled mutation")
	}
	if _, err := runtimeLogonCommand(`C:\`+strings.Repeat("x", 260)+`.exe`, "serve supervisor"); err == nil {
		t.Fatal("oversize command accepted")
	}
}

func TestWindowsLogonMissingPanelRemovesOnlyItsExactOwnedValue(t *testing.T) {
	f := newLogonFixture(t, true)
	if err := reconcileWindowsLogon(context.Background(), true, f.ops); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(f.dir, presenterImage)); err != nil {
		t.Fatal(err)
	}
	if err := reconcileWindowsLogon(context.Background(), true, f.ops); err != nil {
		t.Fatal(err)
	}
	if len(f.values) != 1 || f.removes != 1 {
		t.Fatal(f.values)
	}
}

func TestWindowsLogonSelectedDirectorySurvivesSelfAliasesAndPayloadRemoval(t *testing.T) {
	for _, mode := range []string{"case-alias", "hardlinked-server"} {
		t.Run(mode, func(t *testing.T) {
			f := newLogonFixture(t, true)
			self := filepath.Join(strings.ToUpper(f.dir), windowlessName)
			if mode == "hardlinked-server" {
				other := t.TempDir()
				for _, name := range []string{"openabstractions.exe", windowlessName} {
					if err := os.Link(filepath.Join(f.dir, name), filepath.Join(other, name)); err != nil {
						t.Fatal(err)
					}
				}
				// A real same-file server in another folder is identity admission,
				// never authority to select that folder's siblings for login.
				if err := os.WriteFile(filepath.Join(other, presenterImage), []byte("foreign sibling"), 0600); err != nil {
					t.Fatal(err)
				}
				self = filepath.Join(other, windowlessName)
			}
			f.ops.executable = func() (string, error) { return self, nil }
			if err := reconcileWindowsLogon(context.Background(), true, f.ops); err != nil {
				t.Fatal(err)
			}
			for i, name := range []string{runtimeLogonName, presenterLogonName} {
				program, args := filepath.Join(f.dir, windowlessName), "serve supervisor"
				if i == 1 {
					program, args = filepath.Join(f.dir, presenterImage), "--background"
				}
				expected, err := runtimeLogonCommand(program, args)
				if err != nil || !f.values[name].equal(expected) {
					t.Fatalf("selected command %s differs: %v", name, err)
				}
			}
			for _, name := range []string{"openabstractions.exe", windowlessName, "Abstraction Panel.exe", presenterImage} {
				if err := os.Remove(filepath.Join(f.dir, name)); err != nil {
					t.Fatal(err)
				}
			}
			// Commit cleanup has only MSI's original ToolsFolder after removal.
			if err := removeWindowsLogon(context.Background(), f.dir+`\.`, f.ops.read, f.ops.remove); err != nil {
				t.Fatal(err)
			}
			if len(f.values) != 0 || f.removes != 2 {
				t.Fatal("selected commands survived owned cleanup", f.values)
			}
		})
	}
}

// An installation upgraded from a version whose presenter entry started the
// Panel's background mode: reconciling replaces that entry with the
// presenter's, turning login off removes it, and uninstall cleanup removes it
// before any reconcile ran. A Panel entry of another folder stays foreign.
func TestWindowsLogonMigratesPanelBackgroundEntryToPresenter(t *testing.T) {
	legacy := func(f *logonFixture) logonValue {
		value, err := runtimeLogonCommand(filepath.Join(f.dir, "Abstraction Panel.exe"), "--background")
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	f := newLogonFixture(t, true)
	f.values[presenterLogonName] = legacy(f)
	if err := reconcileWindowsLogon(context.Background(), true, f.ops); err != nil {
		t.Fatal(err)
	}
	want, err := runtimeLogonCommand(filepath.Join(f.dir, presenterImage), "--background")
	if err != nil || !f.values[presenterLogonName].equal(want) || f.writes != 2 {
		t.Fatal("legacy presenter entry not replaced", f.values, f.writes, err)
	}
	if runtime, presenter, err := readWindowsLogon(context.Background(), f.ops); err != nil || !runtime || !presenter {
		t.Fatal(runtime, presenter, err)
	}

	f = newLogonFixture(t, true)
	f.values[presenterLogonName] = legacy(f)
	if runtime, presenter, err := readWindowsLogon(context.Background(), f.ops); err != nil || runtime || presenter {
		t.Fatal("a legacy entry reads as the presenter's", runtime, presenter, err)
	}
	if err := reconcileWindowsLogon(context.Background(), false, f.ops); err != nil {
		t.Fatal(err)
	}
	if len(f.values) != 0 || f.removes != 1 {
		t.Fatal("legacy presenter entry not removed", f.values)
	}

	f = newLogonFixture(t, true)
	f.values[presenterLogonName] = legacy(f)
	if err := removeWindowsLogon(context.Background(), f.dir, f.ops.read, f.ops.remove); err != nil || len(f.values) != 0 {
		t.Fatal("uninstall kept the legacy presenter entry", f.values, err)
	}

	f = newLogonFixture(t, true)
	other, err := runtimeLogonCommand(filepath.Join(t.TempDir(), "Abstraction Panel.exe"), "--background")
	if err != nil {
		t.Fatal(err)
	}
	f.values[presenterLogonName] = other
	if err := reconcileWindowsLogon(context.Background(), true, f.ops); err == nil || f.writes+f.removes != 0 {
		t.Fatal("another folder's Panel entry was taken over", err)
	}
}
