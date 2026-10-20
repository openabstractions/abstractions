package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type codexHookVendorFixture struct {
	home, server, root, name string
	market, plugin           map[string]any
	writes                   int
}

func (v *codexHookVendorFixture) run(args []string) (codexCLIResult, error) {
	out := func(value any) (codexCLIResult, error) {
		raw, _ := json.Marshal(value)
		return codexCLIResult{Output: raw}, nil
	}
	if args[1] == "marketplace" {
		switch args[2] {
		case "list":
			rows := []any{}
			if v.market != nil {
				rows = append(rows, v.market)
			}
			return out(map[string]any{"marketplaces": rows})
		case "add":
			v.root = args[3]
			raw, err := os.ReadFile(filepath.Join(v.root, ".agents", "plugins", "marketplace.json"))
			if err != nil {
				return codexCLIResult{}, err
			}
			var catalog struct{ Name string }
			if json.Unmarshal(raw, &catalog) != nil {
				return codexCLIResult{}, errors.New("catalog")
			}
			v.name = catalog.Name
			v.market = map[string]any{"name": v.name, "root": v.root}
			v.writes++
			return out(map[string]any{})
		case "remove":
			v.market = nil
			v.writes++
			return out(map[string]any{})
		}
	}
	switch args[1] {
	case "list":
		rows := []any{}
		if v.plugin != nil {
			rows = append(rows, v.plugin)
		}
		return out(map[string]any{"installed": rows, "available": []any{}})
	case "add":
		v.plugin = map[string]any{"pluginId": codexHookPlugin + "@" + v.name, "name": codexHookPlugin, "marketplaceName": v.name, "version": codexHookVersion, "installed": true, "enabled": true, "source": map[string]any{"source": "local", "path": filepath.Join(v.root, "plugins", codexHookPlugin)}}
		cache := filepath.Join(v.home, "plugins", "cache", v.name, codexHookPlugin, codexHookVersion)
		if err := os.MkdirAll(filepath.Join(cache, "hooks"), 0700); err != nil {
			return codexCLIResult{}, err
		}
		for path, raw := range codexHookFiles(v.server) {
			if err := os.MkdirAll(filepath.Dir(filepath.Join(cache, filepath.FromSlash(path))), 0700); err != nil {
				return codexCLIResult{}, err
			}
			var err error
			raw, err = os.ReadFile(filepath.Join(v.root, "plugins", codexHookPlugin, filepath.FromSlash(path)))
			if err != nil {
				return codexCLIResult{}, err
			}
			if err := os.WriteFile(filepath.Join(cache, filepath.FromSlash(path)), raw, 0600); err != nil {
				return codexCLIResult{}, err
			}
		}
		v.writes++
		return out(map[string]any{})
	case "remove":
		v.plugin = nil
		v.writes++
		return out(map[string]any{})
	}
	return codexCLIResult{}, errors.New("unexpected plugin command")
}

func TestCodexSessionStartNoPluginCLIPreservesMCPActions(t *testing.T) {
	o, self, home, v := codexFixture(t)
	pluginCalls := 0
	run := func(ctx context.Context, program, directory string, args []string) (codexCLIResult, error) {
		if args[0] == "plugin" {
			pluginCalls++
			return codexCLIResult{Code: 2}, nil
		}
		return v.run(ctx, program, directory, args)
	}
	for _, action := range []string{"connect", "connections", "disconnect"} {
		o.action = action
		before := pluginCalls
		page, err := applyCodexSetup(context.Background(), o, self, home, run)
		want := "saved"
		if action == "disconnect" {
			want = "absent"
		}
		if err != nil || page.Registration != want {
			t.Fatal(action, page, err)
		}
		if action == "connections" {
			if page.SessionStart != "absent" || pluginCalls != before {
				t.Fatal(page, pluginCalls, before)
			}
		} else if page.SessionStart != "unavailable" {
			t.Fatal(page)
		}
	}
}

func TestCodexSessionStartLostMarkerPreservesMCPView(t *testing.T) {
	o, self, home, v := codexFixture(t)
	if _, err := applyCodexSetup(context.Background(), o, self, home, v.run); err != nil {
		t.Fatal(err)
	}
	markers, err := filepath.Glob(filepath.Join(o.state, "codex-session-start-*.json"))
	if err != nil || len(markers) != 1 {
		t.Fatal(markers, err)
	}
	if err := os.Remove(markers[0]); err != nil {
		t.Fatal(err)
	}
	writes := v.hooks.writes
	for _, action := range []string{"connections", "connect", "disconnect"} {
		o.action = action
		page, err := applyCodexSetup(context.Background(), o, self, home, v.run)
		want := "preserved"
		if action == "connections" {
			want = "absent"
		}
		if err != nil || page.SessionStart != want || v.hooks.writes != writes {
			t.Fatal(action, page, err, v.hooks.writes)
		}
	}
}

