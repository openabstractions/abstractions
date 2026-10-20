package cacheproducer

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/openabstractions/abstraction-identity/go/privatedir"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Model the documented copied-plugin cache: an equal generation is a successful
// vendor no-op, and plugin list can omit readFromFolder on older held CLIs.
type cachedClaudePluginFixture struct {
	v                    *pluginVendorFixture
	home, copiedVersion  string
	noOp, lost, mismatch bool
	corruptLate          bool
	marketReads          int
	showSource           bool
}

func (f *cachedClaudePluginFixture) folder() string {
	return filepath.Join(f.home, "plugins", "cache", f.v.market, "openabstractions", f.v.version)
}

func (f *cachedClaudePluginFixture) run(ctx context.Context, cli, project string, args []string) ([]byte, error) {
	update := len(args) > 1 && args[0] == "plugin" && args[1] == "update"
	if update && f.noOp {
		return []byte("{}"), nil
	}
	raw, err := f.v.run(ctx, cli, project, args)
	if err != nil {
		return raw, err
	}
	if reflect.DeepEqual(args, []string{"plugin", "marketplace", "list", "--json"}) && f.copiedVersion != "" {
		f.marketReads++
		if f.corruptLate && f.marketReads == 2 {
			if err := os.WriteFile(filepath.Join(f.folder(), "hooks", "register.js"), []byte("changed during final vendor readback"), 0600); err != nil {
				f.v.t.Fatal(err)
			}
		}
	}
	if len(args) > 1 && args[0] == "plugin" && (args[1] == "install" || args[1] == "update") && f.copiedVersion != f.v.version {
		files := []string{".claude-plugin/plugin.json", "README.md", "hooks/hooks.json", "hooks/register.js"}
		for _, name := range files {
			b, err := os.ReadFile(filepath.Join(f.v.root, "plugin", filepath.FromSlash(name)))
			if err != nil {
				f.v.t.Fatal(err)
			}
			if name == "hooks/register.js" && f.mismatch {
				b = []byte("old cached module")
			}
			path := filepath.Join(f.folder(), filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
				f.v.t.Fatal(err)
			}
			if err := os.WriteFile(path, b, 0600); err != nil {
				f.v.t.Fatal(err)
			}
		}
		f.copiedVersion = f.v.version
	}
	if len(args) > 1 && args[0] == "plugin" && (args[1] == "install" || args[1] == "update") {
		row := map[string]any{"scope": f.v.pluginScope(), "installPath": f.folder(), "version": f.v.version}
		if f.v.pluginScope() == "local" {
			row["projectPath"] = project
		}
		registry := map[string]any{"version": 2, "plugins": map[string]any{"openabstractions@" + f.v.market: []any{row}}}
		if err := os.WriteFile(filepath.Join(f.home, "plugins", "installed_plugins.json"), PluginJSON(registry), 0600); err != nil {
			f.v.t.Fatal(err)
		}
	}
	if reflect.DeepEqual(args, []string{"plugin", "list", "--json"}) && f.v.installed {
		entries, err := PluginList(raw)
		if err != nil {
			f.v.t.Fatal(err)
		}
		if !f.showSource {
			delete(entries[0], "readFromFolder")
		}
		entries[0]["installPath"] = PluginJSON(f.folder())
		raw = PluginJSON(entries)
	}
	if update && f.lost {
		return nil, errors.New("lost vendor update reply")
	}
	return raw, nil
}

func cachedClaudePlugin(t *testing.T) (ClaudeOptions, string, *cachedClaudePluginFixture) {
	o, self, v := pluginFixture(t)
	home := v.configRoot
	return o, self, &cachedClaudePluginFixture{v: v, home: home}
}

func TestClaudePluginCacheGenerationBindsContent(t *testing.T) {
	files := ClaudeFilesForScope("oa-local-user", "helper", "state", "user")
	first := SetClaudeGeneration(files)
	var m map[string]json.RawMessage
	if json.Unmarshal(files["plugin/.claude-plugin/plugin.json"], &m) != nil {
		t.Fatal("manifest")
	}
	var metadata map[string]string
	if json.Unmarshal(m["metadata"], &metadata) != nil || metadata["oaRelease"] != "0.8.0" || !strings.HasPrefix(first, "oa-cache-") || strings.Contains(first, "+") {
		t.Fatal("public release and internal generation")
	}
	other := ClaudeFilesForScope("oa-local-user", "helper", "state", "user")
	if SetClaudeGeneration(other) != first {
		t.Fatal("unstable generation")
	}
	other = ClaudeFilesForScope("oa-local-user", "helper", "state", "user")
	other["plugin/hooks/register.js"] = append(other["plugin/hooks/register.js"], '\n')
	if SetClaudeGeneration(other) == first {
		t.Fatal("changed module reused cache generation")
	}
}

