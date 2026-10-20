package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func placeFixture(t *testing.T, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// The adopter's launcher finds the official OpenCode CLI, the plugin in
// OpenCode's package cache and OpenCode's global config, and names them.
func TestOpenCodeGivenOfficialInstallIsCheckedBeforeAnyGrant(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	program := placeFixture(t, filepath.Join(home, ".opencode", "bin", "opencode.exe"))
	cache := filepath.Join(home, ".cache", "opencode", "packages", "@openabstractions", "opencode")
	plugin := filepath.Dir(placeFixture(t, filepath.Join(cache, "node_modules", "@openabstractions", "opencode", "package.json")))
	config := placeFixture(t, filepath.Join(home, ".config", "opencode", "opencode.json"))

	got, err := checkGivenInstall(program, plugin, config, os.Stat)
	if err != nil {
		t.Fatal(err)
	}
	if got.Program != program || got.Plugin != plugin || got.PluginRoot != cache || got.Config != config {
		t.Fatalf("official install: %+v", got)
	}

	checkout := filepath.Dir(placeFixture(t, filepath.Join(root, "checkout", "adopters", "opencode", "package.json")))
	if got, err := checkGivenInstall(program, checkout, config, os.Stat); err != nil || got.PluginRoot != checkout {
		t.Fatalf("a checkout is its own package root: %+v %v", got, err)
	}
	unzipped := placeFixture(t, filepath.Join(root, "Downloads", "opencode-windows-x64", "opencode.exe"))
	if got, err := checkGivenInstall(unzipped, plugin, config, os.Stat); err != nil || got.Program != unzipped {
		t.Fatalf("an unpacked release zip: %+v %v", got, err)
	}

	folderExe := filepath.Join(root, "folder.exe")
	if err := os.MkdirAll(folderExe, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string][3]string{
		"relative program":   {"opencode.exe", plugin, config},
		"missing program":    {filepath.Join(root, "missing.exe"), plugin, config},
		"folder program":     {folderExe, plugin, config},
		"relative plugin":    {program, "plugin", config},
		"plugin without pkg": {program, root, config},
		"missing config":     {program, plugin, filepath.Join(root, "none.json")},
		"folder config":      {program, plugin, root},
		"program alone":      {program, "", ""},
	} {
		if _, err := checkGivenInstall(c[0], c[1], c[2], os.Stat); err == nil {
			t.Fatalf("%s accepted", name)
		}
	}
	if _, err := checkGivenInstall(program, "", "", os.Stat); err == nil || !strings.Contains(err.Error(), "together") {
		t.Fatalf("the three paths are named together: %v", err)
	}
}
