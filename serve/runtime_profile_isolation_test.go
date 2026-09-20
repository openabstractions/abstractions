package main

import (
	"context"
	"crypto/sha256"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"

	config "github.com/openabstractions/abstraction-config/go"
	configclient "github.com/openabstractions/abstraction-config/go/client"
)

// sentinelLeakMarker is a value an isolated runtime must never read from, or
// write into, the real user profile. It stands in for the owner's actual
// nas_store from the 2026-09-17 report: an isolated runtime's config service
// answered with %APPDATA%\abstraction\config.json's real content.
const sentinelLeakMarker = "SENTINEL-LEAK-nas-store-should-never-be-seen"

// snapshotTree hashes every file under root, keyed by path relative to root.
// A directory that does not exist yet snapshots as empty, which is itself
// meaningful: it proves nothing created it.
func snapshotTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[rel] = string(sum[:])
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return out
}

// TestIsolatedRuntimeConfigDoesNotReadOrWriteRealProfile is the regression
// test for the 2026-09-17 finding: an isolated runtime's config service
// resolved config.UserPath() unconditionally, so it answered reads with the
// real owner's %APPDATA%\abstraction\config.json (or $XDG_CONFIG_HOME /
// $HOME equivalent) and applied edits there, entirely bypassing --state-dir.
func TestIsolatedRuntimeConfigDoesNotReadOrWriteRealProfile(t *testing.T) {
	if !statusTransportProvesProgram(t) {
		t.Skip("Program proof unavailable on current platform")
	}

	// A sentinel stands in for the real user profile. APPDATA, XDG_CONFIG_HOME
	// and HOME are pinned to it so config.UserPath() (and anything else that
	// would consult the real profile) resolves inside it, never outside.
	profile := t.TempDir()
	t.Setenv("APPDATA", profile)
	t.Setenv("XDG_CONFIG_HOME", profile)
	t.Setenv("HOME", profile)

	realConfigPath := config.UserPath()
	if realConfigPath == "" || !filepath.IsAbs(realConfigPath) {
		t.Fatalf("could not resolve a profile-relative config path: %q", realConfigPath)
	}
	if err := os.MkdirAll(filepath.Dir(realConfigPath), 0o755); err != nil {
		t.Fatal(err)
	}
	realBody := []byte(`{"nas_store":"` + sentinelLeakMarker + `"}` + "\n")
	if err := os.WriteFile(realConfigPath, realBody, 0o644); err != nil {
		t.Fatal(err)
	}
	before := snapshotTree(t, profile)

	options, _ := isolatedRuntime(t)
	ctx, cancel := context.WithTimeout(context.Background(), runtimeWait)
	defer cancel()
	ready, done := make(chan struct{}), make(chan error, 1)
	go func() { done <- runRuntimeReady(ctx, options, func() error { close(ready); return nil }) }()
	select {
	case <-ready:
	case err := <-done:
		t.Fatalf("isolated runtime failed to start: %v", err)
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	defer func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(runtimeWait):
			t.Error("runtime did not drain")
		}
	}()

	reader := configclient.New(options.configEndpoint)
	snapshot, err := reader.Read()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.NasStore == sentinelLeakMarker {
		t.Fatalf("isolated runtime's config reader answered with the real profile's setting: %+v", snapshot)
	}
	if snapshot.NasStore != "" {
		t.Fatalf("isolated runtime's config reader answered with an unexpected setting: %+v", snapshot)
	}

	editor := configclient.NewEditor(options.configEndpoint)
	user, err := editor.ReadUser()
	if err != nil {
		t.Fatal(err)
	}
	result, err := editor.ReplaceUser(user.Revision, configclient.UserSettings{Store: "isolated-store"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome.String() != "applied" {
		t.Fatalf("edit through the isolated runtime did not apply: %+v", result)
	}

	// The edit must land under --state-dir, not beside the sentinel profile.
	isolatedConfigPath := filepath.Join(options.stateDir, "config", "config.json")
	if _, err := os.Stat(isolatedConfigPath); err != nil {
		t.Fatalf("isolated runtime did not write its edit under --state-dir: %v", err)
	}

	after := snapshotTree(t, profile)
	if len(after) != len(before) {
		t.Fatalf("files under the sentinel profile changed: before %v after %v", before, after)
	}
	for rel, sum := range before {
		if after[rel] != sum {
			t.Fatalf("file %q under the sentinel profile was modified", rel)
		}
	}
}