func TestCodexSessionStartReadbackFailurePreservesMCPView(t *testing.T) {
	for _, mode := range []string{"unsupported", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			o, self, home, v := codexFixture(t)
			if _, err := applyCodexSetup(context.Background(), o, self, home, v.run); err != nil {
				t.Fatal(err)
			}
			writes := v.hooks.writes
			run := func(ctx context.Context, program, directory string, args []string) (codexCLIResult, error) {
				if args[0] == "plugin" {
					if mode == "malformed" {
						return codexCLIResult{Output: []byte("unsupported-json")}, nil
					}
					return codexCLIResult{Code: 2}, nil
				}
				return v.run(ctx, program, directory, args)
			}
			for _, action := range []string{"connections", "connect", "disconnect"} {
				o.action = action
				page, err := applyCodexSetup(context.Background(), o, self, home, run)
				if err != nil || page.SessionStart != "unavailable" || v.hooks.writes != writes {
					t.Fatal(action, page, err, v.hooks.writes)
				}
				want := "saved"
				if action == "disconnect" {
					want = "absent"
				}
				if page.Registration != want {
					t.Fatal(page)
				}
			}
		})
	}
}

func TestCodexSessionStartSetupPreservesChangedAndUnowned(t *testing.T) {
	for _, mode := range []string{"changed-market", "changed-plugin", "changed-cache", "changed-cache-identity", "changed-catalog", "lost-marker", "foreign-plugin"} {
		t.Run(mode, func(t *testing.T) {
			o, _, home, v := codexFixture(t)
			if err := applyCodexSessionStartSetup(context.Background(), o, home, v.run); err != nil {
				t.Fatal(err)
			}
			if v.hooks.writes != 2 {
				t.Fatal(v.hooks.writes)
			}
			if err := applyCodexSessionStartSetup(context.Background(), o, home, v.run); err != nil || v.hooks.writes != 2 {
				t.Fatal(err, v.hooks.writes)
			}
			switch mode {
			case "changed-cache-identity":
				cache := filepath.Join(home, "plugins", "cache", v.hooks.name, codexHookPlugin, codexHookVersion)
				if err := os.Rename(cache, cache+".previous"); err != nil {
					t.Fatal(err)
				}
				if err := os.CopyFS(cache, os.DirFS(cache+".previous")); err != nil {
					t.Fatal(err)
				}
			case "changed-catalog":
				if err := os.WriteFile(filepath.Join(v.hooks.root, ".agents", "plugins", "marketplace.json"), []byte(`{"name":"foreign"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-market":
				v.hooks.market["root"] = t.TempDir()
			case "changed-plugin":
				v.hooks.plugin["enabled"] = false
			case "foreign-plugin":
				v.hooks.plugin["pluginId"] = "foreign@" + v.hooks.name
			case "changed-cache":
				if err := os.WriteFile(filepath.Join(home, "plugins", "cache", v.hooks.name, codexHookPlugin, codexHookVersion, "foreign.txt"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			case "lost-marker":
				paths, err := filepath.Glob(filepath.Join(o.state, "codex-session-start-*.json"))
				if err != nil || len(paths) != 1 {
					t.Fatal(paths, err)
				}
				if err := os.Remove(paths[0]); err != nil {
					t.Fatal(err)
				}
			}
			o.action = "disconnect"
			if err := applyCodexSessionStartSetup(context.Background(), o, home, v.run); err == nil || v.hooks.writes != 2 {
				t.Fatal("foreign state changed", err, v.hooks.writes)
			}
		})
	}
}

func TestCodexSessionStartSetupRemovesOnlyOwnedPlugin(t *testing.T) {
	o, _, home, v := codexFixture(t)
	if err := applyCodexSessionStartSetup(context.Background(), o, home, v.run); err != nil {
		t.Fatal(err)
	}
	o.action = "disconnect"
	if err := applyCodexSessionStartSetup(context.Background(), o, home, v.run); err != nil || v.hooks.writes != 4 || v.hooks.market != nil || v.hooks.plugin != nil {
		t.Fatal(err, v.hooks.writes)
	}
	if err := applyCodexSessionStartSetup(context.Background(), o, home, v.run); err != nil || v.hooks.writes != 4 {
		t.Fatal(err, v.hooks.writes)
	}
}

func TestCodexSessionStartInstalledCLIDisposableHome(t *testing.T) {
	cli := os.Getenv("OA_TEST_CODEX_PLUGIN_CLI")
	if cli == "" {
		t.Skip("requires explicitly selected CLI; local plugin operations only")
	}
	o, _, home, _ := codexFixture(t)
	o.cli = cli
	t.Setenv("CODEX_HOME", home)
	t.Setenv("HTTP_PROXY", "http://127.0.0.1:9")
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:9")
	t.Setenv("ALL_PROXY", "http://127.0.0.1:9")
	t.Setenv("NO_PROXY", "")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if err := applyCodexSessionStartSetup(ctx, o, home, runCodexSetupCLI); err != nil {
		t.Fatal(err)
	}
	if err := applyCodexSessionStartSetup(ctx, o, home, runCodexSetupCLI); err != nil {
		t.Fatal("idempotence", err)
	}
	o.action = "disconnect"
	if err := applyCodexSessionStartSetup(ctx, o, home, runCodexSetupCLI); err != nil {
		t.Fatal(err)
	}
	if err := applyCodexSessionStartSetup(ctx, o, home, runCodexSetupCLI); err != nil {
		t.Fatal("idempotent removal", err)
	}
}
