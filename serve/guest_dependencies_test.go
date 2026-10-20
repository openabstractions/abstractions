package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func writeGuestJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// installerSkips is the guest installer's own test for a folder (live run
// 2026-10-06, the guest's npm service): node_modules exists and every
// dependency package.json declares, plus the one it adds, is a dependency of
// package-lock.json's root package.
func installerSkips(t *testing.T, dir, adds string) bool {
	t.Helper()
	if _, err := os.Stat(filepath.Join(dir, "node_modules")); err != nil {
		return false
	}
	var manifest struct {
		Dependencies map[string]string `json:"dependencies"`
	}
	var lock struct {
		Packages map[string]struct {
			Dependencies map[string]string `json:"dependencies"`
		} `json:"packages"`
	}
	for name, into := range map[string]any{"package.json": &manifest, "package-lock.json": &lock} {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return false
		}
		if err := json.Unmarshal(raw, into); err != nil {
			t.Fatal(err)
		}
	}
	declared := map[string]bool{adds: true}
	for name := range manifest.Dependencies {
		declared[name] = true
	}
	for name := range declared {
		if _, ok := lock.Packages[""].Dependencies[name]; !ok {
			return false
		}
	}
	return true
}

// TestSeedGuestDependenciesRecordsCarriedPeers: each peer the plugin package
// carries is recorded at its own version, so the guest's installer skips the
// folder; a peer the package lacks is not recorded.
func TestSeedGuestDependenciesRecordsCarriedPeers(t *testing.T) {
	root := t.TempDir()
	plugin := filepath.Join(root, "node_modules", "@oa", "adapter")
	writeGuestJSON(t, filepath.Join(plugin, "package.json"), map[string]any{"name": "@oa/adapter",
		"peerDependencies": map[string]string{"@guest/plugin": "^1.18.31", "absent-peer": "*"}})
	writeGuestJSON(t, filepath.Join(root, "node_modules", "@guest", "plugin", "package.json"),
		map[string]string{"name": "@guest/plugin", "version": "1.18.31"})
	config := filepath.Join(t.TempDir(), "config", "guest")
	if installerSkips(t, config, "@guest/plugin") {
		t.Fatal("an empty folder already satisfies the installer")
	}
	got, err := seedGuestDependencies(config, root, filepath.Join(plugin, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"@guest/plugin@1.18.31"}) {
		t.Fatalf("recorded %v", got)
	}
	if !installerSkips(t, config, "@guest/plugin") {
		t.Fatal("the guest's installer would still reach the registry")
	}
	if installerSkips(t, config, "absent-peer") {
		t.Fatal("a peer the package lacks was recorded")
	}
	// Seeding again is the same record.
	if again, err := seedGuestDependencies(config, root, filepath.Join(plugin, "package.json")); err != nil || !reflect.DeepEqual(again, got) {
		t.Fatalf("again %v %v", again, err)
	}
}

// TestSeedGuestDependenciesWithoutCarriedPeersWritesNothing: a plugin package
// that carries none of its peers leaves the guest's folder untouched.
func TestSeedGuestDependenciesWithoutCarriedPeersWritesNothing(t *testing.T) {
	root := t.TempDir()
	plugin := filepath.Join(root, "plugin")
	writeGuestJSON(t, filepath.Join(plugin, "package.json"), map[string]any{"peerDependencies": map[string]string{"@guest/plugin": "^1"}})
	config := filepath.Join(t.TempDir(), "guest")
	if got, err := seedGuestDependencies(config, root, filepath.Join(plugin, "package.json")); err != nil || got != nil {
		t.Fatalf("%v %v", got, err)
	}
	if _, err := os.Stat(config); !os.IsNotExist(err) {
		t.Fatalf("the folder was written: %v", err)
	}
	writeGuestJSON(t, filepath.Join(root, "node_modules", "@guest", "plugin", "package.json"), map[string]string{"name": "other", "version": "1"})
	if _, err := seedGuestDependencies(config, root, filepath.Join(plugin, "package.json")); err == nil {
		t.Fatal("a peer folder naming another package was recorded")
	}
}
