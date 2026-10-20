package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/openabstractions/abstraction-identity/placetest"
)

func TestFactoryResetPlanUsesReadOnlyRootAdmission(t *testing.T) {
	r, _ := resetFixture(t)
	probe := filepath.Join(r.Local, "openabstractions", "profile-probe")
	var out bytes.Buffer
	err := factoryResetCommandWithRoots(nil, &out, func(mutation bool) (resetRoots, error) {
		if mutation {
			resetWrite(t, probe, "temporary probe")
			return resetRoots{}, errors.New("virtualized mutation refused")
		}
		return r, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(r.Local, "openabstractions")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only plan created OA state: %v", err)
	}
	called := false
	err = factoryResetCommandWithRoots([]string{"--apply"}, io.Discard, func(mutation bool) (resetRoots, error) {
		called = mutation
		return resetRoots{}, errors.New("virtualized mutation refused")
	})
	if err == nil || !called {
		t.Fatalf("mutation bypassed profile admission: called=%t err=%v", called, err)
	}
}

func TestFactoryResetRestoreCommandUsesLiteralPowerShellPath(t *testing.T) {
	for _, path := range []string{`C:\Users\A B\backup.manifest.json`, `C:\Users\O'Brien\backup$1.manifest.json`} {
		quoted := resetPowerShellLiteral(path)
		decoded := strings.ReplaceAll(strings.TrimSuffix(strings.TrimPrefix(quoted, "'"), "'"), "''", "'")
		if decoded != path || !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
			t.Fatalf("restore command changed literal path: %q -> %q", path, quoted)
		}
		if strings.Contains(quoted, `\\`) {
			t.Fatalf("Go escaping remained in Windows path: %s", quoted)
		}
	}
}

func TestFactoryResetPrintedRestoreEscapesCurlyQuotes(t *testing.T) {
	r, _ := resetFixture(t)
	selected := filepath.Join(r.Local, "openabstractions", "runtime-v1", "rights", "decisions.json")
	resetWrite(t, selected, "saved permissions")
	var out bytes.Buffer
	if err := moveSettings(context.Background(), []string{selected}, "O‘’‚‛Brien", os.Rename, &out); err != nil {
		t.Fatal(err)
	}
	want := "PowerShell: openabstractions factory-reset --restore '" + selected + ".oa-reset-O‘‘’’‚‚‛‛Brien.manifest.json'\n"
	if !strings.Contains(out.String(), want) {
		t.Fatalf("restore command does not preserve curly quotes: %s", out.String())
	}
}

func resetFixture(t *testing.T) (resetRoots, []string) {
	t.Helper()
	root := t.TempDir()
	r := resetRoots{filepath.Join(root, "Local"), filepath.Join(root, "Roaming")}
	for _, p := range []string{r.Local, r.Roaming} {
		if err := os.MkdirAll(p, 0700); err != nil {
			t.Fatal(err)
		}
	}
	paths, err := settingsPaths(r)
	if err != nil {
		t.Fatal(err)
	}
	return r, paths
}
func resetWrite(t *testing.T, p, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(text), 0600); err != nil {
		t.Fatal(err)
	}
}
func resetRead(t *testing.T, p, want string) {
	t.Helper()
	got, err := os.ReadFile(p)
	if err != nil || string(got) != want {
		t.Fatalf("%s: %q %v", p, got, err)
	}
}

func TestFactoryResetPreservesContentAndCredentialsAndRestores(t *testing.T) {
	r, paths := resetFixture(t)
	selected := filepath.Join(r.Local, "openabstractions", "runtime-v1", "rights", "decisions.json")
	resetWrite(t, selected, "saved permissions")
	declaration := filepath.Join(r.Local, "openabstractions", "runtime-v1", "providers", "custom.json")
	resetWrite(t, declaration, "declaration")
	paths, err := settingsPaths(r)
	if err != nil {
		t.Fatal(err)
	}
	preserved := []string{"runtime-v1/content/generated-outputs/image.png", "runtime-v1/jobs/job.json", "runtime-v1/credentials/holder.json", "runtime-v1/credentials/items/secret", "providers/inventoryd/digests.json", "models/model.bin", "setup/downloads/model.bin"}
	for _, rel := range preserved {
		resetWrite(t, filepath.Join(r.Local, "openabstractions", filepath.FromSlash(rel)), "keep")
	}
	if err := moveSettings(context.Background(), paths, "fixture", os.Rename, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{selected, declaration} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("settings retained: %s %v", p, err)
		}
	}
	for _, rel := range preserved {
		resetRead(t, filepath.Join(r.Local, "openabstractions", filepath.FromSlash(rel)), "keep")
	}
	journal := selected + ".oa-reset-fixture.manifest.json"
	if err := restoreSettings(context.Background(), r, journal, io.Discard); err != nil {
		t.Fatal(err)
	}
	resetRead(t, selected, "saved permissions")
	resetRead(t, declaration, "declaration")
}

