package cacheproducer

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

// Seed a prior genuine-format receipt/native epoch. Its source is data; the
// test never executes a helper or a vendor executable.
func codexBackupEpoch(t *testing.T, v *copyHookVendor, selection CompanionSelection, self, key, carrier, version string) []byte {
	t.Helper()
	files := CodexFiles(selection.Name)
	var manifest map[string]any
	if json.Unmarshal(files[".codex-plugin/plugin.json"], &manifest) != nil {
		t.Fatal("manifest")
	}
	manifest["version"] = version
	files[".codex-plugin/plugin.json"] = PluginJSON(manifest)
	cache := filepath.Join(v.home, "plugins", "cache", v.name, codexHookPlugin, version)
	for name, body := range files {
		for _, root := range []string{filepath.Join(v.root, "plugins", codexHookPlugin), cache} {
			path := filepath.Join(root, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, body, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	v.plugin["version"] = version
	raw, err := os.ReadFile(v.root + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var receipt codexHookOwnership
	if json.Unmarshal(raw, &receipt) != nil {
		t.Fatal("receipt")
	}
	_, receipt.Cache, err = setupIdentityClosed(t.Context(), cache, true)
	if err != nil {
		t.Fatal(err)
	}
	receipt.Program, receipt.FileKey, receipt.Version = self, key, version
	receipt.Plugin = PluginJSON(v.plugin)
	receipt.Market = PluginJSON(v.market)
	receipt.Files = map[string]string{}
	receipt.CacheFiles = map[string]string{}
	catalog, err := os.ReadFile(filepath.Join(v.root, ".agents", "plugins", "marketplace.json"))
	if err != nil {
		t.Fatal(err)
	}
	receipt.Files[".agents/plugins/marketplace.json"] = PluginHash(catalog)
	for name, body := range files {
		receipt.Files["plugins/"+codexHookPlugin+"/"+name] = PluginHash(body)
		receipt.CacheFiles[name] = PluginHash(body)
	}
	if err := os.WriteFile(v.root+".json", PluginJSON(receipt), 0600); err != nil {
		t.Fatal(err)
	}
	decl, err := DesiredCompanion(t.Context(), selection, carrier, self, key)
	if err != nil {
		t.Fatal(err)
	}
	d, err := parseCompanion(decl)
	if err != nil {
		t.Fatal(err)
	}
	d.Members[0].Version, d.Members[0].Files = version, receipt.Files
	decl, err = canonicalCompanion(d)
	if err != nil {
		t.Fatal(err)
	}
	declaredCarrier(t, carrier, self, decl)
	return decl
}

func TestCompanionBackupCodexRestoresRecordedOlderRecipeThroughVendorOwner(t *testing.T) {
	home, config := t.TempDir(), t.TempDir()
	t.Setenv("CODEX_HOME", home)
	state := filepath.Join(config, "codex-connections")
	if err := privatedir.Make(state); err != nil {
		t.Fatal(err)
	}
	self, cli := filepath.Join(t.TempDir(), "helper.exe"), filepath.Join(t.TempDir(), "cli.exe")
	for _, path := range []string{self, cli} {
		if err := os.WriteFile(path, []byte("old inert helper"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	old, _, err := setupIdentityClosed(t.Context(), self, false)
	if err != nil {
		t.Fatal(err)
	}
	v := &copyHookVendor{home: home}
	status := ""
	options := CodexOptions{Action: "connect", State: state, CLI: cli, Name: "OpenAbstractions", Program: self, FileKey: old.Key()}
	if err := ApplyCodexStatus(t.Context(), options, home, v.run, &status); err != nil {
		t.Fatal(err)
	}
	selection := CompanionSelection{Host: "codex", Requirement: "required", State: state, CLI: cli, Name: options.Name}
	carrier := filepath.Join(state, "codex-setup-declared.json")
	before := codexBackupEpoch(t, v, selection, self, old.Key(), carrier, "0.7.0")
	prior, err := captureCompanionBackup(t.Context(), before, strings.Repeat("a", 48), func() error { return nil }, nil, v.run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(self, self+".old"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(self, []byte("new inert helper"), 0600); err != nil {
		t.Fatal(err)
	}
	next, _, err := setupIdentityClosed(t.Context(), self, false)
	if err != nil {
		t.Fatal(err)
	}
	after := codexBackupEpoch(t, v, selection, self, next.Key(), carrier, "0.8.0")
	current, err := captureCompanionBackup(t.Context(), after, strings.Repeat("b", 48), func() error { return nil }, nil, v.run)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(self, self+".new"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(self+".old", self); err != nil {
		t.Fatal(err)
	}
	goal, err := RebindCompanionDeclaration(t.Context(), before, self, old.Key())
	if err != nil {
		t.Fatal(err)
	}
	run := func(ctx context.Context, p, dir string, args []string) (CodexResult, error) {
		if len(args) > 1 && args[0] == "plugin" && args[1] == "remove" {
			v.plugin = nil
			v.writes++
			return CodexResult{Output: []byte("{}")}, nil
		}
		if len(args) > 1 && args[0] == "plugin" && args[1] == "add" {
			var manifest struct{ Version string }
			raw, err := os.ReadFile(filepath.Join(v.root, "plugins", codexHookPlugin, ".codex-plugin", "plugin.json"))
			if err != nil || json.Unmarshal(raw, &manifest) != nil {
				t.Fatal("restored source missing", err)
			}
			v.plugin = map[string]any{"pluginId": codexHookPlugin + "@" + v.name, "name": codexHookPlugin, "marketplaceName": v.name, "version": manifest.Version, "installed": true, "enabled": true, "source": map[string]any{"source": "local", "path": filepath.Join(v.root, "plugins", codexHookPlugin)}}
			for name := range CodexFiles(options.Name) {
				body, err := os.ReadFile(filepath.Join(v.root, "plugins", codexHookPlugin, filepath.FromSlash(name)))
				if err != nil {
					t.Fatal(err)
				}
				path := filepath.Join(home, "plugins", "cache", v.name, codexHookPlugin, manifest.Version, filepath.FromSlash(name))
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, body, 0600); err != nil {
					t.Fatal(err)
				}
			}
			v.writes++
			return CodexResult{Output: []byte("{}")}, nil
		}
		return v.run(ctx, p, dir, args)
	}
	mutations := v.writes
	if err := restoreCompanionBackup(t.Context(), prior, current, goal, func() error { return nil }, nil, run); err != nil {
		t.Fatal(err)
	}
	if v.writes != mutations+2 {
		t.Fatal("expected exact owned remove/add", v.writes-mutations)
	}
	if reading, err := observeCompanionDeclaration(t.Context(), goal, nil, run); err != nil || reading.State != CopyCurrent {
		t.Fatal(reading, err)
	}
}
