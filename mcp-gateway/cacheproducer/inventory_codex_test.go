package cacheproducer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/openabstractions/abstraction-identity/go/privatedir"
)

type copyHookVendor struct {
	home, root, name string
	market, plugin   map[string]any
	writes           int
	noOp             bool
}

func (v *copyHookVendor) run(_ context.Context, _, _ string, args []string) (CodexResult, error) {
	out := func(value any) (CodexResult, error) {
		raw, _ := json.Marshal(value)
		return CodexResult{Output: raw}, nil
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
			var catalog struct{ Name string }
			if err != nil || json.Unmarshal(raw, &catalog) != nil {
				return CodexResult{}, errors.New("fixture catalog unavailable")
			}
			v.name = catalog.Name
			v.market = map[string]any{"name": v.name, "root": v.root}
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
		return out(map[string]any{"installed": rows})
	case "add":
		if v.noOp {
			return out(map[string]any{})
		}
		v.plugin = map[string]any{"pluginId": codexHookPlugin + "@" + v.name, "name": codexHookPlugin, "marketplaceName": v.name, "version": codexHookVersion, "installed": true, "enabled": true, "source": map[string]any{"source": "local", "path": filepath.Join(v.root, "plugins", codexHookPlugin)}}
		for name := range CodexFiles("OpenAbstractions") {
			input := filepath.Join(v.root, "plugins", codexHookPlugin, filepath.FromSlash(name))
			raw, err := os.ReadFile(input)
			if err != nil {
				return CodexResult{}, err
			}
			output := filepath.Join(v.home, "plugins", "cache", v.name, codexHookPlugin, codexHookVersion, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(output), 0700); err != nil {
				return CodexResult{}, err
			}
			if err := os.WriteFile(output, raw, 0600); err != nil {
				return CodexResult{}, err
			}
		}
		v.writes++
		return out(map[string]any{})
	}
	return CodexResult{}, errors.New("unexpected fixture plugin operation")
}

func TestCopyReceiptDiscoveryUsesNativeHookOwnerAndPreservesTrustBoundary(t *testing.T) {
	for _, edge := range []string{"current", "source-missing", "cache-missing", "cache-noop", "cache-changed", "cache-replaced", "locator-absent"} {
		t.Run(edge, func(t *testing.T) {
			ctx := context.Background()
			config, home := t.TempDir(), t.TempDir()
			t.Setenv("CODEX_HOME", home)
			state := filepath.Join(config, "codex-connections")
			if err := privatedir.Make(state); err != nil {
				t.Fatal(err)
			}
			cli := filepath.Join(t.TempDir(), "selected-cli.exe")
			if err := os.WriteFile(cli, []byte("inert selected CLI"), 0600); err != nil {
				t.Fatal(err)
			}
			vendor := &copyHookVendor{home: home}
			status := ""
			if err := ApplyCodexStatus(ctx, CodexOptions{Action: "connect", State: state, CLI: cli, Name: "OpenAbstractions"}, home, vendor.run, &status); err != nil || status != "installed" {
				t.Fatal(status, err)
			}
			scope, scopeID, err := setupIdentityClosed(ctx, home, true)
			if err != nil {
				t.Fatal(err)
			}
			_, stateID, err := setupIdentityClosed(ctx, state, true)
			if err != nil {
				t.Fatal(err)
			}
			_, cliID, err := setupIdentityClosed(ctx, cli, false)
			if err != nil {
				t.Fatal(err)
			}
			sum := sha256.Sum256([]byte(scope.Key() + "\x00OpenAbstractions"))
			locator := filepath.Join(state, "codex-setup-"+hex.EncodeToString(sum[:])+".json")
			if edge != "locator-absent" {
				record := map[string]any{"Scope": scopeID, "State": stateID, "CLI": cliID, "Self": cliID, "Name": "OpenAbstractions", "Active": true, "Entry": map[string]any{"name": "OpenAbstractions", "enabled": true}}
				if err := os.WriteFile(locator, PluginJSON(record), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if edge == "source-missing" {
				if err := os.Remove(filepath.Join(vendor.root, "plugins", codexHookPlugin, "hooks", "hooks.json")); err != nil {
					t.Fatal(err)
				}
			}
			if edge == "cache-missing" || edge == "cache-noop" {
				if err := os.Remove(filepath.Join(home, "plugins", "cache", vendor.name, codexHookPlugin, codexHookVersion, "hooks", "hooks.json")); err != nil {
					t.Fatal(err)
				}
			}
			vendor.noOp = edge == "cache-noop"
			cache := filepath.Join(home, "plugins", "cache", vendor.name, codexHookPlugin, codexHookVersion)
			if edge == "cache-changed" {
				if err := os.WriteFile(filepath.Join(cache, "hooks", "hooks.json"), []byte("person changes"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if edge == "cache-replaced" {
				if err := os.Rename(cache, cache+".prior"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(cache, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before := vendor.writes
			copies, err := copiesInConfig(ctx, config, nil, vendor.run)
			if err != nil || len(copies) != 2 {
				t.Fatalf("hook discovery: %+v, %v", copies, err)
			}
			for _, copy := range copies {
				reading, err := copy.Observe(ctx)
				if err != nil || copy.CanReconcile() != (edge != "locator-absent") {
					t.Fatalf("hook owner/trust: %+v, %v", reading, err)
				}
				want := CopyCurrent
				if edge == "locator-absent" {
					want = CopyHumanNeeded
				} else if edge == "source-missing" && copy.Kind == SourceCopy {
					want = CopyAbsent
				} else if (edge == "cache-missing" || edge == "cache-noop") && copy.Kind == VendorCopy {
					want = CopyAbsent
				} else if (edge == "source-missing" || edge == "cache-changed" || edge == "cache-replaced") && copy.Kind == VendorCopy {
					want = CopyUnknown
				}
				if reading.State != want {
					t.Fatalf("%s/%s: %+v, want %s", edge, copy.Kind, reading, want)
				}
			}
			if vendor.writes != before {
				t.Fatal("observation changed vendor state or hook trust")
			}
			for _, copy := range copies {
				if edge == "source-missing" && copy.Kind == SourceCopy || edge == "cache-missing" && copy.Kind == VendorCopy {
					if err := copy.Reconcile(ctx); err != nil {
						t.Fatal("authorized producer repair", err)
					}
					if after, err := copy.Observe(ctx); err != nil || after.State != CopyCurrent {
						t.Fatalf("repaired native copy readback: %+v, %v", after, err)
					}
				}
				if (edge == "cache-changed" || edge == "cache-replaced") && copy.Kind == VendorCopy {
					if err := copy.Reconcile(ctx); err == nil || vendor.writes != before {
						t.Fatal("foreign or replaced cache accepted for repair", err)
					}
				}
				if edge == "cache-noop" && copy.Kind == VendorCopy {
					if err := copy.Reconcile(ctx); err == nil {
						t.Fatal("vendor no-op claimed a repaired cache")
					}
					if after, err := copy.Observe(ctx); err != nil || after.State != CopyAbsent {
						t.Fatalf("vendor no-op readback: %+v, %v", after, err)
					}
				}
			}
			if edge == "source-missing" && vendor.writes != before {
				t.Fatal("source restoration mutated vendor settings")
			}
			if edge == "cache-missing" && vendor.writes != before+1 {
				t.Fatal("cache restoration bypassed its native producer")
			}
		})
	}
}