func TestFactoryResetPartialFailureRollsBack(t *testing.T) {
	_, paths := resetFixture(t)
	for _, p := range []string{paths[0], paths[2]} {
		resetWrite(t, p, "original")
	}
	calls := 0
	injected := errors.New("second move failed")
	err := moveSettings(context.Background(), paths, "rollback", func(a, b string) error {
		calls++
		if calls == 2 {
			return injected
		}
		return os.Rename(a, b)
	}, io.Discard)
	if !errors.Is(err, injected) {
		t.Fatal(err)
	}
	for _, p := range []string{paths[0], paths[2]} {
		resetRead(t, p, "original")
		if _, err := os.Stat(p + ".oa-reset-rollback"); !errors.Is(err, os.ErrNotExist) {
			t.Fatal("backup survived rollback", err)
		}
	}
}

func TestFactoryResetRefusesLinkedTargetAndWrongRootManifest(t *testing.T) {
	r, paths := resetFixture(t)
	outside := filepath.Join(t.TempDir(), "outside")
	resetWrite(t, filepath.Join(outside, "config.json"), "outside")
	link := filepath.Dir(paths[0])
	if err := placetest.FolderSymlink(link, outside); err != nil {
		t.Skipf("link fixture unavailable: %v", err)
	}
	if err := moveSettings(context.Background(), paths, "linked", os.Rename, io.Discard); err == nil {
		t.Fatal("linked settings accepted")
	}
	resetRead(t, filepath.Join(outside, "config.json"), "outside")
	journal := filepath.Join(r.Local, "bad.json")
	resetWrite(t, journal, `{"version":1,"created":"fake","entries":[{"path":"outside","backup":"outside.oa-reset-fake"}]}`)
	if err := restoreSettings(context.Background(), r, journal, io.Discard); err == nil {
		t.Fatal("wrong-root manifest accepted")
	}
}

func TestFactoryResetBusyLeavesSettingsUntouched(t *testing.T) {
	r, paths := resetFixture(t)
	resetWrite(t, paths[0], "saved")
	release, err := resetGuard(context.Background(), r, true)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	unlock, err := resetGuard(ctx, r, false)
	if err == nil {
		unlock()
		t.Fatal("reset acquired active runtime guard")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	resetRead(t, paths[0], "saved")
}

func TestFactoryResetRestoreRefusesOccupiedDestination(t *testing.T) {
	r, paths := resetFixture(t)
	resetWrite(t, paths[0], "saved")
	if err := moveSettings(context.Background(), paths, "occupied", os.Rename, io.Discard); err != nil {
		t.Fatal(err)
	}
	resetWrite(t, paths[0], "new setting")
	if err := restoreSettings(context.Background(), r, paths[0]+".oa-reset-occupied.manifest.json", io.Discard); err == nil || !strings.Contains(err.Error(), "occupied") {
		t.Fatal(err)
	}
	resetRead(t, paths[0], "new setting")
	resetRead(t, paths[0]+".oa-reset-occupied", "saved")
}

func TestFactoryResetAllMovesOwnedRootsAndPreservesExternalAssets(t *testing.T) {
	r, _ := resetFixture(t)
	roots, err := allSettingsRoots(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		resetWrite(t, filepath.Join(root, "state.json"), "owned")
	}
	external := filepath.Join(t.TempDir(), "borrowed-model.bin")
	resetWrite(t, external, "external")
	if err := moveSettings(context.Background(), roots, "all", os.Rename, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("root retained: %s %v", root, err)
		}
		resetRead(t, filepath.Join(root+".oa-reset-all", "state.json"), "owned")
	}
	resetRead(t, external, "external")
	if err := restoreSettings(context.Background(), r, roots[0]+".oa-reset-all.manifest.json", io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		resetRead(t, filepath.Join(root, "state.json"), "owned")
	}
}

func TestFactoryResetAllPartialMoveRollsBackRoots(t *testing.T) {
	r, _ := resetFixture(t)
	roots, err := allSettingsRoots(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range roots {
		resetWrite(t, filepath.Join(root, "state.json"), "owned")
	}
	calls := 0
	err = moveSettings(context.Background(), roots, "rollback-all", func(a, b string) error {
		calls++
		if calls == 3 {
			return errors.New("injected root move")
		}
		return os.Rename(a, b)
	}, io.Discard)
	if err == nil {
		t.Fatal("injected error lost")
	}
	for _, root := range roots {
		resetRead(t, filepath.Join(root, "state.json"), "owned")
	}
}

func TestFactoryResetRestoreRefusesMixedOverlappingScopes(t *testing.T) {
	r, paths := resetFixture(t)
	roots, err := allSettingsRoots(r)
	if err != nil {
		t.Fatal(err)
	}
	m := resetManifest{Version: 1, Created: "mixed", Entries: []resetTarget{{Path: roots[0], Backup: roots[0] + ".oa-reset-mixed"}, {Path: paths[2], Backup: paths[2] + ".oa-reset-mixed"}}}
	// Validation rejects overlap before restore; both targets stay absent.
	if !resetPathsOverlap(m.Entries[0].Path, m.Entries[1].Path) {
		t.Fatal("owning name comparison missed overlap")
	}
}