func TestClaudePluginCachedCopyFinalReadbackFence(t *testing.T) {
	o, self, f := cachedClaudePlugin(t)
	f.corruptLate = true
	if err := ApplyClaude(context.Background(), o, self, f.run); !errors.Is(err, ErrClaudeUpdate) {
		t.Fatal("late changed cache reported installed", err)
	}
}

func TestClaudePluginCachedCopyCannotHideBehindReadFromFolder(t *testing.T) {
	o, self, f := cachedClaudePlugin(t)
	f.showSource = true
	if err := ApplyClaude(context.Background(), o, self, f.run); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.folder(), "hooks", "register.js"), []byte("stale actual cache"), 0600); err != nil {
		t.Fatal(err)
	}
	o.Action = "status"
	if err := ApplyClaude(context.Background(), o, self, f.run); !errors.Is(err, ErrClaudeUpdate) {
		t.Fatal("source hid stale registry cache", err)
	}
}

func TestClaudePluginCachedCopyUpdateAndRecovery(t *testing.T) {
	for _, failure := range []string{"none", "noop", "lost", "mismatch"} {
		t.Run(failure, func(t *testing.T) {
			o, self, f := cachedClaudePlugin(t)
			original := claudePluginModule
			t.Cleanup(func() { claudePluginModule = original })
			claudePluginModule = append(append([]byte(nil), original...), []byte("\n// predecessor\n")...)
			if err := ApplyClaude(context.Background(), o, self, f.run); err != nil {
				t.Fatal("initial", err)
			}
			old := f.copiedVersion
			claudePluginModule = original
			o.Action = "status"
			if err := ApplyClaude(context.Background(), o, self, f.run); !errors.Is(err, ErrClaudeUpdate) {
				t.Fatal("stale status", err)
			}
			o.Action = "update"
			f.noOp, f.lost, f.mismatch = failure == "noop", failure == "lost", failure == "mismatch"
			err := ApplyClaude(context.Background(), o, self, f.run)
			if (failure == "none") != (err == nil) {
				t.Fatal("update result", failure, err)
			}
			f.noOp, f.lost, f.mismatch = false, false, false
			if failure == "mismatch" {
				// Equal-generation vendor updates remain no-ops: report pending,
				// rather than repairing a vendor-owned cache by hand.
				if err := ApplyClaude(context.Background(), o, self, f.run); err == nil {
					t.Fatal("accepted mismatched actual cache")
				}
				return
			}
			if err := ApplyClaude(context.Background(), o, self, f.run); err != nil {
				t.Fatal("recovery", err)
			}
			if f.copiedVersion == old {
				t.Fatal("cache generation unchanged")
			}
			o.Action = "status"
			if err := ApplyClaude(context.Background(), o, self, f.run); err != nil {
				t.Fatal("final status", err)
			}
		})
	}
}

func TestClaudePluginCachedCopyReadyOwnerRecoversLostUpdate(t *testing.T) {
	o, self, f := cachedClaudePlugin(t)
	original := claudePluginModule
	t.Cleanup(func() { claudePluginModule = original })
	claudePluginModule = append(append([]byte(nil), original...), []byte("\n// predecessor\n")...)
	if err := ApplyClaude(context.Background(), o, self, f.run); err != nil {
		t.Fatal(err)
	}
	claudePluginModule = original
	// Panel's existing ready owner calls install idempotently. Its first update
	// may publish successfully while losing the vendor response.
	f.lost = true
	if err := ApplyClaude(context.Background(), o, self, f.run); err == nil {
		t.Fatal("lost update response reported success")
	}
	f.lost = false
	if err := ApplyClaude(context.Background(), o, self, f.run); err != nil {
		t.Fatal("same ready owner failed to recover", err)
	}
	o.Action = "status"
	if err := ApplyClaude(context.Background(), o, self, f.run); err != nil {
		t.Fatal("actual installed copy", err)
	}
}

