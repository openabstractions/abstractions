package main

import (
	"context"
	"errors"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"github.com/openabstractions/abstraction-storage/go/layout"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func isolationFixture(t *testing.T) (runtimeFlags, resetRoots) {
	t.Helper()
	root := t.TempDir()
	o, err := parseTestRuntime(t, []string{"--isolated", "isolation1572", "--state-dir", filepath.Join(root, "private"), "--without-jobs", "--without-models"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	return o, resetRoots{Local: filepath.Join(root, "account-local"), Roaming: filepath.Join(root, "account-roaming")}
}

func TestRuntimeSettingsIsolationPrivateNeverTakesAccountLock(t *testing.T) {
	o, r := isolationFixture(t)
	account := func() (resetRoots, error) { return r, nil }
	lock := func(context.Context, resetRoots, bool) (func(), error) {
		t.Fatal("private runtime opened account lock")
		return nil, nil
	}
	release, err := runtimeStateGuard(context.Background(), o, account, lock)
	if err != nil {
		t.Fatal(err)
	}
	release()
	for _, path := range []string{r.Local, r.Roaming} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("account fixture was touched: %s: %v", path, err)
		}
	}
	if _, err := os.Stat(filepath.Join(o.stateDir, ".openabstractions-settings-lifecycle.lock")); err != nil {
		t.Fatal(err)
	}
}

func TestRuntimeSettingsIsolationDefaultHoldsResetExclusion(t *testing.T) {
	_, r := isolationFixture(t)
	account := func() (resetRoots, error) { return r, nil }
	release, err := runtimeStateGuard(context.Background(), runtimeFlags{}, account, resetGuard)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if unlock, err := resetGuard(ctx, r, false); !errors.Is(err, context.DeadlineExceeded) {
		if unlock != nil {
			unlock()
		}
		release()
		t.Fatalf("exclusive reset entered shared lifetime: %v", err)
	}
	release()
	unlocked, err := resetGuard(context.Background(), r, false)
	if err != nil {
		t.Fatal(err)
	}
	unlocked()
}

func TestRuntimeSettingsIsolationRefusesOverlapEscapedLogsAndPartialSelection(t *testing.T) {
	o, r := isolationFixture(t)
	for _, change := range []func(*runtimeFlags){
		func(o *runtimeFlags) {
			o.stateDir = filepath.Join(r.Local, layout.Name, "nested")
			o.out = filepath.Join(o.stateDir, "log")
		},
		func(o *runtimeFlags) { o.stateDir = r.Local; o.out = filepath.Join(o.stateDir, "log") },
		func(o *runtimeFlags) { o.out = filepath.Join(filepath.Dir(o.stateDir), "escaped.log") },
		func(o *runtimeFlags) { o.endpoint = "" },
		func(o *runtimeFlags) { o.configEndpoint = "" },
		func(o *runtimeFlags) { o.stateDir = "relative" },
		func(o *runtimeFlags) { o.jobRoot = filepath.Join(o.stateDir, "jobs"); o.jobOwner = "fixture" },
	} {
		bad := o
		change(&bad)
		release, err := runtimeStateGuard(context.Background(), bad, func() (resetRoots, error) { return r, nil }, func(context.Context, resetRoots, bool) (func(), error) {
			t.Fatal("invalid private selection opened account lock")
			return nil, nil
		})
		if release != nil {
			release()
		}
		if err == nil {
			t.Fatalf("invalid selection accepted: %#v", bad)
		}
	}
	if _, err := os.Stat(o.stateDir); !os.IsNotExist(err) {
		t.Fatalf("rejected selections created private state: %v", err)
	}
}

func TestRuntimeSettingsIsolationAliasCannotEscapeResetRoots(t *testing.T) {
	_, r := isolationFixture(t)
	accountState := filepath.Join(r.Local, layout.Name)
	if err := os.MkdirAll(accountState, 0700); err != nil {
		t.Fatal(err)
	}
	for name, alias := range stateAliases(t, accountState) {
		if err := privateStatePath(context.Background(), filepath.Join(alias, "pending"), []string{accountState}); err == nil {
			t.Fatalf("%s account descendant admitted as private", name)
		}
		t.Logf("refused fixture alias: %s", name)
	}
}

func TestRuntimeSettingsIsolationExistingOwnedDirectoryUsesLayoutOwner(t *testing.T) {
	o, r := isolationFixture(t)
	parent := filepath.Join(filepath.Dir(o.stateDir), "owner-only")
	if err := privatedir.Ensure(parent); err != nil {
		t.Fatal(err)
	}
	o.stateDir = filepath.Join(parent, "ordinary")
	o.out = filepath.Join(o.stateDir, "records.jsonl")
	if err := os.Mkdir(o.stateDir, 0700); err != nil {
		t.Fatal(err)
	}
	release, err := runtimeStateGuard(context.Background(), o, func() (resetRoots, error) { return r, nil }, func(context.Context, resetRoots, bool) (func(), error) {
		t.Fatal("private existing directory opened account lock")
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	release()
}

func TestRuntimeSettingsIsolationFileStateRefusesWithoutAccountLock(t *testing.T) {
	o, r := isolationFixture(t)
	if err := os.WriteFile(o.stateDir, []byte("owned fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	release, err := runtimeStateGuard(context.Background(), o, func() (resetRoots, error) { return r, nil }, func(context.Context, resetRoots, bool) (func(), error) {
		t.Fatal("file state opened account lock")
		return nil, nil
	})
	if release != nil {
		release()
	}
	if err == nil {
		t.Fatal("ordinary file admitted as state directory")
	}
}

func TestRuntimeSettingsIsolationDirectEntryRejectsBeforeAccountLookup(t *testing.T) {
	// A malformed direct entry must return before the native metadata/lock owner.
	if err := runRuntimeReady(context.Background(), runtimeFlags{stateDir: "relative"}, nil); err == nil {
		t.Fatal("direct malformed selection accepted")
	}
	for _, args := range [][]string{{"--isolated", "fixture"}, {"--without-jobs", "--jobs-root", "relative"}, {"--unknown"}, {"--help"}} {
		if _, err := parseRuntime(args, os.Stderr); err == nil {
			t.Fatalf("invalid/help selection accepted: %q", args)
		}
	}
}

func TestRuntimeSettingsIsolationPortableDefaultsAreMetadataOnly(t *testing.T) {
	o, r := isolationFixture(t)
	defaults := filepath.Join(t.TempDir(), "account-default-state")
	logs := filepath.Join(t.TempDir(), "account-default-log-directory")
	metadata := func() ([]string, error) { return []string{defaults, logs}, nil }
	account := func() (resetRoots, error) { return r, nil }
	noAccountLock := func(context.Context, resetRoots, bool) (func(), error) {
		t.Fatal("private selection acquired account lock")
		return nil, nil
	}
	release, err := runtimeStateGuardWithDefaults(context.Background(), o, account, noAccountLock, metadata)
	if err != nil {
		t.Fatal(err)
	}
	release()
	for _, path := range []string{defaults, logs} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("default metadata created state: %s %v", path, err)
		}
	}
	for _, root := range []string{defaults, filepath.Join(defaults, "child"), filepath.Dir(defaults), logs} {
		bad := o
		bad.stateDir = root
		bad.out = filepath.Join(root, "own.log")
		if release, err := runtimeStateGuardWithDefaults(context.Background(), bad, account, noAccountLock, metadata); err == nil {
			release()
			t.Fatalf("default overlap admitted: %s", root)
		}
	}
	bad := o
	bad.stateDir = filepath.Join(t.TempDir(), "unmade")
	bad.out = filepath.Join(bad.stateDir, "own.log")
	if release, err := runtimeStateGuardWithDefaults(context.Background(), bad, account, noAccountLock, func() ([]string, error) { return nil, errors.New("unresolved metadata") }); err == nil {
		release()
		t.Fatal("unresolved defaults admitted")
	}
	if _, err := os.Stat(bad.stateDir); !os.IsNotExist(err) {
		t.Fatal("unresolved metadata created private root")
	}
	called := false
	release, err = runtimeStateGuardWithDefaults(context.Background(), runtimeFlags{}, account, func(context.Context, resetRoots, bool) (func(), error) { called = true; return func() {}, nil }, func() ([]string, error) { t.Fatal("default account scope requested private metadata"); return nil, nil })
	if err != nil || !called {
		t.Fatalf("default guard changed: %v", err)
	}
	release()
	if err := os.Mkdir(defaults, 0700); err != nil {
		t.Fatal(err)
	}
	for name, alias := range stateAliases(t, defaults) {
		bad := o
		bad.stateDir = filepath.Join(alias, "child")
		bad.out = filepath.Join(bad.stateDir, "own.log")
		if release, err := runtimeStateGuardWithDefaults(context.Background(), bad, account, noAccountLock, metadata); err == nil {
			release()
			t.Fatalf("portable default alias admitted: %s", name)
		}
	}
}

// Shared positive fixtures establish the same protected private boundary before
// writing descendants; this control never starts a runtime or reads account state.
func TestRuntimeSettingsIsolationSharedFixtureSelection(t *testing.T) {
	o, args := isolatedRuntime(t)
	if o.out != filepath.Join(o.stateDir, "records") || !strings.Contains(strings.Join(args, " "), o.out) {
		t.Fatal("shared runtime log escaped private state")
	}
	writeStateFile(t, filepath.Join(o.stateDir, "inference", inferenceHostsFile), `{"local": []}`)
	_, roots := isolationFixture(t)
	release, err := runtimeStateGuard(context.Background(), o, func() (resetRoots, error) { return roots, nil },
		func(context.Context, resetRoots, bool) (func(), error) {
			t.Fatal("fixture selected account lock")
			return nil, nil
		})
	if err != nil {
		t.Fatal(err)
	}
	release()
}