func TestClaudePluginCachedCopyMissingOrChangedNeverCurrent(t *testing.T) {
	for _, edge := range []string{"missing", "changed", "foreign", "scope"} {
		t.Run(edge, func(t *testing.T) {
			o, self, f := cachedClaudePlugin(t)
			if err := ApplyClaude(context.Background(), o, self, f.run); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(f.folder(), "hooks", "register.js")
			switch edge {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "changed":
				if err := os.WriteFile(path, []byte("stale module"), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				registry := filepath.Join(f.home, "plugins", "installed_plugins.json")
				b, err := os.ReadFile(registry)
				if err != nil {
					t.Fatal(err)
				}
				if edge == "scope" {
					b = []byte(strings.ReplaceAll(string(b), `"scope":"local"`, `"scope":"user"`))
				} else {
					b = []byte(strings.ReplaceAll(string(b), `"installPath":`, `"foreignPath":`))
				}
				if err := os.WriteFile(registry, b, 0600); err != nil {
					t.Fatal(err)
				}
			}
			o.Action = "status"
			if err := ApplyClaude(context.Background(), o, self, f.run); err == nil {
				t.Fatal("incorrectly current")
			}
		})
	}
}

type pluginVendorFixture struct {
	t                     *testing.T
	project, root, market string
	known, installed      bool
	calls                 [][]string
	lost, malformed       bool
	wrongReadback         string
	failValidate          bool
	editOnValidate        bool
	scope                 string
	version               string
	configRoot            string
}

func (v *pluginVendorFixture) pluginScope() string {
	if v.scope == "user" {
		return "user"
	}
	return "local"
}

func (v *pluginVendorFixture) save() {
	v.t.Helper()
	if v.scope == "user" {
		return
	}
	dir := filepath.Join(v.project, ".claude")
	if err := os.MkdirAll(dir, 0700); err != nil {
		v.t.Fatal(err)
	}
	doc := map[string]any{"unrelated": "preserved"}
	if b, err := os.ReadFile(filepath.Join(dir, "settings.local.json")); err == nil {
		if json.Unmarshal(b, &doc) != nil {
			v.t.Fatal("invalid fixture local settings")
		}
	} else if !os.IsNotExist(err) {
		v.t.Fatal(err)
	}
	for _, key := range []string{"enabledPlugins", "extraKnownMarketplaces"} {
		if doc[key] == nil {
			doc[key] = map[string]any{}
		}
	}
	delete(doc["extraKnownMarketplaces"].(map[string]any), v.market)
	delete(doc["enabledPlugins"].(map[string]any), "openabstractions@"+v.market)
	if v.known {
		doc["extraKnownMarketplaces"].(map[string]any)[v.market] = map[string]any{"source": map[string]string{"source": "directory", "path": v.root}}
	}
	if v.installed {
		doc["enabledPlugins"].(map[string]any)["openabstractions@"+v.market] = true
	}
	if err := os.WriteFile(filepath.Join(dir, "settings.local.json"), PluginJSON(doc), 0600); err != nil {
		v.t.Fatal(err)
	}
}
func (v *pluginVendorFixture) run(_ context.Context, _ string, project string, args []string) ([]byte, error) {
	if !filepath.IsAbs(v.configRoot) || os.Getenv("CLAUDE_CONFIG_DIR") != v.configRoot {
		return nil, errors.New("plugin fixture configuration root is unavailable or changed")
	}
	if project != v.project {
		v.t.Fatal("wrong project")
	}
	v.calls = append(v.calls, append([]string(nil), args...))
	if reflect.DeepEqual(args, []string{"--version"}) {
		return []byte("2.1.308 (Claude Code)"), nil
	}
	if reflect.DeepEqual(args, []string{"plugin", "marketplace", "list", "--json"}) {
		if !v.known {
			return []byte("[]"), nil
		}
		return PluginJSON([]any{map[string]string{"name": v.market, "source": "directory", "path": v.root, "scope": v.pluginScope()}}), nil
	}
	if reflect.DeepEqual(args, []string{"plugin", "list", "--json"}) {
		if v.malformed {
			return []byte("[null]"), nil
		}
		if !v.installed {
			return []byte("[]"), nil
		}
		row := map[string]any{"id": "openabstractions@" + v.market, "scope": "local", "projectPath": v.project, "readFromFolder": filepath.Join(v.root, "plugin"), "version": v.version, "enabled": true}
		if v.scope == "user" {
			row["scope"] = "user"
			delete(row, "projectPath")
		}
		switch v.wrongReadback {
		case "source":
			row["readFromFolder"] = "foreign"
		case "version":
			row["version"] = "0.0.0"
		case "disabled":
			row["enabled"] = false
		}
		return PluginJSON([]any{row}), nil
	}
	if len(args) == 4 && args[0] == "plugin" && args[1] == "validate" && args[3] == "--json" {
		v.root = filepath.Dir(args[2])
		var m struct {
			Name string `json:"name"`
		}
		b, err := os.ReadFile(filepath.Join(v.root, ".claude-plugin", "marketplace.json"))
		if err != nil {
			v.t.Fatal(err)
		}
		if json.Unmarshal(b, &m) != nil {
			v.t.Fatal("marketplace")
		}
		v.market = m.Name
		if v.failValidate {
			return nil, errors.New("invalid fixture manifest")
		}
		if v.editOnValidate {
			if err := os.WriteFile(filepath.Join(v.root, "plugin", "hooks", "register.js"), []byte("external after-validation edit"), 0600); err != nil {
				v.t.Fatal(err)
			}
		}
		return []byte("{}"), nil
	}
	switch {
	case len(args) == 7 && reflect.DeepEqual(args[:3], []string{"plugin", "marketplace", "add"}):
		if !reflect.DeepEqual(args[4:], []string{"--scope", v.pluginScope(), "--json"}) {
			v.t.Fatal(args)
		}
		v.root = args[3]
		var market struct {
			Name string `json:"name"`
		}
		b, err := os.ReadFile(filepath.Join(v.root, ".claude-plugin", "marketplace.json"))
		if err != nil {
			v.t.Fatal(err)
		}
		if json.Unmarshal(b, &market) != nil {
			v.t.Fatal("marketplace")
		}
		v.market = market.Name
		v.known = true
	case len(args) == 5 && reflect.DeepEqual(args[:3], []string{"plugin", "marketplace", "update"}):
	case len(args) == 7 && reflect.DeepEqual(args[:3], []string{"plugin", "marketplace", "remove"}):
		v.known = false
	case len(args) == 6 && args[0] == "plugin" && (args[1] == "install" || args[1] == "update" || args[1] == "uninstall"):
		if !reflect.DeepEqual(args[3:], []string{"--scope", v.pluginScope(), "--json"}) {
			v.t.Fatal(args)
		}
		v.installed = args[1] != "uninstall"
		if v.installed {
			b, err := os.ReadFile(filepath.Join(v.root, "plugin", ".claude-plugin", "plugin.json"))
			var manifest struct {
				Version string `json:"version"`
			}
			if err != nil || json.Unmarshal(b, &manifest) != nil {
				v.t.Fatal("fixture plugin version")
			}
			v.version = manifest.Version
			home := v.configRoot
			if err := os.MkdirAll(filepath.Join(home, "plugins"), 0700); err != nil {
				v.t.Fatal(err)
			}
			row := map[string]any{"scope": v.pluginScope(), "installPath": filepath.Join(v.root, "plugin"), "version": v.version}
			if v.pluginScope() == "local" {
				row["projectPath"] = v.project
			}
			registry := map[string]any{"version": 2, "plugins": map[string]any{"openabstractions@" + v.market: []any{row}}}
			if err := os.WriteFile(filepath.Join(home, "plugins", "installed_plugins.json"), PluginJSON(registry), 0600); err != nil {
				v.t.Fatal(err)
			}
		}
	default:
		v.t.Fatalf("unexpected native command %v", args)
	}
	v.save()
	if v.lost {
		return nil, errors.New("lost native reply")
	}
	return []byte("{}"), nil
}
func pluginFixture(t *testing.T) (ClaudeOptions, string, *pluginVendorFixture) {
	t.Helper()
	configRoot := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", configRoot)
	root := t.TempDir()
	project, state := filepath.Join(root, "project with spaces"), filepath.Join(root, "private state")
	if err := os.Mkdir(project, 0700); err != nil {
		t.Fatal(err)
	}
	if err := privatedir.Make(state); err != nil {
		t.Fatal(err)
	}
	self, cli := filepath.Join(root, "gateway with spaces.exe"), filepath.Join(root, "vendor cli.exe")
	for _, p := range []string{self, cli} {
		if err := os.WriteFile(p, []byte("identity fixture; never executed"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	return ClaudeOptions{Action: "install", Project: project, State: state, CLI: cli}, self, &pluginVendorFixture{t: t, project: project, configRoot: configRoot}
}

func TestClaudePluginCacheFixtureRefusesConfigurationDrift(t *testing.T) {
	for _, profile := range []string{"", "relative", "changed-absolute"} {
		t.Run(profile, func(t *testing.T) {
			o, _, v := pluginFixture(t)
			if profile == "changed-absolute" {
				profile = t.TempDir()
			}
			t.Setenv("CLAUDE_CONFIG_DIR", profile)
			if _, err := v.run(context.Background(), o.CLI, o.Project, []string{"plugin", "install", "fixture"}); err == nil {
				t.Fatal("fixture accepted missing or changed profile")
			}
			if len(v.calls) != 0 {
				t.Fatal("refused profile recorded a vendor effect", v.calls)
			}
		})
	}
}
